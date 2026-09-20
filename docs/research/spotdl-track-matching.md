# How spotDL matches Spotify tracks to YouTube results

Research date: 2026-09-20

## Scope and source baseline

This note describes the current spotDL release checked for this research: **v4.5.2**, commit [`cd4a4203f5b12bd6dbbdf22d7674807858d35e05`](https://github.com/spotDL/spotify-downloader/tree/cd4a4203f5b12bd6dbbdf22d7674807858d35e05). The supporting yt-dlp source was checked at commit [`c7fb478d21e9e59524befbe23f7801bb267fb880`](https://github.com/yt-dlp/yt-dlp/tree/c7fb478d21e9e59524befbe23f7801bb267fb880).

Only first-party sources are used: spotDL's repository and documentation, plus yt-dlp's repository for the `ytsearchN:` contract.

## Executive summary

spotDL's default is a YouTube Music search, not a plain YouTube search. It normally searches up to 50 YouTube Music `songs`, then up to 50 `videos`, and ranks results using fuzzy title and artist similarity, duration, conditional album similarity, version-word penalties, and sometimes explicitness. It can also search by ISRC before the text query. Plain YouTube is an optional provider that asks yt-dlp for ten video results.

The algorithm has useful candidate-generation and field-gating ideas, but it is not conservative in Offbeat's sense. A candidate that survives spotDL's gates can be returned regardless of its final score, close alternatives are resolved partly by view count rather than declared ambiguous, and there is no unique-winner requirement. Offbeat should borrow bounded search, normalized field comparisons, explicit version-conflict checks, and duration gates, but should reject close or weak winners rather than copy spotDL's final selection policy.

## End-to-end flow

1. spotDL constructs a default query as `Artist1, Artist2 - Track Name`, lowercases it, and optionally replaces it with a user-supplied metadata template ([`formatter.py` lines 76-96](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/formatter.py#L76-L96), [`base.py` lines 171-176](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L171-L176)).
2. When the provider supports ISRC, the track has one, and no custom query is configured, spotDL searches the ISRC first ([`base.py` lines 180-239](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L180-L239)).
3. It runs each search mode declared by the provider. YouTube Music uses `songs` first and `videos` second; plain YouTube has one mode ([`ytmusic.py` lines 24-29](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/ytmusic.py#L24-L29), [`youtube.py` lines 20-21](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/youtube.py#L20-L21)).
4. `order_results` removes candidates that fail title, artist, or duration gates and computes scores for survivors ([`matching.py` lines 650-846](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L650-L846)).
5. A verified candidate scoring at least 80 can return immediately. Otherwise candidates from the provider's search modes are pooled, and spotDL returns a best result if any survived ([`base.py` lines 289-329](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L289-L329)).
6. If a provider returns no result, configured providers are tried in order. Exhausting them raises `LookupError` ([`downloader.py` lines 378-396](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/download/downloader.py#L378-L396)).

## Search query generation

### Default text query

`create_song_title` joins every Spotify artist with `, ` and emits `<all artists> - <track name>`. The provider lowercases that whole string before searching ([`formatter.py` lines 76-96](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/formatter.py#L76-L96), [`base.py` lines 171-178](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L171-L178)). The Spotify title is used as supplied, so title suffixes such as `Remastered`, `Live`, or `Acoustic` naturally enter the query.

### Custom templates

`--search-query` accepts the same metadata variables as output templates, including title, artists, album, duration, year, ISRC, and Spotify track ID ([official usage documentation, lines 412-414](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/docs/usage.md#L412-L414)). If a template contains no known variable, spotDL prepends `{artist} - {title}` ([`formatter.py` lines 268-294](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/formatter.py#L268-L294)). Search formatting uses a "short" artist form: `{artists}` becomes only the primary artist, while artists already named in the track title are removed from the working list ([`formatter.py` lines 213-245](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/formatter.py#L213-L245)). Configuring a custom query disables the ISRC-first path.

### ISRC query

YouTube Music and Piped declare ISRC support; plain YouTube does not ([`ytmusic.py` lines 24-29](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/ytmusic.py#L24-L29), [`youtube.py` lines 20-21](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/youtube.py#L20-L21), [`piped.py` lines 41-50](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/piped.py#L41-L50)). For ISRC-capable providers:

- One verified ISRC result returns immediately without metadata scoring.
- Otherwise ISRC results are scored, and the top one returns if its score is strictly greater than 80.
- If the later title search returns a URL also present in the ISRC result set, that URL returns immediately.

These branches are in [`base.py` lines 180-239 and 260-273](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L180-L273).

## Providers and candidate counts

The documented audio providers are `youtube`, `youtube-music`, `slider-kz`, `soundcloud`, `bandcamp`, and `piped`; users may order multiple providers for fallback ([official usage documentation, lines 404-405](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/docs/usage.md#L404-L405)). Only the YouTube-backed behavior is relevant here:

| Provider | Default? | Search calls and candidate bounds | Candidate metadata used by spotDL |
| --- | --- | --- | --- |
| YouTube Music | Yes | Up to 50 `songs`, then up to 50 `videos`. The second call may be skipped after a verified score of at least 80. An additional ISRC search may precede them; spotDL does not set a limit for that call. Empty API responses are retried up to three times with a fresh client. | title, artist list, duration, album, explicit flag, result type, video ID |
| Plain YouTube | No | One yt-dlp `ytsearch10:` request, hence at most 10 entries. | video title, duration, uploader, view count, video ID |
| Piped | No | `music_songs`, then `music_videos`; spotDL itself does not send a result limit. An ISRC query may precede them. | title, duration, uploader, views; song results treat uploader as artist |

The default provider and matching-related defaults are set in [`config.py` lines 322-359](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/config.py#L322-L359). YouTube Music's limits, order, retries, and result mapping are in [`ytmusic.py` lines 24-29 and 52-131](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/ytmusic.py#L24-L131). Plain YouTube's `ytsearch10:` and mapping are in [`youtube.py` lines 23-67](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/youtube.py#L23-L67). Piped's modes and mapping are in [`piped.py` lines 41-50 and 80-193](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/piped.py#L41-L193).

yt-dlp defines search pseudo-URLs as `_SEARCH_KEY(|all|[0-9]):query`; a numeric suffix requests that many results ([`SearchInfoExtractor`, lines 4140-4174](https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/extractor/common.py#L4140-L4174)). Its YouTube extractor binds this contract to `ytsearch` and filters to videos ([`youtube/_search.py` lines 8-15](https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/extractor/youtube/_search.py#L8-L15)). Therefore the `10` in spotDL's `ytsearch10:` is an explicit candidate bound, not a yt-dlp default.

## Candidate normalization and fields

The common `Result` type can carry source, URL, verified status, title, duration, author, artists, views, explicitness, album, year, track number, genre, lyrics, query, and whether it came from an ISRC search ([`result.py` lines 13-43](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/types/result.py#L13-L43)). Matching actually uses:

- title/name;
- Spotify artists against candidate artists, author/channel, and candidate title;
- duration;
- album when supplied by a verified non-ISRC result;
- explicitness in one scoring branch;
- verified and ISRC-search flags for short circuits and score logic;
- view count as a final popularity adjustment.

String comparisons use `rapidfuzz.fuzz.ratio` after slugification ([`formatter.py` lines 533-547](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/formatter.py#L533-L547)). The matching code also sorts word/artist representations and fills artist tokens missing from one side before some title comparisons ([`matching.py` lines 224-257](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L224-L257)).

Plain YouTube candidates have no structured artist list, album, explicitness, or verified status: they use uploader as `author` and are always `verified=False` ([`youtube.py` lines 55-66](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/youtube.py#L55-L66)). YouTube Music marks `resultType == "song"` as verified and maps its structured artists, album, duration, and explicit flag ([`ytmusic.py` lines 85-106](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/ytmusic.py#L85-L106)). "Verified" here is therefore a provider/result-type signal, not proof that the YouTube recording is identical to the Spotify recording.

## Scoring and thresholds

### Preliminary hard filters

A candidate must pass all applicable gates before final ranking:

| Gate | Exact spotDL behavior | Practical meaning |
| --- | --- | --- |
| Common title word | At least one slugified Spotify-title word must occur as a substring in the slugified candidate title. | Very weak first-pass title overlap; short words can match inside other words. |
| Title score | Reject when `name_match <= 60`. | Effective requirement is strictly greater than 60. |
| Artist score | Reject when `artists_match < 70`, except Slider.kz. | At least 70 after artist fixups. |
| Duration score | `100 * exp(-0.1 * abs(seconds difference))`; reject below 25. | Rejects differences greater than about **13.86 seconds**. |
| Combined weak-duration gate | If duration score is below 50 and current title/artist average is below 75, reject. | Differences greater than about **6.93 seconds** need at least a 75 title/artist average. |

The common-word and title/artist gates are in [`matching.py` lines 678-772](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L678-L772). The duration formula is in [`matching.py` lines 615-629](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L615-L629), and its gates are in [lines 793-811](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L793-L811).

### Title and version matching

The base title score is a fuzzy ratio between word-sorted, slugified track names. If it is at most 75, spotDL also compares expanded strings containing title and artist tokens and keeps the higher score ([`matching.py` lines 563-612](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L563-L612)).

Version handling is a penalty rather than a hard compatibility rule. The fixed list is `bassboosted`, `remix`, `remastered`, `remaster`, `reverb`, `bassboost`, `live`, `acoustic`, `8daudio`, `concert`, `acapella`, `slowed`, `instrumental`, and `cover`. Each term found in the candidate title but absent from the Spotify title subtracts 15 points from title similarity ([`matching.py` lines 42-57, 201-221, and 735-746](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L42-L57)). Because the penalty is applied before the `> 60` title gate, one or more extra version terms can exclude a candidate, but there is no symmetrical, explicit requirement that a meaningful version marker present on Spotify also appear in the candidate.

### Artist matching

Artist scoring is heuristic and multi-stage:

- The primary artists are fuzzy-compared after slugification and ordering. If that score is below 50 and Spotify has multiple artists, spotDL tries pairings among the first two artists and keeps the maximum ([`matching.py` lines 288-354](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L288-L354)).
- Additional artists are compared positionally and averaged, then primary and additional scores are averaged when Spotify has multiple artists ([`matching.py` lines 357-389 and 686-703](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L357-L389)).
- For unverified video-like results, low artist scores can be rescued by channel/author similarity, artist names embedded in the title, or token-list similarity ([`matching.py` lines 392-460](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L392-L460)).
- Verified results and multi-artist edge cases have two more fixup passes that add small artist-presence bonuses or compare reconstructed title/artist strings ([`matching.py` lines 463-560](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L463-L560)).

This tolerates YouTube titles that embed artists and uploaders that are labels rather than performers, but it also makes the score difficult to reason about as a calibrated confidence value.

### Base score, album, duration, and explicitness

For a survivor, the initial score is an equal average of artist and title scores:

```text
base = (artist_score + title_score) / 2
```

Album is not a general weighted field. For a verified, non-ISRC result with album metadata, spotDL folds album similarity into the score only when `album_match <= 80`:

```text
base = (base + album_score) / 2
```

Thus a weak or mismatched album can halve-influence and lower the score, while an album match above 80 does not increase it. This exact conditional is in [`matching.py` lines 774-791](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L774-L791); album similarity itself is a slugified fuzzy ratio, or zero when either album is absent ([lines 632-647](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L632-L647)).

For ordinary non-ISRC candidates with `base <= 85`, spotDL then equally averages duration score into the total. If both explicit flags are known and differ, it subtracts 5 in this same branch ([`matching.py` lines 813-840](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L813-L840)). A strong title/artist score above 85 therefore still faces the hard duration gates, but duration and explicitness do not otherwise affect its final rank.

### Early acceptance and popularity adjustment

During each search mode, a verified best result with score at least 80 returns immediately, so later modes are not searched ([`base.py` lines 289-310](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L289-L310)). This threshold is an optimization/acceptance shortcut, not a universal minimum: after all modes, spotDL returns the best survivor without checking an absolute final-score threshold ([lines 315-329](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L315-L329)).

Final selection keeps every result within 8 points of the top metadata score. Unless there is only one such candidate or the top candidate is an ISRC result above 80, spotDL obtains view counts and adds a linearly normalized popularity bonus from 0 to 15 points, capped at 100 ([`matching.py` lines 260-285](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py#L260-L285), [`base.py` lines 331-386](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L331-L386)). Popularity can therefore overturn the metadata-score ordering among close candidates.

## Fallbacks, no match, and ambiguity

### Fallback strategies

spotDL has several sequential fallbacks:

- ISRC search before text search when supported.
- YouTube Music `songs` before `videos`.
- Artist recovery from candidate author/channel and title when structured artist metadata is weak or absent.
- Multiple configured audio providers, tried in user-specified order.
- Manual `YouTubeURL|SpotifyURL` matching, documented as a user override ([official usage documentation, lines 400-405](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/docs/usage.md#L400-L405)).
- Three retries with a new YouTube Music client when a search call yields no usable result; these retry the same query rather than broaden it ([`ytmusic.py` lines 72-131](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/ytmusic.py#L72-L131)).

### No match

A provider returns `None` only when no candidates remain after filtering (or the provider returned no candidates). The downloader then tries the next configured provider; if all fail, it raises `LookupError` naming the song ([`base.py` lines 315-318](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L315-L318), [`downloader.py` lines 389-396](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/download/downloader.py#L389-L396)).

### Ambiguity

spotDL has **no explicit ambiguous outcome** and no unique-best requirement. Multiple close survivors are not a reason to abstain; they enter the within-eight-points set and are resolved using metadata score plus view popularity. If one candidate survives, it is returned. If several survive all modes, one is still returned regardless of the final score. This differs directly from Offbeat's requirement that an ambiguous result remain unresolved.

## Configurability

Users can configure:

- provider list and fallback order (`--audio`);
- custom search-query template (`--search-query`);
- whether result filtering is disabled (`--dont-filter-results`);
- whether only verified results are retained (`--only-verified-results`);
- cookies and arbitrary yt-dlp arguments.

The CLI documents these controls in [`docs/usage.md` lines 404-420](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/docs/usage.md#L404-L420), and the downloader passes them into every provider in [`downloader.py` lines 198-212](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/download/downloader.py#L198-L212).

The following are hard-coded rather than user-configurable: candidate limits, provider search-mode order, fuzzy title and artist thresholds, duration decay and gates, album behavior, forbidden/version words and their 15-point penalty, the verified score-80 shortcut, the eight-point finalist band, and the 15-point popularity bonus.

Disabling filtering is especially non-conservative: spotDL assigns the provider's first result a score of 100 rather than validating metadata ([`base.py` lines 275-285](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py#L275-L285)). Requiring verified results also excludes every plain YouTube result because that provider marks all entries unverified.

## Techniques applicable to Offbeat's conservative resolver

These are research recommendations, not changes to Offbeat's product specification.

### Worth adapting

1. **Use a small bounded result set.** `ytsearch10:` is a concrete, supported way to ask yt-dlp for ten YouTube videos. A bound around 10 is easier to fixture and reason about than YouTube Music's potential 100-result pool.
2. **Generate a deterministic default query from all artists plus the complete Spotify title.** Keeping the full title preserves version markers. Offbeat can add a second narrowly defined query only if fixtures demonstrate a clear need; spotDL's many provider/search fallbacks should not be the starting point.
3. **Normalize before comparing, but retain semantic markers.** Case, punctuation, separators, and artist ordering can be normalized. `live`, `remix`, `remaster`, `acoustic`, `instrumental`, `cover`, `slowed`, and similar terms should be compared explicitly rather than allowed to disappear into one fuzzy score.
4. **Apply field-specific eligibility gates before ranking.** A candidate with conflicting artist, title/version, or duration should be ineligible even if another field scores highly. This is safer and more explainable than one compensating aggregate score.
5. **Use duration as both a hard guard and a ranking signal.** spotDL demonstrates both roles. Offbeat should choose fixture-backed absolute and possibly relative tolerances rather than copy spotDL's exponential constants blindly, especially for short tracks and extended mixes.
6. **Handle unstructured YouTube metadata deliberately.** Plain YouTube supplies title and uploader but no reliable structured artists or album. Artist names embedded in titles and a matching uploader can be supporting evidence, but label/topic channels should not independently prove a match.
7. **Keep provider metadata and scoring diagnostics in the resolver boundary.** Recording per-candidate reasons in logs or command output makes an unresolved result inspectable without creating a persistent review queue or candidate-history model.
8. **Turn spotDL's close-result band into an abstention rule.** If the top two eligible candidates are tied or too close under deterministic metadata scoring, Offbeat should return unresolved. This directly implements ADR-0011's unique eligible best-candidate requirement.
9. **Fixture the algorithm with captured/synthetic metadata.** spotDL's live matching test accepts several possible URLs and skips on changing results or failures ([`tests/test_matching.py` lines 404-431](https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/tests/test_matching.py#L404-L431)). That illustrates why Offbeat's normal CI should test deterministic captured candidates, with live search only as an opt-in integration check.

### Suggested conservative shape

A lean Offbeat resolver can remain much smaller than spotDL:

```text
query = all artists + full Spotify title
candidates = first bounded yt-dlp YouTube results

eligible(candidate):
  required metadata exists
  title identity is sufficiently similar
  primary artist is present/sufficiently similar
  meaningful version markers do not conflict
  duration difference is within a fixture-backed limit
  media satisfies source eligibility rules

rank eligible candidates with deterministic metadata only
resolve only if the best candidate clears an acceptance floor
  and beats the runner-up by a declared margin
otherwise unresolved
```

Album agreement may be useful corroborating evidence when reliable metadata is actually available, but plain yt-dlp YouTube search does not provide it in spotDL's mapping. It should not become a required dependency or a substitute for title/artist/version/duration eligibility.

## What Offbeat should not copy

The following conflict with Offbeat's lean v1 or conservative resolution requirements:

1. **Do not copy "always choose a survivor."** spotDL has no final universal acceptance threshold and no ambiguity outcome. Offbeat must prefer unresolved over a plausible but uncertain candidate.
2. **Do not use view count as a tie-breaker.** Popularity is volatile, can vary by region/time, and rewards popular covers, music videos, or unrelated uploads. It weakens determinism and says little about recording identity.
3. **Do not equate YouTube Music `resultType == song` with identity verification.** It is useful evidence, not proof that version, edit, remaster, explicitness, and recording all match.
4. **Do not port the full artist-fixup stack.** Its channel/title/token rescues are broad, interacting heuristics rather than a small explainable v1 policy. Start with explicit artist-presence rules proven by fixtures.
5. **Do not copy the exact score arithmetic or thresholds unvalidated.** In particular, album affects only verified low-album matches, duration sometimes affects rank and sometimes only gates eligibility, and explicit mismatch is conditional. These scores are not calibrated confidence probabilities.
6. **Do not make version terms mere soft penalties.** For Offbeat, an extra `live`, `cover`, `instrumental`, `remix`, `slowed`, or similar marker should normally be a conflict unless Spotify metadata requests that version. Missing requested version markers also need explicit treatment.
7. **Do not add spotDL's generalized provider list or fallback framework.** Offbeat v1 calls for one built-in YouTube resolver, not YouTube Music, Piped, SoundCloud, Bandcamp, Slider.kz, provider plugins, or cross-provider score comparison.
8. **Do not add arbitrary query templates or a disable-filtering switch in lean v1.** They enlarge policy/configuration surface and can bypass conservative guarantees. The existing direct-URL acquisition path is the clearer user-controlled override.
9. **Do not add ISRC-specific search unless a later scoped decision and real fixtures justify it.** ADR-0011 names Spotify title, artist, duration, and version metadata. Adding YouTube Music solely for ISRC broadens dependencies and source semantics.
10. **Do not persist candidate lists, confidence tiers, or review state.** Return a selected URL or an inspectable unresolved reason for each current Missing track; persistent review workflows and generalized matching remain deferred.
11. **Do not couple selection to downloading.** spotDL's providers own search and yt-dlp handling together. Offbeat should preserve its small resolution boundary and pass only the uniquely selected URL into the existing restart-safe acquisition path.

## Primary source index

- spotDL v4.5.2 source tree: <https://github.com/spotDL/spotify-downloader/tree/cd4a4203f5b12bd6dbbdf22d7674807858d35e05>
- spotDL audio-provider orchestration: <https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/base.py>
- spotDL matching implementation: <https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/utils/matching.py>
- spotDL YouTube Music provider: <https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/ytmusic.py>
- spotDL plain YouTube provider: <https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/spotdl/providers/audio/youtube.py>
- spotDL official usage documentation: <https://github.com/spotDL/spotify-downloader/blob/cd4a4203f5b12bd6dbbdf22d7674807858d35e05/docs/usage.md>
- yt-dlp search extractor contract: <https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/extractor/common.py#L4140-L4174>
- yt-dlp YouTube search extractor: <https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/extractor/youtube/_search.py#L8-L15>
