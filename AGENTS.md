## Agent skills

### Issue tracker

Issues are tracked in GitHub Issues for this repository. See `docs/agents/issue-tracker.md`.

### Triage labels

Triage uses the canonical `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, and `wontfix` labels. See `docs/agents/triage-labels.md`.

### Domain docs

Domain documentation uses a single-context layout. See `docs/agents/domain.md`.

### Planning after M3

Read `docs/PRD.md`, `docs/IMPLEMENTATION_PLAN.md`, `CONTEXT.md`, ADR-0010, and ADR-0011 before proposing post-M3 architecture.

The project follows a current-state-first lean v1. Implement YouTube resolution as the concrete M6A Missing-set acquisition workflow. Keep historical revisions, library-wide matching/review, cross-track asset deduplication, generalized acquisition infrastructure, and advanced Android sync deferred unless a later scoped decision requires them.

Use `/to-spec` then `/to-tickets` for planned implementation. Wayfinder is optional and should be used only when a concrete external uncertainty or unresolved policy blocks implementation.
