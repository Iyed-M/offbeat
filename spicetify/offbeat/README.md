# M3 Spicetify Collection Adapter

This directory contains the dependency-free Milestone 3 extension. It uses the
browser-native `WebSocket` API and the ambiently authenticated Spotify Desktop
`Platform` facade. On a daemon-issued request it collects Liked Songs and every
playlist in the recursive rootlist, then sends one normalized candidate snapshot.
It does not access files or SQLite, communicate with Android, retain Spotify
credentials, call private routes, or send unsolicited snapshots.

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
      --output /tmp/offbeat-m3/offbeat.js
   ```

3. Manually copy the generated artifact to the Spicetify custom-extension
   location appropriate for the local Spicetify installation, enable
   `offbeat.js` using the user's normal Spicetify configuration workflow, and
   manually apply that configuration. This repository does not discover that
   location, change enabled extensions, or invoke `spicetify`.

4. Start an M3-compatible `offbeatd` configured with the same loopback endpoint
   and development credential, then start Spotify Desktop. Confirm the adapter
   is connected, run one real sync, and retain the daemon log line produced by
   the extension:

    ```sh
    offbeat status
    offbeat spotify sync
    ```

    The expected output is `Spotify adapter: connected` followed by `Spotify
    candidate snapshot received.` Record the log's `playlists`, `entries`,
    `unsupported_entries`, `liked_songs`, and `candidate_bytes` values. Compare
    the first four counts with the visible account under M3 semantics, and
    record `candidate_bytes` for the 16 MiB adapter-message-limit check. The
    logged size is the serialized candidate snapshot only; it deliberately
    excludes the adapter credential and Spotify error objects.

5. Without restarting Spotify, repeat `offbeat spotify sync`. It must again
   report `Spotify candidate snapshot received.` and produce a complete set of
   counts.

6. In Spotify DevTools, deliberately force one required Liked Songs page to
   fail, then run one more sync. This example fails only the next offset-zero
   request and leaves later requests untouched:

    ```js
    const originalGetTracks = Spicetify.Platform.LibraryAPI.getTracks;
    let failOnce = true;
    Spicetify.Platform.LibraryAPI.getTracks = async function (request) {
      if (failOnce && request.offset === 0) {
        failOnce = false;
        throw new Error("forced M3 page failure");
      }
      return originalGetTracks.call(this, request);
    };
    ```

    The command must report a snapshot rejection during `liked_songs` at offset
    zero. The extension may send the bounded error response, but it must not
    send a candidate response. Restore the original method after the check:

    ```js
    Spicetify.Platform.LibraryAPI.getTracks = originalGetTracks;
    ```

7. Confirm no partial candidate was accepted: the failed command has no
   candidate-success output, the daemon has no snapshot persistence, and no
   SQLite snapshot/reconciliation state exists in M3. This procedure is the
   human validation gate for issue #34; do not mark it complete based on the
   automated tests alone.

## Pagination and size limits

The adapter requests every page with explicit `offset` and `limit`, verifies a
non-negative stable `totalLength`, rejects pages that overrun the remaining
total, rejects an empty page before completion, and requires forward progress.
Spotify Desktop can report `totalLength: 0` alongside nonempty Liked Songs
items. The adapter treats that combination as an unavailable-count sentinel,
requires every subsequent page to retain that sentinel, and completes on a
short page (or an empty page after a full page).
When a Platform response exposes an `offset`, the adapter also requires it to
match the requested offset, which rejects an API that repeats an earlier page.
Some supported Platform builds expose only `items` and `totalLength`; without a
returned page position, two distinct pages with identical entries cannot be
reliably distinguished from legitimate duplicate Spotify entries. The adapter
does not deduplicate entries to guess at that condition.

The adapter and daemon both retain the 16 MiB message limit. Do not change that
limit without real-client evidence. During the acceptance run, compare the
recorded `candidate_bytes` with 16 MiB (16,777,216 bytes); if it approaches the
limit, retain the log evidence for a later design decision rather than changing
the protocol ad hoc.

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
