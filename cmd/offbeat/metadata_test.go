package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/managed"
)

func TestCLIMetadataRefreshOverControl(t *testing.T) {
	home := t.TempDir()
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		t.Fatal("refresh invoked retriever")
		return nil, nil
	}))
	defer stop()
	track := desired.Track{URI: "spotify:track:one", Name: "one", Artists: []desired.NamedURI{{URI: "artist", Name: "Artist"}}, Album: desired.NamedURI{URI: "album", Name: "Album"}, DurationMS: 1000}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
	files, err := managed.Open(d.Cfg.Paths.MusicRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.PublishSynthetic(track.URI); err != nil {
		t.Fatal(err)
	}
	files.Close()
	if err := d.DB.RegisterManagedTrack(context.Background(), track.URI, managed.FixturePath(track.URI)); err != nil {
		t.Fatal(err)
	}
	if out, _, err := runCLI(t, home, "metadata", "refresh"); err == nil || !strings.Contains(out, "1 failed") {
		t.Fatalf("unsupported: %q %v", out, err)
	}
	if out, stderr, err := runCLI(t, home, "metadata", "refresh"); err != nil || stderr != "" || !strings.Contains(out, "1 skipped") {
		t.Fatalf("repeat: %q %q %v", out, stderr, err)
	}
	if _, err := os.Stat(filepath.Join(d.Cfg.Paths.MusicRoot, managed.FixturePath(track.URI))); err != nil {
		t.Fatalf("audio lost: %v", err)
	}
}
