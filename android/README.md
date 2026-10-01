# Android storage proof (#86)

A Kotlin/Compose fixture publisher, before LAN sync. It has no network permission,
Spotify integration, authentication state, or playback engine. Synthetic MP3 tones
with ID3 title/artist/album/track and embedded covers are bundled in the APK.

## Build and deterministic checks

Use JDK 17 or newer and Android SDK platform 35 / build-tools 35.0.0. Set
`ANDROID_HOME` or create ignored `local.properties` with `sdk.dir=/path/to/sdk`.
The pinned Gradle wrapper verifies its distribution SHA-256.

```sh
export JAVA_HOME=/path/to/jdk-17-or-newer
export ANDROID_HOME=/path/to/android-sdk
cd android
./gradlew testDebugUnitTest assembleDebug assembleDebugAndroidTest lintDebug
python3 scripts/verify-fixtures.py  # requires ffmpeg/ffprobe
```

APK: `app/build/outputs/apk/debug/app-debug.apk`. JVM tests exercise the public
fixture publication seam with an in-memory document provider, including controlled
failures. Instrumentation exercises the same publisher over real SAF operations
on a device; it requires a folder selected by the app (instructions below).

Fixtures are authored synthetic tones and flat-color embedded covers; no remote
media or copyrighted recordings are used. To regenerate them (FFmpeg with
libmp3lame): `python3 scripts/generate-fixtures.py`. Committed bytes are the normal
build/test input; rebuilding them is optional and can differ across FFmpeg versions.

## Storage choice and limits

Use SAF `ACTION_OPEN_DOCUMENT_TREE` and persist both read and write grants. The
initial supported *candidate* is the platform `com.android.externalstorage.documents`
provider on the `primary` volume, in a dedicated subtree such as `Music/Offbeat`.
Cloud providers, removable volumes, root destinations and arbitrary third-party
providers are rejected before writes. No all-files, legacy storage, or media-read
permission is requested. SAF URIs never appear in M3U8 contents.

Before publication a unique scratch directory exercises actual create, write,
read-back, file delete, directory rename and directory delete. Capability flags
alone do not establish provider behavior. The probe only modifies its own newly
created scratch content. Previously published files and unrelated content remain
untouched on rejection.

Each press stages a fresh fixture set below a hidden `.offbeat-staging-<id>`
directory with `.nomedia`. Every audio and playlist byte is read back and checked
before the directory is renamed to `Offbeat-fixture-<id>`. `.nomedia` is removed
only after that complete directory exists. Relative playlist paths stay valid
across rename: `playlists/*.m3u8` references `../tracks/{a,b}.mp3`.

Replacement v2 uses different tagged audio/artwork metadata and ordering. It
publishes a fresh set instead of editing the old set in place. **Old sets are
retained deliberately** in this storage proof; select the new set explicitly in
the player. This proves failure-safe replacement without assuming provider-wide
atomic overwrites. It is not the final sync inventory/planner or a claim of stable
file handles across replacement. Do not carry generation retention into full sync
without the publication/recovery decisions in #85.

Controlled failure after the first staged audio deletes only the new staging set
and leaves the previous set intact. If cleanup is denied, the result reports
incomplete cleanup. Process death may leave a hidden `.nomedia` staging directory
or a complete renamed set; the next publication uses a new unique directory and
never removes unknown content. Private selected-tree and last-result preferences
survive restart and are excluded from backups. Changing destination preserves the
old destination. Clearing app data loses the remembered selection, not music.

After publication the app requests `MediaScannerConnection.scanFile` for the
supported provider's local audio/playlist paths; paths are derived only for
indexing, never for file mutation. Null scan results are reported, and the player
may need its own folder rescan/import. Indexing, hidden-directory exclusion,
playlist parsing and cover display are physical-device checks, not inferred from
SAF flags or JVM tests. No provider/player is validated until evidence is recorded.

Platform references: [SAF and persisted grants](https://developer.android.com/training/data-storage/shared/documents-files),
[document operations](https://developer.android.com/reference/android/provider/DocumentsContract),
[media scanning](https://developer.android.com/reference/android/media/MediaScannerConnection).

## Physical device validation (completion gate)

Install with `adb install -r app/build/outputs/apk/debug/app-debug.apk`. Keep the app
foreground while publishing. See [device evidence](../docs/android-storage-device-evidence.md)
for the exact checklist and a place to record phone/Android/player versions.

Choose `Music/Offbeat` via the system picker before running provider instrumentation:

```sh
./gradlew connectedDebugAndroidTest
```

Instrumentation reads the app's private persisted selection, uses unique sets, and
cleans only its own test output. No selected tree is a failure, not a silently
skipped compatibility test. The tests cannot prove a third-party player's UI or
airplane-mode playback. Those require the separate checklist and human evidence.

For a phone check without developer tools, follow [PHONE-CHECK.md](PHONE-CHECK.md).
