# M2 Spicetify Synthetic Adapter

This directory contains the dependency-free Milestone 2 extension. It uses the
browser-native `WebSocket` API only. It does not call Spotify APIs, inspect
Spotify data, access files or SQLite, communicate with Android, or send an
unsolicited snapshot.

## Development-only setup

This is a manual development path, not Offbeat's final installation behavior.
Milestone 12 owns production credential provisioning, extension installation,
Spicetify configuration, and `offbeat setup`.

1. Supply the daemon's development adapter endpoint and credential. The M2
   protocol default endpoint is `ws://127.0.0.1:16352/v1/adapter`. Generate a
   local test value if needed, but never commit it:

   ```sh
   credential="$(openssl rand -base64 32 | tr -d '\n')"
   ```

   Supply that exact value to the compatible daemon through its development
   Adapter credential injection mechanism, then use the same value in step 2.
   Daemon-side injection is implemented by M2 issue #11 and is deliberately
   not invented or implemented by this extension-only ticket. It is not a
   final production setup interface.

2. Generate a local configured artifact. This script only writes the file you
   name; it does not discover, copy to, or configure Spicetify. The output is
   mode `0600` because it contains the credential.

   ```sh
   ./spicetify/offbeat/configure-extension.sh \
     --endpoint ws://127.0.0.1:16352/v1/adapter \
     --credential "$credential" \
     --output /tmp/offbeat-m2/offbeat.js
   ```

3. Manually copy the generated artifact to the Spicetify custom-extension
   location appropriate for the local Spicetify installation, enable
   `offbeat.js` using the user's normal Spicetify configuration workflow, and
   manually apply that configuration. This repository does not discover that
   location, change enabled extensions, or invoke `spicetify`.

4. Start an M2-compatible `offbeatd` configured with the same loopback endpoint
   and development credential, then start Spotify Desktop. Run:

   ```sh
   offbeat status
   offbeat spotify sync
   ```

   The expected output is `Spotify adapter: connected` followed by `Spotify
   synthetic snapshot received.` Stop Spotify and verify status becomes
   `Spotify adapter: disconnected`; restart Spotify, wait for reconnection, and
   run the sync command again.

The configured artifact is derived local state. Deleting it does not rotate the
daemon credential or alter Offbeat identity.

## Tests

Run the extension tests without Spotify, Spicetify, or network access:

```sh
node --test spicetify/offbeat/offbeat.test.js
```

The tests use Node's built-in test runner and a controlled in-memory
WebSocket-compatible peer. Node is test-only; the extension has no Node runtime
or package dependency.
