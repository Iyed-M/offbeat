package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/Iyed-M/offbeat/internal/managed"
)

func TestSyntheticFixtureRegistrationReconcilesLivePlaylistAvailability(t *testing.T) {
	d, err := NewDaemon(context.Background(), Options{
		HomeDir:                 t.TempDir(),
		AdapterCredential:       testAdapterCredential,
		EnableSyntheticFixtures: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	one := lifecycleTrack("one")
	two := lifecycleTrack("two")
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: []desired.CandidateEntry{
		{Position: 0, Kind: desired.EntrySupported, Track: &one},
		{Position: 1, Kind: desired.EntrySupported, Track: &two},
	}}); err != nil {
		t.Fatal(err)
	}

	onePath, err := d.RegisterSyntheticTrackFixture(context.Background(), one.URI)
	if err != nil {
		t.Fatal(err)
	}
	twoPath, err := d.RegisterSyntheticTrackFixture(context.Background(), two.URI)
	if err != nil {
		t.Fatal(err)
	}
	playlist := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists", likedSongsPlaylistFilename)
	assertFileBytes(t, playlist, "#EXTM3U\n../"+onePath+"\n../"+twoPath+"\n")
	assertEveryPlaylistPathReadable(t, filepath.Dir(playlist))

	if err := os.Remove(filepath.Join(d.Cfg.Paths.MusicRoot, onePath)); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(playlist)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterSyntheticTrackFixture(context.Background(), two.URI); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, playlist, "#EXTM3U\n../"+twoPath+"\n")
	afterChange, err := os.Stat(playlist)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, afterChange) {
		t.Fatal("changed availability did not replace playlist")
	}

	time.Sleep(time.Millisecond)
	if _, err := d.RegisterSyntheticTrackFixture(context.Background(), two.URI); err != nil {
		t.Fatal(err)
	}
	afterNoop, err := os.Stat(playlist)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(afterChange, afterNoop) || !afterChange.ModTime().Equal(afterNoop.ModTime()) {
		t.Fatal("unchanged reconciliation rewrote playlist")
	}
}

func TestCompletedAcquisitionReconcilesPlaylistAndFailedAcquisitionDoesNot(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-m7-acq-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d := acquisitionDaemon(t, home, retrieveFunc(func(_ context.Context, source string) (*acquisition.Media, error) {
		if strings.Contains(source, "failed") {
			return nil, errors.New("controlled retrieval failure")
		}
		return fakeMedia(t)
	}), 1)
	seedAcquisitionTracks(t, d, "complete", "failed")
	runAcquisitionDaemon(t, d)

	complete := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:complete", SourceURL: "https://fixture.test/complete"}})
	waitAcquisition(t, d, complete.ID, "complete")
	failed := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:failed", SourceURL: "https://fixture.test/failed"}})
	waitAcquisition(t, d, failed.ID, "failed")

	playlist := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists", likedSongsPlaylistFilename)
	assertFileBytes(t, playlist, "#EXTM3U\n../"+managed.TrackPath("spotify:track:complete", "wav")+"\n")
	assertEveryPlaylistPathReadable(t, filepath.Dir(playlist))
}

func TestDaemonStartupRepairsPlaylistBeforeReadiness(t *testing.T) {
	home := t.TempDir()
	configPath := lifecycleConfig(t, home)
	d, err := NewDaemon(context.Background(), Options{HomeDir: home, ConfigPath: configPath, AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatal(err)
	}
	track := lifecycleTrack("startup")
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
	path, err := d.managedFiles.PublishSynthetic(track.URI)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DB.RegisterManagedTrack(context.Background(), track.URI, path); err != nil {
		t.Fatal(err)
	}
	playlist := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists", likedSongsPlaylistFilename)
	if err := os.WriteFile(playlist, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = NewDaemon(context.Background(), Options{HomeDir: home, ConfigPath: configPath, AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatal(err)
	}
	runAcquisitionDaemon(t, d)
	assertFileBytes(t, playlist, "#EXTM3U\n../"+path+"\n")
	logs, err := os.ReadFile(d.Cfg.Paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if ready := strings.Index(string(logs), "offbeatd ready"); ready < 0 {
		t.Fatal("daemon never declared readiness")
	}
}

func TestStartupReconciliationFailurePreventsReadinessWithBoundedError(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-m7-start-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d, err := NewDaemon(context.Background(), Options{HomeDir: home, ConfigPath: lifecycleConfig(t, home), AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists", likedSongsPlaylistFilename)
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	err = d.Run(context.Background(), RunOptions{SignalCh: make(chan os.Signal)})
	if err == nil || !strings.Contains(err.Error(), "reconcile desktop playlists before readiness") {
		t.Fatalf("Run error = %v", err)
	}
	if strings.Contains(err.Error(), home) || len(err.Error()) > 160 {
		t.Fatalf("startup error is not bounded: %q", err)
	}
	if _, statErr := os.Stat(SocketPath(d.Cfg.Paths.SocketDir)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("control socket remained after startup failure: %v", statErr)
	}
	logs, readErr := os.ReadFile(d.Cfg.Paths.LogFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(logs), "offbeatd ready") {
		t.Fatal("daemon declared readiness after reconciliation failure")
	}
}

func TestPostCommitReconciliationFailureKeepsMappingAndLaterConvergesWithoutPathLeak(t *testing.T) {
	home := t.TempDir()
	d, err := NewDaemon(context.Background(), Options{HomeDir: home, AdapterCredential: testAdapterCredential, EnableSyntheticFixtures: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	track := lifecycleTrack("isolated")
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
	playlistsDir := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists")
	oldPlaylistsDir := filepath.Join(d.Cfg.Paths.MusicRoot, "old-playlists")
	outside := filepath.Join(t.TempDir(), "unsafe-path-marker")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(playlistsDir, oldPlaylistsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, playlistsDir); err != nil {
		t.Fatal(err)
	}

	path, err := d.RegisterSyntheticTrackFixture(context.Background(), track.URI)
	if err != nil {
		t.Fatalf("authoritative registration rolled back by reconciliation: %v", err)
	}
	managedTracks, err := d.DB.DesiredManagedTracks(context.Background())
	if err != nil || len(managedTracks) != 1 || managedTracks[0].RelativePath != path {
		t.Fatalf("managed mapping = %#v, %v", managedTracks, err)
	}
	logs, err := os.ReadFile(d.Cfg.Paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logs), "reconcile desktop playlists after Managed track commit") {
		t.Fatalf("missing reconciliation failure log: %s", logs)
	}
	if strings.Contains(string(logs), outside) {
		t.Fatal("unsafe filesystem path leaked to log")
	}

	if err := os.Remove(playlistsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPlaylistsDir, playlistsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterSyntheticTrackFixture(context.Background(), track.URI); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, filepath.Join(playlistsDir, likedSongsPlaylistFilename), "#EXTM3U\n../"+path+"\n")
}

func lifecycleTrack(id string) desired.Track {
	return desired.Track{
		URI:        "spotify:track:" + id,
		Name:       id,
		Artists:    []desired.NamedURI{{URI: "spotify:artist:" + id, Name: "Artist"}},
		Album:      desired.NamedURI{URI: "spotify:album:" + id, Name: "Album"},
		DurationMS: 1000,
	}
}

func lifecycleConfig(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, "config.toml")
	content := fmt.Sprintf("[spotify_adapter]\nport=%d\n", availableAdapterTestPort(t))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
