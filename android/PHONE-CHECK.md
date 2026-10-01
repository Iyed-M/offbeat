# Try the storage proof on your phone

You do not need Android development experience for this check. The app is a small
test tool that writes bundled sound files and playlists, before real music sync is
added. The test sounds are deliberately simple tones, not songs.

1. Build the APK using `README.md`, or download the `android-storage-proof`
   artifact from a passing GitHub Actions run. The installable file is
   `app-debug.apk`. Copy it to your Android phone and open it. Android may ask you
   to allow installation from the app that opened the file; allow that source for
   this install. This test app requires Android 10 or newer.
2. Open **Offbeat storage proof**. Tap **Choose shared folder**. In the Android
   folder picker, choose internal storage, open **Music**, create an **Offbeat**
   folder, and choose **Use this folder**. This gives Offbeat access to that folder.
3. Tap **Publish fixture v1**. Wait for the result. The app shows a folder name
   beginning `Offbeat-fixture-`. Each publication creates its own such folder.
4. Open whichever music app you already use for local audio. Use its folder scan
   or playlist import to open `Music/Offbeat/<the displayed folder>/playlists/Ordered
   duplicates.m3u8`. Check that it plays tone A, then B, then A again. Check that
   the title, artist, album, and colored cover display. The player must support
   local M3U8 playlist import; tell us the app's name and result if it does not.
5. Return to Offbeat and tap **Fail replacement after staging audio**. It should
   report failure. Go back to the player: the original playlist should still play,
   and no unfinished new sound should appear after rescanning.
6. Tap **Publish replacement v2**. Open the newly displayed folder's playlist in
   the player. It should play B, A, B, with v2 titles. The earlier folder is retained
   so you can compare it; the app does not delete your other files.
7. Close and reopen Offbeat, then restart your phone. The selected folder should
   still be remembered. Turn on airplane mode and play the playlists again. Your
   computer can be switched off for this check.

Tell us the phone model, Android version (usually under Settings → About phone),
the music app name/version (usually its About screen), and which steps passed or
failed. Screenshots are useful. A failure is useful information too; we need this
check before choosing the storage behavior for real music sync.

The developer checklist and evidence record are in
[docs/android-storage-device-evidence.md](../docs/android-storage-device-evidence.md).
