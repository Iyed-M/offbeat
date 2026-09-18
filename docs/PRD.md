# Offbeat v1 — Product Requirements Document

> **Workflow status:** This document is the v1 product baseline, not an active task queue. Use `/to-spec` to publish a scoped milestone/change as a GitHub issue and `/to-tickets` to split approved work into agent-ready issues. GitHub issue state and discussion are authoritative for active delivery.
>
> **Scope reset (2026-09-17):** ADR-0010 replaces the original infrastructure-heavy v1 roadmap with a current-state-first product. Milestones 1–3 remain valid and complete. ADR-0011 restores one concrete requirement from the earlier direction: explicit missing-set acquisition through a built-in YouTube resolver.

## 1. Product summary

Offbeat is a personal, local-first system that mirrors a user's Spotify playlists and Liked Songs into ordinary local audio files and `.m3u8` playlists on:

- one Linux desktop;
- one Android phone on the same local network.

Spotify remains the source of truth for playlist identity, names, membership, ordering, duplicate occurrences, and Liked Songs. Offbeat owns only its local representation and managed files.

Offbeat does not implement music playback. Existing desktop and Android music players consume the files it produces.

## 2. v1 product goal

The shortest successful v1 flow is:

```text
Spotify Desktop
  -> Spicetify collects complete metadata
  -> offbeatd atomically stores current desired state
  -> missing supported tracks are identified
  -> an explicit command resolves missing tracks to eligible YouTube URLs
  -> yt-dlp acquires the resolved media into the managed library
  -> desktop M3U8 playlists are generated
  -> one Android device manually syncs the current playable library
  -> ordinary local players work offline
```

The product is successful when this flow is reliable. v1 does not need to pre-solve historical replay, generalized matching, broad multi-device sync, or speculative future acquisition backends.

## 3. Primary requirements

Offbeat v1 must:

1. collect every supported Spotify playlist exposed by the established Spicetify collection path;
2. collect Liked Songs;
3. preserve playlist and Liked Songs ordering;
4. preserve duplicate playlist occurrences;
5. preserve structurally readable unsupported entries as ordered placeholders;
6. reject incomplete Spotify candidates without changing committed desired state;
7. persist the current Spotify desired state atomically in SQLite;
8. expose a simple monotonically increasing state revision when desired state changes;
9. maintain one managed local file per supported Spotify track when available;
10. identify supported desired tracks whose managed file is missing;
11. let the user explicitly request acquisition of every currently missing supported track;
12. resolve each requested track to one eligible YouTube media URL using its Spotify metadata;
13. acquire resolved media with `yt-dlp` only when the user is authorized to download it;
14. isolate per-track resolution/download failures so the rest of the missing set continues;
15. generate ordinary `.m3u8` playlists containing the playable subset in Spotify order;
16. manually synchronize the current playable library to one Android device over the local network;
17. leave already-synchronized desktop and Android files usable when Spotify, the Internet, or the desktop is later unavailable.

## 4. Explicitly deferred from v1

The following were present or implied in the original roadmap but are not required for the lean v1:

- historical Spotify snapshot retention;
- replayable revision history and a general `revision_changes` event log;
- `offbeat history` as a required command;
- cross-track physical asset deduplication;
- many-to-many Spotify-track/asset mappings;
- reference-counted automatic file deletion;
- library-wide fuzzy metadata matching and cross-track equivalence decisions;
- high/medium/low match confidence;
- persistent human match review queues and `offbeat review`;
- acoustic fingerprinting;
- scanning arbitrary external music libraries;
- a generalized multi-resolver acquisition framework;
- elaborate acquisition-attempt history and retry state machines;
- automatic destructive cleanup of the managed library;
- deep integrity scanning and `offbeat verify` as a required command;
- mDNS/DNS-SD Android discovery;
- broad multi-device management;
- arbitrary old-revision-to-new-revision replay semantics;
- resumable Android file transfers;
- Android background automatic sync;
- complex storage preflight and transactional sync protocols;
- mandatory per-file SHA-256 verification on Android;
- one-command automation of every setup/install step;
- Windows, macOS, iOS, cloud sync, Internet-based device sync, or a built-in music player.

Deferred features may be added after v1 when real usage demonstrates their value.

## 5. Safety and acquisition boundary

The acquisition subsystem must operate only on sources the user is authorized to download. The user is responsible for ensuring that a selected YouTube item may be downloaded and used as intended.

`yt-dlp` is a media retrieval backend. It is not a Spotify catalog downloader.

Offbeat v1 has one concrete source-resolution workflow: on explicit request, resolve Missing tracks against YouTube and retrieve eligible results with `yt-dlp`. It must not bypass authentication, DRM, paywalls, geographic controls, or other access controls.

YouTube candidate selection must be conservative and use the Spotify metadata already present in Desired Spotify state. Ambiguous or ineligible candidates remain unresolved. This resolver-local selection is not library-wide fuzzy matching, cross-track deduplication, or a persistent review system.

A small boundary around YouTube resolution and `yt-dlp` is appropriate so external command details do not leak through the daemon, but v1 does not require a speculative resolver/plugin framework.

## 6. Components and ownership

### 6.1 Spicetify extension

The extension is the only component that communicates with Spotify.

It must:

- run inside Spotify Desktop;
- connect outbound to `offbeatd` through the established loopback Adapter endpoint;
- authenticate with the Adapter credential;
- collect a complete normalized candidate snapshot on a daemon-issued Snapshot request;
- collect playlists and Liked Songs using the M3 adapter contract;
- preserve ordering, duplicate occurrences, and unsupported placeholders;
- send no raw Spotify Desktop response shapes to the daemon.

It must not:

- access SQLite;
- manage local files;
- acquire audio;
- communicate with Android;
- expose Spotify credentials to `offbeatd`.

### 6.2 Daemon owner

`offbeatd` remains the sole owner of:

- SQLite;
- committed Spotify desired state;
- managed-library mutation;
- acquisition work;
- playlist generation;
- Android sync serving/state owned by Offbeat.

The daemon starts independently of Spotify. Spotify being stopped must not make the existing local library unusable.

### 6.3 CLI

`offbeat` communicates with the daemon only through the existing local Control protocol.

The CLI must not:

- open SQLite directly;
- talk to Spotify directly;
- mutate managed files directly.

Required v1 commands are limited to commands that support the actual product flow. At minimum, by the end of v1 the CLI needs equivalents of:

```bash
offbeat status
offbeat config
offbeat spotify sync
offbeat missing
offbeat acquire missing
offbeat acquire <spotify-track-uri> <authorized-url>
offbeat acquire status [id]
```

Additional setup or Android diagnostic commands may be added when their milestone needs them. `history`, `review`, `verify`, and broad `devices` management are not baseline v1 requirements.

### 6.4 Android app

The Android app is a minimal synchronization client, not a Spotify client or music player.

For v1 it targets one personal phone. It may require manual desktop address/configuration. It synchronizes Offbeat-owned files into an Android-managed Offbeat directory that ordinary music players can read.

## 7. Spotify candidate semantics

The M3 candidate collection contract remains authoritative.

A candidate is acceptable only when every required playlist and Liked Songs page succeeds and the normalized candidate validates completely. A required failure rejects the candidate.

Candidate rejection must never partially modify committed Spotify desired state.

Unsupported entries remain represented at their source positions. They are not silently dropped and are not considered missing supported tracks.

Spotify folders are discovery-only and are not persisted as desired playlist hierarchy in v1.

## 8. Current Spotify desired state

### 8.1 Persistence model

M4 persists the current desired Spotify state, not a historical event stream.

The database must be able to represent at least:

- supported Spotify track metadata used by downstream features;
- playlists identified by stable Spotify URI/identity;
- mutable playlist names;
- ordered playlist entries including duplicate occurrences;
- ordered Liked Songs entries;
- unsupported ordered placeholders where supplied by M3;
- current state revision and commit timestamp.

The exact normalized M3 identity already supplied over the Adapter protocol should remain the persistence boundary. The daemon must not start decoding Spotify-private response structures.

### 8.2 Atomic commit

Applying a valid candidate occurs in one SQLite transaction.

Conceptually:

```text
validated candidate
  -> compare with current desired state
  -> BEGIN
  -> write complete new/current state
  -> update state revision when state changed
  -> COMMIT
```

Any database failure rolls back the whole candidate and leaves the previous committed desired state authoritative.

### 8.3 State revision

Offbeat maintains a monotonically increasing integer state revision.

- Increment it only when the committed desired Spotify state changes.
- A successful no-op observation does not need to create a new revision.
- The revision is a current-state version for change detection; it is not a promise that historical revisions can be replayed.

### 8.4 Sync result

`offbeat spotify sync` should report a concise useful summary after commit, such as counts of changed playlists/tracks/Liked entries. The exact wording belongs to the M4 spec.

Detailed historical diff storage is not required.

## 9. SQLite

v1 uses one SQLite database at the configured data path. Only the Daemon owner accesses it.

Schema changes use versioned migrations.

The schema should be introduced only as features need it. v1 does not need tables merely because a future architecture might use them.

By the end of the relevant milestones, SQLite needs to model:

- current Spotify desired state;
- state revision metadata;
- current managed-track availability/mapping;
- minimal restart-safe acquisition work;
- minimal Android sync/authentication state if the chosen M8 design requires persistence.

Historical snapshots, review decisions, generalized source-resolution graphs, and multi-device history are not required. Persist only the selected source and minimal resolution/acquisition outcome needed for the concrete workflow.

## 10. Managed desktop library

Default managed root:

```text
~/Music/Offbeat/
├── tracks/
└── playlists/
```

The root is configurable and owned by Offbeat. External applications may read/play managed files but should treat them as read-only.

### 10.1 Track/file model

For lean v1, one supported Spotify track has zero or one managed local audio file.

Offbeat does not initially attempt to prove that two Spotify track identities are the same recording and share one physical file.

This deliberately favors a simple and trustworthy model over storage optimization. Cross-track deduplication can be introduced later with a migration if it becomes valuable.

### 10.2 Missing tracks

A supported Spotify track is missing when current desired state references it and Offbeat has no valid managed file for it.

`offbeat missing` should report these tracks through the daemon.

Unsupported Spotify entries are not included in missing-track counts.

### 10.3 File lifecycle

v1 should be conservative about deletion. Removing a Spotify reference must immediately update desired state and generated playlists, but physical-file deletion does not need to be automatic in the first release.

No required v1 behavior may delete files outside Offbeat's managed root.

## 11. Acquisition

### 11.1 Scope

v1 supports two entry points into the same daemon-owned acquisition path:

```bash
offbeat acquire missing
offbeat acquire <spotify-track-uri> <authorized-url>
```

`offbeat acquire missing` snapshots the distinct supported Missing tracks in the current Desired Spotify state. For each track, the built-in YouTube resolver searches using Spotify title, artists, duration, and meaningful version markers, selects one eligible unambiguous result, and queues its URL for retrieval. One unresolved or failed track must not block the rest of the batch.

The direct-URL form remains available when automatic resolution is ambiguous, produces no eligible candidate, or the user prefers a different authorized source. Repeating missing-set acquisition must not create competing active work or reacquire an already available track.

The missing-set command is explicit. `offbeat spotify sync` does not silently start a large acquisition batch.

The rest of the daemon should not depend on `yt-dlp` command-line syntax directly. A small internal interface/process wrapper is sufficient.

### 11.2 Work persistence

Acquisition may take long enough that daemon restart should not silently lose requested work. Persist only the minimal states required for safe restart and user-visible failure, for example:

```text
pending
running
unresolved
failed
complete
```

Exact names are implementation-defined.

Bounded concurrency is required. A simple manual retry is acceptable for v1; complex retry scheduling is not required unless real failure behavior demonstrates a need.

`offbeat acquire missing` must report queued and already-available/active counts. `offbeat acquire status` must report aggregate and per-track pending, running, unresolved, failed, and complete outcomes without requiring a persistent human review queue.

### 11.3 Media processing

After successful authorized retrieval, Offbeat may use FFmpeg when technically necessary for a supported output/container. Avoid transcoding solely to save disk space.

Managed filenames must be filesystem-safe, stable, and collision-resistant.

Presentation metadata from Spotify may be written to files where practical, but artwork or optional tagging failure must not prevent otherwise usable audio from becoming available.

## 12. Desktop playlist generation

Generate flat UTF-8 M3U playlists under:

```text
~/Music/Offbeat/playlists/
```

At minimum:

- one Liked Songs playlist;
- one file per Spotify playlist.

Rules:

- include only tracks with an available managed file;
- preserve relative Spotify ordering among included entries;
- preserve duplicate occurrences;
- use Spotify playlist identity internally so rename does not create a second logical playlist;
- handle duplicate playlist names with deterministic collision-safe filenames;
- update/remove stale generated playlist files owned by Offbeat when safe.

An ordinary desktop music player must be able to open the generated playlists.

## 13. Android manual sync

### 13.1 Scope

Android v1 synchronizes the daemon's current playable library to one phone on the same LAN.

A manual configuration/address and a manual **Sync now** action are acceptable.

The Android app must not depend on Spotify.

### 13.2 Minimum sync model

The daemon exposes a current-state manifest sufficient for the phone to determine the desired Offbeat-owned files and playlists. The design should use stable identities/paths and sizes or other minimal metadata needed for correct current-state comparison.

The client:

1. obtains the current manifest;
2. downloads files it does not have or needs to replace;
3. places completed files into its Offbeat-owned directory;
4. removes obsolete Offbeat-owned files when the sync plan safely identifies them;
5. writes/replaces playlists after required files for that sync are available;
6. records enough local state to make the next manual sync efficient.

A failed file transfer may restart that file from the beginning. Resume support is deferred.

### 13.3 Security

The LAN API must not be anonymously writable or expose Offbeat data to arbitrary network clients. M8 must choose the smallest practical authenticated transport for one personal device. The product does not require a generalized pairing/device-management subsystem before that concrete design exists.

### 13.4 Offline result

After a successful sync, ordinary Android local music players must be able to read the synchronized audio and playlists without Spotify, Internet access, or a live desktop connection.

## 14. Setup and platform assumptions

Desktop v1 supports Linux.

Expected external prerequisites may include:

- Spotify Desktop;
- Spicetify;
- `yt-dlp`;
- FFmpeg when required by the selected media path.

Offbeat does not need to install those external applications automatically.

The project should provide clear installation/configuration documentation and may provide focused helpers for directories, Adapter credential provisioning, systemd user service setup, or extension configuration. A single `offbeat setup` command that automates every prerequisite is not a v1 completion requirement.

The daemon should be usable as a `systemd --user` service for normal Linux operation.

## 15. Reliability requirements

v1 must have automated tests for failure modes that threaten user state, especially:

- malformed/incomplete Spotify candidate rejection;
- failed SQLite candidate commit rollback;
- ordering and duplicate preservation;
- playlist rename/deletion behavior;
- daemon restart with committed Spotify state intact;
- acquisition work surviving restart when marked persistent;
- failed acquisition not blocking unrelated tracks;
- generated playlist correctness;
- interrupted Android sync leaving already-completed desktop/Android content usable;
- no mutation outside Offbeat-owned state/directories.

Tests should prefer controlled fixtures and local fakes. CI must not require Spotify, Spicetify, copyrighted external media, or a physical Android device except for explicit manual acceptance gates.

## 16. v1 acceptance scenario

v1 is complete when the following end-to-end scenario works:

1. `offbeatd` starts for the Linux user.
2. Spotify Desktop with the Offbeat Spicetify extension connects to the daemon.
3. `offbeat spotify sync` collects a complete normalized candidate.
4. The daemon atomically commits the current Spotify desired state.
5. `offbeat missing` accurately identifies supported desired tracks without managed files.
6. The user runs `offbeat acquire missing`.
7. Offbeat resolves each eligible Missing track to a YouTube URL and submits independent Acquisition work.
8. Successful acquisitions appear as managed local audio files; unresolved or failed tracks remain missing without blocking others.
9. Offbeat generates desktop `.m3u8` playlists containing available tracks in Spotify order with duplicates preserved.
10. A normal desktop player can play the managed files/playlists.
11. One Android phone manually connects/authenticates to Offbeat over the LAN and runs a sync.
12. The phone receives the current playable audio and playlists.
13. A normal Android music player can play them.
14. Already-produced desktop and phone content remains usable after Spotify, Internet access, and the desktop sync service are unavailable.

## 17. Planning rule

Do not add infrastructure to v1 solely because it might be useful later.

When a later milestone reveals a concrete need for a deferred feature, add the smallest design that solves that observed problem through a scoped issue/ADR and a versioned migration where necessary.
