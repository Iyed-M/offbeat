package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func metadataPlaylistFixture(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
				t.Fatal(err)
			}
			t.Skipf("%s unavailable", tool)
		}
	}
	if err := exec.Command("python3", "-I", "-c", "import mutagen; assert mutagen.version_string == '1.47.0'").Run(); err != nil {
		if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
			t.Fatal("pinned Mutagen unavailable")
		}
		t.Skip("pinned Mutagen unavailable")
	}
	home := t.TempDir()
	fixture := filepath.Join(home, "before.mp3")
	if output, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.35", "-c:a", "libmp3lame", fixture).CombinedOutput(); err != nil {
		t.Fatalf("generate MP3: %v %s", err, output)
	}
	d := acquisitionDaemon(t, home, nil, 1)
	t.Cleanup(func() { _ = d.Close() })
	track := desired.Track{URI: "spotify:track:one", Name: "Spotify Title", Artists: []desired.NamedURI{{URI: "spotify:artist:one", Name: "Artist"}}, Album: desired.NamedURI{URI: "spotify:album:one", Name: "Album"}, DurationMS: 900000}
	entry := desired.CandidateEntry{Kind: desired.EntrySupported, Track: &track}
	candidate := desired.Candidate{LikedSongs: []desired.CandidateEntry{entry}, Playlists: []desired.CandidatePlaylist{{URI: "spotify:playlist:mix", Name: "Mix", Entries: []desired.CandidateEntry{entry}}}}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path, err := d.managedFiles.PublishNew(context.Background(), track.URI, file, "mp3")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DB.RegisterManagedTrack(context.Background(), track.URI, path); err != nil {
		t.Fatal(err)
	}
	if err := d.materializePlaylists(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d, track.URI, path
}

func refreshMetadataPlaylist(t *testing.T, d *Daemon) ipc.MetadataRefreshResult {
	t.Helper()
	value, err := d.handleControlRequest(context.Background(), ipc.Request{Command: "metadata.refresh"})
	if err != nil {
		t.Fatal(err)
	}
	return value.(ipc.MetadataRefreshResult)
}

func assertCurrentMetadataPlaylist(t *testing.T, d *Daemon, path string) {
	t.Helper()
	full := filepath.Join(d.Cfg.Paths.MusicRoot, path)
	output, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", full).Output()
	if err != nil {
		t.Fatal(err)
	}
	duration := formatPlaylistDuration(strings.TrimSpace(string(output)))
	if duration == "" {
		t.Fatal("published audio has no measured duration")
	}
	want := "#EXTM3U\n#EXTINF:" + duration + ",Artist - Spotify Title\n../" + path + "\n"
	for _, name := range []string{likedSongsPlaylistFilename, "Mix.m3u8"} {
		playlist := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists", name)
		assertFileBytes(t, playlist, want)
		assertPlayableExtendedPlaylist(t, playlist)
	}
}

func TestMetadataRefreshReconcilesMeasuredPlayablePlaylist(t *testing.T) {
	d, uri, path := metadataPlaylistFixture(t)
	// Simulate a playlist based on the earlier MP3 bytes or an interrupted
	// prior materialization. Refresh must replace its stale derived content.
	for _, name := range []string{likedSongsPlaylistFilename, "Mix.m3u8"} {
		if err := os.WriteFile(filepath.Join(d.Cfg.Paths.MusicRoot, "playlists", name), []byte("#EXTM3U\n#EXTINF:900,Old - Old\n../"+path+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := refreshMetadataPlaylist(t, d); got.Changed != 1 || got.Failed != 0 {
		t.Fatalf("refresh: %+v", got)
	}
	if !d.managedFiles.Available(uri, path) {
		t.Fatal("audio unavailable after refresh")
	}
	assertCurrentMetadataPlaylist(t, d, path)
}

func TestMetadataRefreshPlaylistFailurePreservesCommitAndRetryRepairsProjection(t *testing.T) {
	d, uri, path := metadataPlaylistFixture(t)
	root := d.Cfg.Paths.MusicRoot
	playlists := filepath.Join(root, "playlists")
	parked := filepath.Join(root, "parked-playlists")
	if err := os.Rename(playlists, parked); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, playlists); err != nil {
		t.Fatal(err)
	}
	if got := refreshMetadataPlaylist(t, d); got.Changed != 1 || got.Failed != 0 {
		t.Fatalf("authoritative refresh changed by derived failure: %+v", got)
	}
	if !d.metadataPlaylistPending.Load() {
		t.Fatal("failed playlist reconciliation not pending for retry")
	}
	var tagState, digest string
	if err := d.DB.QueryRow(`SELECT tag_state, file_sha256 FROM managed_tracks WHERE track_uri=?`, uri).Scan(&tagState, &digest); err != nil || tagState != "tagged" || len(digest) != 64 || !d.managedFiles.Available(uri, path) {
		t.Fatalf("committed file/DB lost: %q %q %v", tagState, digest, err)
	}
	before, err := os.Stat(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(playlists); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parked, playlists); err != nil {
		t.Fatal(err)
	}
	if got := refreshMetadataPlaylist(t, d); got.Skipped != 1 || got.Changed != 0 || got.Failed != 0 {
		t.Fatalf("idempotent retry: %+v", got)
	}
	after, err := os.Stat(filepath.Join(root, path))
	if err != nil || !os.SameFile(before, after) || d.metadataPlaylistPending.Load() {
		t.Fatalf("retry rewrote audio or failed to clear pending: %v", err)
	}
	assertCurrentMetadataPlaylist(t, d, path)
}

func TestMetadataRefreshPlaylistProbeDoesNotHoldManagedMutex(t *testing.T) {
	d, _, path := metadataPlaylistFixture(t)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	started, release := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	script := filepath.Join(t.TempDir(), "slow-playlist-probe")
	content := fmt.Sprintf("#!/bin/sh\nfor arg in \"$@\"; do\n if [ \"$arg\" = 'format=duration' ]; then\n  : > %s\n  while [ ! -e %s ]; do sleep 0.01; done\n fi\ndone\nexec %s \"$@\"\n", strconv.Quote(started), strconv.Quote(release), strconv.Quote(ffprobe))
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	d.Cfg.Downloader.FFprobePath = script
	done := make(chan error, 1)
	go func() {
		value, err := d.handleControlRequest(context.Background(), ipc.Request{Command: "metadata.refresh"})
		if err == nil && value.(ipc.MetadataRefreshResult).Changed != 1 {
			err = fmt.Errorf("refresh did not publish audio: %+v", value)
		}
		done <- err
	}()
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("playlist probe did not start after refresh")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	acquired := make(chan struct{})
	go func() { d.managedMu.Lock(); d.managedMu.Unlock(); close(acquired) }()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("metadata refresh held managedMu during playlist FFprobe")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metadata refresh deadlocked during playlist reconciliation")
	}
	assertCurrentMetadataPlaylist(t, d, path)
}
