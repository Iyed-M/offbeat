package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/managed"
)

func TestAcquisitionTagOutcomeIndependentOfPlayableAudio(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	withMutagen := exec.Command("python3", "-c", "import mutagen; assert mutagen.version_string == '1.47.0'").Run() == nil
	for _, tc := range []struct{ name, extension, probe, expected string }{
		{"tagged", "flac", "ffprobe", "tagged"},
		{"unsupported", "wav", "ffprobe", "unsupported"},
		{"tag failure", "flac", "missing-offbeat-probe", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.expected == "tagged" && !withMutagen {
				t.Skip("pinned offline Mutagen interpreter unavailable")
			}
			home := t.TempDir()
			original := filepath.Join(home, "fixture."+tc.extension)
			codec := "flac"
			if tc.extension == "wav" {
				codec = "pcm_s16le"
			}
			cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.2", "-c:a", codec, original)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			mediaBytes, _ := os.ReadFile(original)
			d := acquisitionDaemon(t, home, retrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
				f, err := os.Open(original)
				return &acquisition.Media{File: f, Extension: tc.extension}, err
			}), 1)
			d.Cfg.Downloader.FFprobePath = tc.probe
			seedAcquisitionTracks(t, d, "one")
			ctx := context.Background()
			if _, err := d.DB.EnqueueAcquisition(ctx, "spotify:track:one", "https://fixture.test/audio"); err != nil {
				t.Fatal(err)
			}
			work, err := d.DB.ClaimAcquisition(ctx)
			if err != nil {
				t.Fatal(err)
			}
			d.runAcquisition(ctx, work)
			work, err = d.DB.Acquisition(ctx, work.ID)
			if err != nil || work.State != db.AcquisitionComplete {
				t.Fatalf("acquisition = %+v: %v", work, err)
			}
			path := managed.TrackPath("spotify:track:one", tc.extension)
			if !d.managedFiles.Available("spotify:track:one", path) {
				t.Fatal("audio unavailable after tag failure")
			}
			published, err := os.ReadFile(filepath.Join(d.Cfg.Paths.MusicRoot, path))
			if err != nil {
				t.Fatal(err)
			}
			if tc.expected != "tagged" && !bytes.Equal(published, mediaBytes) {
				t.Fatal("original audio not published intact")
			}
			if tc.expected == "tagged" {
				if bytes.Equal(published, mediaBytes) {
					t.Fatal("tags were not written")
				}
				probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags=title,album,spotify_uri", "-of", "json", filepath.Join(d.Cfg.Paths.MusicRoot, path)).CombinedOutput()
				if err != nil || !bytes.Contains(probe, []byte("one")) || !bytes.Contains(probe, []byte("spotify:track:one")) {
					t.Fatalf("published native tags: %s %v", probe, err)
				}
			}
			var state, diagnostic string
			if err := d.DB.QueryRowContext(ctx, `SELECT tag_state, tag_error FROM managed_tracks WHERE track_uri=?`, "spotify:track:one").Scan(&state, &diagnostic); err != nil || state != tc.expected || (tc.expected == "tagged" && diagnostic != "") || (tc.expected != "tagged" && diagnostic == "") {
				t.Fatalf("tag state = %q %q: %v", state, diagnostic, err)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			d = acquisitionDaemon(t, home, nil, 1)
			defer d.Close()
			if err := d.DB.QueryRowContext(ctx, `SELECT tag_state FROM managed_tracks WHERE track_uri=?`, "spotify:track:one").Scan(&state); err != nil || state != tc.expected {
				t.Fatalf("restarted tag state = %q: %v", state, err)
			}
		})
	}
}

func TestCanceledOrConflictingPublicationPreservesGoodTrack(t *testing.T) {
	root := t.TempDir()
	files, err := managed.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	uri := "spotify:track:one"
	good := filepath.Join(t.TempDir(), "good.wav")
	if err := os.WriteFile(good, []byte("previous valid audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(good)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	path, err := files.Publish(uri, file, "wav")
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.wav")
	if err := os.WriteFile(other, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Open(other)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := files.PublishNew(ctx, uri, replacement, "wav"); err == nil {
		t.Fatal("canceled publish succeeded")
	}
	if _, adopted, err := files.PublishNew(context.Background(), uri, replacement, "wav"); err != nil || !adopted {
		t.Fatalf("adoption = %v %v", adopted, err)
	}
	bytesOnDisk, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || string(bytesOnDisk) != "previous valid audio" {
		t.Fatalf("prior audio replaced: %s %v", bytesOnDisk, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "tracks"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging left in managed root: %v %v", entries, err)
	}
}
