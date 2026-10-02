# Current-state LAN sync

The daemon provides the desktop half of one-phone manual sync. LAN serving is disabled by default (`sync.https_port = 0`). The Android storage/client work is delivered separately under M8.

## Enable and provision

Choose the desktop's LAN IP and an unused port in `~/.config/offbeat/config.toml`:

```toml
[sync]
lan_bind_address = "192.168.1.20"
https_port = 8443
```

Restart `offbeatd`, then run:

```sh
offbeat sync setup
```

This explicit private command prints JSON containing `address`, `certificate_sha256` and `credential`. Transfer all three values out of band to the phone. Keep the credential private. Setup reads the running daemon through the owner-only Control socket; the CLI never opens SQLite or manages secrets itself.

If binding to `0.0.0.0` or `::`, replace that wildcard in the provisioned address with the desktop's reachable LAN IP. Keep this listener on the local network. The Control socket and loopback Spicetify Adapter endpoint remain separate.

`offbeat sync status` prints enabled/address/trust information without the phone credential. `offbeat status` and `offbeat config` also omit it. `offbeat sync reset` persists and prints a new phone credential; subsequent requests with the old credential fail. Reset preserves desktop trust. Setup repeated after restart returns the same identity and credential.

The daemon owns `lan-sync.json` in the configured `paths.certs_dir` (directory mode 0700, file mode 0600). It contains one TLS certificate/private key and a distinct 256-bit phone credential, and is written with atomic replacement and file/directory sync. Invalid or non-private identity files stop LAN startup instead of silently changing trust. The self-signed Ed25519 identity is valid for ten years. To deliberately replace a lost/expired identity, stop the daemon, remove that private identity file, restart and explicitly provision the phone again with the new certificate fingerprint and credential.

## HTTPS protocol v1

Authenticate every request using `Authorization: Bearer <credential>` over TLS 1.3. Validate the exact provisioned certificate SHA-256 fingerprint and certificate validity during the TLS handshake **before** sending the credential. Public CA/hostname trust is replaced by this exact identity pin. Never accept an unpinned certificate. Redirects are not followed.

Only two read operations exist:

- `GET /v1/sync/manifest`
- `GET /v1/sync/files/{tracks|playlists}/{hashed-identity}?version={content-version}`, using the manifest's `download` reference verbatim.

The manifest contains `version`, `content_version`, informational `state_revision`, `tracks` and `playlists`. Each entry contains stable `id`, root-relative destination `path`, byte `size`, SHA-256 `content_version` and an authenticated `download` reference. Liked Songs uses reserved identity `offbeat:liked-songs`. Spotify playlist identities survive rename; safe filenames use the existing desktop collision rules. Track paths are canonical `tracks/...`; playlist paths are `playlists/...` and M3U8 references use `../tracks/...`.

Only currently desired playable audio is advertised, once per track identity. Missing tracks, unsupported placeholders and retained unreferenced audio are omitted. Playlists, including empty playlists and Liked Songs, are rendered from the same observation as audio, preserving order and duplicates. Existing M3U8 rendering and descriptor-based duration probing provide safe EXTINF text when the media can be probed. No directory browsing, source URLs, absolute desktop paths, credentials or desktop mutations are exposed.

Versions identify published bytes, including tag/artwork replacements of the same size. The daemon hashes confined descriptors and caches versions/durations against file identity, size, modification time and change time, rather than assuming recorded acquisition hashes remain current. The manifest content version covers playable identities, paths and file versions; it excludes informational State revision. Consequently a new acquisition or metadata refresh can change it without a Spotify commit.

A download opens a confined descriptor from a fresh coherent observation. It serves exactly the requested version or rejects it. An already-open descriptor survives atomic managed replacement and may finish serving the advertised old bytes. A later request for an obsolete version gets HTTP 409; an unknown, removed, missing or unsafe file gets HTTP 404. Fetch a fresh manifest after either response and bound retries (for example, two retries per manual sync). No historical files/manifests are retained by the protocol. Downloads release desktop mutation locks before hashing, probing and network writes.

## Limits and failure responses

Manifests are limited to 8 MiB, with at most 4,096 desired track/playlist entries including Liked Songs. Audio files are nonempty regular files of at most 512 MiB. An observation exceeding bounds or descriptor capacity fails entirely rather than advertising a truncated library. Paths come from canonical managed mappings and existing bounded playlist filenames, never a requested filesystem path. Symlink files and symlink track directories are excluded.

There are at most four concurrent observations/transfers. Excess requests return HTTP 503. Requests have 8 KiB header and 1,024-byte request-target limits, a five-second header timeout, a 90-second observation deadline, a two-minute write timeout and a 30-second idle timeout. Range transfers are rejected; interrupted transfers restart from byte zero. Shutdown cancels observations and interrupts active transfers before releasing SQLite or the managed root.

HTTP 401 means authentication failed; 405 means the method is not GET; 400 means an invalid version/query or unsupported Range request; 404 means no advertised resource; 409 means a stale content version; 503 means busy/unavailable/over-limit. Diagnostics are bounded and do not reflect credentials or filesystem paths.

The Go fake phone/reference client in `internal/lansync` pins desktop identity before authentication and bounds manifest reads. Daemon acceptance tests use real temporary managed files and synthetic audio to cover authentication, trust mismatch, reset/restart, disabled serving, coherent playlists, actual download bytes, version changes, stale requests, traversal/symlinks, slow concurrent replacement and cancellation. Normal CI needs no phone, Spotify or Internet service.
