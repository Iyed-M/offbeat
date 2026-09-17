# ADR 0010: Prefer a current-state-first lean v1

- Status: Accepted
- Date: 2026-09-17

## Context

The original v1 PRD and implementation plan were written before Offbeat had exercised its real Spotify/Spicetify boundary. They intentionally explored a robust end-state architecture, but several later requirements turned speculative concerns into mandatory v1 infrastructure: historical snapshot/revision logs, cross-track asset deduplication, confidence-based matching and review, generalized acquisition stages, rich retry state, automatic discovery and pairing, resumable Android transfer, background sync, and full setup automation.

Milestones 1 through 3 established the boundaries that have demonstrated value in real implementation: one Daemon owner, a local Control protocol, one authenticated Adapter session, and complete all-or-nothing Spotify candidate collection. The next goal is to reach a useful end-to-end product with the smallest architecture that preserves those guarantees.

## Decision

Offbeat v1 is current-state-first. New infrastructure must be justified by a concrete v1 behavior rather than by possible future reuse.

The following decisions apply to work after Milestone 3:

1. Preserve the existing M1-M3 architecture: Daemon ownership, Unix-socket Control protocol, authenticated loopback Adapter protocol, strict normalized Spotify candidate snapshots, ordering, duplicates, unsupported placeholders, and candidate atomicity remain required.
2. Persist the current Spotify desired state atomically. Maintain a simple monotonically increasing state revision when committed desired state changes. Historical snapshots, a general `revision_changes` event log, and `offbeat history` are not v1 requirements.
3. Model one managed local file per supported Spotify track for v1. Cross-track physical-asset deduplication, many-to-many asset mappings, and reference-counted deletion are deferred.
4. Do not build fuzzy metadata matching, high/medium/low match confidence, or a persistent human review queue in v1. If acquisition needs an explicit user-provided or otherwise authorized source mapping, model that concrete workflow directly.
5. Keep the acquisition implementation narrow. `yt-dlp` may remain behind a small process boundary/interface, but v1 does not require a speculative multi-resolver framework or an elaborate state machine. Persist only the work needed for reliable restart-safe acquisition.
6. Prefer conservative file lifecycle behavior. Automatic destructive cleanup, deep integrity scanning, and sophisticated repair are deferred unless needed to make the core flow safe.
7. Android v1 targets one personal device and current-state synchronization. Manual address/configuration is acceptable. mDNS/DNS-SD discovery, arbitrary historical revision jumps, resumable file transfer, background automatic sync, and broad multi-device management are deferred.
8. Setup automation follows the working product. Good documented/manual setup and small helpers are sufficient until the desktop and Android core flows are proven.
9. Deferred features may be added later through versioned SQLite migrations and scoped ADRs/specs when an observed requirement justifies them.

## Consequences

- Milestone 4 becomes current desired-state persistence and atomic replacement rather than a revision-history subsystem.
- The v1 roadmap becomes shorter and more vertical: persist Spotify state, manage local tracks, acquire authorized media, generate playlists, synchronize one Android device, then package/harden.
- Some original CLI commands (`history`, `review`, `verify`, broad `devices` management) are no longer mandatory for the first v1 release.
- The schema should optimize for correctness and evolvability, not for representing every future relationship on day one.
- A simple state revision is still useful for change detection and synchronization, but it is not a promise of replayable historical revisions.
- Existing ADRs remain valid historical architectural decisions unless explicitly superseded by a later ADR.
