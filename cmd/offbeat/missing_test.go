package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/desired"
)

func TestCLIMissingDesiredTracks(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-m5-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	configPath := writeCLIAdapterConfig(t, home)
	d, stop := startMissingDaemon(t, home)
	defer stop()
	assertMissingOutput(t, home, "Missing tracks: 0 (0 available of 0 desired).\n")
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), missingCandidate()); err != nil {
		t.Fatal(err)
	}
	want := "Missing tracks: 2 (0 available of 2 desired).\nspotify:track:alpha\tSame title\tArtist\nspotify:track:beta\tSame title\tArtist\n"
	assertMissingOutput(t, home, want)
	stop()
	_, stop = startMissingDaemon(t, home)
	defer stop()
	// The CLI must only read bootstrap settings, not revalidate daemon config.
	if err := os.WriteFile(configPath, []byte("[spotify_adapter]\nport = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertMissingOutput(t, home, want)
}

func missingCandidate() desired.Candidate {
	alpha := desired.Track{URI: "spotify:track:alpha", Name: "Same title", Artists: []desired.NamedURI{{URI: "spotify:artist:one", Name: "Artist"}}, Album: desired.NamedURI{URI: "spotify:album:one", Name: "Album"}, DurationMS: 1000}
	beta := alpha
	beta.URI = "spotify:track:beta"
	return desired.Candidate{
		Playlists: []desired.CandidatePlaylist{{URI: "spotify:playlist:one", Name: "Playlist", Entries: []desired.CandidateEntry{
			{Position: 0, Kind: desired.EntrySupported, Track: &beta},
			{Position: 1, Kind: desired.EntrySupported, Track: &alpha},
			{Position: 2, Kind: desired.EntrySupported, Track: &alpha},
			{Position: 3, Kind: desired.EntryUnsupported, SourceURI: "spotify:episode:one"},
		}}},
		LikedSongs: []desired.CandidateEntry{{Position: 0, Kind: desired.EntrySupported, Track: &beta}},
	}
}

func TestCLIManagedFixtureAvailability(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-m5-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	configPath := writeCLIAdapterConfig(t, home)
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	customRoot := filepath.Join(home, "custom-managed")
	configBytes = append(configBytes, []byte(fmt.Sprintf("\n[paths]\nmusic_root = %q\n", customRoot))...)
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	d, stop := startMissingDaemon(t, home)
	defer stop()
	if _, err := os.Stat(filepath.Join(customRoot, "playlists")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "Music", "Offbeat")); !os.IsNotExist(err) {
		t.Fatalf("created default managed root with override: %v", err)
	}
	ctx := context.Background()
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(ctx, missingCandidate()); err != nil {
		t.Fatal(err)
	}
	path, err := d.RegisterSyntheticTrackFixture(ctx, "spotify:track:alpha")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := d.RegisterSyntheticTrackFixture(ctx, "spotify:track:alpha"); err != nil || again != path {
		t.Fatalf("repeat fixture = %q, %v", again, err)
	}
	files, err := os.ReadDir(filepath.Join(d.Cfg.Paths.MusicRoot, "tracks"))
	if err != nil || len(files) != 1 {
		t.Fatalf("managed files = %v, %v", files, err)
	}
	want := "Missing tracks: 1 (1 available of 2 desired).\nspotify:track:beta\tSame title\tArtist\n"
	assertMissingOutput(t, home, want)
	stop()
	d, stop = startMissingDaemon(t, home)
	defer stop()
	assertMissingOutput(t, home, want)
	if err := os.Remove(filepath.Join(d.Cfg.Paths.MusicRoot, path)); err != nil {
		t.Fatal(err)
	}
	assertMissingOutput(t, home, "Missing tracks: 2 (0 available of 2 desired).\nspotify:track:alpha\tSame title\tArtist\nspotify:track:beta\tSame title\tArtist\n")
	if _, err := d.RegisterSyntheticTrackFixture(ctx, "spotify:track:alpha"); err != nil {
		t.Fatal(err)
	}
	other, err := d.RegisterSyntheticTrackFixture(ctx, "spotify:track:beta")
	if err != nil || other == path {
		t.Fatalf("distinct identity path = %q, %v", other, err)
	}
	assertMissingOutput(t, home, "Missing tracks: 0 (2 available of 2 desired).\n")
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(ctx, desired.Candidate{}); err != nil {
		t.Fatal(err)
	}
	assertMissingOutput(t, home, "Missing tracks: 0 (0 available of 0 desired).\n")
	if _, err := os.Stat(filepath.Join(d.Cfg.Paths.MusicRoot, path)); err != nil {
		t.Fatalf("removing desired references deleted managed audio: %v", err)
	}
	candidate := missingCandidate()
	candidate.Playlists[0].Entries[1].Track.Name = "Renamed / unsafe title"
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	assertMissingOutput(t, home, "Missing tracks: 0 (2 available of 2 desired).\n")
	if again, err := d.RegisterSyntheticTrackFixture(ctx, "spotify:track:alpha"); err != nil || again != path {
		t.Fatalf("metadata change altered path: %q %v", again, err)
	}
}

func TestCLIManagedFixtureRegistrationFailure(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-m5-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	writeCLIAdapterConfig(t, home)
	d, stop := startMissingDaemon(t, home)
	defer stop()
	ctx := context.Background()
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(ctx, missingCandidate()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterSyntheticTrackFixture(ctx, "spotify:episode:one"); err == nil {
		t.Fatal("registered unsupported entry")
	}
	if _, err := d.DB.Exec(`CREATE TRIGGER fail_registration BEFORE INSERT ON managed_tracks BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterSyntheticTrackFixture(ctx, "spotify:track:alpha"); err == nil {
		t.Fatal("registration unexpectedly succeeded")
	}
	assertMissingOutput(t, home, "Missing tracks: 2 (0 available of 2 desired).\nspotify:track:alpha\tSame title\tArtist\nspotify:track:beta\tSame title\tArtist\n")
	if _, err := d.DB.Exec(`DROP TRIGGER fail_registration`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterSyntheticTrackFixture(ctx, "spotify:track:alpha"); err != nil {
		t.Fatal(err)
	}
	assertMissingOutput(t, home, "Missing tracks: 1 (1 available of 2 desired).\nspotify:track:beta\tSame title\tArtist\n")
}

func TestCLIMissingLargeLibrary(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-m5-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	writeCLIAdapterConfig(t, home)
	d, stop := startMissingDaemon(t, home)
	defer stop()
	candidate := desired.Candidate{}
	var want strings.Builder
	want.WriteString("Missing tracks: 600 (128 available of 728 desired).\n")
	for i := 0; i < 728; i++ {
		// Large display metadata forces byte-budget pagination before 128 tracks.
		track := desired.Track{URI: fmt.Sprintf("spotify:track:%04d", i), Name: strings.Repeat("title", 1200), Artists: []desired.NamedURI{{URI: "artist", Name: "Artist"}}, Album: desired.NamedURI{URI: "album", Name: "Album"}, DurationMS: 1000}
		candidate.LikedSongs = append(candidate.LikedSongs, desired.CandidateEntry{Position: i, Kind: desired.EntrySupported, Track: &track})
		if i >= 128 {
			fmt.Fprintf(&want, "%s\t%s\tArtist\n", track.URI, track.Name)
		}
	}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	// The first page has no missing tracks, but the CLI must continue past it.
	for i := 0; i < 128; i++ {
		if _, err := d.RegisterSyntheticTrackFixture(context.Background(), fmt.Sprintf("spotify:track:%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	assertMissingOutput(t, home, want.String())
}

func startMissingDaemon(t *testing.T, home string) (*app.Daemon, func()) {
	t.Helper()
	d, err := app.NewDaemon(context.Background(), app.Options{HomeDir: home, AdapterCredential: "test-credential", EnableSyntheticFixtures: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, app.RunOptions{SignalCh: make(chan os.Signal)}) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	}
	t.Cleanup(stop)
	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	return d, stop
}

func assertMissingOutput(t *testing.T, home, want string) {
	t.Helper()
	out, stderr, err := runCLI(t, home, "missing")
	if err != nil || stderr != "" || out != want {
		if len(want) > 4096 {
			t.Fatalf("missing response mismatch: got %d bytes, want %d; stderr %q err %v", len(out), len(want), stderr, err)
		}
		t.Fatalf("missing = %q, stderr %q, err %v; want %q", out, stderr, err, want)
	}
}
