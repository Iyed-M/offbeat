# Test fixtures

Add fixtures only when a current feature needs them. Expected categories include:

- Adapter protocol and normalized Spotify candidate fixtures;
- M4 desired-state persistence/reconciliation fixtures;
- M5+ synthetic managed audio files;
- M6 controlled acquisition sources/local servers;
- M6B synthetic frozen YouTube evaluation observations and labels;
- M7 M3U8 golden files;
- M8 Android current-state manifest/sync fixtures.

Do not create fixture frameworks for deferred features before their implementation is scoped.

`youtube-evaluation-synthetic-v1.json` is deliberately synthetic. Its video IDs, metadata, recording-identity labels, and evidence are invented to test deterministic replay and outcome accounting; they are not captured YouTube results or real ground-truth claims.
