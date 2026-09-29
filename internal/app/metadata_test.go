package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/Iyed-M/offbeat/internal/managed"
	"github.com/Iyed-M/offbeat/internal/tagging"
)

func TestMetadataRefreshExistingAudioSyncRetryAndRestart(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
				t.Fatal(err)
			}
			t.Skip(err)
		}
	}
	if err := exec.Command("python3", "-I", "-c", "import mutagen; assert mutagen.version_string == '1.47.0'").Run(); err != nil {
		if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
			t.Fatal("pinned Mutagen unavailable")
		}
		t.Skip("pinned Mutagen unavailable")
	}
	home := t.TempDir()
	fixture := filepath.Join(home, "original.flac")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.2", "-c:a", "flac", fixture).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	var retrievals atomic.Int32
	retriever := retrieveFunc(func(context.Context, string) (*acquisition.Media, error) { retrievals.Add(1); return nil, nil })
	d := acquisitionDaemon(t, home, retriever, 1)
	seedAcquisitionTracks(t, d, "one", "two")
	uri := "spotify:track:one"
	file, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path, err := d.managedFiles.PublishNew(context.Background(), uri, file, "flac")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DB.RegisterManagedTrack(context.Background(), uri, path); err != nil {
		t.Fatal(err)
	}
	refresh := func() ipc.MetadataRefreshResult {
		t.Helper()
		value, err := d.handleControlRequest(context.Background(), ipc.Request{Command: "metadata.refresh"})
		if err != nil {
			t.Fatal(err)
		}
		return value.(ipc.MetadataRefreshResult)
	}
	if got := refresh(); got.Changed != 1 || got.Missing != 1 || got.Failed != 0 {
		t.Fatalf("initial: %+v", got)
	}
	published := filepath.Join(d.Cfg.Paths.MusicRoot, path)
	first, err := os.ReadFile(published)
	if err != nil {
		t.Fatal(err)
	}
	probe := func(want string) {
		t.Helper()
		out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags=title", "-of", "json", published).CombinedOutput()
		if err != nil || !bytes.Contains(out, []byte(want)) {
			t.Fatalf("native title: %s %v", out, err)
		}
	}
	probe("one")
	if got := refresh(); got.Skipped != 1 || got.Missing != 1 {
		t.Fatalf("repeat: %+v", got)
	}
	if after, _ := os.ReadFile(published); !bytes.Equal(first, after) {
		t.Fatal("idempotent refresh rewrote file")
	}
	track := desired.Track{URI: uri, Name: "Renamed", Artists: []desired.NamedURI{{URI: "artist", Name: "Artist"}}, Album: desired.NamedURI{URI: "album", Name: "Album"}, DurationMS: 1000}
	other := track
	other.URI, other.Name = "spotify:track:two", "two"
	candidate := desired.Candidate{LikedSongs: []desired.CandidateEntry{{Position: 0, Kind: desired.EntrySupported, Track: &track}, {Position: 1, Kind: desired.EntrySupported, Track: &other}}}
	if _, _, changed, err := d.DB.ApplyDesiredSpotifyState(context.Background(), candidate); err != nil || !changed {
		t.Fatalf("metadata sync: %v %v", changed, err)
	}
	var state string
	if err := d.DB.QueryRow(`SELECT tag_state FROM managed_tracks WHERE track_uri=?`, uri).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("stale state: %s %v", state, err)
	}
	d.Cfg.Downloader.FFprobePath = "missing-probe"
	if got := refresh(); got.Failed != 1 || len(got.Diagnostics) != 1 || got.Missing != 1 {
		t.Fatalf("failure isolation: %+v", got)
	}
	if after, _ := os.ReadFile(published); !bytes.Equal(first, after) {
		t.Fatal("tagging failure changed prior audio")
	}
	d.Cfg.Downloader.FFprobePath = "ffprobe"
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = acquisitionDaemon(t, home, retriever, 1)
	t.Cleanup(func() { _ = d.Close() })
	if got := refresh(); got.Changed != 1 || got.Failed != 0 {
		t.Fatalf("durable retry: %+v", got)
	}
	probe("Renamed")
	if got := refresh(); got.Skipped != 1 || got.Missing != 1 {
		t.Fatalf("post-restart repeat: %+v", got)
	}
	// Artwork failure is independently retryable and must not replace good
	// text-tagged audio or make the track Missing.
	if _, err := d.DB.Exec(`UPDATE spotify_tracks SET artwork_url='https://127.0.0.1/invalid' WHERE uri=?`, uri); err != nil {
		t.Fatal(err)
	}
	beforeArt, _ := os.ReadFile(published)
	if got := refresh(); got.Failed != 1 || got.Missing != 1 {
		t.Fatalf("artwork failure: %+v", got)
	}
	if afterArt, _ := os.ReadFile(published); !bytes.Equal(beforeArt, afterArt) {
		t.Fatal("failed artwork replaced audio")
	}
	var artState string
	if err := d.DB.QueryRow(`SELECT artwork_state FROM managed_tracks WHERE track_uri=?`, uri).Scan(&artState); err != nil || artState != "failed" {
		t.Fatalf("durable artwork failure: %s %v", artState, err)
	}
	if _, err := d.DB.Exec(`UPDATE spotify_tracks SET artwork_url=NULL WHERE uri=?`, uri); err != nil {
		t.Fatal(err)
	}
	if got := refresh(); got.Changed != 1 || got.Failed != 0 {
		t.Fatalf("artwork recovery: %+v", got)
	}
	beforeCancel, _ := os.ReadFile(published)
	if _, err := d.DB.Exec(`UPDATE managed_tracks SET tag_state='pending' WHERE track_uri=?`, uri); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.handleMetadataRefresh(canceled); err == nil {
		t.Fatal("canceled batch succeeded")
	}
	if afterCancel, _ := os.ReadFile(published); !bytes.Equal(beforeCancel, afterCancel) {
		t.Fatal("cancellation replaced good audio")
	}
	if got := refresh(); got.Changed != 1 {
		t.Fatalf("retry after cancel: %+v", got)
	}
	// Simulate a crash after the replacement intent is committed but before
	// the file exchange; the previous digest permits a safe fresh attempt.
	current, err := d.managedFiles.OpenManaged(uri, path)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := managed.Digest(context.Background(), current)
	current.Close()
	if err != nil {
		t.Fatal(err)
	}
	preExchange := db.RefreshIntent{SHA256: strings.Repeat("0", 64), PreviousSHA: previous, Temporary: managed.NewRefreshTemporary(), TagState: "tagged"}
	encoded, _ := json.Marshal(preExchange)
	if _, err := d.DB.Exec(`UPDATE managed_tracks SET refresh_intent=?, tag_state='pending' WHERE track_uri=?`, string(encoded), uri); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = acquisitionDaemon(t, home, retriever, 1)
	if got := refresh(); got.Changed != 1 {
		t.Fatalf("pre-exchange restart: %+v", got)
	}
	if retrievals.Load() != 0 || !d.managedFiles.Available(uri, path) {
		t.Fatalf("reacquisition or lost audio: %d", retrievals.Load())
	}
	// A complete rename followed by an interrupted DB update is reconciled
	// from the exact published digest, without rewriting the file.
	f, err := d.managedFiles.OpenManaged(uri, path)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := managed.Digest(context.Background(), f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	text, art := db.PresentationFingerprint(track)
	intent := db.RefreshIntent{SHA256: sha, PreviousSHA: sha, Temporary: managed.NewRefreshTemporary(), TagFingerprint: text, ArtworkFingerprint: art, TagState: tagging.Tagged, ArtworkState: "unavailable"}
	data, _ := json.Marshal(intent)
	if _, err := d.DB.Exec(`UPDATE managed_tracks SET refresh_intent=?, tag_state='pending' WHERE track_uri=?`, string(data), uri); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = acquisitionDaemon(t, home, retriever, 1)
	if got := refresh(); got.Skipped != 1 {
		t.Fatalf("reconcile: %+v", got)
	}
	if err := d.DB.QueryRow(`SELECT refresh_intent FROM managed_tracks WHERE track_uri=?`, uri).Scan(&state); err != nil || state != "" {
		t.Fatalf("intent not reconciled: %s %v", state, err)
	}
	if _, err := d.DB.Exec(`UPDATE managed_tracks SET tag_state='pending' WHERE track_uri=?`, uri); err != nil {
		t.Fatal(err)
	}
	// The recorded digest prevents clobbering an externally modified valid file.
	if err := os.WriteFile(published, append(first, []byte("external")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := refresh(); got.Failed != 1 || !strings.Contains(got.Diagnostics[0].Reason, "outside Offbeat") {
		t.Fatalf("changed file: %+v", got)
	}
}
