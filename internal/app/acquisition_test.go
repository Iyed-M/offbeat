package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Iyed-M/offbeat/internal/managed"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

type retrieveFunc func(context.Context, string) (*acquisition.Media, error)

func (f retrieveFunc) Retrieve(ctx context.Context, url string) (*acquisition.Media, error) {
	return f(ctx, url)
}

type resolveFunc func(context.Context, desired.Track) (string, error)

func (f resolveFunc) Resolve(ctx context.Context, track desired.Track) (string, error) {
	return f(ctx, track)
}

func acquisitionDaemon(t *testing.T, home string, retrieve acquisition.Retriever, concurrency int) *Daemon {
	t.Helper()
	cfgPath := filepath.Join(home, "config.toml")
	config := fmt.Sprintf("[spotify_adapter]\nport=%d\n[acquisition]\nconcurrency=%d\n", availableAdapterTestPort(t), concurrency)
	if err := os.WriteFile(cfgPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(context.Background(), Options{HomeDir: home, ConfigPath: cfgPath, AdapterCredential: testAdapterCredential, Retriever: retrieve})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func runAcquisitionDaemon(t *testing.T, d *Daemon) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, RunOptions{SignalCh: make(chan os.Signal)}) }()
	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("acquisition shutdown did not drain")
		}
	}
	t.Cleanup(stop)
	waitForSocket(t, SocketPath(d.socketDir))
	return stop
}

func seedAcquisitionTracks(t *testing.T, d *Daemon, ids ...string) {
	t.Helper()
	candidate := desired.Candidate{}
	for i, id := range ids {
		track := desired.Track{URI: "spotify:track:" + id, Name: id, Artists: []desired.NamedURI{{URI: "artist", Name: "Artist"}}, Album: desired.NamedURI{URI: "album", Name: "Album"}, DurationMS: 1000}
		candidate.LikedSongs = append(candidate.LikedSongs, desired.CandidateEntry{Position: i, Kind: desired.EntrySupported, Track: &track})
	}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
}

func acquireControl(t *testing.T, d *Daemon, req ipc.Request) ipc.AcquisitionResult {
	t.Helper()
	req.Version = ipc.ProtocolVersion
	raw, err := ipc.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	response := sendRaw(t, SocketPath(d.socketDir), append(raw, '\n'))
	if response.Error != nil {
		t.Fatalf("acquire control: %v", response.Error)
	}
	data, _ := json.Marshal(response.Result)
	var result ipc.AcquisitionResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func waitAcquisition(t *testing.T, d *Daemon, id int64, state string) ipc.AcquisitionResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := acquireControl(t, d, ipc.Request{Command: "acquire.status", AcquisitionStatus: &ipc.AcquisitionIDRequest{ID: id}})
		if result.State == state {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("acquisition %d did not reach %s", id, state)
	return ipc.AcquisitionResult{}
}

func acquireBatchControl(t *testing.T, d *Daemon) ipc.AcquisitionBatchResult {
	t.Helper()
	raw, err := ipc.Encode(ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.missing"})
	if err != nil {
		t.Fatal(err)
	}
	response := sendRaw(t, SocketPath(d.socketDir), append(raw, '\n'))
	if response.Error != nil {
		t.Fatalf("acquire missing: %v", response.Error)
	}
	data, _ := json.Marshal(response.Result)
	var result ipc.AcquisitionBatchResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func fakeMedia(t *testing.T) (*acquisition.Media, error) {
	// Retrieval tests validate actual audio. This injected boundary supplies a
	// completed regular file to exercise daemon ownership/publication behavior.
	file, err := os.CreateTemp(t.TempDir(), "audio-")
	if err != nil {
		return nil, err
	}
	if _, err = file.Write([]byte("controlled completed audio")); err != nil {
		file.Close()
		return nil, err
	}
	if _, err = file.Seek(0, 0); err != nil {
		file.Close()
		return nil, err
	}
	return &acquisition.Media{File: file, Extension: "wav"}, nil
}

func TestAcquisitionSuccessFailureIsolationAndRetry(t *testing.T) {
	var shouldFail atomic.Bool
	shouldFail.Store(true)
	retriever := retrieveFunc(func(ctx context.Context, url string) (*acquisition.Media, error) {
		if url == "https://fixture.test/fail" && shouldFail.Load() {
			return nil, errors.New("secret raw process output")
		}
		return fakeMedia(t)
	})
	d := acquisitionDaemon(t, t.TempDir(), retriever, 2)
	seedAcquisitionTracks(t, d, "one", "two")
	runAcquisitionDaemon(t, d)
	failed := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:one", SourceURL: "https://fixture.test/fail"}})
	good := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:two", SourceURL: "https://fixture.test/good"}})
	failure := waitAcquisition(t, d, failed.ID, "failed")
	if failure.Error == "" || failure.Error == "secret raw process output" {
		t.Fatalf("failure not sanitized: %#v", failure)
	}
	waitAcquisition(t, d, good.ID, "complete")
	result, err := d.handleMissing(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	missing := result.(ipc.MissingResult)
	if len(missing.Tracks) != 1 || missing.Tracks[0].URI != "spotify:track:one" {
		t.Fatalf("missing after acquire: %#v", missing)
	}
	shouldFail.Store(false)
	acquireControl(t, d, ipc.Request{Command: "acquire.retry", AcquisitionRetry: &ipc.AcquisitionIDRequest{ID: failed.ID}})
	waitAcquisition(t, d, failed.ID, "complete")
	result, err = d.handleMissing(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	missing = result.(ipc.MissingResult)
	if len(missing.Tracks) != 0 || missing.AvailableCount != 2 || missing.StateRevision != 1 {
		t.Fatalf("final missing: %#v", missing)
	}
}

func TestYouTubeMissingSetResolutionIsolationAndDirectOverride(t *testing.T) {
	retriever := retrieveFunc(func(ctx context.Context, url string) (*acquisition.Media, error) {
		if strings.Contains(url, "three") {
			return nil, errors.New("controlled failure")
		}
		return fakeMedia(t)
	})
	home, err := os.MkdirTemp("", "offbeat-m6a-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d := acquisitionDaemon(t, home, retriever, 2)
	d.resolver = resolveFunc(func(_ context.Context, track desired.Track) (string, error) {
		switch track.Name {
		case "two":
			return "", acquisition.ErrUnresolved
		case "three":
			return "https://www.youtube.com/watch?v=three000000", nil
		default:
			return "https://www.youtube.com/watch?v=one00000000", nil
		}
	})
	seedAcquisitionTracks(t, d, "one", "two", "three")
	runAcquisitionDaemon(t, d)
	batch := acquireBatchControl(t, d)
	if batch.Queued != 3 || batch.Considered != 3 {
		t.Fatalf("batch = %#v", batch)
	}
	ids := make(map[string]int64)
	for _, name := range []string{"one", "two", "three"} {
		var id int64
		if err := d.DB.QueryRowContext(context.Background(), `SELECT id FROM acquisition_work WHERE track_uri = ?`, "spotify:track:"+name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	waitAcquisition(t, d, ids["one"], "complete")
	waitAcquisition(t, d, ids["two"], "unresolved")
	waitAcquisition(t, d, ids["three"], "failed")
	for id, want := range map[int64]string{ids["one"]: "https://www.youtube.com/watch?v=one00000000", ids["three"]: "https://www.youtube.com/watch?v=three000000"} {
		work, err := d.DB.Acquisition(context.Background(), id)
		if err != nil || work.SourceURL != want {
			t.Fatalf("work %d = %#v, %v", id, work, err)
		}
	}
	again := acquireBatchControl(t, d)
	if again.Queued != 0 || again.SkippedAttempted != 2 || again.Available != 1 {
		t.Fatalf("repeat = %#v", again)
	}
	override := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:two", SourceURL: "https://fixture.test/override"}})
	waitAcquisition(t, d, override.ID, "complete")
}

func TestSelectedYouTubeURLSurvivesRestart(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-m6a-restart-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	started := make(chan struct{})
	d := acquisitionDaemon(t, home, retrieveFunc(func(ctx context.Context, _ string) (*acquisition.Media, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}), 1)
	d.resolver = resolveFunc(func(context.Context, desired.Track) (string, error) {
		return "https://www.youtube.com/watch?v=restart0000", nil
	})
	seedAcquisitionTracks(t, d, "one")
	stop := runAcquisitionDaemon(t, d)
	if batch := acquireBatchControl(t, d); batch.Queued != 1 {
		t.Fatalf("batch = %#v", batch)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("retrieval did not start")
	}
	work, err := d.DB.Acquisition(context.Background(), 1)
	if err != nil || work.SourceURL != "https://www.youtube.com/watch?v=restart0000" {
		t.Fatalf("selected work = %#v, %v", work, err)
	}
	stop()

	d = acquisitionDaemon(t, home, retrieveFunc(func(context.Context, string) (*acquisition.Media, error) { return fakeMedia(t) }), 1)
	d.resolver = resolveFunc(func(context.Context, desired.Track) (string, error) {
		return "", errors.New("resolver must not run again")
	})
	runAcquisitionDaemon(t, d)
	waitAcquisition(t, d, work.ID, "complete")
}

func TestAcquisitionBoundedConcurrencyAndRestart(t *testing.T) {
	started := make(chan struct{}, 4)
	var active, maxActive atomic.Int32
	blocked := retrieveFunc(func(ctx context.Context, _ string) (*acquisition.Media, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
		}
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	home := t.TempDir()
	d := acquisitionDaemon(t, home, blocked, 2)
	seedAcquisitionTracks(t, d, "one", "two", "three")
	stop := runAcquisitionDaemon(t, d)
	var ids []int64
	for _, name := range []string{"one", "two", "three"} {
		work := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:" + name, SourceURL: "https://fixture.test/" + name}})
		ids = append(ids, work.ID)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("concurrency exceeded")
	case <-time.After(150 * time.Millisecond):
	}
	result, err := d.handleMissing(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.(ipc.MissingResult).Tracks) != 3 {
		t.Fatal("incomplete media became available")
	}
	stop()
	if active.Load() != 0 || maxActive.Load() != 2 {
		t.Fatal("workers not drained or concurrency incorrect")
	}
	d = acquisitionDaemon(t, home, retrieveFunc(func(context.Context, string) (*acquisition.Media, error) { return fakeMedia(t) }), 2)
	runAcquisitionDaemon(t, d)
	for _, id := range ids {
		waitAcquisition(t, d, id, "complete")
	}
}

func TestAcquisitionRemovedTrackDoesNotPublish(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	retriever := retrieveFunc(func(ctx context.Context, _ string) (*acquisition.Media, error) {
		close(started)
		select {
		case <-release:
			return fakeMedia(t)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	d := acquisitionDaemon(t, t.TempDir(), retriever, 1)
	seedAcquisitionTracks(t, d, "one")
	runAcquisitionDaemon(t, d)
	work := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:one", SourceURL: "https://fixture.test/audio"}})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("retrieval did not start")
	}
	seedAcquisitionTracks(t, d)
	close(release)
	waitAcquisition(t, d, work.ID, "failed")
	entries, err := os.ReadDir(filepath.Join(d.Cfg.Paths.MusicRoot, "tracks"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("removed track published: %v %v", entries, err)
	}
}

func TestAcquisitionRealToolsMakesMissingTrackAvailable(t *testing.T) {
	if os.Getenv("OFFBEAT_TEST_REAL_TOOLS") != "1" {
		t.Skip("set OFFBEAT_TEST_REAL_TOOLS=1 to exercise installed media tools")
	}
	for _, name := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("required tool %s: %v", name, err)
		}
	}
	fixtureRoot := t.TempDir()
	files, err := managed.Open(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	path, err := files.PublishSynthetic("controlled-source")
	if err != nil {
		t.Fatal(err)
	}
	files.Close()
	data, err := os.ReadFile(filepath.Join(fixtureRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(data)
	}))
	defer server.Close()
	d := acquisitionDaemon(t, t.TempDir(), nil, 1)
	seedAcquisitionTracks(t, d, "real")
	runAcquisitionDaemon(t, d)
	work := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:real", SourceURL: server.URL + "/fixture.wav"}})
	waitAcquisition(t, d, work.ID, "complete")
	result, err := d.handleMissing(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	missing := result.(ipc.MissingResult)
	if len(missing.Tracks) != 0 || missing.AvailableCount != 1 {
		t.Fatalf("real acquisition not available: %#v", missing)
	}
}

func TestAcquisitionCommitFailureStaysMissingAndCanRetry(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-m6-commit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	d := acquisitionDaemon(t, home, retrieveFunc(func(context.Context, string) (*acquisition.Media, error) { return fakeMedia(t) }), 1)
	seedAcquisitionTracks(t, d, "one")
	if _, err := d.DB.Exec(`CREATE TRIGGER fail_acquired_mapping BEFORE INSERT ON managed_tracks BEGIN SELECT RAISE(ABORT, 'injected mapping failure'); END`); err != nil {
		t.Fatal(err)
	}
	runAcquisitionDaemon(t, d)
	work := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:one", SourceURL: "https://fixture.test/audio"}})
	waitAcquisition(t, d, work.ID, "failed")
	result, err := d.handleMissing(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.(ipc.MissingResult).Tracks) != 1 {
		t.Fatal("failed mapping commit made track available")
	}
	if _, err := d.DB.Exec(`DROP TRIGGER fail_acquired_mapping`); err != nil {
		t.Fatal(err)
	}
	acquireControl(t, d, ipc.Request{Command: "acquire.retry", AcquisitionRetry: &ipc.AcquisitionIDRequest{ID: work.ID}})
	waitAcquisition(t, d, work.ID, "complete")
	for _, req := range []ipc.Request{
		{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:one", SourceURL: "https://fixture.test/audio"}},
		{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:absent", SourceURL: "https://fixture.test/audio"}},
		{Command: "acquire.retry", AcquisitionRetry: &ipc.AcquisitionIDRequest{ID: work.ID}},
		{Command: "acquire.status", AcquisitionStatus: &ipc.AcquisitionIDRequest{ID: work.ID + 100}},
	} {
		req.Version = ipc.ProtocolVersion
		raw, _ := ipc.Encode(req)
		response := sendRaw(t, SocketPath(d.socketDir), append(raw, '\n'))
		if response.Error == nil || response.Error.Code != ipc.CodeFailedPrecondition {
			t.Fatalf("precondition response: %#v", response)
		}
	}
}
