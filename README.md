# offbeat

Local Spotify-replica for Linux and Android.

`offbeat` mirrors Spotify playlists and Liked Songs into a managed local audio
library, then synchronizes the playable subset to a paired Android device on
the same network. Spotify stays the source of truth. `offbeat` owns only the
local representation.

This repository is at **Milestone 2**. The daemon, CLI, and Spicetify extension
can prove their authenticated local transport with a synthetic snapshot only.
They do not yet collect, persist, or reconcile real Spotify state.

## Components

| Component | Path             | Status |
|-----------|------------------|--------|
| Daemon    | `cmd/offbeatd`   | lifecycle, Control protocol, and local adapter endpoint |
| CLI       | `cmd/offbeat`    | `status`, `config`, and synthetic `spotify sync` |
| Extension | `spicetify/offbeat` | M2 synthetic WebSocket adapter |
| Domain    | `internal/domain`| typed IDs and value types |
| Config    | `internal/config`| TOML loader with defaults |
| DB        | `internal/db`    | SQLite + migration runner |
| Logging   | `internal/logging`| `log/slog` wrapper |

## Build & test

```bash
go test ./...
go build ./cmd/offbeat
go build ./cmd/offbeatd
```

## Configuration

Default location: `~/.config/offbeat/config.toml`.

Example:

```toml
[logging]
level = "info"     # debug | info | warn | error
format = "text"    # text | json

[paths]
database = "~/.local/share/offbeat/offbeat.db"
music_root = "~/Music/Offbeat"

[acquisition]
concurrency = 2
temp_retry_backoff = "30s"
max_temp_retries = 5

[downloader]
yt_dlp_path = "yt-dlp"
ffmpeg_path = "ffmpeg"

[sync]
https_port = 0
pairing_timeout = "5m"
```

CLI flags override the config path:

```bash
offbeatd --config /path/to/config.toml --home /tmp/sandbox
offbeat  --config /path/to/config.toml status
```

## M2 development credential

Until `offbeat setup` is implemented, the daemon requires a development-only
adapter credential from `OFFBEAT_ADAPTER_CREDENTIAL`. This value is not TOML
configuration and is never included in daemon status or effective-config
output. Use a locally chosen secret when developing the adapter transport:

```bash
export OFFBEAT_ADAPTER_CREDENTIAL='development-only-secret'
offbeatd
```

The adapter listener binds to `ws://127.0.0.1:16352/v1/adapter` by default.
Only literal loopback addresses and nonzero ports are accepted. The adapter
authenticates as its first WebSocket message and supports one active session.

## M2 Spicetify smoke test

The manual development procedure, including the configured extension artifact
and complete connect, disconnect, and reconnect smoke sequence, is in
[`spicetify/offbeat/README.md`](spicetify/offbeat/README.md). It is not a
production installation flow: Milestone 12 owns credential provisioning,
Spicetify installation/linking, and `offbeat setup`.

## Project layout

```text
offbeat/
├── cmd/
│   ├── offbeat/         CLI entry point
│   └── offbeatd/        Daemon entry point
├── internal/
│   ├── app/             Daemon lifecycle
│   ├── config/          TOML loader
│   ├── db/              SQLite + migrations
│   ├── domain/          Typed IDs and value types
│   └── logging/         slog wrapper
├── migrations/          Future SQLite migrations (M0: empty)
├── testdata/            Test fixtures
├── docs/                PRD and implementation plan
└── .github/workflows/   CI
```

## Roadmap

This repository is implementing the milestone plan in `docs/IMPLEMENTATION_PLAN.md` and currently working on Milestone 2.
