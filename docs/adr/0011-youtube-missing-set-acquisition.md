# ADR 0011: Make YouTube resolution a concrete v1 acquisition workflow

- Status: Accepted
- Date: 2026-09-18

Explicit per-track source URLs proved the `yt-dlp` retrieval boundary, but they do not achieve Offbeat's intended desktop workflow. After `offbeat spotify sync`, an explicit `offbeat acquire missing` command will take the current Missing track set, use a single built-in YouTube resolver to select one eligible media URL for each track from Spotify title, artist, duration, and version metadata, and submit each selection to the existing restart-safe Acquisition work path. Resolution must be deterministic and conservative: an ambiguous or ineligible result remains unresolved, while other tracks continue. Direct URL acquisition remains available as the user-controlled override.

This amends ADR-0010 only for acquisition source resolution. v1 still does not add a generalized resolver/plugin framework, cross-track deduplication, library-wide fuzzy matching, confidence tiers, or a persistent human review queue. The command is an explicit batch action rather than an automatic side effect of Spotify sync. Users are responsible for acquiring only media they are permitted to download; Offbeat does not bypass authentication, DRM, paywalls, or other access controls.
