# Spotify presentation metadata: compatibility gate (#79)

This is a decision record for the current retriever outputs, **not** an enabled daemon tagging pipeline. Reproduce the generated-media probe (no downloaded or copyrighted media):

```sh
uv run --no-project --with 'mutagen==1.47.0' python scripts/verify_tag_compatibility.py
```

Requires `ffmpeg` and `ffprobe` on `PATH` and access to the pinned Mutagen package on first run. Checked locally with FFmpeg/FFprobe n9.0.1 and Mutagen 1.47.0. The probe synthesizes a one-second tone per retriever extension in a temporary directory and a 2×2 original PNG in memory. It tags a **copy**, reads tags back using the relevant format parser, checks basic title/album via independent FFprobe, compares codec/sample rate/channels and SHA-256 of independently decoded PCM before/after, then discards all fixtures. A passing decode digest demonstrates sample equivalence on these fixtures; it is not a proof of compatibility with every container variant or player. No actual Spotify Desktop responses, cover URLs or offline music-player UIs were tested.

## Current source-of-truth audit

| Requested value | Current collection and persistence | Decision for later work |
| --- | --- | --- |
| Spotify URI | Required track URI in `spicetify/offbeat/offbeat.js` `normalizeEntry`; `internal/app/adapter.go` validates; `spotify_tracks.uri` primary key | Stable identity, write as an explicit custom tag when tagging is supported. No YouTube substitution. |
| Title | `source.name` required nonempty; `desired.Track.Name` / `spotify_tracks.name` | Present for supported entries; retain exact Spotify text after bounding/validating at the new ingestion boundary. |
| Ordered artist names | Required nonempty `source.artists[]` with URI and name; array order survives `artists_json` and `DesiredTrack` reads | Present; preserve repeated per-artist values where native, with a documented display fallback for players that flatten them. Do not sort or infer album artist. |
| Album | Required `source.album.uri` and `.name`; stored as `album_uri`, `album_name` | Name present for supported entries. |
| Duration | Positive integer from `duration_ms` or `duration.milliseconds`; `spotify_tracks.duration_ms` | Available as catalog duration, **not** an audio tag to write. Verify actual audio duration independently before using it for playback display. |
| Album artist | Not read from `source.album` or track, absent from candidate, model and schema | Unavailable. Add optional, normalized collection only if confirmed on actual Desktop responses; otherwise omit the tag. Never assume first artist. |
| Track/disc number, release date | Not read from source, absent from candidate, model and schema | Unavailable. Confirm source shape and optionality on real responses before adding; omit absent tags. |
| Artwork URL | Not read from source, absent from candidate, model and schema | Unavailable. Check actual image/album source shape before adding an optional validated HTTPS URL. Never use YouTube thumbnail as a substitute. |

The extension's `source` is `raw.item` when object-shaped, otherwise `raw`; it only uses the fields above, treats missing required fields/non-track/unplayable entries as unsupported placeholders, and does not collect extra presentation metadata. The tests in `spicetify/offbeat/offbeat.test.js` exercise synthetic Desktop shapes, not guarantees about optional fields on all Spotify builds. `parseCandidateSnapshot`/`materializeTrack` in `internal/app/adapter.go` require an exact field set, so adding an optional field needs coordinated extension, parser and persistence changes; simply sending extra JSON is rejected. `internal/desired/state.go` and `internal/db/desired_state.go` model current state only. `internal/db/migrations/0002_current_spotify_state.sql` stores URI, name, ordered artists JSON, album URI/name and duration; `0003_managed_tracks.sql` stores URI → relative path independently of desired-state removal. No credentials need to cross the adapter: keep all Desktop collection in Spicetify, transport only bounded normalized fields, and fetch any validated artwork URL without a Spotify token in the daemon. Do not infer that the current test fixtures establish availability of optional source fields in real Spotify Desktop data.

## Fixture-tested format matrix and field mapping

Writer candidate: narrowly scoped **Mutagen 1.47.0** tagging tool (Python runtime dependency to evaluate before production), applied only to private staged copies. The Go daemon currently has no tag writer. Avoid choosing FFmpeg's generic cover-art mapping for Opus. Matrix entries mean *this candidate and these generated fixtures passed*, not that tags are already written by Offbeat or accepted by all players.

| Retriever extension (fixture codec) | Text, ordered artists, URI | Picture | Gate result |
| --- | --- | --- | --- |
| `.opus` (Opus in Ogg) | Ogg Vorbis comments: `TITLE`, repeated `ARTIST`, `ALBUM`, `ALBUMARTIST`, `TRACKNUMBER`, `DISCNUMBER`, `DATE`, `SPOTIFY_URI` | Base64 FLAC Picture block (type 3, PNG) in `METADATA_BLOCK_PICTURE` comment | Passed, including decoded picture bytes. FFprobe exposes the comment as an attached-picture stream; that does **not** mean the writer mapped a separate video track. |
| `.ogg` (Vorbis in Ogg) | Same comments and ordered repeated `ARTIST` | Same `METADATA_BLOCK_PICTURE` comment | Passed for Vorbis fixture. Extension alone is not a codec check: probe actual codec. |
| `.flac` (FLAC) | Same Vorbis comment mapping | Native FLAC Picture metadata block (type 3) | Passed. |
| `.mp3` (MP3) | ID3v2.4 `TIT2`, ordered `TPE1` text values, `TALB`, `TPE2`, `TRCK`, `TPOS`, `TDRC`, `TXXX:SPOTIFY_URI` | ID3 `APIC` (type 3) | Passed Mutagen readback. Multi-value `TPE1` is not consistently displayed by all players; test target players before claiming multi-artist presentation. |
| `.m4a` (AAC in MP4) | `©nam`, ordered `©ART` values, `©alb`, `aART`, `trkn`, `disk`, `©day`, `----:com.apple.iTunes:SPOTIFY_URI` (UTF-8 bytes) | `covr` PNG | Passed Mutagen readback. Freeform URI and multiple artists are player-dependent. |
| `.wav` (PCM WAV) | None | None | Explicit pass-through; generated fixture is readable and copied byte-for-byte. No text/artwork support claimed. |
| `.aac` (raw ADTS AAC) | None | None | Explicit pass-through; generated fixture is readable and copied byte-for-byte. No text/artwork support claimed. |

All five tagged formats retain their original audio codec, sample rate, channels and decoded PCM digest in the fixture probe. Synthetic album artist, numbers and date exercise **writer capability**, not availability in Desired Spotify state. Artist names and Spotify URI are test data; there are no real artist IDs in these files. No duration tag is written. For WAV/raw AAC, skip tagging and report unsupported metadata separately from audio availability; do not silently rewrap or re-encode to gain tag support. If an encountered variant or codec fails readback or audio verification, leave its original usable file alone and record a tag failure/unsupported status, not a missing track or acquisition retry.

## Safe handoff for later tickets

`internal/acquisition/retriever.go` stages one retrieved file in a private directory and FFprobes for readable audio; `internal/app/acquisition.go` currently calls `managed.Files.Publish`, which copies the open staged descriptor to `tracks/.publish-*`, syncs it, renames to `tracks/<SHA-256-of-Spotify-URI>.<ext>`, then commits `managed_tracks` through `CompleteAcquisition`. `Available` is a shallow nonempty/readability check, not media or tag verification. A later tag step should work on a *new private staged copy* before publication, independently verify tags, picture and decoded audio/codec, then hand the verified descriptor to the existing managed publication boundary. On tag/art failure retain or publish usable audio according to the explicit tag-state policy; never edit the sole good managed copy in place. Refresh needs a safe root-confined read of the existing file and an atomic root-owned replacement with crash-safe bookkeeping. A source revision changing metadata must not change the SHA-256 path, download audio again or borrow titles/art from yt-dlp.

Bound normalized input lengths and artist count before persistence (the current adapter only checks nonemptiness), reject controls that could inject M3U lines when displaying names, and keep optional fields absent rather than fabricating values. For later artwork retrieval: accept only bounded HTTPS Spotify-supplied URLs (no userinfo), validate every redirect and destination including private-address/SSRF protections, enforce time/byte/type/dimension limits, decode image before embedding, and cache safely by stable album identity when appropriate. Cover failure should permit text tags and playable audio. The fixture probe deliberately does not validate network retrieval, crash recovery, file replacement, realistic input sizes, player-specific display, or end-to-end daemon tagging; those are successor-ticket work, not proven matrix cells here.
