package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

type cliRetrieveFunc func(context.Context, string) (*acquisition.Media, error)

func (f cliRetrieveFunc) Retrieve(ctx context.Context, sourceURL string) (*acquisition.Media, error) {
	return f(ctx, sourceURL)
}

type cliResolveFunc func(context.Context, desired.Track) (string, error)

func (f cliResolveFunc) Resolve(ctx context.Context, track desired.Track) (string, error) {
	return f(ctx, track)
}

func TestDecodeAcquisitionResultRejectsInvalidDaemonReply(t *testing.T) {
	for _, result := range []any{
		nil,
		map[string]any{},
		map[string]any{"acquisition_id": 1, "track_uri": "spotify:track:one", "state": "unknown"},
		map[string]any{"acquisition_id": 1, "track_uri": "spotify:playlist:one", "state": "pending"},
		map[string]any{"acquisition_id": 1, "track_uri": "spotify:track:one", "state": "pending", "source_url": "https://example.test/media"},
	} {
		if _, err := decodeAcquisitionResult(result); err == nil {
			t.Errorf("decodeAcquisitionResult(%#v) succeeded", result)
		}
	}
}

func TestDecodeAcquisitionResult(t *testing.T) {
	result, err := decodeAcquisitionResult(map[string]any{
		"acquisition_id": 7,
		"track_uri":      "spotify:track:one",
		"state":          "failed",
		"error":          "retrieval failed",
	})
	if err != nil {
		t.Fatalf("decodeAcquisitionResult: %v", err)
	}
	if result.ID != 7 || result.State != "failed" || result.Error != "retrieval failed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCLIAcquisitionSubmitStatusAndRetry(t *testing.T) {
	home := t.TempDir()
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("controlled retrieval failure")
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "one")

	out, stderr, err := runCLI(t, home, "acquire", "spotify:track:one", "http://127.0.0.1:8080/owned.wav")
	if err != nil || stderr != "" || out == "" {
		t.Fatalf("acquire = stdout %q stderr %q err %v", out, stderr, err)
	}
	work := waitCLIAcquisitionState(t, d, 1, "failed")

	out, stderr, err = runCLI(t, home, "acquire", "status", fmt.Sprint(work.ID))
	if err != nil || stderr != "" || out != "Acquisition 1: failed (spotify:track:one).\nError: media retrieval failed; check source and configured yt-dlp/FFmpeg tools, then retry\n" {
		t.Fatalf("acquire status = stdout %q stderr %q err %v", out, stderr, err)
	}

	out, stderr, err = runCLI(t, home, "acquire", "retry", fmt.Sprint(work.ID))
	if err != nil || stderr != "" || out == "" {
		t.Fatalf("acquire retry = stdout %q stderr %q err %v", out, stderr, err)
	}
	_ = waitCLIAcquisitionState(t, d, work.ID, "failed")
}

func TestCLIAcquisitionCompletesWithInjectedRetriever(t *testing.T) {
	home := t.TempDir()
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		file, err := os.CreateTemp(t.TempDir(), "controlled-audio-*.wav")
		if err != nil {
			return nil, err
		}
		if _, err := file.Write([]byte("controlled completed audio")); err != nil {
			_ = file.Close()
			return nil, err
		}
		if _, err := file.Seek(0, 0); err != nil {
			_ = file.Close()
			return nil, err
		}
		return &acquisition.Media{File: file, Extension: "wav"}, nil
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "complete")

	out, stderr, err := runCLI(t, home, "acquire", "spotify:track:complete", "http://127.0.0.1:8080/owned.wav")
	if err != nil || stderr != "" || out == "" {
		t.Fatalf("acquire = stdout %q stderr %q err %v", out, stderr, err)
	}
	work := waitCLIAcquisitionState(t, d, 1, "complete")
	out, stderr, err = runCLI(t, home, "acquire", "status", fmt.Sprint(work.ID))
	if err != nil || stderr != "" || out != "Acquisition 1: complete (spotify:track:complete).\n" {
		t.Fatalf("acquire status = stdout %q stderr %q err %v", out, stderr, err)
	}
}

func TestCLIAcquireMissingAndAggregateStatus(t *testing.T) {
	home := t.TempDir()
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("controlled retrieval failure")
	}), cliResolveFunc(func(context.Context, desired.Track) (string, error) {
		return "", acquisition.ErrUnresolved
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "one")

	out, stderr, err := runCLI(t, home, "acquire", "missing")
	if err != nil || stderr != "" || out != "Missing acquisition: 1 queued, 0 active, 0 previously attempted, 0 available.\n" {
		t.Fatalf("acquire missing = stdout %q stderr %q err %v", out, stderr, err)
	}
	waitCLIAcquisitionState(t, d, 1, "unresolved")
	out, stderr, err = runCLI(t, home, "acquire", "status")
	if err != nil || stderr != "" || out != "Acquisitions: 0 pending, 0 running, 1 unresolved, 0 failed, 0 complete.\nAcquisition 1: unresolved (spotify:track:one).\nError: no unique eligible YouTube result\n" {
		t.Fatalf("aggregate status = stdout %q stderr %q err %v", out, stderr, err)
	}
	out, stderr, err = runCLI(t, home, "acquire", "retry", "unresolved")
	if err != nil || stderr != "" || out != "Unresolved acquisition retry: 1 queued, 0 active, 0 available, 0 removed.\n" {
		t.Fatalf("acquire retry unresolved = stdout %q stderr %q err %v", out, stderr, err)
	}
	waitCLIAcquisitionState(t, d, 1, "unresolved")
}

func TestCLIAcquireDoesNotRetryUnknownOutcome(t *testing.T) {
	home := t.TempDir()
	socketDir := filepath.Join(home, "control")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".config", "offbeat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[paths]\nsocket_dir = %q\n", socketDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", app.SocketPath(socketDir))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan int, 1)
	go func() {
		count := 0
		conn, err := listener.Accept()
		if err == nil {
			count++
			_, _ = ipc.ReadFrame(bufio.NewReader(conn))
			_ = conn.Close() // Deliberately drop the response after receiving the mutation.
		}
		_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(200 * time.Millisecond))
		if conn, err := listener.Accept(); err == nil {
			count++
			_ = conn.Close()
		}
		done <- count
	}()

	out, stderr, err := runCLI(t, home, "acquire", "spotify:track:one", "http://127.0.0.1:8080/owned.wav")
	if err == nil || out != "" || !strings.Contains(stderr, "unknown outcome") {
		t.Fatalf("acquire = stdout %q stderr %q err %v", out, stderr, err)
	}
	if count := <-done; count != 1 {
		t.Fatalf("control requests = %d, want one", count)
	}
}

func startCLIAcquisitionDaemon(t *testing.T, home string, retriever acquisition.Retriever, resolver ...acquisition.Resolver) (*app.Daemon, func()) {
	t.Helper()
	port := reserveCLIAdapterPort(t)
	configPath := filepath.Join(home, ".config", "offbeat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[spotify_adapter]\nport = %d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	options := app.Options{HomeDir: home, AdapterCredential: "test-credential", Retriever: retriever}
	if len(resolver) > 0 {
		options.Resolver = resolver[0]
	}
	d, err := app.NewDaemon(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, app.RunOptions{SignalCh: make(chan os.Signal)}) }()
	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		cancel()
		t.Fatal(err)
	}
	return d, func() {
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
}

func seedCLIAcquisitionTrack(t *testing.T, d *app.Daemon, id string) {
	t.Helper()
	track := desired.Track{URI: "spotify:track:" + id, Name: id, Artists: []desired.NamedURI{{URI: "spotify:artist:one", Name: "Artist"}}, Album: desired.NamedURI{URI: "spotify:album:one", Name: "Album"}, DurationMS: 1000}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
}

func waitCLIAcquisitionState(t *testing.T, d *app.Daemon, id int64, state string) db.AcquisitionWork {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		work, err := d.DB.Acquisition(context.Background(), id)
		if err == nil && work.State == state {
			return work
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("acquisition %d did not become %s", id, state)
	return db.AcquisitionWork{}
}
