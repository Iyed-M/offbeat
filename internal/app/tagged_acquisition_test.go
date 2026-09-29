package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/artwork"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/managed"
)

func TestAcquisitionTagOutcomeIndependentOfPlayableAudio(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
				t.Fatalf("required tagging check needs %s: %v", tool, err)
			}
			t.Skipf("%s unavailable", tool)
		}
	}
	withMutagen := exec.Command("python3", "-c", "import mutagen; assert mutagen.version_string == '1.47.0'").Run() == nil
	for _, tc := range []struct {
		name, extension, probe, expected string
		artwork                          bool
	}{
		{"tagged", "flac", "ffprobe", "tagged", false},
		{"artwork failure keeps text tags", "flac", "ffprobe", "tagged", true},
		{"unsupported", "wav", "ffprobe", "unsupported", false},
		{"tag failure", "flac", "missing-offbeat-probe", "failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.expected == "tagged" && !withMutagen {
				if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
					t.Fatal("pinned Mutagen 1.47.0 unavailable: provision offline before required integration check")
				}
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
			if tc.artwork {
				if _, err := d.DB.ExecContext(ctx, `UPDATE spotify_tracks SET artwork_url='https://127.0.0.1/unsafe' WHERE uri='spotify:track:one'`); err != nil {
					t.Fatal(err)
				}
			}
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
			var artworkState, artworkError string
			if err := d.DB.QueryRowContext(ctx, `SELECT artwork_state, artwork_error FROM managed_tracks WHERE track_uri=?`, "spotify:track:one").Scan(&artworkState, &artworkError); err != nil {
				t.Fatal(err)
			}
			wantArtwork := "unavailable"
			if tc.artwork {
				wantArtwork = "failed"
			}
			if tc.extension == "wav" {
				wantArtwork = "unsupported"
			}
			if artworkState != wantArtwork || (tc.artwork && (artworkError == "" || bytes.Contains([]byte(artworkError), []byte("127.0.0.1")))) {
				t.Fatalf("artwork outcome: %q %q", artworkState, artworkError)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			d = acquisitionDaemon(t, home, nil, 1)
			defer d.Close()
			if err := d.DB.QueryRowContext(ctx, `SELECT tag_state FROM managed_tracks WHERE track_uri=?`, "spotify:track:one").Scan(&state); err != nil || state != tc.expected {
				t.Fatalf("restarted tag state = %q: %v", state, err)
			}
			if err := d.DB.QueryRowContext(ctx, `SELECT artwork_state FROM managed_tracks WHERE track_uri=?`, "spotify:track:one").Scan(&artworkState); err != nil || artworkState != wantArtwork {
				t.Fatalf("restarted artwork state = %q: %v", artworkState, err)
			}
		})
	}
}

func TestAcquisitionEmbedsFetchedArtworkBeforePublication(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
				t.Fatalf("required artwork integration gate needs %s: %v", tool, err)
			}
			t.Skipf("%s unavailable", tool)
		}
	}
	if err := exec.Command("python3", "-I", "-c", "import mutagen; assert mutagen.version_string == '1.47.0'").Run(); err != nil {
		if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
			t.Fatal("required artwork integration gate needs pinned Mutagen 1.47.0")
		}
		t.Skip("pinned Mutagen 1.47.0 unavailable")
	}
	home := t.TempDir()
	original := filepath.Join(home, "fixture.opus")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.2", "-c:a", "libopus", "-f", "opus", original).CombinedOutput(); err != nil {
		t.Fatalf("generate Opus fixture: %v %s", err, out)
	}
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, color.RGBA{R: 250, A: 255})
	var picture bytes.Buffer
	if err := png.Encode(&picture, im); err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(picture.Bytes())
	}))
	defer server.Close()
	d := acquisitionDaemon(t, home, retrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		file, err := os.Open(original)
		return &acquisition.Media{File: file, Extension: "opus"}, err
	}), 1)
	defer d.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	d.artworkFetcher = artwork.NewWithTestRoute(server.Listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: server.Certificate().DNSNames[0]})
	seedAcquisitionTracks(t, d, "one")
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `UPDATE spotify_tracks SET artwork_url = 'https://art.example/cover' WHERE uri = 'spotify:track:one'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.EnqueueAcquisition(ctx, "spotify:track:one", "https://fixture.test/audio"); err != nil {
		t.Fatal(err)
	}
	work, err := d.DB.ClaimAcquisition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d.runAcquisition(ctx, work)
	work, err = d.DB.Acquisition(ctx, work.ID)
	if err != nil || work.State != db.AcquisitionComplete || fetches.Load() != 1 {
		t.Fatalf("acquisition: %+v %v fetches=%d", work, err, fetches.Load())
	}
	path := managed.TrackPath("spotify:track:one", "opus")
	if !d.managedFiles.Available("spotify:track:one", path) {
		t.Fatal("hashed published Opus unavailable")
	}
	var storedPath, tagState, artState, artError string
	if err := d.DB.QueryRowContext(ctx, `SELECT relative_path, tag_state, artwork_state, artwork_error FROM managed_tracks WHERE track_uri='spotify:track:one'`).Scan(&storedPath, &tagState, &artState, &artError); err != nil || storedPath != path || tagState != "tagged" || artState != "embedded" || artError != "" {
		t.Fatalf("committed presentation: %q %q %q %q: %v", storedPath, tagState, artState, artError, err)
	}
	published := filepath.Join(d.Cfg.Paths.MusicRoot, path)
	// Independent native readback from the published file, including the exact
	// Opus METADATA_BLOCK_PICTURE payload rather than an in-memory writer value.
	inspect := `import base64,sys
from mutagen.flac import Picture
from mutagen.oggopus import OggOpus
a=OggOpus(sys.argv[1]); p=Picture(base64.b64decode(a['metadata_block_picture'][0]))
assert a['title']==['one'] and a['artist']==['Artist'] and a['album']==['Album']
assert a['spotify_uri']==['spotify:track:one'] and p.mime=='image/png' and p.type==3
assert p.data==open(sys.argv[2],'rb').read()`
	picturePath := filepath.Join(home, "cover.png")
	if err := os.WriteFile(picturePath, picture.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("python3", "-I", "-c", inspect, published, picturePath).CombinedOutput(); err != nil {
		t.Fatalf("published native tags: %v %s", err, out)
	}
	for _, file := range []string{original, published} {
		out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "default=noprint_wrappers=1", file).CombinedOutput()
		if err != nil || !bytes.Contains(out, []byte("codec_name=opus")) {
			t.Fatalf("published audio probe: %v %s", err, out)
		}
	}
	decode := func(file string) []byte {
		t.Helper()
		out, err := exec.Command("ffmpeg", "-v", "error", "-i", file, "-map", "0:a:0", "-f", "hash", "-hash", "SHA256", "-").CombinedOutput()
		if err != nil {
			t.Fatalf("decode %s: %v %s", file, err, out)
		}
		return out
	}
	if !bytes.Equal(decode(original), decode(published)) {
		t.Fatal("published audio changed")
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
	if _, err := files.PublishNew(ctx, uri, replacement, "wav"); err == nil {
		t.Fatal("canceled publish succeeded")
	}
	if _, err := files.PublishNew(context.Background(), uri, replacement, "wav"); err == nil {
		t.Fatal("existing track overwritten")
	}
	bytesOnDisk, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || string(bytesOnDisk) != "previous valid audio" {
		t.Fatalf("prior audio replaced: %s %v", bytesOnDisk, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "tracks"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging left in managed root: %v %v", entries, err)
	}
	orphan, extension, err := files.OpenOrphan(uri)
	if err != nil || extension != "wav" {
		t.Fatalf("open orphan: %s %v", extension, err)
	}
	defer orphan.Close()
	before, err := orphan.Stat()
	if err != nil {
		t.Fatal(err)
	}
	otherCopy := filepath.Join(root, "tracks", ".swapped")
	if err := os.WriteFile(otherCopy, bytesOnDisk, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(otherCopy, filepath.Join(root, path)); err != nil {
		t.Fatal(err)
	}
	if files.OrphanUnchanged(uri, extension, orphan, before) {
		t.Fatal("replaced orphan accepted after verification")
	}
}

func TestOrphanRecoveryDoesNotAdoptCorruptAudioOrRetrieveAgain(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	for _, crash := range []bool{false, true} {
		name := "retry"
		if crash {
			name = "restart"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			fixture := filepath.Join(home, "fixture.wav")
			if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.2", "-c:a", "pcm_s16le", fixture).CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			var calls atomic.Int32
			retriever := retrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
				calls.Add(1)
				file, err := os.Open(fixture)
				return &acquisition.Media{File: file, Extension: "wav"}, err
			})
			d := acquisitionDaemon(t, home, retriever, 1)
			seedAcquisitionTracks(t, d, "one")
			ctx := context.Background()
			if _, err := d.DB.Exec(`CREATE TRIGGER fail_mapping BEFORE INSERT ON managed_tracks BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := d.DB.EnqueueAcquisition(ctx, "spotify:track:one", "https://fixture.test/audio"); err != nil {
				t.Fatal(err)
			}
			work, err := d.DB.ClaimAcquisition(ctx)
			if err != nil {
				t.Fatal(err)
			}
			d.runAcquisition(ctx, work)
			work, err = d.DB.Acquisition(ctx, work.ID)
			if err != nil || work.State != db.AcquisitionFailed || calls.Load() != 1 {
				t.Fatalf("initial failure: %+v %v calls=%d", work, err, calls.Load())
			}
			path := filepath.Join(d.Cfg.Paths.MusicRoot, managed.TrackPath(work.TrackURI, "wav"))
			if err := os.WriteFile(path, []byte("nonempty but not audio"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := d.DB.Exec(`DROP TRIGGER fail_mapping`); err != nil {
				t.Fatal(err)
			}
			if crash {
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
				d = acquisitionDaemon(t, home, retriever, 1)
			}
			defer d.Close()
			if _, err := d.DB.RetryAcquisition(ctx, work.ID); err != nil {
				t.Fatal(err)
			}
			work, err = d.DB.ClaimAcquisition(ctx)
			if err != nil {
				t.Fatal(err)
			}
			d.runAcquisition(ctx, work)
			work, err = d.DB.Acquisition(ctx, work.ID)
			if err != nil || work.State == db.AcquisitionComplete {
				t.Fatalf("corrupt orphan adopted: %+v %v", work, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("corrupt orphan redownloaded: %d", calls.Load())
			}
			if err := os.WriteFile(path, mustReadFixture(t, fixture), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := d.DB.RetryAcquisition(ctx, work.ID); err != nil {
				t.Fatal(err)
			}
			work, err = d.DB.ClaimAcquisition(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if crash {
				// Simulate a crash with running work and a fully published,
				// unregistered file. Startup recovery must adopt without Retrieve.
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
				d = acquisitionDaemon(t, home, retriever, 1)
				if err := d.DB.RecoverAcquisitions(ctx); err != nil {
					t.Fatal(err)
				}
				work, err = d.DB.ClaimAcquisition(ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			d.runAcquisition(ctx, work)
			work, err = d.DB.Acquisition(ctx, work.ID)
			if err != nil || work.State != db.AcquisitionComplete || calls.Load() != 1 {
				t.Fatalf("valid orphan not adopted without retrieval: %+v %v calls=%d", work, err, calls.Load())
			}
		})
	}
}

func mustReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
