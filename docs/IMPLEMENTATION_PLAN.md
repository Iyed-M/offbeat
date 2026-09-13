# Offbeat v1 — SMART Implementation Plan

> **Workflow status:** This document is the delivery roadmap, not an active task queue. Before implementation, use `/to-tickets` to turn the relevant milestone or smaller slice into dependency-linked GitHub issues. Track execution, decisions, and completion in those issues; update this roadmap only when sequencing or milestone scope changes.

## 1. Planning Principles

Implementation should proceed in vertical slices.

Every milestone must produce:

* executable software;
* automated tests;
* a demonstrable behavior;
* a clear completion gate before dependent work begins.

The sequencing should minimize simultaneous uncertainty.

The project should first prove Spotify ingestion and reconciliation, then the local managed library, then acquisition, and only then Android synchronization.

---

# 2. SMART Definition

Every milestone below is:

**Specific**
It defines one concrete capability.

**Measurable**
It has explicit automated tests and acceptance conditions.

**Achievable**
It limits scope and avoids unrelated future functionality.

**Relevant**
It directly contributes to the v1 acceptance scenario.

**Time-bound**
It has a recommended engineering timebox.

The timeboxes are planning constraints, not promises. If a milestone exceeds its box, investigate scope/architecture rather than silently expanding it.

---

# 3. Recommended Repository Structure

Use one repository:

```text
offbeat/
├── cmd/
│   ├── offbeat/
│   └── offbeatd/
│
├── internal/
│   ├── app/
│   ├── config/
│   ├── db/
│   ├── spotify/
│   ├── reconcile/
│   ├── library/
│   ├── matcher/
│   ├── acquisition/
│   ├── downloader/
│   │   └── ytdlp/
│   ├── media/
│   ├── playlists/
│   ├── devices/
│   ├── sync/
│   └── ipc/
│
├── migrations/
│
├── spicetify/
│   └── offbeat/
│
├── android/
│
├── testdata/
│
├── docs/
│
├── scripts/
│
├── go.mod
└── README.md
```

Architectural rule:

```text
domain packages
    must not depend on
CLI / HTTP / Spicetify / Android details
```

---

# 4. Milestone 0 — Repository and Architecture Skeleton

## Timebox

**1–2 days**

## Specific objective

Create the repository foundation without implementing product behavior.

## Deliverables

Create:

```text
offbeatd
offbeat
Go module
SQLite migration runner
config loader
logging
testdata
CI
```

Define core domain IDs/types for:

```text
SpotifyTrack
Playlist
PlaylistEntry
LocalAsset
Snapshot
Revision
Device
AcquisitionJob
```

Do not implement the full schema yet.

## Measurable acceptance criteria

The following commands must work:

```bash
go test ./...
go build ./cmd/offbeat
go build ./cmd/offbeatd
```

`offbeatd` starts, loads config, initializes an empty SQLite DB, and exits cleanly on SIGTERM.

CI runs formatting/vetting/tests.

## Completion gate

Do not begin feature development until:

```text
build green
tests green
migration framework working
daemon lifecycle working
```

---

# 5. Milestone 1 — Daemon Ownership and CLI IPC

## Timebox

**2–3 days**

## Specific objective

Establish the architectural rule that the daemon owns all state.

## Deliverables

Implement Unix-domain-socket control API.

Initial commands:

```bash
offbeat status
offbeat config
```

Implement:

```text
CLI
 ↓
Unix socket
 ↓
daemon
 ↓
SQLite
```

The CLI must never open SQLite.

## Tests

Automated tests must prove:

* daemon creates socket;
* CLI reaches daemon;
* daemon shutdown removes/invalidates socket safely;
* CLI produces understandable error when daemon unavailable.

## Completion gate

```bash
offbeat status
```

must report daemon/database status exclusively through IPC.

---

# 6. Milestone 2 — Spicetify Adapter Connectivity

## Timebox

**3–4 days**

## Specific objective

Establish reliable bidirectional communication between Spotify/Spicetify and the daemon.

## Deliverables

Implement:

```text
Spicetify extension
      ↓
localhost WebSocket
      ↓
offbeatd
```

Features:

* adapter token;
* connect;
* reconnect;
* daemon knows connected/disconnected state;
* daemon can send `snapshot.request`;
* extension can respond with a synthetic test snapshot.

Add:

```bash
offbeat status
```

output indicating:

```text
Spotify adapter: connected/disconnected
```

## Tests

Use a fake WebSocket adapter in Go integration tests.

Must test:

* valid authentication;
* invalid authentication;
* connection loss;
* reconnection;
* request/response correlation.

## Completion gate

With Spotify running:

```bash
offbeat spotify sync
```

must successfully request a synthetic snapshot from the extension and wait for the result.

No real Spotify parsing requirement yet.

---

# 7. Milestone 3 — Real Spotify Snapshot Collection

## Timebox

**4–6 days**

## Specific objective

Collect real Liked Songs and all supported Spotify playlists through Spicetify.

## Deliverables

Extension must:

1. enumerate playlists;
2. fetch every page;
3. fetch Liked Songs;
4. preserve ordering;
5. preserve duplicate entries;
6. mark unsupported entries;
7. send one complete candidate snapshot.

Implement strict candidate completion semantics.

## Important architecture constraint

All Spicetify/Spotify private API calls must live behind a thin adapter module.

Example:

```text
SpotifyAdapter
├── listPlaylists()
├── fetchPlaylist()
└── fetchLikedSongs()
```

No daemon/domain logic may depend on Spotify internal response shapes.

This is necessary because Spicetify/Spotify private APIs may change.

## Tests

Create fixtures representing:

* multiple playlists;
* empty playlist;
* duplicate playlist track;
* pagination;
* unsupported entry;
* simulated failed page.

## Measurable acceptance criteria

Running:

```bash
offbeat spotify sync
```

against real Spotify must report counts matching the visible account within supported semantics.

If any required fetch fails:

```text
snapshot rejected
previous state unchanged
```

## Completion gate

No reconciliation work begins until real snapshots can be repeatedly collected and rejected atomically on deliberate failure.

---

# 8. Milestone 4 — Spotify State Model and Atomic Reconciliation

## Timebox

**4–5 days**

## Specific objective

Persist Spotify as desired state and compute deterministic revisions.

## Deliverables

Implement schema for:

```text
snapshots
spotify_tracks
playlists
playlist_entries
liked_entries
revisions
revision_changes
```

Snapshot commit must occur in one SQLite transaction.

Implement reconciliation diff:

```text
added tracks
removed references
playlist rename
playlist addition/deletion
entry ordering changes
liked changes
```

Implement:

```bash
offbeat history
offbeat missing
```

initially without acquisition.

## Tests

Must prove:

* failed snapshot changes nothing;
* valid snapshot updates everything atomically;
* order is preserved;
* duplicate entries survive;
* playlist rename preserves ID;
* playlist deletion removes desired playlist;
* Liked Songs changes correctly.

## Completion gate

Given fixture snapshots A and B, applying:

```text
A → B
```

must always produce exactly the expected deterministic DB state and revision diff.

---

# 9. Milestone 5 — Managed Library and Asset Model

## Timebox

**4–5 days**

## Specific objective

Introduce playable local assets independently of acquisition.

## Deliverables

Implement:

```text
LocalAsset
SpotifyTrackAssetMapping
```

Managed root:

```text
~/Music/Offbeat/
├── tracks/
└── playlists/
```

Implement hybrid stable filenames.

Add an internal test mechanism that places synthetic audio fixtures into the managed library and maps them to Spotify tracks.

Do not implement scanning arbitrary external music directories.

## Tests

Prove:

* one asset can serve multiple Spotify tracks;
* duplicate playlist references use one physical file;
* reference count reaches zero correctly;
* referenced shared asset is never prematurely deleted;
* missing physical file becomes missing state.

## Completion gate

Spotify desired state + manually supplied test assets must produce an accurate playable/missing model.

---

# 10. Milestone 6 — Matching and Review Queue

## Timebox

**4–6 days**

## Specific objective

Implement conservative metadata matching.

## Deliverables

Normalize:

```text
title
artists
duration
album support signal
version markers
```

Classify:

```text
high
medium
low
```

Implement persistent review queue.

CLI:

```bash
offbeat review
```

must allow:

```text
accept
reject
skip
```

Decisions persist.

## Tests

Fixture cases must include:

```text
same recording / different album
live vs studio
radio edit vs original
remix vs original
minor duration difference
conflicting artists
ambiguous candidates
```

## Completion gate

No automatic acquisition should depend on matching until high-confidence cases show near-zero false positives in the project's fixture suite.

Bias toward unresolved rather than incorrect.

---

# 11. Milestone 7 — Playlist Materialization

## Timebox

**2–3 days**

## Specific objective

Generate correct desktop M3U8 playlists.

## Deliverables

Generate:

```text
Liked Songs.m3u8
<Playlist>.m3u8
```

Rules:

* omit missing tracks;
* retain relative Spotify ordering;
* retain duplicate occurrences;
* support duplicate playlist names;
* remove stale filename after rename;
* delete playlist file after Spotify playlist deletion.

## Tests

Golden-file tests for generated `.m3u8`.

## Completion gate

An ordinary desktop music player must successfully open generated fixture playlists.

---

# 12. Milestone 8 — Persistent Acquisition Work Queue

## Timebox

**3–4 days**

## Specific objective

Build acquisition orchestration independently of real yt-dlp behavior.

## Deliverables

SQLite-backed jobs with states equivalent to:

```text
pending
running
retry_wait
review_required
unresolved
failed
complete
```

Implement:

* bounded concurrency;
* daemon restart recovery;
* temporary failure retry;
* capped exponential backoff;
* permanent failure handling.

Use a fake downloader first.

## Tests

Mandatory concurrency/integration tests:

* daemon killed while jobs running;
* work survives restart;
* configured concurrency never exceeded;
* temporary failures retry;
* permanent failures do not retry forever.

## Completion gate

Fake acquisition pipeline can process a large deterministic fixture workload across daemon restarts without lost work.

---

# 13. Milestone 9 — yt-dlp and FFmpeg Integration

## Timebox

**4–6 days**

## Specific objective

Add the real v1 media backend for authorized source URLs.

## Deliverables

Create internal interface:

```go
type Downloader interface {
    Probe(...)
    Download(...)
}
```

Implement:

```text
yt-dlp backend
```

Separate media processing:

```text
download
→ inspect
→ normalize if necessary
→ tag
→ artwork
→ atomic move into managed library
```

Require:

```text
yt-dlp
ffmpeg
```

from configurable paths / `$PATH`.

## Rules

* quality over disk usage;
* do not transcode without a concrete reason;
* preserve acceptable FLAC/MP3/AAC/M4A/Opus;
* failures do not block unrelated jobs.

## Tests

Do not make CI depend on copyrighted external media.

Use local/test servers and synthetic media wherever possible.

Integration tests may exercise binaries using controlled test fixtures.

## Completion gate

Given an authorized test source URL, the daemon can produce a valid managed asset and persist its mapping.

---

# 14. Milestone 10 — Auto-Acquisition and Bootstrap

## Timebox

**2–3 days**

## Specific objective

Connect Spotify reconciliation to acquisition.

## Deliverables

After snapshot:

```text
desired
→ match
→ unresolved/missing
→ acquisition queue
```

Implement:

```toml
auto_acquire = true|false
```

Implement first-snapshot state:

```text
BOOTSTRAP_PENDING
```

Add explicit CLI approval.

## Tests

Prove:

* first snapshot never automatically launches mass acquisition;
* approval starts eligible work;
* later Spotify additions auto-queue;
* auto-acquire false never blocks metadata sync.

## Completion gate

The desktop-only user flow from Spotify startup through managed local library works without Android.

---

# 15. Milestone 11 — Deletion and Integrity Semantics

## Timebox

**3–4 days**

## Specific objective

Make destructive behavior safe and deterministic.

## Deliverables

Implement:

* immediate desired-state removal;
* asset reference counting;
* physical deletion only at zero references;
* deferral while Offbeat operation actively uses file;
* lightweight existence checks;
* automatic reacquisition when appropriate;
* `offbeat verify`.

## Tests

Destructive tests are mandatory:

```text
remove from one playlist only
remove from all references
shared asset still referenced
file deleted manually
file corrupt
delete while acquisition/sync handle active
```

## Completion gate

No test may demonstrate deletion of an asset with a live desired reference.

---

# 16. Milestone 12 — Desktop Setup and systemd Integration

## Timebox

**2–3 days**

## Specific objective

Make desktop installation reproducible.

## Deliverables

Implement:

```bash
offbeat setup
```

It must:

* create directories;
* initialize config;
* initialize DB;
* generate secrets;
* generate TLS material;
* install/update user systemd unit;
* install/link Spicetify extension;
* verify external prerequisites;
* print actionable errors.

## Acceptance test

Starting from a clean supported Linux user environment with prerequisites installed:

```bash
offbeat setup
systemctl --user start offbeat
```

must result in a working daemon.

## Completion gate

A fresh installation can reach completed Spotify snapshot without manual source-code editing.

---

# 17. Milestone 13 — Device Model and LAN API

## Timebox

**4–5 days**

## Specific objective

Implement Android-facing protocol before building Android UI.

## Deliverables

Daemon:

* HTTPS server;
* generated certificate;
* mDNS/DNS-SD advertisement;
* manual-address-compatible endpoint;
* pairing session;
* six-digit code;
* per-device credential;
* certificate identity;
* device persistence.

CLI:

```bash
offbeat pair
offbeat devices
```

## Tests

Use Go HTTP clients as fake Android devices.

Test:

* successful pairing;
* wrong pairing code;
* expired pairing flow;
* invalid credential;
* certificate identity;
* multiple modeled devices;
* unpaired access rejection.

## Completion gate

A fake client can securely pair and retrieve authenticated metadata over LAN HTTPS.

---

# 18. Milestone 14 — Revision Manifest Protocol

## Timebox

**3–4 days**

## Specific objective

Define sync semantics independently of Android implementation.

## Deliverables

Manifest must provide:

```text
revision
assets[]
asset hashes
sizes
remote paths/IDs
playlists[]
playlist hashes
```

Implement endpoint allowing client current-state description and reconciliation against latest state.

Client can jump:

```text
revision 3 → revision 57
```

directly.

## Tests

Test:

* no-op sync;
* new files;
* deleted files;
* renamed playlist;
* changed playlist;
* stale client;
* shared asset;
* missing desktop asset.

## Completion gate

A fake client can derive an exact latest-state reconciliation without replaying historical revisions.

---

# 19. Milestone 15 — Resumable File Transfer

## Timebox

**3–4 days**

## Specific objective

Make media transfer reliable.

## Deliverables

Implement:

```text
HTTP Range
SHA-256
immutable asset download
```

Requirements:

* interrupted transfers resume;
* invalid ranges handled correctly;
* hashes exposed through manifest;
* stale asset request fails safely when latest state changed.

## Tests

Automated transfer interruption/resumption tests.

Corrupt downloaded bytes deliberately and verify rejection.

## Completion gate

A fake Android client can download and verify a multi-megabyte asset across deliberate connection interruption.

---

# 20. Milestone 16 — Minimal Android Pairing App

## Timebox

**4–6 days**

## Specific objective

Create the Android application shell and secure pairing.

## Deliverables

Native Kotlin + Compose.

Screens:

```text
Pair PC
Home
Settings
```

Implement:

* mDNS discovery;
* manual host fallback;
* pairing code submission;
* certificate pinning;
* secure credential storage;
* paired daemon status.

No media synchronization yet.

## Tests

Unit tests for protocol/state logic.

Android instrumentation test for pairing flow where practical.

## Completion gate

A physical Android device can discover/pair with Offbeat and persist its identity across app restart.

---

# 21. Milestone 17 — Android Storage and Manual Sync

## Timebox

**5–7 days**

## Specific objective

Complete one end-to-end manual phone synchronization.

## Deliverables

Android app must:

1. fetch latest manifest;
2. calculate required bytes;
3. perform storage preflight;
4. stop without mutation if insufficient;
5. download required files;
6. resume transfers;
7. verify SHA-256;
8. publish into `Music/Offbeat/`;
9. remove obsolete managed files;
10. write playlists last;
11. save committed revision.

Use MediaStore/shared media APIs appropriately.

## Tests

Test:

* first sync;
* incremental sync;
* deletion;
* interruption;
* hash mismatch;
* insufficient storage;
* stale revision.

## Completion gate

On a physical Android device:

```text
tap Sync now
→ files appear under Music/Offbeat
→ M3U8 playlists appear
→ third-party player can play them offline
```

---

# 22. Milestone 18 — Android Automatic Sync

## Timebox

**2–3 days**

## Specific objective

Add opportunistic automatic synchronization.

## Deliverables

Use WorkManager.

Conditions:

* auto-sync enabled;
* suitable network available;
* daemon reachable/authenticated.

Manual `Sync now` remains available.

Automatic sync must not run overlapping copies of itself.

## Tests

Test scheduling state transitions.

## Completion gate

After desktop revision changes, the Android device eventually reaches the latest state without manually opening Spotify or Offbeat Android, subject to Android background scheduling behavior.

---

# 23. Milestone 19 — End-to-End Reliability Pass

## Timebox

**5–7 days**

## Specific objective

Validate v1 against realistic failure scenarios.

## Required scenarios

### Desktop

* kill daemon during acquisition;
* restart daemon;
* close Spotify;
* restart Spotify;
* deliberately fail snapshot page;
* remove playlist;
* rename playlist;
* remove shared asset reference;
* manually delete managed file.

### Android

* disconnect Wi-Fi during download;
* reconnect;
* phone several revisions behind;
* insufficient storage;
* corrupt partial transfer;
* daemon restart during sync.

## Measurable goal

Every scenario must either:

```text
recover automatically
```

or:

```text
leave system in a safe diagnosable state
```

No scenario may silently corrupt desired state or delete still-required assets.

## Completion gate

All v1 failure-path integration tests pass.

---

# 24. Milestone 20 — Documentation and Release Candidate

## Timebox

**3–4 days**

## Specific objective

Make v1 usable without implementation knowledge.

## Deliverables

Documentation:

```text
README
architecture overview
installation
Spicetify setup
first bootstrap
CLI reference
Android pairing
Android sync
configuration
failure recovery
known limitations
security model
```

Create a release checklist.

## Completion gate

Perform a clean install from documentation alone.

No undocumented manual DB/file modification should be necessary.

---

# 25. Suggested Overall Schedule

For one developer/agent working sequentially:

```text
Week 1
M0–M2
repo, daemon, IPC, Spicetify connection

Week 2
M3–M4
real Spotify snapshots + reconciliation

Week 3
M5–M7
assets, matching, review, playlists

Week 4
M8–M10
persistent acquisition + yt-dlp + bootstrap

Week 5
M11–M14
deletion safety, setup, pairing, manifests

Week 6
M15–M17
resumable transfer + Android manual sync

Week 7
M18–M20
auto sync, reliability, documentation
```

This is a target sequence, not a requirement to ship exactly in seven weeks.

If using coding agents, milestone completion—not elapsed days—should control progress.

---

# 26. Agent Execution Rules

GitHub issues are the unit of executable work. Use `/to-spec` when a change still needs a focused specification, `/to-tickets` to decompose an approved milestone or plan into tracer-bullet tickets, and `/triage` to move each issue toward `ready-for-agent`, `ready-for-human`, or `wontfix`.

Every coding-agent task should be limited to one agent-ready issue. A milestone may map to one issue only when it already forms a small vertical slice; otherwise, split it into dependency-linked issues whose acceptance criteria can be verified independently.

Do not prompt:

```text
Implement Offbeat.
```

Prefer:

```text
Implement the agent-ready GitHub issue for M4 Spotify snapshot persistence and reconciliation.

Constraints:
- daemon exclusively owns SQLite;
- use existing migrations framework;
- commit valid snapshots in one SQLite transaction;
- failed candidate snapshots must not modify committed state;
- preserve playlist ordering and duplicates;
- do not implement acquisition.

Before editing:
1. read the issue and its comments;
2. inspect `CONTEXT.md` and relevant ADRs when present;
3. inspect existing DB interfaces, migrations, tests, and adjacent packages.

Completion:
- implementation;
- migrations;
- unit tests;
- integration tests;
- go test ./... passes;
- document any architecture decisions.
```

---

# 27. Required Agent Workflow

For each agent-ready GitHub issue:

### Step 0 — claim and verify

Agent must:

* fetch the issue and comments from GitHub;
* confirm that blocking issues are closed;
* claim the issue before making the first repository change;
* treat the issue's scope and acceptance criteria as the implementation contract.

### Step 1 — inspect

Agent must inspect:

* relevant architecture docs;
* current interfaces;
* migrations;
* tests;
* adjacent packages.

### Step 2 — plan when needed

For non-trivial work, the agent writes a short implementation plan containing:

```text
files/modules affected
interfaces added/changed
schema changes
tests to add
risks
```

### Step 3 — implement

Agent makes the smallest coherent implementation satisfying the issue's requirements.

### Step 4 — test

Agent must run all relevant automated tests.

For Go work:

```bash
go test ./...
go vet ./...
```

For Android work, use the repository's Gradle test commands.

### Step 5 — self-review

Agent verifies:

* no scope creep;
* no duplicate domain concepts;
* errors handled;
* context cancellation respected;
* destructive operations tested;
* architecture boundaries preserved.

### Step 6 — completion report

Agent posts or reports:

```text
what changed
tests run
acceptance criteria satisfied
known limitations
follow-up work intentionally deferred
```

Record newly discovered work as a separate issue rather than silently expanding scope. Close the active issue only after its acceptance criteria and verification steps pass.

---

# 28. Dependency Graph

```text
M0 Repository
 |
 v
M1 Daemon/CLI IPC
 |
 +------> M2 Spicetify transport
 |            |
 |            v
 |       M3 Spotify collection
 |            |
 |            v
 |       M4 Reconciliation
 |            |
 |      +-----+------+
 |      |            |
 |      v            v
 |     M5           M6
 |   Assets       Matching
 |      |            |
 |      +-----+------+
 |            |
 |            v
 |           M7
 |       Playlists
 |            |
 |            v
 |           M8
 |     Acquisition queue
 |            |
 |            v
 |           M9
 |      yt-dlp/media
 |            |
 |            v
 |          M10
 |       Auto acquire
 |            |
 |            v
 |          M11
 |      deletion safety
 |            |
 |            v
 |          M12
 |         setup
 |
 +--------------------> M13 Pairing/LAN
                          |
                          v
                        M14 Manifest
                          |
                          v
                        M15 Transfer
                          |
                          v
                        M16 Android pairing
                          |
                          v
                        M17 Manual sync
                          |
                          v
                        M18 Auto sync
                          |
                          v
                        M19 Reliability
                          |
                          v
                        M20 Release
```

---

# 29. Project-Wide Engineering Constraints

## Go

Use `context.Context` for:

* daemon lifecycle;
* WebSocket/session lifetime;
* database operations;
* acquisition jobs;
* external processes;
* media processing;
* sync requests.

Long-running subprocesses must be cancellable.

Do not store request-scoped contexts permanently inside domain objects.

---

## Database

All state mutations go through daemon-owned repositories/services.

Transactions must protect:

* snapshot commit;
* revision generation;
* reference-count-affecting changes;
* acquisition state transitions where atomicity matters.

---

## Filesystem

Never expose partially written final assets.

Required pattern:

```text
temporary path
→ process
→ fsync/close where appropriate
→ atomic rename
→ DB state commit in safe order
```

Destructive changes must be idempotent.

---

## Protocols

Version both:

```text
Spicetify ↔ daemon protocol
Android ↔ daemon protocol
```

The initial version may simply be:

```text
v1
```

but protocol-version negotiation/failure must be explicit.

---

# 30. SMART v1 Product Goal

## Specific

Deliver a Linux/Android system that mirrors Spotify playlists/Liked Songs into an offline managed library and synchronizes playable files to Android.

## Measurable

v1 succeeds when the complete 25-step acceptance scenario from the PRD works and all required reliability tests pass.

## Achievable

Scope excludes:

* music-player development;
* desktop GUI;
* cross-platform desktop;
* cloud sync;
* bidirectional Spotify editing;
* per-device media transcoding;
* advanced acoustic matching.

## Relevant

Every implemented component directly serves the user's requirement:

> Spotify-organized music should remain playable offline on Linux and Android.

## Time-bound

Target a **7-week sequential implementation window**, with progress gated by the milestone acceptance criteria rather than calendar pressure.

A milestone that fails its completion gate does not unlock dependent milestones.

---

# 31. Final Release Gate

Offbeat v1 is releasable only when all of the following are true:

```text
[ ] clean Linux setup succeeds
[ ] systemd daemon starts automatically
[ ] real Spotify snapshot succeeds
[ ] partial snapshot cannot corrupt state
[ ] Spotify ordering is preserved
[ ] missing tracks are visible
[ ] review queue works
[ ] acquisition survives restart
[ ] managed files are tagged/generated correctly
[ ] playlist rename/delete works
[ ] shared assets are not prematurely deleted
[ ] first bootstrap requires approval
[ ] subsequent auto-acquisition works
[ ] Android pairing works
[ ] HTTPS credentials/certificate trust work
[ ] Android storage preflight works
[ ] resumable download works
[ ] SHA-256 verification works
[ ] M3U8 is committed last
[ ] third-party Android player can play synced library
[ ] Wi-Fi interruption recovers safely
[ ] phone can jump directly to latest revision
[ ] automated test suite passes
[ ] clean-install documentation has been validated
```

Only after this checklist passes should additional v2 features be considered.
