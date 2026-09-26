# ADR 0013: Allow explicit YouTube choices and local review

- Status: Accepted
- Date: 2026-09-26
- Scope: [Issue #70](https://github.com/Iyed-M/offbeat/issues/70); decision gate [#71](https://github.com/Iyed-M/offbeat/issues/71)

## Context

ADR-0010 and ADR-0011 deferred persistent human review in favor of a concrete, explicitly requested Missing-set YouTube workflow. ADR-0012 requires a scored eligible winner to beat the runner-up by a declared margin. In observed batches, many otherwise plausible results are close or tied; these are metadata diagnostics, not evidence that the videos contain the same recording. Without a saved user choice, reviewing an ambiguous track repeatedly is cumbersome.

## Decision

Amend ADR-0010/0011 **only** to permit a durable, explicitly user-chosen Spotify track URI → YouTube video ID mapping and a thin local candidate-review interface for the existing plain-YouTube Acquisition workflow. Keep the daemon authoritative for Desired Spotify state, mapping persistence, source selection, and Acquisition transitions. Use a minimal versioned SQLite migration for manual choices with provenance and timestamps; validate the video ID and construct a canonical YouTube watch URL server-side. Automated selections remain on Acquisition work, not in the manual-choice mapping. A mapping survives Spotify sync and changes to Desired Spotify state, but only a currently desired Missing track is eligible for new Acquisition work. Removing or replacing a choice never deletes or silently replaces a Managed track file.

For **new eligible work**, a saved manual choice takes precedence over resolver policy. A source URL already selected and persisted on active Acquisition work is immutable: a later mapping change cannot redirect an in-flight retrieval. A chosen video that fails or becomes unavailable retains its mapping and surfaces the failure; there is no silent fallback. Changing or removing the mapping affects future eligible attempts, while deliberate replacement and explicit recovery remain possible. Direct-URL acquisition stays a separately authorized override. Manual selection is an explicit user action: validate the chosen ID and current Desired/Missing/work state at confirmation, coordinate mapping persistence with a safe enqueue or retry without duplicate active work, and require stronger explicit confirmation to choose an ineligible candidate. Inspection, refresh, skipping, and opening the review page do not mutate Acquisition state.

Amend ADR-0012 **only** for an opt-in `auto_best` ambiguity policy. The default `manual` policy retains the conservative acceptance floor **and** runner-up margin; unambiguous eligible winners still resolve normally, while ambiguous ones remain unresolved pending a user choice. `auto_best` may select the deterministically highest-ranked eligible candidate above the existing floor without the runner-up margin. It does not relax search bounds, hard title/artist/version/duration gates, scoring, or eligibility; weak or ineligible results still abstain. Identify the selection policy in new diagnostics/replay outputs without changing the meaning of frozen conservative captures. A metadata score, tie, or automatic selection is never an assertion of recording identity.

Expose a narrow, temporary **loopback-only** review page as a thin client of the daemon's existing local Control protocol. Derive its paginated list from current desired Missing tracks and Acquisition state; do not persist a candidate history or generalized review queue. Fresh, bounded read-only inspection shows Spotify metadata and each YouTube result's title, uploader, duration, score, eligibility, and rejection reason, with an external YouTube link for independent listening. The user can skip, refresh, or explicitly confirm one video ID; the daemon rechecks state before committing. The HTTP listener binds to loopback only and protects the session and mutations against unauthorized or cross-origin access; it is not a LAN service or another owner of SQLite, Spotify access, or resolver decisions.

Neither Spotify sync nor a page view or candidate inspection starts Acquisition. Retrieval remains limited to sources the user is authorized to download. This decision does not introduce recording-equivalence claims, cross-track asset deduplication, library-wide matching/review history, generalized resolver backends or acquisition infrastructure, or automatic sync/page-view acquisition. Existing explicit Missing-set commands, retries, direct-URL overrides, restart-safe work, and Managed-file safety remain the concrete workflow.

## Consequences

- An explicit choice can be reused after a restart or Spotify sync without turning every search observation into durable review state.
- Users may opt into a greater risk of a wrong recording by bypassing only the ambiguity margin; the default continues to abstain on close candidates.
- The local page is a convenience for deliberate review, not a second daemon or a background acquisition trigger.
