# offbeat

Local-first Spotify-organized music for Linux and Android.

Offbeat mirrors Spotify playlists and Liked Songs into a managed local audio library, generates ordinary `.m3u8` playlists, and synchronizes the current playable library to one Android phone on the same network. Spotify remains the source of truth for organization; Offbeat owns only its local state and files.

## Current status

Milestones 1–5 are implemented.

The daemon, CLI, and Spicetify extension can collect and strictly validate a complete normalized candidate containing real playlists and Liked Songs through Spotify Desktop's authenticated Platform facade. Collection preserves ordering and duplicate occurrences and rejects incomplete required pages.

The daemon atomically persists current Spotify desired state in SQLite. `offbeat missing` reports distinct supported desired tracks without a usable managed file. An opt-in synthetic-audio test seam exercises managed-file registration and availability. **Milestone 6 is next:** minimal authorized acquisition.

On 2026-09-17 the post-M3 v1 roadmap was simplified by ADR-0010. The project now favors a current-state-first implementation over speculative historical revision, matching/review, asset-deduplication, and advanced Android-sync infrastructure.

## Components

| Component | Path | Current responsibility |
|---|---|---|
| Daemon | `cmd/offbeatd` | lifecycle, Control protocol, Adapter session, desired-state commits, managed-track availability |
| CLI | `cmd/offbeat` | `status`, `config`, `spotify sync`, and `missing` requests |
| Spicetify extension | `spicetify/offbeat` | complete normalized playlist + Liked Songs collection |
| Config | `internal/config` | TOML configuration/defaults |
| DB | `internal/db` | SQLite ownership, migrations, desired-state reconciliation, managed-file mappings |
| Managed files | `internal/managed` | confined file access and atomic synthetic WAV publication |
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

Remaining milestones after M5:

```text
M6  minimal authorized acquisition
M7  desktop M3U8 materialization
M8  one-device Android manual sync
M9  setup, packaging, and reliability
```

Historical snapshots/revision replay, fuzzy matching/review, cross-track asset deduplication, advanced deletion/integrity tooling, mDNS discovery, resumable Android transfer, background Android sync, and broad multi-device management are deferred until real usage justifies them.

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

## Planning workflow

`docs/PRD.md` and `docs/IMPLEMENTATION_PLAN.md` are baselines, not task queues.

For new implementation work:

1. use `/to-spec` for the next milestone/slice;
2. review/approve that scoped issue;
3. use `/to-tickets` for agent-ready implementation tickets;
4. use Wayfinder only when a concrete external uncertainty actually blocks implementation.

GitHub issue state and discussion are authoritative for active delivery.
