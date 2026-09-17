# Spicetify Collection Adapter

This directory contains the dependency-free Spicetify extension established in Milestones 2–3. It uses the browser-native `WebSocket` API and the ambiently authenticated Spotify Desktop `Platform` facade. On a daemon-issued Snapshot request it collects Liked Songs and every playlist in the recursive rootlist, then sends one normalized candidate snapshot.

It does not access files or SQLite, communicate with Android, retain Spotify credentials, call Spotify private HTTP routes directly, or manage Offbeat desired state.

## Development-only setup

This is a manual development path, not the final polished installation experience. Under the lean v1 roadmap, production-friendly credential provisioning and extension setup belong to later setup/packaging work after the core desktop and Android flows are proven. They are no longer tied to the old Milestone 12 numbering.

1. Supply the daemon's development Adapter endpoint and credential. The default endpoint is:

   ```text
   ws://127.0.0.1:16352/v1/adapter
   ```

   Generate a local development value if needed, but never commit it:

   ```sh
   credential="$(openssl rand -base64 32 | tr -d '\n')"
   export OFFBEAT_ADAPTER_CREDENTIAL="$credential"
   ```

2. Generate a local configured extension artifact. The helper writes only the output file you name; it does not discover, copy to, or configure Spicetify. The output is mode `0600` because it contains the credential.

   ```sh
   ./spicetify/offbeat/configure-extension.sh \
     --endpoint ws://127.0.0.1:16352/v1/adapter \
     --credential "$credential" \
     --output /tmp/offbeat-extension/offbeat.js
   ```

3. Manually copy the generated artifact to the custom-extension location appropriate for the local Spicetify installation, enable `offbeat.js` using the normal Spicetify workflow, and apply the configuration. Offbeat's current development path does not discover that location, modify enabled extensions, or invoke `spicetify` automatically.

4. Start `offbeatd` with the same Adapter credential, then start Spotify Desktop. Verify the Adapter session and run a real sync:

   ```sh
   offbeat status
   offbeat spotify sync
   ```

   Expected status includes:

   ```text
   Spotify adapter: connected
   ```

   A successful M3 collection reports candidate success and the extension logs counts for playlists, entries, unsupported entries, Liked Songs, and serialized candidate bytes.

5. Repeat `offbeat spotify sync` without restarting Spotify. It must collect another complete candidate successfully.

6. For collection-regression testing, a required Spotify page can be forced to fail from Spotify DevTools. For example, fail the next Liked Songs offset-zero request:

   ```js
   const originalGetTracks = Spicetify.Platform.LibraryAPI.getTracks;
   let failOnce = true;
   Spicetify.Platform.LibraryAPI.getTracks = async function (request) {
     if (failOnce && request.offset === 0) {
       failOnce = false;
       throw new Error("forced collection page failure");
     }
     return originalGetTracks.call(this, request);
   };
   ```

   The command must report candidate rejection and the extension must not send a partial candidate. Restore the method afterward:

   ```js
   Spicetify.Platform.LibraryAPI.getTracks = originalGetTracks;
   ```

Milestone 3 is complete. Once M4 persistence exists, the same forced failure must additionally prove that the previously committed Desired Spotify state remains unchanged.

## Pagination and size limits

The Adapter requests every page with explicit `offset` and `limit`, verifies usable pagination metadata, rejects early empty/non-advancing/inconsistent pages, and preserves returned item order.

Spotify Desktop can report `totalLength: 0` alongside nonempty Liked Songs items. The adapter treats that combination as the observed unavailable-count sentinel and terminates using the validated short/empty-page behavior established by M3.

When a Platform response exposes an `offset`, the adapter validates it against the requested page subject to the observed Desktop compatibility rule. The adapter never deduplicates entries to guess whether repeated content is pagination failure, because legitimate Spotify duplicate occurrences must survive.

The Adapter and daemon retain the 16 MiB application-message limit established and validated during M3. Do not change that limit without concrete evidence from a supported client/account shape.

The configured extension artifact is derived local state. Deleting it does not rotate the daemon's Adapter credential or alter Offbeat identity.

## Tests

Run extension tests without Spotify, Spicetify, or Internet access:

```sh
node --test spicetify/offbeat/offbeat.test.js
```

The tests use Node's built-in test runner and controlled fakes. Node is test-only; the extension itself has no Node runtime or package dependency.
