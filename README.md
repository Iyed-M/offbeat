# offbeat

Local-first Spotify-organized music for Linux and Android.

Offbeat mirrors Spotify playlists and Liked Songs into a managed local audio library, generates ordinary `.m3u8` playlists, and synchronizes the current playable library to one Android phone on the same network. Spotify remains the source of truth for organization; Offbeat owns only its local state and files.

## Current status

Milestones 1–3 are complete.

The daemon, CLI, and Spicetify extension can collect and strictly validate a complete normalized candidate containing real playlists and Liked Songs through Spotify Desktop's authenticated Platform facade. Collection preserves ordering and duplicate occurrences and rejects incomplete required pages.

The candidate is not persisted yet. **Milestone 4 is next:** atomically persist the current Spotify desired state in SQLite.

On 2026-09-17 the post-M3 v1 roadmap was simplified by ADR-0010. The project now favors a current-state-first implementation over speculative historical revision, matching/review, asset-deduplication, and advanced Android-sync infrastructure.

## Components

| Component | Path | Current responsibility |
|---|---|---|
| Daemon | `cmd/offbeatd` | lifecycle, Control protocol, Adapter endpoint/session, candidate validation |
| CLI | `cmd/offbeat` | `status`, `config`, and `spotify sync` requests |
| Spicetify extension | `spicetify/offbeat` | complete normalized playlist + Liked Songs collection |
| Domain | `internal/domain` | foundational typed IDs/value types; future skeleton types are not binding architecture |
| Config | `internal/config` | TOML configuration/defaults |
| DB | `internal/db` | SQLite ownership + migration runner; product schema begins in M4 |
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

Remaining milestones after M3:

```text
M4  persist current Spotify desired state
M5  managed tracks + missing state
M6  minimal authorized acquisition
M7  desktop M3U8 materialization
M8  one-device Android manual sync
M9  setup, packaging, and reliability
```

Historical snapshots/revision replay, fuzzy matching/review, cross-track asset deduplication, advanced deletion/integrity tooling, mDNS discovery, resumable Android transfer, background Android sync, and broad multi-device management are deferred until real usage justifies them.

## Planning workflow

`docs/PRD.md` and `docs/IMPLEMENTATION_PLAN.md` are baselines, not task queues.

For new implementation work:

1. use `/to-spec` for the next milestone/slice;
2. review/approve that scoped issue;
3. use `/to-tickets` for agent-ready implementation tickets;
4. use Wayfinder only when a concrete external uncertainty actually blocks implementation.

GitHub issue state and discussion are authoritative for active delivery.
