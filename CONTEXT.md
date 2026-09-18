# Offbeat

Offbeat maintains a local, offline representation of Spotify-organized music for one Linux user and one Android phone in lean v1.

## Product direction

ADR-0010 establishes a **current-state-first** v1, amended by ADR-0011's concrete YouTube Missing-set acquisition workflow. Preserve the proven M1–M3 boundaries and add the smallest persistence, managed-file, acquisition, playlist, and Android-sync behavior needed for the end-to-end product. Do not treat old roadmap abstractions as requirements.

## Language

**Daemon owner**:
The sole active `offbeatd` process for one Offbeat data directory. It is the exclusive owner of SQLite and all Offbeat-managed mutable state.
_Avoid_: daemon instance, server owner

**Control protocol**:
The versioned local request-response protocol between the Offbeat CLI and its daemon over a Unix domain socket.
_Avoid_: CLI API, daemon API

**Adapter credential**:
The secret that authenticates the local Spicetify extension to its daemon. It is distinct from a Spotify credential and is never exposed through Offbeat's Control protocol.
_Avoid_: Spotify token, Spotify API key

**Adapter endpoint**:
The loopback-only versioned WebSocket address at which the daemon accepts an Adapter session.
_Avoid_: Spotify endpoint, LAN endpoint

**Adapter session**:
The single authenticated WebSocket connection from the Spicetify extension to the daemon. It carries daemon-issued Snapshot requests.
_Avoid_: Spotify connection, adapter instance

**Snapshot request**:
A daemon-issued, uniquely correlated request for the connected adapter to collect one complete Spotify candidate.
_Avoid_: sync event, adapter push

**Candidate snapshot**:
The complete normalized Spotify observation produced by the Spicetify adapter for one Snapshot request. It contains playlists, Liked Songs, ordered supported entries, and ordered unsupported placeholders. It is transient until the daemon validates and commits it.
_Avoid_: database snapshot, revision

**Desired Spotify state**:
The daemon's currently committed representation of Spotify organization. It is the authoritative local metadata state used by downstream Offbeat features until a newer complete candidate commits.
_Avoid_: snapshot history, event log

**State revision**:
A monotonically increasing current-state version that advances when committed Desired Spotify state changes. It supports change detection; lean v1 does not promise replayable historical revisions.
_Avoid_: event sequence, revision history

**Managed track**:
For lean v1, a supported Spotify track with zero or one Offbeat-managed local audio file. Cross-track physical-file deduplication is deferred.
_Avoid_: shared asset graph

**Missing track**:
A supported track referenced by current Desired Spotify state that has no valid Managed track file. Unsupported Spotify placeholders are not Missing tracks.

**Acquisition work**:
Daemon-owned restart-relevant work that retrieves media from a source the user is authorized to download and publishes a Managed track file. v1 keeps this workflow concrete and narrow.
_Avoid_: generalized resolver pipeline

**YouTube resolution**:
The concrete v1 process that uses a Missing track's Spotify metadata to select one eligible YouTube media URL for Acquisition work. It is source selection for one track, not a claim that two catalog tracks are the same recording.
_Avoid_: library matching, fuzzy deduplication

**Missing-set acquisition**:
An explicitly requested batch that attempts YouTube resolution and Acquisition work for every Missing track in the current Desired Spotify state. Each track succeeds, fails, or remains unresolved independently.
_Avoid_: full acquire, automatic sync acquisition

**Current-state manifest**:
The minimal Android-facing description of the playable files/playlists currently desired by the daemon. Lean v1 compares against current phone state; it does not require historical revision replay.
_Avoid_: revision replay log
