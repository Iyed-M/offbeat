# ADR 0012: Use conservative scored YouTube resolution

- Status: Accepted
- Date: 2026-09-20

## Context

M6A's exact token, artist-phrase, marker-set, and narrow-duration rules abstain on ordinary YouTube metadata variation. Real use produced unresolved outcomes for plausible tracks without explaining which rule rejected the candidates. spotDL demonstrates that bounded candidate search, normalized fuzzy field comparison, duration scoring, and artist evidence from titles and uploaders materially improve recall, but its policy always chooses a surviving result and may use popularity to break close matches.

## Decision

Offbeat will keep one built-in plain-YouTube resolver and replace exact metadata equality with deterministic field-specific eligibility gates and scoring. Resolution requires a best candidate above an acceptance floor and ahead of the runner-up by a declared margin. Meaningful version conflicts and implausible durations remain hard rejections. Popularity, provider expansion, and a surviving candidate alone are insufficient evidence.

Unresolved work will retain a bounded reason summary, not candidate history, confidence tiers, or review state. An explicit batch retry will requeue unresolved work after resolver policy changes.

## Consequences

- Resolver-local fuzzy comparison is allowed; library-wide matching and cross-track equivalence remain deferred.
- Fixture-backed recall and precision become completion criteria rather than exact-token behavior.
- Existing unresolved work can benefit from policy improvements without database surgery or per-ID retries.
- The resolver remains deterministic, inspectable, and able to abstain on ambiguity.
