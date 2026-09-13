# Offbeat v1 — Product Requirements Document

## 1. Product Summary

Offbeat is a personal, local-first system that mirrors a user's Spotify playlists and Liked Songs into an offline music library available on:

* a Linux desktop;
* an Android phone on the same local network.

Spotify remains the source of truth for playlist membership, playlist ordering, playlist names, and Liked Songs.

Offbeat consists of four primary components:

1. a Spicetify extension running inside Spotify Desktop;
2. an always-running Go daemon on Linux;
3. a Linux CLI that controls and inspects the daemon;
4. a minimal native Android synchronization application.

Offbeat does not implement music playback. On desktop and Android it produces ordinary audio files plus standard `.m3u8` playlists that can be consumed by existing music players.

---

# 2. Problem Statement

The user has Spotify Desktop on Linux without Spotify Premium and wants their Spotify playlists and Liked Songs available offline on both their PC and Android phone.

The system must:

* read Spotify playlist and Liked Songs metadata from the running Spotify Desktop client through Spicetify;
* maintain a local representation of that Spotify state;
* reconcile that desired state against locally available audio;
* acquire authorized media through a pluggable acquisition pipeline whose v1 downloader backend is `yt-dlp`;
* maintain a managed local music library;
* generate offline playlists;
* synchronize the playable library to Android over the local network;
* preserve usability even when Spotify, the Internet, or the desktop is later unavailable.

Spotify itself is not used for offline audio storage.

---

# 3. Product Goals

## 3.1 Primary goals

Offbeat v1 must:

1. mirror all normal Spotify music playlists;
2. mirror all Liked Songs;
3. preserve Spotify playlist ordering;
4. keep Spotify as the source of truth;
5. maintain one managed local audio library;
6. identify missing Spotify tracks;
7. automatically process missing tracks through the acquisition pipeline after first-time approval;
8. expose uncertain matches for human review;
9. generate `.m3u8` playlists for all playable tracks;
10. synchronize the current playable Spotify-referenced library to Android;
11. make synchronized Android audio usable by ordinary local music players;
12. remain usable offline after synchronization.

---

# 4. Non-Goals

The following are explicitly outside v1:

* Spotify Premium functionality;
* extracting or decrypting Spotify's cached audio;
* a music player;
* desktop GUI;
* desktop web UI;
* Windows support;
* macOS support;
* iOS support;
* editing Spotify playlists from Offbeat;
* editing Spotify Liked Songs from Offbeat;
* bidirectional synchronization with Spotify;
* Android-to-PC metadata edits;
* Spotify integration on Android;
* podcast support;
* Spotify playlist-folder preservation;
* acoustic fingerprinting;
* per-device audio transcoding;
* dedicated backup tooling;
* cloud synchronization;
* Internet-based device synchronization;
* automatic installation of Spotify, Spicetify, `yt-dlp`, or FFmpeg.

---

# 5. Safety and Acquisition Boundary

The acquisition subsystem must only operate on sources the user is authorized to download.

`yt-dlp` is a media retrieval backend, not Offbeat's Spotify catalog downloader.

The product architecture must separate:

```text
Spotify metadata
      ↓
logical desired track
      ↓
SourceResolver
      ↓
authorized source candidate
      ↓
Downloader
      ↓
yt-dlp backend
```

The v1 implementation must not hard-code an automatic workflow whose purpose is to search arbitrary copyrighted Spotify tracks on third-party services solely to bypass Spotify's offline/Premium restrictions.

The resolver architecture must remain pluggable.

---

# 6. High-Level Architecture

```text
                           Spotify Desktop
                                  │
                                  │
                         Spicetify Extension
                                  │
                      localhost WebSocket
                                  │
                                  ▼
                    ┌────────────────────────┐
                    │       offbeatd        │
                    │                        │
                    │ Spotify reconciliation │
                    │ SQLite                 │
                    │ matching               │
                    │ review queue           │
                    │ acquisition queue      │
                    │ yt-dlp backend         │
                    │ FFmpeg normalization   │
                    │ metadata tagging       │
                    │ M3U8 generation        │
                    │ revision management    │
                    │ device pairing         │
                    │ LAN synchronization    │
                    └───────────┬────────────┘
                                │
                    ┌───────────┴────────────┐
                    │                        │
                    ▼                        ▼
           ~/Music/Offbeat/        HTTPS LAN sync API
                                             │
                                             ▼
                                    Android companion
                                             │
                                             ▼
                                     Music/Offbeat/
                                             │
                                             ▼
                                    existing music player
```

---

# 7. Component Responsibilities

## 7.1 Spicetify extension

The extension is the only component that communicates with Spotify.

Responsibilities:

* run inside Spotify Desktop;
* connect outbound to `offbeatd` using localhost WebSocket;
* identify itself using a locally generated adapter credential;
* collect the user's complete Spotify state;
* collect all normal music playlists;
* collect all Liked Songs;
* preserve playlist ordering;
* preserve duplicate playlist entries;
* send playlist names and Spotify playlist IDs;
* send track metadata;
* send Spotify track identity;
* send duration and artwork references when available;
* answer daemon-triggered snapshot requests;
* perform an automatic snapshot on extension startup.

It must not:

* download audio;
* manage SQLite;
* modify Spotify;
* perform filesystem management;
* communicate with Android.

---

# 8. Spotify Snapshot Semantics

## 8.1 Full snapshot atomicity

Every Spotify synchronization produces a candidate snapshot.

A candidate snapshot is valid only if Offbeat successfully obtains:

* the complete playlist list;
* every page of Liked Songs;
* every page of every supported playlist;
* expected counts where Spotify exposes them.

If any required request fails:

```text
candidate snapshot
       ↓
   discarded
```

The previous committed Spotify state remains authoritative.

No partial update may modify the desired local library.

---

## 8.2 Snapshot contents

A logical snapshot contains at minimum:

```text
snapshot_id
spotify_user_id
captured_at

liked_songs:
    total_count
    ordered_entries[]

playlists[]:
    spotify_playlist_id
    name
    total_track_count
    ordered_entries[]
```

Each supported track entry should include, where available:

```text
spotify_track_id / URI
title
artists
album
duration
track number
disc number
artwork reference
entry position
```

---

## 8.3 Supported Spotify entries

v1 supports normal Spotify catalog music tracks.

Unsupported entries, including unsupported media types, remain represented logically as placeholders with a reason such as:

```text
unsupported_type
```

They must not break snapshot import.

---

## 8.4 Eventual consistency

Spotify does not provide Offbeat with a cross-library transactional snapshot.

Offbeat therefore accepts that Spotify may change while a snapshot is being collected.

A successfully fetched snapshot is treated as a valid observation.

Later snapshots converge Offbeat toward newer Spotify state.

---

# 9. Spotify Synchronization Triggers

v1 supports:

### Automatic startup sync

```text
Spotify starts
→ Spicetify extension loads
→ extension connects to daemon
→ complete snapshot collected
→ daemon commits snapshot
```

### Manual CLI sync

```bash
offbeat spotify sync
```

The CLI:

1. asks the daemon to request a snapshot from the connected extension;
2. waits for completion;
3. reports success/failure;
4. prints the resulting diff.

If Spotify/Spicetify is unavailable:

```text
Spotify adapter is not connected.
```

The existing local library remains fully usable.

---

# 10. Daemon

## 10.1 Lifecycle

The daemon executable is:

```text
offbeatd
```

It runs continuously as a Linux user service:

```text
systemd --user
```

It starts independently of Spotify.

Spotify being stopped must not stop Offbeat.

---

## 10.2 Daemon ownership

`offbeatd` exclusively owns:

* SQLite;
* managed-library mutation;
* acquisition work;
* playlist generation;
* revision creation;
* Android sync state;
* pairing state.

No other Offbeat component may directly mutate the database.

---

# 11. CLI

The executable is:

```text
offbeat
```

The CLI never directly:

* opens SQLite;
* talks to Spotify;
* edits managed files.

It communicates with the daemon through local IPC.

Preferred v1 IPC:

```text
Unix domain socket
```

---

# 12. Required CLI Commands

At minimum:

```bash
offbeat status
offbeat spotify sync
offbeat review
offbeat missing
offbeat retry
offbeat pair
offbeat devices
offbeat history
offbeat verify
offbeat config
offbeat setup
```

An explicit initial-acquisition approval mechanism must also exist, for example:

```bash
offbeat acquire --approve-initial
```

Exact command naming may vary if the semantics remain equivalent.

---

# 13. SQLite

v1 uses one SQLite database.

Default location:

```text
~/.local/share/offbeat/offbeat.db
```

Only the daemon accesses it.

Schema changes must use versioned migrations.

SQLite should model at least:

* Spotify snapshots/revisions;
* Spotify tracks;
* playlists;
* ordered playlist entries;
* Liked Songs;
* local assets;
* Spotify-track → asset mappings;
* acquisition work;
* acquisition attempts;
* source mappings;
* review decisions;
* Android devices;
* device credentials;
* sync revisions;
* lightweight revision history.

---

# 14. Managed Desktop Library

Default media root:

```text
~/Music/Offbeat/
```

Layout:

```text
~/Music/Offbeat/
├── tracks/
└── playlists/
```

The root must be configurable.

Offbeat owns the directory structure.

External applications may read/play the files but must treat Offbeat-managed content as read-only.

---

# 15. Track and Asset Model

A Spotify track and a physical audio asset are separate concepts.

Example:

```text
spotify:track:A ─┐
                 ├── local asset X
spotify:track:B ─┘
```

Multiple Spotify track IDs may map to one physical asset.

A physical asset may be deleted only when no currently desired Spotify track references it.

---

# 16. Matching and Deduplication

v1 uses metadata-based matching.

Relevant signals include:

* normalized track title;
* normalized artist set;
* duration;
* album as a supporting signal;
* meaningful version markers.

The matcher must not blindly erase semantic differences such as:

```text
Live
Acoustic
Remix
Radio Edit
Extended Mix
Instrumental
```

Album equality is not mandatory for deduplication.

---

# 17. Match Confidence

Matches are classified as:

```text
high
medium
low
```

## High confidence

May be accepted automatically.

It requires strong metadata agreement and no meaningful conflicting version information.

## Medium confidence

Must enter the review queue.

Example:

```bash
offbeat review
```

The user may:

```text
accept
reject
skip
```

Accepted/rejected decisions persist.

## Low confidence

Remains unresolved.

An unresolved track must never block other tracks from becoming available.

---

# 18. Missing Tracks

A Spotify track is considered missing when:

* Spotify desires it;
* no valid local asset is mapped to it.

Missing tracks remain fully represented in SQLite.

Example:

```text
Spotify playlist:

A
B [missing]
C
D [missing]
E
```

Generated M3U8:

```text
A
C
E
```

Playable tracks retain their Spotify relative ordering.

---

# 19. Acquisition Pipeline

v1 supports only one downloader implementation:

```text
yt-dlp
```

The internal architecture must still separate the downloader through an interface so the rest of the daemon does not depend on yt-dlp command-line details.

Logical states may include:

```text
pending
resolving
downloading
normalizing
ready
failed-temporary
unresolved
failed-permanent
review-required
```

Exact internal names are implementation-defined.

---

# 20. Acquisition Queue

Acquisition work must:

* persist in SQLite;
* survive daemon restarts;
* have bounded concurrency;
* default to a conservative concurrency such as 2;
* allow configuration;
* classify temporary vs permanent/unresolved failures;
* retry temporary failures with capped exponential backoff;
* avoid infinite retries for permanent/unresolved failures.

---

# 21. Auto-Acquisition

After every successful Spotify reconciliation:

```text
new missing tracks
      ↓
auto_acquire?
```

When:

```toml
auto_acquire = true
```

eligible missing tracks automatically enter acquisition.

When false:

* metadata sync still completes;
* playlists still reconcile;
* missing state still updates;
* acquisition does not start until explicitly requested.

---

# 22. First Bootstrap

The first successful Spotify snapshot is special.

If the library initially contains a large number of missing tracks, acquisition must not start silently.

Example:

```text
Spotify import complete.

Desired tracks: 2013
Available: 0
Missing: 2013

Initial acquisition approval required.
```

The daemon enters a bootstrap-pending state.

The user explicitly approves initial acquisition through the CLI.

After that:

```text
auto_acquire = true
```

works normally for future changes.

---

# 23. Media Processing

The acquisition pipeline is logically:

```text
SourceResolver
      ↓
Downloader
      ↓
temporary media
      ↓
Normalizer
      ↓
Tagger
      ↓
managed asset
```

`yt-dlp` is responsible for media retrieval.

Offbeat owns normalization/tagging.

FFmpeg is used only when technically necessary.

---

# 24. Audio Quality Policy

v1 must avoid over-engineering audio quality.

Rule:

> Quality takes priority over disk usage.

Offbeat should:

* preserve good source quality;
* avoid transcoding simply to save storage;
* accept FLAC;
* accept MP3;
* accept AAC/M4A;
* accept Opus;
* use FFmpeg when compatibility/container/tagging requirements justify it;
* maintain one canonical asset representation for both desktop and Android.

No per-device quality variants exist in v1.

---

# 25. Metadata and Artwork

Spotify is authoritative for presentation metadata where available.

Managed assets should receive:

* title;
* artist;
* album;
* track/disc metadata when appropriate;
* artwork.

Artwork should be embedded when supported by the container.

Artwork-fetch failure:

* must not block audio availability;
* should remain retryable.

---

# 26. Managed Filenames

Track filenames should be:

* human-readable;
* stable;
* collision-resistant.

Recommended pattern:

```text
<artist> - <title> [<stable-id>].ext
```

Sanitization is required for filesystem safety.

The stable suffix prevents collisions.

---

# 27. Playlist Generation

Desktop playlists live under:

```text
~/Music/Offbeat/playlists/
```

Format:

```text
M3U8
```

Playlists are flat.

Spotify playlist-folder structure is ignored.

---

# 28. Playlist Identity and Names

Spotify playlist ID is the persistent identity.

Playlist name is mutable presentation data.

When Spotify renames:

```text
Coding
→ Programming
```

the generated M3U8 filename should rename accordingly.

Old generated playlist files must be removed.

---

# 29. Duplicate Playlist Names

When names are unique:

```text
Coding.m3u8
```

When multiple Spotify playlists share the same name, append a short stable identity suffix:

```text
Coding [a1b2c3].m3u8
Coding [f9e8d7].m3u8
```

Do not expose IDs unnecessarily when there is no collision.

---

# 30. Liked Songs

Liked Songs is modeled separately from Spotify playlists internally but materialized as:

```text
Liked Songs.m3u8
```

Its Spotify ordering must be preserved.

---

# 31. Duplicate Playlist Entries

If Spotify intentionally includes the same track multiple times in a playlist, Offbeat must preserve every occurrence.

One physical asset may therefore appear multiple times in one M3U8 file.

---

# 32. Removal Semantics

Spotify is authoritative for desired state.

If a track becomes unreferenced by:

* all playlists;
* and Liked Songs;

then its local asset becomes eligible for deletion.

Deletion behavior is immediate at the logical state level.

Physical deletion may wait until Offbeat-controlled operations release the file.

If an asset is shared by other still-desired Spotify tracks, it must remain.

---

# 33. Missing/Corrupt Local Assets

If Offbeat detects that a referenced managed asset:

* disappeared;
* is invalid;
* fails explicit verification;

the corresponding track returns to missing state.

When auto-acquire is enabled, acquisition may automatically be queued again.

Normal reconciliation should perform lightweight existence checking.

Full integrity verification occurs through:

```bash
offbeat verify
```

which may perform SHA-256 verification.

---

# 34. Revision History

Offbeat should retain lightweight revision/diff history.

Example:

```text
Revision 42
+7 tracks
-2 tracks
Coding: +3 / -1
```

The system does not need to retain every complete raw Spotify snapshot indefinitely.

History exists primarily for:

* debugging;
* explaining automatic deletions;
* inspection.

---

# 35. Android Companion

The Android component is a native Kotlin application.

Recommended technologies:

* Kotlin;
* Jetpack Compose;
* WorkManager;
* Android shared media storage / MediaStore.

It is a sync utility only.

It does not implement:

* playback;
* library browsing;
* search;
* playlist editing;
* Spotify login;
* Spotify metadata access.

---

# 36. Android UI Scope

Minimum UI:

```text
Pair PC

Home:
    PC status
    last sync
    current revision
    number of tracks
    storage usage
    sync progress

    [Sync now]

Settings:
    Auto Sync
    Pair/Unpair
    connection status
```

Error/progress states must be visible.

---

# 37. Android Storage

Default output:

```text
Music/Offbeat/
├── tracks/
└── playlists/
```

Audio must live in shared media storage so ordinary Android music players can access it.

Playlists use standard `.m3u8`.

Android is a read-only replica.

It never independently:

* edits tags;
* renames Offbeat files;
* changes playlists;
* sends playlist/like modifications upstream.

---

# 38. Android Desired State

Android receives only the union of currently Spotify-referenced, locally playable tracks.

It does not automatically receive unrelated/orphaned desktop assets.

Android playlists mirror the daemon-generated playable playlist state.

---

# 39. Pairing

Pairing uses a one-time code.

Example:

```bash
$ offbeat pair

Waiting for device...
Pairing code: 482913
```

Android discovers or manually connects to the daemon and submits the code.

Successful pairing creates:

* a persistent device ID;
* a per-device credential;
* trust in the daemon's certificate/fingerprint.

No cloud account is required.

---

# 40. LAN Discovery

Preferred discovery:

```text
mDNS / DNS-SD
```

Example conceptual service:

```text
_offbeat._tcp.local
```

Manual host/IP entry must exist as a fallback.

Authentication, not SSID identity, determines trust.

---

# 41. Transport Security

PC ↔ Android synchronization uses HTTPS.

The daemon generates its own certificate.

During pairing, Android pins the daemon certificate/fingerprint.

Subsequent requests use the device credential over the encrypted channel.

No external certificate authority is required.

---

# 42. Multi-Device Model

The domain model supports multiple read-only client devices.

Each device may have:

```text
device_id
name
credential
last_sync
last_revision
```

v1 needs to be implemented and tested with only one Android device.

---

# 43. Sync Initiation

Android initiates synchronization.

Supported modes:

### Manual

```text
Sync now
```

### Automatic

WorkManager opportunistically performs sync when:

* Wi-Fi/network conditions permit;
* the paired Offbeat daemon is discoverable/reachable.

Instant background synchronization is not required.

---

# 44. Revisioned Sync

The daemon publishes immutable logical library revisions.

A revision manifest contains at minimum:

* revision ID;
* required assets;
* asset paths/identities;
* content hashes;
* playlist files and hashes.

Android may jump directly from any old state to the newest revision.

Historical revisions do not need to be replayed.

---

# 45. Delta Sync

Android compares its current Offbeat state against the newest manifest.

The resulting plan may contain:

```text
download assets
delete assets
replace playlists
```

All diff logic should preferably remain daemon-driven or manifest-driven rather than reproducing Spotify semantics on Android.

---

# 46. Transfer Integrity

Asset downloads support:

```text
HTTP Range
```

for resuming interrupted transfers.

Every completed file must be checked against its expected SHA-256 before publication.

Files are downloaded to temporary state first.

Only verified files become visible as final managed media.

---

# 47. Android Commit Semantics

Synchronization is not globally transactional, but must preserve usability.

Required order:

1. compute plan;
2. verify sufficient storage;
3. download required files;
4. resume interrupted files when possible;
5. verify each completed file;
6. publish verified assets;
7. remove obsolete Offbeat assets according to the plan;
8. replace playlist files last;
9. commit local revision marker.

If sync fails midway, the previously valid playlists should remain usable.

---

# 48. Android Storage Preflight

Before modifying the Android library, Offbeat must determine whether enough storage is available.

If storage is insufficient:

```text
Need: X
Free: Y
```

then synchronization must stop before making any changes.

v1 does not partially synchronize merely because some files fit.

---

# 49. Configuration

Default config path:

```text
~/.config/offbeat/config.toml
```

Configuration may include:

```text
managed media path
auto_acquire
acquisition concurrency
yt-dlp executable path
ffmpeg executable path
local ports
sync settings
```

CLI flags may temporarily override selected settings.

Secrets must not be stored as plain ordinary config values where avoidable.

---

# 50. Local Adapter Authentication

The Spicetify WebSocket binds only to loopback.

A random adapter credential is generated during setup.

The extension uses that credential when connecting.

---

# 51. Setup

```bash
offbeat setup
```

should configure components Offbeat controls:

* config/data directories;
* SQLite initialization;
* certificates/secrets;
* systemd user service;
* Spicetify extension installation/linking;
* prerequisite checks.

It must check for:

```text
Spotify
Spicetify
yt-dlp
FFmpeg
```

but does not install those external dependencies itself.

---

# 52. Platform Requirements

Desktop v1:

```text
Linux only
systemd --user
Spotify Desktop
Spicetify
yt-dlp
FFmpeg
```

Android:

```text
native Android application
```

Cross-platform desktop support is deferred.

---

# 53. Observability

The daemon must produce useful structured logs for:

* startup;
* Spotify adapter connection/disconnection;
* snapshot success/failure;
* reconciliation;
* acquisition;
* review decisions;
* file deletion;
* Android pairing;
* Android sync;
* errors.

Logs must never expose long-lived device/adapter credentials.

---

# 54. Reliability Requirements

The implementation must tolerate:

* daemon restart;
* Spotify restart;
* Spotify being absent;
* failed snapshot fetch;
* network interruption;
* failed acquisition;
* interrupted Android transfer;
* Android being offline for long periods;
* phone jumping many revisions;
* managed file disappearing;
* playlist rename;
* playlist deletion;
* duplicate playlist names.

---

# 55. Testing Requirements

Tests are mandatory.

At minimum, automated coverage must exist for:

### Spotify state

* full-snapshot commit;
* failed candidate snapshot rollback;
* playlist rename;
* playlist deletion;
* ordering;
* duplicate entries;
* Liked Songs;
* unsupported placeholder entries.

### Reconciliation

* added track;
* removed track;
* shared asset reference counting;
* deletion only after last reference disappears;
* missing asset recovery.

### Matching

* high-confidence auto-match;
* medium-confidence review;
* rejected match persistence;
* duplicate metadata cases;
* meaningful version distinction.

### Acquisition

* persistent queue;
* bounded concurrency;
* retryable failure;
* permanent/unresolved failure;
* restart recovery.

### Playlist generation

* relative order preservation;
* missing-track omission;
* duplicate playlist names;
* rename cleanup.

### Android sync

* pairing;
* manifest generation;
* direct old→latest revision reconciliation;
* interrupted transfer resume;
* hash mismatch;
* storage preflight;
* playlist replacement last;
* asset deletion;
* reconnect after daemon restart.

End-to-end tests must use synthetic/test media, not depend on real copyrighted media.

---

# 56. Main v1 Acceptance Scenario

v1 is complete when the following works end-to-end:

1. Linux login starts `offbeatd` through `systemd --user`.
2. Spotify starts.
3. Offbeat's Spicetify extension connects to the daemon.
4. The extension collects all supported playlists and Liked Songs.
5. The daemon receives a complete valid candidate snapshot.
6. The snapshot is atomically committed.
7. Missing tracks are identified.
8. Initial acquisition waits for explicit first-time approval.
9. After approval, eligible missing tracks enter the persistent acquisition pipeline.
10. Successfully acquired media becomes managed Offbeat assets.
11. Metadata/artwork is applied where possible.
12. Desktop M3U8 playlists are generated.
13. Playable tracks preserve Spotify ordering.
14. A subsequent Spotify snapshot correctly handles additions, removals, and renames.
15. Assets no longer referenced by Spotify are deleted when their reference count reaches zero.
16. Android discovers or manually connects to the daemon.
17. Android pairs with a one-time code.
18. Android requests the newest revision.
19. Android performs storage preflight.
20. Android downloads only required playable assets.
21. Interrupted downloads can resume.
22. SHA-256 verification succeeds before publication.
23. Android playlists are committed after their required assets.
24. An existing Android local music player can read the files/playlists.
25. After Spotify, the Internet, and the Linux PC become unavailable, already-synchronized Android tracks remain playable offline.

---

# 57. Definition of v1 Done

v1 is done only when:

* all acceptance scenario steps work;
* all destructive synchronization behavior has automated tests;
* first-time setup can be reproduced on a clean supported Linux environment;
* daemon restarts do not lose acquisition work or Spotify state;
* no component other than the daemon owns SQLite;
* Android contains no Spotify integration;
* playback works using external music players;
* documentation describes setup, dependencies, architecture, recovery, and known limitations.

