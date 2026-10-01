# Android shared-storage / ordinary-player evidence (#86)

Status: **PENDING — no physical-device or player compatibility claim.**

Issue #86 remains gated on this record even when deterministic tests and APK builds
pass. Populate this file (or attach a linked report to the issue) after testing.
Do not substitute an emulator or successful SAF probe for ordinary-player evidence.

| Field | Observed value |
|---|---|
| Date / app commit | Pending |
| Phone manufacturer / model | Pending |
| Android version / API / build | Pending |
| Player name / version | Pending |
| Selected tree URI / provider authority | Pending |
| Shared local folder (e.g. internal Music/Offbeat) | Pending |
| Storage approach | SAF persisted read/write tree grant; MediaScanner-assisted indexing |
| Initial free space | Pending |
| APK / deterministic checks | Linux build and five JVM storage tests passed; device tests not run |
| Screenshots / recording / instrumentation log | Pending |

## Required observations

1. Create a dedicated `Music/Offbeat` folder in internal shared storage. Select it
   in the app. Record the provider authority and probe result. Confirm the manifest
   requests no broad all-files permission. Try a cloud/unsupported destination;
   verify clear rejection without modifying any existing files.
2. Publish v1. Import `Offbeat-fixture-<id>/playlists/Ordered duplicates.m3u8` into
   the chosen player. Confirm A, B, A in order, all three occurrences play, titles,
   artist and album tags display, and embedded cover art appears. Open Liked Songs
   and the empty playlist. Record whether a player folder rescan was required and
   whether Android media scanning indexed audio and/or playlists.
3. Force-stop and relaunch Offbeat; then reboot the phone. Confirm folder access
   remains and the saved destination/result can be used without choosing again.
   Verify the picker grant in Android settings/ADB where available. Confirm private
   preferences are absent from shared output.
4. Use **Fail replacement after staging audio**. Confirm it reports incomplete
   publication, v1 audio and playlists still play unchanged, and no new playable
   partial track/playlist appears in the player after a rescan. With a file manager
   show no leftover staging set after successful cleanup. If intentionally killing
   the app mid-write, any remaining staging set must stay hidden with `.nomedia`.
5. Publish replacement v2. Open the new set explicitly: B, A, B with v2 tags and
   different pitches. Confirm relative playlist paths work without editing. Old
   fixture sets are intentionally retained in this proof; do not treat a combined
   library listing of both sets as duplication in one playlist.
6. Run `cd android && ./gradlew connectedDebugAndroidTest` with the chosen tree
   already selected. Attach logs. This exercises real provider create/read/write,
   rename/delete, complete publication, controlled failure and existing-content
   preservation. Confirm unrelated files placed in the chosen root survive.
7. Revoke the selected folder grant and attempt publication. Confirm a clear
   access error and preserved existing content. Re-select and retry. Switch to
   another dedicated Music subtree and confirm the former set is untouched.
8. Enable airplane mode, stop the desktop, and restart the player. Open the v1 and
   v2 playlists and play all occurrences with artwork visible. Neither the app nor
   player may rely on Spotify, Offbeat desktop, or Internet access.

## Decision after the proof

- Validated phone/provider/player combination: **Pending**.
- Provider operation behavior / failure caveats: **Pending**.
- Media index and player rescan procedure: **Pending**.
- SAF retained or MediaStore-assisted publication needed for full sync: **Pending**.
- Follow-up constraints for #85 / dependent Android sync tickets: **Pending**.

Record failures as evidence too. If the chosen player ignores `.nomedia`/hidden
staging or cannot consume these relative M3U8 files, the approach has not passed;
change the scoped storage implementation before integrating network sync.

## Automated baseline (Linux, 2026-10-01)

JDK 21, Android SDK platform 35/build-tools 35.0.0, pinned Gradle 8.13:

```sh
./gradlew testDebugUnitTest assembleDebug assembleDebugAndroidTest lintDebug
python3 scripts/verify-fixtures.py
```

Results: five JVM storage tests pass, app and instrumentation APKs build, lint
passes with no errors, and four synthetic MP3s decode with expected tags and
embedded artwork. Lint retains advisory warnings about pinned older dependencies,
launcher icon and optional Kotlin extensions. `adb devices -l` listed no connected
device. Real-provider instrumentation, persisted access after phone reboot,
ordinary-player indexing/playlists and airplane-mode playback remain **unrun**.
