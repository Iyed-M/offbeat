# offbeat

Local-first Spotify-organized music for Linux and Android.

Offbeat mirrors Spotify playlists and Liked Songs into a managed local audio library, generates ordinary `.m3u8` playlists, and synchronizes the current playable library to one Android phone on the same network. Spotify remains the source of truth for organization; Offbeat owns only its local state and files.

## Current status

Milestones 1–6A are implemented.

The daemon, CLI, and Spicetify extension can collect and strictly validate a complete normalized candidate containing real playlists and Liked Songs through Spotify Desktop's authenticated Platform facade. Collection preserves ordering and duplicate occurrences and rejects incomplete required pages.

The daemon atomically persists current Spotify desired state in SQLite. `offbeat missing` reports distinct supported desired tracks without a usable managed file. An opt-in synthetic-audio test seam exercises managed-file registration and availability. `offbeat acquire` supports both explicit authorized media URLs and restart-safe Missing-set acquisition through conservative YouTube resolution. **Milestone 7 is next:** desktop M3U8 materialization.

On 2026-09-17 the post-M3 v1 roadmap was simplified by ADR-0010. The project now favors a current-state-first implementation over speculative historical revision, library-wide matching/review, asset-deduplication, and advanced Android-sync infrastructure. ADR-0011 restores the concrete YouTube Missing-set acquisition workflow without restoring those generalized systems.

## Components

| Component | Path | Current responsibility |
|---|---|---|
| Daemon | `cmd/offbeatd` | lifecycle, Control protocol, Adapter session, desired-state commits, managed-track availability, acquisition workers |
| CLI | `cmd/offbeat` | `status`, `config`, `spotify sync`, `missing`, and `acquire` requests |
| Spicetify extension | `spicetify/offbeat` | complete normalized playlist + Liked Songs collection |
| Config | `internal/config` | TOML configuration/defaults |
| DB | `internal/db` | SQLite ownership, migrations, desired-state reconciliation, managed-file mappings, durable acquisition work |
| Managed files | `internal/managed` | confined file access and atomic audio publication |
| Logging | `internal/logging` | `log/slog` wrapper |

## Build and test

```bash
go test ./... -count=1
go test ./... -race -count=1
go vet ./...
go build ./cmd/offbeat
go build ./cmd/offbeatd
node --test spicetify/offbeat/offbeat.test.js
```

## Configuration

Default config location:

```text
~/.config/offbeat/config.toml
```

The currently implemented Adapter endpoint can be configured with:

```toml
[spotify_adapter]
bind_address = "127.0.0.1"
port = 16352
```

The Adapter listener is loopback-only.

Other configuration fields already present from the repository skeleton are not promises that the older post-M3 architecture will be implemented unchanged. The PRD and implementation plan are authoritative for current v1 scope.

CLI flags can override bootstrap locations, for example:

```bash
offbeatd --config /path/to/config.toml --home /tmp/sandbox
offbeat  --config /path/to/config.toml status
```

## Development Adapter credential

Until setup/provisioning is implemented, the daemon accepts a development-only Adapter credential through:

```bash
export OFFBEAT_ADAPTER_CREDENTIAL='development-only-secret'
offbeatd
```

The credential is not ordinary TOML configuration and is not returned through daemon status/effective config.

The default Adapter endpoint is:

```text
ws://127.0.0.1:16352/v1/adapter
```

See [`spicetify/offbeat/README.md`](spicetify/offbeat/README.md) for the manual extension development/validation procedure. Production-friendly provisioning and packaging are deferred until the core product flow is working; they are no longer tied to the old Milestone 12 numbering.

## Lean v1 roadmap

The authoritative roadmap is [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md).

Roadmap after M6A:

```text
M6B resolver recall and explainability
M7  desktop M3U8 materialization
M8  one-device Android manual sync
M9  setup, packaging, and reliability
```

Historical snapshots/revision replay, library-wide fuzzy matching/review, cross-track asset deduplication, advanced deletion/integrity tooling, mDNS discovery, resumable Android transfer, background Android sync, and broad multi-device management are deferred until real usage justifies them.

## Managed tracks and missing state

```bash
offbeat spotify sync
offbeat missing
```

`missing` uses committed desired state, so Spotify does not need to be running. It reports a summary followed by URI, title, and artists for each missing track, sorted by URI. Duplicate playlist/Liked references count once; unsupported placeholders are excluded.

The CLI collects bounded Control-protocol pages before printing, so large libraries fit the existing 1 MiB frame limit. Continuations carry the last examined desired URI and state revision. If Spotify desired state changes during listing, the command fails clearly so you can run it again. File availability is checked live as each page is read. A single track with exceptionally large display metadata that cannot fit a page returns a structured error.

The daemon creates `~/Music/Offbeat/tracks/` and `~/Music/Offbeat/playlists/`. Override the managed root with:

```toml
[paths]
music_root = "/absolute/path/to/Offbeat"
```

Managed filenames use the full SHA-256 of the Spotify URI, independent of artist/title changes. SQLite schema version 3 stores one relative path per track. Removing a desired reference retains both its mapping and file, allowing reuse if that identity returns. Managed availability does not change the Spotify state revision.

A usable file must be registered, readable, non-empty, regular, and inside the managed root. The daemon checks the physical file on each `missing` request. Symlink track files and symlink `tracks`/`playlists` directories are rejected. M5 performs shallow file checks, not audio decoding or integrity verification.

### Synthetic fixture completion gate

Run the deterministic end-to-end demonstration without Spotify or external media:

```bash
go test ./cmd/offbeat -run 'TestCLI(MissingDesiredTracks|ManagedFixtureAvailability|ManagedFixtureRegistrationFailure)$' -count=1 -v
```

The harness starts a real daemon owner in a temporary home, seeds desired state through its database boundary, and runs the built CLI against its Unix socket. It enables `app.Options.EnableSyntheticFixtures` and calls `Daemon.RegisterSyntheticTrackFixture(ctx, spotifyURI)` to atomically publish a 100ms silent PCM WAV for a supported desired track. This in-process development/test mechanism is disabled by default and has no CLI, environment, or Control-protocol switch.

The gate verifies exact available/missing sets, restart, duplicate references, metadata changes, reference removal/re-addition, physical file loss, and injected registration failure. A failed database registration may leave a complete unregistered file; it stays missing until registration succeeds on retry. Temporary files are not registered as available.

## Authorized acquisition

Choose a supported track from `offbeat missing` and explicitly supply a media URL you are authorized to download:

```bash
offbeat acquire spotify:track:TRACK_ID 'https://your-media-host.example/audio.flac'
offbeat acquire status 1
offbeat acquire retry 1
```

Submission declares that you are authorized to download the supplied source. The direct track-and-URL form does not search for matches or obtain Spotify audio. HTTP(S) URLs are accepted; local paths, search expressions, embedded credentials, and fragments are rejected. Each request retrieves one item. Supported output formats are WAV, MP3, M4A, Opus, Ogg, FLAC, and AAC.

The intended batch workflow is:

```bash
offbeat spotify sync
offbeat acquire missing
offbeat acquire status
offbeat acquire retry unresolved
```

To inspect a fresh resolver decision without queuing or downloading anything:

```bash
offbeat acquire inspect spotify:track:TRACK_ID > resolution.json
```

The daemon searches YouTube metadata anew and writes a versioned JSON report containing the capture time, exact query, ordered raw yt-dlp fields, duplicate-ID treatment, per-candidate rejection or component scores, ranking, thresholds, and final selected URL or unresolved reason. This is not a replay of any earlier unresolved attempt. Inspection accepts any currently desired supported track, including one that already has a Managed track file or Acquisition work, and never changes that state.

`acquire missing` atomically queues each distinct Missing track before returning and reuses the existing acquisition workers. Available tracks, active work, and tracks with a previous YouTube attempt are skipped idempotently. `offbeat acquire retry unresolved` explicitly requeues reusable unresolved YouTube work after resolver improvements; removed, available, active, failed, and completed work is not requeued. `offbeat acquire status` prints aggregate counts and paginates every work outcome over bounded Control responses; add an ID to inspect just one item.

The built-in resolver inspects at most ten yt-dlp YouTube search results. It normalizes punctuation, title words, artist order, and featured-artist presentation, then scores title identity, artist evidence, and duration at 60%, 25%, and 15%. Title and artist fields must independently score at least 60 and 75. Label and Topic uploaders can support a match but cannot prove the primary artist without title evidence. Version markers such as live, remix, remaster, acoustic, instrumental, cover, slowed, or sped-up must agree exactly, and duration must be within the greater of 12 seconds or 5%, capped at 20 seconds. These thresholds are local to the resolver and are fixed by the deterministic safe/wrong/ambiguous fixture corpus rather than copied from spotDL.

A best candidate must score at least 82 and lead the runner-up by at least seven points. No result, field/version/duration rejection, a weak winner, or a close runner-up becomes `unresolved`; status stores only its bounded category and aggregate rejection counts, never candidate metadata or source URLs. Another track's work continues independently. The selected canonical YouTube URL is persisted before retrieval, so restart does not repeat a successful resolution. Direct URL acquisition remains the user-controlled override for an unresolved track.

The direct submission returns a durable acquisition ID immediately. Use that ID with `status` to inspect `pending`, `running`, `unresolved`, `failed`, or `complete`. `retry` requeues failed work with the same source and ID. To correct the source, submit a new acquisition after the previous work has failed. Repeating the same active track/source returns its existing ID; a competing active source is rejected. An available track cannot be acquired again. The CLI does not automatically retry requests after connection loss.

Install yt-dlp, FFmpeg, and ffprobe separately. Their executable locations and the worker limit are configurable:

```toml
[downloader]
yt_dlp_path = "yt-dlp"
ffmpeg_path = "ffmpeg"
ffprobe_path = "ffprobe"

[acquisition]
concurrency = 2
```

Concurrency must be between 1 and 32. Restart the daemon to apply configuration changes. The older `temp_retry_backoff` and `max_temp_retries` configuration fields are retained for compatibility but unused; failures require manual retry. Tool failures are reported without reflecting source URLs or subprocess output. Check the source and configured tool installations before retrying.

The daemon stores acquisition work in SQLite schema version 5 and requeues interrupted running work on startup. A failed or unresolved track does not stop other workers. yt-dlp uses private temporary staging, ignores user configuration/plugins, and preserves native audio when possible; FFmpeg extracts audio when necessary. ffprobe checks the completed audio before publication. The daemon copies completed media into a managed temporary file, then atomically installs it and commits its mapping together with completion. A failed database commit may leave an unregistered complete file, which stays missing until a later successful acquisition. Abrupt termination can leave staging files in the system temporary directory; these are never registered as playable files.

A track removed from Desired Spotify state during retrieval is not published. Acquisition does not change the Spotify state revision. Playlist generation is M7.

The deterministic tests inject the retrieval boundary and cover failure isolation, manual retry, bounded workers, shutdown, and restart. To also exercise installed tools against generated local audio:

```bash
OFFBEAT_TEST_REAL_TOOLS=1 go test ./internal/acquisition ./internal/app -run 'RealTools' -count=1 -v
```

To opt into the external YouTube resolution/download smoke test, supply metadata for a YouTube item you are authorized to download:

```bash
OFFBEAT_TEST_REAL_YOUTUBE=1 \
OFFBEAT_TEST_YOUTUBE_TITLE='Fixture title' \
OFFBEAT_TEST_YOUTUBE_ARTIST='Fixture artist' \
OFFBEAT_TEST_YOUTUBE_DURATION_MS=123000 \
OFFBEAT_TEST_YOUTUBE_ID='abcdefghijk' \
go test ./internal/acquisition -run TestYouTubeResolverRealAuthorizedFixture -count=1 -v
```

## Planning workflow

`docs/PRD.md` and `docs/IMPLEMENTATION_PLAN.md` are baselines, not task queues.

For new implementation work:

1. use `/to-spec` for the next milestone/slice;
2. review/approve that scoped issue;
3. use `/to-tickets` for agent-ready implementation tickets;
4. use Wayfinder only when a concrete external uncertainty actually blocks implementation.

GitHub issue state and discussion are authoritative for active delivery.
