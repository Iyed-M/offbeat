# Offbeat v1 — Lean SMART Implementation Plan

> **Workflow status:** This document is the delivery roadmap, not an active task queue. Before implementation, use `/to-spec` to publish the next milestone as a GitHub issue and `/to-tickets` to split the approved spec into dependency-linked agent-ready issues. GitHub issues are authoritative for execution.
>
> **Scope reset (2026-09-17):** ADR-0010 replaces the original post-M3 roadmap. M0–M3 remain completed foundations. M4 onward follows the current-state-first plan below. ADR-0011 adds only the concrete YouTube missing-set workflow in M6A; it does not restore the older generalized matching/review/acquisition architecture.
>
> **Current delivery status:** M4–M6A are implemented. M7 is the next planned milestone.

## 1. Planning principles

Implementation proceeds in vertical slices that produce observable product behavior.

Every milestone must have:

- executable software;
- automated tests around state-threatening behavior;
- a concrete demonstration/completion gate;
- deliberately limited scope.

Prefer a migration later over an abstraction now. Do not build historical/reusable/generalized infrastructure until a concrete requirement needs it.

Use Wayfinder only when a real implementation blocker depends on an uncertain external seam or unresolved product policy. It is not a default step between milestones.

## 2. SMART definition

Each milestone is:

- **Specific:** one end-user or architectural capability;
- **Measurable:** explicit tests and completion gate;
- **Achievable:** avoids speculative adjacent systems;
- **Relevant:** directly advances the v1 acceptance scenario;
- **Time-bound:** a planning timebox used to detect scope growth, not a promise.

## 3. Architecture rules that survive the scope reset

The following M1–M3 decisions remain required:

```text
CLI
  -> local Unix Control protocol
  -> Daemon owner
  -> SQLite / managed state
```

and:

```text
Spotify Desktop
  -> Spicetify extension
  -> authenticated loopback Adapter protocol
  -> Daemon owner
```

Rules:

- only `offbeatd` owns SQLite and managed mutation;
- the CLI does not open SQLite;
- Spotify/Spicetify private response shapes stay inside the extension adapter;
- a candidate snapshot is complete-or-rejected;
- playlist/Liked ordering and duplicate occurrences are preserved;
- unsupported normalized placeholders are not silently dropped.

## 4. Completed foundation — M0 to M3

### M0 — Repository and architecture skeleton

Completed foundation: Go module, daemon/CLI skeleton, configuration, logging, SQLite migration runner, and CI.

### M1 — Daemon ownership and CLI IPC

Completed foundation: single Daemon owner, user-private Unix control socket, daemon-served status/config, lifecycle and protocol guarantees.

### M2 — Spicetify adapter connectivity

Completed foundation: authenticated loopback WebSocket Adapter endpoint, one Adapter session, correlated Snapshot requests, liveness/reconnect, CLI-triggered sync transport.

### M3 — Real Spotify candidate collection

Completed foundation: normalized complete candidate collection through Spicetify Platform APIs, recursive playlists, Liked Songs, pagination validation, ordering/duplicates, unsupported placeholders, bounded collection errors, real-client validation.

M3 intentionally does not persist Spotify state.

---

# 5. Milestone 4 — Persist Current Spotify Desired State

## Timebox

**2–3 engineering days**

## Objective

Turn the validated M3 candidate into durable current Spotify desired state with one atomic SQLite commit.

## Deliverables

Introduce only the schema required for current state, approximately:

```text
spotify_tracks
playlists
playlist_entries
liked_entries
state_metadata
```

The exact schema is implementation-owned, but it must preserve:

- stable Spotify URI/identity;
- mutable playlist names;
- supported track metadata required downstream;
- ordered entries;
- duplicate occurrences;
- unsupported ordered placeholders;
- current state revision;
- last committed timestamp.

Refactor the M3 daemon boundary so strict candidate validation yields a typed normalized candidate value that the persistence/reconciliation code can consume. Do not expose Spotify Desktop private objects to Go.

Apply a valid candidate in one SQLite transaction.

Maintain a monotonically increasing current-state revision. Increment it only when desired state changes; a no-op sync may leave it unchanged.

`offbeat spotify sync` should report a concise post-commit summary. Detailed historical change storage is not required.

## Tests

Prove at minimum:

- first candidate creates the exact expected desired state;
- candidate A -> B yields the exact expected current state;
- failed transaction leaves A completely unchanged;
- playlist order and duplicate occurrences survive persistence;
- playlist rename keeps the same Spotify identity;
- deleted playlists disappear from desired state;
- Liked Songs additions/removals/order changes are correct;
- unsupported placeholders remain represented and ordered;
- no-op candidate does not needlessly advance the state revision;
- daemon restart reads the previously committed state.

## Out of scope

- historical snapshots;
- `revision_changes` event logs;
- `offbeat history`;
- local audio assets;
- acquisition;
- playlist-file generation;
- Android.

## Completion gate

Given deterministic candidate fixtures A and B:

```text
fresh DB + A -> exact state A
state A + B -> exact state B
state B + B -> same logical state/revision
injected commit failure -> state B unchanged
```

---

# 6. Milestone 5 — Managed Tracks and Missing State

## Timebox

**2–3 engineering days**

## Objective

Represent whether each supported desired Spotify track has a usable Offbeat-managed local file.

## Deliverables

Use a deliberately simple v1 relationship:

```text
Spotify track -> zero or one managed file
```

Create the managed root:

```text
~/Music/Offbeat/
├── tracks/
└── playlists/
```

Implement stable collision-safe managed filenames and the minimum database fields needed to associate a supported Spotify track with its managed file.

Implement daemon-backed:

```bash
offbeat missing
```

A missing track is a supported currently desired track without a valid managed file. Unsupported Spotify placeholders are never missing tracks.

Provide a controlled test mechanism for registering/placing synthetic local audio fixtures; do not scan arbitrary external music directories.

## Tests

Prove:

- desired supported track with no file is missing;
- registering a managed file makes it available;
- missing physical file returns the track to missing state;
- one Spotify track maps to at most one managed file in v1;
- duplicate playlist references do not create duplicate physical files;
- unsupported entries do not appear in `missing`;
- all managed mutation stays under the configured managed root.

## Out of scope

- cross-track asset deduplication;
- fuzzy metadata matching;
- review queues;
- automatic deletion;
- deep integrity verification;
- acquisition.

## Completion gate

Fixture desired state plus fixture managed files produces an exact available/missing set through the daemon and CLI.

---

# 7. Milestone 6 — Minimal Authorized Acquisition

## Timebox

**4–6 engineering days**

## Objective

Given a supported missing Spotify track and an authorized source supported by the v1 workflow, produce its managed local audio file reliably.

## Deliverables

Accept an explicit Spotify track plus an authorized HTTP(S) source URL. Automatic source resolution is a separate M6A slice so the retrieval boundary is proven first.

Keep `yt-dlp` behind a small internal process/interface boundary so command syntax does not leak through the application.

Persist only restart-relevant acquisition work. A small state set such as:

```text
pending
running
failed
complete
```

is preferred unless implementation evidence requires another state.

Requirements:

- bounded concurrency;
- daemon restart does not silently lose requested persistent work;
- one failed track does not block unrelated work;
- manual retry is sufficient for v1 unless a concrete transient-failure requirement justifies automatic retry;
- use FFmpeg only when technically required;
- write finished media atomically into the managed root;
- produce stable safe filenames;
- optional artwork/tagging failure does not invalidate otherwise usable audio.

## Tests

Use controlled/local fixtures rather than copyrighted Internet media.

Test:

- successful acquisition to managed file;
- process/tool failure;
- restart recovery of persistent work;
- bounded concurrency;
- unrelated work continues after one failure;
- partial/temp files are not published as usable tracks;
- output never escapes the managed root.

## Completion gate

An authorized controlled source can turn a fixture missing track into an available managed track through daemon-owned work.

---

# 8. Milestone 6A — YouTube Missing-Set Acquisition

## Timebox

**3–5 engineering days**

## Objective

Make the intended desktop acquisition workflow concrete:

```bash
offbeat spotify sync
offbeat acquire missing
```

The second command durably queues every distinct supported Missing track, resolves one eligible YouTube URL per track, and sends resolved work through the existing M6 `yt-dlp` retrieval and managed-file publication path.

## Deliverables

Add daemon-backed commands equivalent to:

```bash
offbeat acquire missing
offbeat acquire status [id]
```

`acquire missing` operates on committed Desired Spotify state and creates at most one active Acquisition work item per distinct track. It must enqueue the batch durably before returning so CLI disconnect or daemon restart does not lose the unprocessed remainder. Already available tracks and tracks with active work are skipped idempotently.

Extend the minimal acquisition schema only as needed to distinguish direct-URL and YouTube-resolution work, persist the selected URL, and represent an `unresolved` outcome separately from tool/download failure. Do not add a generalized source graph, candidate history, confidence tiers, or review decisions.

Implement one built-in YouTube resolver behind a small boundary. It should:

- derive a deterministic query from Spotify title, artists, duration, and meaningful version markers;
- inspect a bounded candidate set before downloading;
- reject candidates with conflicting artist/title/version information or unacceptable duration difference;
- select only a unique eligible best candidate;
- leave no-result or ambiguous cases unresolved;
- persist the selected canonical YouTube URL before invoking the existing downloader.

The exact eligibility thresholds and tie rules belong in the M6A spec and fixture suite. They must favor unresolved over a predictably wrong recording. This is resolver-local source selection, not cross-track equivalence or library deduplication.

Workers retain M6 bounded concurrency and failure isolation. Direct URL acquisition remains supported for unresolved tracks and other authorized sources. `acquire status` must make aggregate and per-work queued/running/unresolved/failed/complete outcomes inspectable.

No live YouTube dependency is allowed in normal CI. Use a fake resolver with captured/synthetic result metadata and the existing fake/controlled downloader. A separately invoked manual or opt-in integration gate may exercise the real `yt-dlp` YouTube search/probe boundary.

## Tests

Prove at minimum:

- all distinct Missing tracks are queued once despite duplicate playlist/Liked references;
- available tracks and existing active work are skipped;
- repeating the command is idempotent;
- query construction is stable for the same Spotify metadata;
- an exact unique candidate resolves and enters the existing download path;
- no result, conflicting version markers, excessive duration difference, and a tied best result become unresolved;
- one unresolved or failed track does not block unrelated tracks;
- selected URLs and queued work survive daemon restart;
- a track removed from Desired Spotify state before publication is not installed;
- direct URL acquisition can supersede a completed unresolved outcome without creating competing active work;
- batch/status responses remain bounded for a large missing set.

## Out of scope

- acquisition triggered automatically by `spotify sync`;
- generalized resolver plugins or non-YouTube search backends;
- library-wide fuzzy matching or cross-track asset deduplication;
- high/medium/low confidence tiers;
- persistent candidate lists or human review queues;
- automatic retry/backoff policy beyond existing manual retry.

## Completion gate

With a deterministic Desired Spotify state and fake YouTube result catalog:

```text
offbeat acquire missing
-> every eligible distinct missing track becomes durable work
-> unique eligible candidates become managed files
-> ambiguous/no-match tracks remain inspectably unresolved
-> failures do not stop the rest of the batch
```

An opt-in real-tool smoke test must also resolve and download a user-authorized YouTube fixture without making normal CI depend on external media or YouTube availability.

---

# 9. Milestone 7 — Desktop M3U8 Materialization

## Timebox

**1–2 engineering days**

## Objective

Produce ordinary desktop playlists from current desired state and available managed tracks.

## Deliverables

Generate:

```text
Liked Songs.m3u8
<playlist>.m3u8
```

under the managed `playlists/` directory.

Rules:

- omit missing/unsupported entries;
- preserve relative Spotify ordering of included tracks;
- preserve duplicate occurrences;
- use Spotify playlist identity internally;
- rename updates the generated filename safely;
- duplicate Spotify playlist names get deterministic collision-safe filenames;
- deleted playlists remove their Offbeat-owned generated playlist file.

Regenerate after relevant desired-state or managed-track changes.

## Tests

Use golden files for:

- ordinary playlist;
- missing tracks;
- duplicate occurrences;
- Liked Songs;
- rename;
- duplicate playlist names;
- playlist deletion.

## Completion gate

An ordinary desktop music player can open fixture-generated playlists and resolve their referenced managed audio files.

At this point Offbeat should be useful as a desktop-only product. Use it before expanding scope if possible.

---

# 10. Milestone 8 — One-Device Android Manual Sync

## Timebox

**5–7 engineering days**

## Objective

Manually synchronize the current playable Offbeat library to one Android phone over the LAN.

## Deliverables

Before building UI complexity, define the smallest authenticated current-state LAN protocol needed by one device.

Daemon provides a current manifest containing only metadata needed to compare/synchronize the current playable files and playlists.

Android app provides:

- manual daemon address/configuration;
- the minimal authentication flow selected by the M8 spec;
- a **Sync now** action;
- Offbeat-owned local storage;
- current-state file comparison;
- download of missing/replacement audio;
- safe removal of obsolete files inside the Android Offbeat root;
- playlist replacement after required files are available;
- enough local state to make a later manual sync efficient.

A failed transfer may restart that file from byte zero.

The LAN API must not be anonymous or Internet-facing by default.

## Tests

Use a Go/fake client for protocol tests and Android tests for local storage behavior. A physical-device smoke test is the final gate.

Test at minimum:

- authentication rejection/success;
- no-op current-state sync;
- new audio file;
- removed audio file;
- changed playlist;
- interrupted file transfer retries cleanly later;
- failed sync does not destroy previously usable completed content;
- operations stay inside the Offbeat Android root.

## Out of scope

- mDNS/DNS-SD;
- automatic background sync;
- resumable/range transfer;
- arbitrary historical revision jumps;
- multiple-device management UI;
- per-device transcoding;
- Spotify integration on Android.

## Completion gate

On one physical Android device:

```text
manual connect/authenticate
-> Sync now
-> current playable Offbeat audio + playlists present
-> ordinary Android player can play them offline
```

---

# 11. Milestone 9 — Setup, Packaging, and Reliability

## Timebox

**4–7 engineering days**

## Objective

Make the completed lean v1 reproducible and harden the failure cases discovered while using it.

## Deliverables

Provide clear supported-Linux installation documentation and focused helpers where they remove real friction.

The normal desktop deployment should support a `systemd --user` service.

Setup work may automate Offbeat-owned concerns such as:

- creating config/data/music directories;
- generating/provisioning the Adapter credential;
- installing a user service file;
- producing/configuring the Spicetify extension artifact;
- checking for external prerequisites.

It does not need to install Spotify, Spicetify, `yt-dlp`, or FFmpeg automatically, and a single all-encompassing `offbeat setup` command is not required if documented focused steps are simpler and reliable.

Run an end-to-end reliability pass across:

- daemon restart;
- Spotify unavailable;
- candidate collection failure;
- SQLite commit failure;
- acquisition failure/restart;
- missing managed files;
- playlist regeneration;
- interrupted Android manual sync;
- clean reinstall/reconfiguration path.

Remove obsolete configuration fields/docs introduced only for deferred architecture when doing so is safe and clearly scoped.

## Completion gate

Starting from a clean supported Linux user environment with external prerequisites installed, the documented process reaches:

```text
Spotify candidate
-> persisted desired state
-> managed audio from authorized source workflow
-> desktop M3U8
-> one-phone manual sync
-> offline playback through ordinary players
```

without source-code editing.

---

# 12. Estimated remaining effort after M3

Planning timeboxes:

| Milestone | Timebox |
|---|---:|
| M4 — current Spotify state | 2–3 days |
| M5 — managed tracks/missing | 2–3 days |
| M6 — minimal acquisition | 4–6 days |
| M6A — YouTube missing-set acquisition | 3–5 days |
| M7 — M3U8 | 1–2 days |
| M8 — Android manual sync | 5–7 days |
| M9 — setup/reliability | 4–7 days |
| **Total** | **21–33 engineering days** |

These are scope-control estimates, not delivery promises. If a milestone exceeds its timebox, first check whether deferred architecture has been pulled back into scope.

## 13. Deferred backlog after lean v1

Do not schedule these automatically. Reconsider them from real usage:

- historical revision/snapshot browsing;
- richer sync history;
- cross-track asset deduplication;
- library-wide metadata matching and review;
- source-resolver plugin architecture;
- sophisticated automatic retry/backoff;
- automatic physical garbage collection;
- deep verify/repair tooling;
- LAN discovery;
- richer pairing/device management;
- resumable transfers;
- Android background sync;
- multi-device support;
- end-to-end setup automation.
