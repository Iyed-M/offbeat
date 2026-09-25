# Evaluating YouTube resolution with frozen observations

This procedure builds a user-controlled, offline benchmark for Offbeat's current YouTube selection policy. It does not create Acquisition work, download media, persist candidate history, or establish recording identity from metadata.

## Fresh capture and frozen replay

A capture always performs a **fresh** bounded YouTube metadata search for a track in current Desired Spotify state:

```bash
offbeat acquire capture spotify:track:TRACK_ID > track-capture.json
```

The daemon must be running and `yt-dlp` must be configured. The emitted format records:

- format version, capture timestamp, CLI version, daemon decision version, `yt-dlp` version, and the daemon OS/architecture;
- the exact Desired Spotify metadata and query used;
- the ordered, bounded raw fields returned by the search;
- the inspection decision made at capture time; and
- a separate, initially empty `annotations` object.

This command does not recreate the search behind an earlier unresolved Acquisition outcome. Search ordering and metadata can change. Capture the observation you intend to evaluate; redirecting `offbeat acquire inspect` is not the versioned corpus format.

Replay never searches YouTube:

```bash
offbeat acquire replay corpus.json > outcome-report.json
```

It passes the frozen ordered result set through the same selection function used by production. Each case reports `decision_matches_recorded`; its replay has `fresh_search: false`. A mismatch means policy behavior changed, not that live search improved or regressed.

Each capture command emits a one-case corpus. To combine captures with `jq`:

```bash
jq -s '{format_version: 1, description: "user evaluation", captures: map(.captures[]) }' captures/*.json > corpus.json
```

Keep an untouched copy of every original capture.

## Sequential batch capture

For a predeclared sample of unresolved YouTube work, the CLI can perform the fresh searches sequentially and produce replay-ready artifacts:

```bash
offbeat acquire capture-batch --output-dir ./captures --limit 30
```

The default state is `unresolved`. To audit a predeclared sample of automatic selections instead:

```bash
offbeat acquire capture-batch --state complete --output-dir ./selection-audit --limit 10
```

The command pages through current Acquisition status, keeps only YouTube work in the requested state, deduplicates tracks, and confirms each track is still in current Desired Spotify state when it inspects it. Work removed from Desired Spotify state is skipped. It performs at most one fresh search at a time and does not queue, retry, download, or otherwise change Desired Spotify state, Acquisition work, or Managed track files.

The output directory is private (`0700`), and JSON artifacts are private (`0600`). Stable hashed filenames named `capture-*.json` hold independent one-case captures. A resume reuses valid successful captures byte-for-byte; recorded or invalid failures are skipped unless `--retry-failed` is supplied. Put human annotations in the independent capture files. If annotations exist only in a generated combined corpus, a later run refuses to overwrite that corpus so the labels are not silently lost.

`corpus.json`, or numbered `corpus-*.json` chunks when the corpus would exceed the replay size limit, contains the deterministic combination of the selected independent captures. Only files listed in `summary.json` are current batch outputs; unrelated files in the directory are never scanned into the corpus. The summary reports attempted, captured, reused, skipped, search-failure, and retrieval-failure counts, plus diagnostic-reason, eligible-candidate-count, and score-gap distributions. These fields describe observations only and are not correctness labels.

An interrupted run stops the active search before starting another track. Run the same command again to resume. Batch capture is collection machinery, not a sampling method: predeclare the representative sample and independent labeling procedure below before using its output to justify policy changes.

## Independent annotations

Add one annotation for each candidate whose recording identity you can assess:

```json
{
  "video_id": "abcdefghijk",
  "label": "acceptable",
  "evidence": "independently verified same recording and version",
  "provenance": "manual audit, 2026-09-25; source/listening method noted here"
}
```

Allowed labels are:

- `acceptable`: independently verified as an acceptable upload of the intended recording;
- `incorrect`: independently verified as a different recording, master, version, or content; and
- `unknown`: identity could not be established.

Multiple different video IDs may be `acceptable` for one track. There is no canonical required upload. Conversely, matching titles, durations, album text, uploader names, or other similar metadata do **not** prove identical audio. Label distinct IDs independently, even when every captured metadata field is identical. When evidence is insufficient, use `unknown`; do not force a binary judgment.

Every annotation requires evidence and provenance. An annotation ID must occur in that capture's frozen raw results. Unannotated candidates remain unlabeled.

For an operational search or retrieval failure, a corpus case may contain `failure` instead of `observation`:

```json
"failure": {"stage": "search", "message": "locally recorded summary"}
```

The supported stages are `search` and `retrieval`. Keep error summaries free of credentials, private paths, and unnecessary command output.

## Representative sampling procedure

Predeclare the sample before changing policy. Aim for about 30 representative ambiguous tracks, spread across:

- close runner-up score gaps and different candidate counts;
- duplicate IDs versus genuinely distinct uploads;
- live, remix, remaster, acoustic, instrumental, cover, edit, and other version patterns;
- short, ordinary, and long durations;
- featured/reordered artists, label or Topic uploaders, and sparse/messy metadata; and
- cases where the apparently correct recording is absent from the bounded results.

Also audit a sample of automatic selections and a sample of other unresolved reasons (`no_candidates`, title/artist/version/duration rejection, and weak winners). This is necessary to reveal false automatic selections and discovery failures; sampling only ambiguous cases would measure neither.

For every candidate judgment, record independently supported same-recording, different-recording, or unknown identity. Use listening or another independent source only when authorized and available. Do not use Offbeat's score or search rank as ground truth.

The corpus contains potentially personal library metadata and public video identifiers. Store it with user-private permissions. For optional sharing, redact free-form notes and replace non-policy identifiers such as `capture_id` and Spotify URIs consistently. Do not change query text, title/artist/duration inputs, video IDs, ordered raw result fields, or labels and still call the result an exact replay. If those fields cannot be shared, publish aggregate counts instead and retain the private original.

## Reading the outcome report

The report keeps these outcomes separate:

- `correct_automatic_selection`: the selected ID is labeled acceptable;
- `incorrect_automatic_selection`: the selected ID is labeled incorrect;
- `avoidable_unresolved`: replay abstained although at least one frozen candidate is labeled acceptable;
- `appropriate_unresolved`: replay abstained and every frozen candidate is labeled incorrect, or the frozen result set is empty;
- `unknown_or_unlabeled`: the selected ID is unknown/unlabeled, or unresolved alternatives are not fully verifiable;
- `search_failure`; and
- `retrieval_failure`.

An unknown ID is never counted as correct or incorrect. An unresolved case with any unknown or unlabeled alternative is not silently called avoidable. Completion percentage alone is not a quality result; always inspect incorrect automatic selections first.

## Evidence gates for later work

Use the labeled cases to distinguish two hypotheses:

1. **Duplicate-recording ambiguity:** a substantial, representative share of avoidable unresolved cases has multiple independently verified acceptable IDs. Before proposing grouping, review every false automatic selection and show that the proposed identity evidence separates same recordings from different masters/versions. Metadata resemblance alone is not such evidence.
2. **Candidate discovery or structured-metadata gaps:** acceptable recordings are absent from bounded plain-YouTube results, or safe decisions repeatedly fail because useful artist/album evidence is unavailable. Measure this separately from duplicate uploads.

Recording grouping and provider changes are not authorized by this evaluation. Either needs a subsequent scoped decision. In particular, adding YouTube Music reopens ADR-0012's plain-YouTube decision and requires a new scoped ADR/spec before implementation.

The checked-in [`testdata/youtube-evaluation-synthetic-v1.json`](../testdata/youtube-evaluation-synthetic-v1.json) fixture exercises the format and accounting only. Its video IDs, observations, labels, and evidence are explicitly synthetic and make no real-world ground-truth claim.
