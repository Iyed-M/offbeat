package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

// This crosses the real CLI, HTTP review, Unix Control, worker, and SQLite
// boundaries. Only the external search and media bytes are simulated.
func TestReviewLifecycleAcrossCLIHTTPAndRestart(t *testing.T) {
	home := t.TempDir()
	tools := t.TempDir()
	writeCLITool(t, tools, "yt-dlp", `printf '%s\n' '{"entries":[{"id":"bbbbbbbbbbb","title":"Artist - Song","channel":"Artist","duration":1},{"id":"aaaaaaaaaaa","title":"Artist - Song","channel":"Artist","duration":1}]}'`)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	configPath := filepath.Join(home, ".config", "offbeat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[spotify_adapter]\nport=%d\n[acquisition]\nconcurrency=4\n", reserveCLIAdapterPort(t))), 0600); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	releaseChoice := make(chan struct{})
	retrieve := cliRetrieveFunc(func(ctx context.Context, source string) (*acquisition.Media, error) {
		attempts.Add(1)
		if !strings.HasSuffix(source, "v=bbbbbbbbbbb") && !strings.HasSuffix(source, "v=aaaaaaaaaaa") {
			t.Errorf("unexpected source %s", source)
		}
		if strings.HasSuffix(source, "v=bbbbbbbbbbb") {
			select {
			case <-releaseChoice:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, fmt.Errorf("fixture video unavailable")
	})
	start := func() (*app.Daemon, func()) {
		d, err := app.NewDaemon(context.Background(), app.Options{HomeDir: home, AdapterCredential: "test-credential", Retriever: retrieve})
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
			if err := <-done; err != nil {
				t.Errorf("daemon: %v", err)
			}
		}
	}
	d, stop := start()
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	entries := make([]desired.CandidateEntry, 105)
	for i := range entries {
		track := desired.Track{URI: fmt.Sprintf("spotify:track:%03d", i), Name: "Song", Artists: []desired.NamedURI{{URI: "spotify:artist:test", Name: "Artist"}}, DurationMS: 1000}
		entries[i] = desired.CandidateEntry{Position: i, Kind: desired.EntrySupported, Track: &track}
	}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: entries}); err != nil {
		t.Fatal(err)
	}
	cli := func(args ...string) string {
		t.Helper()
		out, stderr, err := runCLI(t, home, args[0], args[1:]...)
		if err != nil || stderr != "" {
			t.Fatalf("CLI %v: %s %s %v", args, out, stderr, err)
		}
		return out
	}
	if out := cli("acquire", "missing"); !strings.Contains(out, "105 queued") {
		t.Fatal(out)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		counts, err := d.DB.AcquisitionCounts(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if counts.Unresolved == 105 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("batch did not resolve: %+v", counts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if attempts.Load() != 0 {
		t.Fatal("conservative tie downloaded media")
	}
	control := func(ctx context.Context, req ipc.Request, timeout time.Duration) (ipc.Response, error) {
		return requestControlMessageContext(ctx, app.SocketPath(d.Cfg.Paths.SocketDir), req, timeout)
	}
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	host := "127.0.0.1:9876"
	h := newReviewHandler(host, token, control)
	base := "http://" + host + "/s/" + token + "/"
	get := func(route string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", base+route, nil))
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", route, w.Code, w.Body.String())
		}
		return w
	}
	seen := map[string]bool{}
	for cursor, pages := "", 0; ; pages++ {
		if pages > 4 {
			t.Fatal("review cursor did not terminate")
		}
		page := get(cursor)
		for _, match := range regexp.MustCompile(`spotify%3Atrack%3A\d{3}`).FindAllString(page.Body.String(), -1) {
			seen[match] = true
		}
		if !strings.Contains(page.Body.String(), "Next page") {
			break
		}
		// The actual HTTP link is used, so Control cursor and HTML encoding are exercised.
		link := regexp.MustCompile(`href="([^"]+)"[^>]*>Next page`).FindStringSubmatch(page.Body.String())
		if len(link) != 2 {
			t.Fatalf("missing next link: %s", page.Body.String())
		}
		cursor = strings.TrimPrefix(html.UnescapeString(link[1]), "/s/"+token+"/")
	}
	if len(seen) != 105 {
		t.Fatalf("review pages contain %d distinct tracks", len(seen))
	}
	uri := "spotify:track:000"
	inspection := get("inspect?uri=" + url.QueryEscape(uri)).Body.String()
	if !strings.Contains(inspection, "score") || !strings.Contains(inspection, "Open on YouTube") {
		t.Fatal("candidate details missing")
	}
	choiceMarkup := regexp.MustCompile(`data-choice="([^"]+)"`).FindStringSubmatch(inspection)
	if len(choiceMarkup) != 2 {
		t.Fatal("no selectable candidate")
	}
	var choice ipc.AcquisitionChoice
	if err := json.Unmarshal([]byte(html.UnescapeString(choiceMarkup[1])), &choice); err != nil {
		t.Fatal(err)
	}
	if choice.TrackURI != uri {
		t.Fatalf("wrong choice: %+v", choice)
	}
	before, err := d.DB.AcquisitionCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	get("track?uri=" + url.QueryEscape(uri))
	get("") // skip
	after, err := d.DB.AcquisitionCounts(context.Background())
	if err != nil || after != before || attempts.Load() != 0 {
		t.Fatalf("GET mutated work: %+v -> %+v, retrieves %d, %v", before, after, attempts.Load(), err)
	}
	// CLI selection uses the very same daemon operation as HTTP selection.
	cli("acquire", "select", "spotify:track:001", "aaaaaaaaaaa")
	waitCLIAcquisitionState(t, d, 2, db.AcquisitionFailed)
	post := func(c ipc.AcquisitionChoice) int {
		t.Helper()
		body, _ := json.Marshal(c)
		r := httptest.NewRequest("POST", base+"select", strings.NewReader(string(body)))
		r.Header.Set("Origin", "http://"+host)
		r.Header.Set("X-Offbeat-Token", token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("HTTP choice: %d %s", w.Code, w.Body.String())
		}
		return w.Code
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); post(choice) }()
	}
	wg.Wait()
	close(releaseChoice)
	waitCLIAcquisitionState(t, d, 1, db.AcquisitionFailed)
	status := get("status?id=1&uri=" + url.QueryEscape(uri))
	var selectedWork ipc.AcquisitionResult
	if err := json.Unmarshal(status.Body.Bytes(), &selectedWork); err != nil || selectedWork.ID != 1 || selectedWork.TrackURI != uri || selectedWork.State != "failed" || !strings.Contains(selectedWork.Error, "fixture video unavailable") {
		t.Fatalf("selected work outcome: %+v (%v)", selectedWork, err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected exactly two retrievals, got %d", attempts.Load())
	}
	if out := cli("acquire", "mapping", "show", uri); !strings.Contains(out, choice.VideoID) || !strings.Contains(out, "fixture video unavailable") {
		t.Fatal(out)
	}
	stop()
	stop = nil
	d, stop = start()
	if out := cli("acquire", "mapping", "show", uri); !strings.Contains(out, choice.VideoID) {
		t.Fatal(out)
	}
	if out := cli("acquire", "missing"); !strings.Contains(out, "0 queued") {
		t.Fatal(out)
	}
	if attempts.Load() != 2 {
		t.Fatal("restart implicitly retried failed retrieval")
	}
	cli("acquire", "retry", "1")
	waitCLIAcquisitionState(t, d, 1, db.AcquisitionFailed)
	if attempts.Load() != 3 {
		t.Fatalf("explicit retry attempts: %d", attempts.Load())
	}
	work, err := d.DB.AcquisitionsAfter(context.Background(), 0, 200)
	if err != nil || len(work) != 105 {
		t.Fatalf("work after restart: %d %v", len(work), err)
	}
	if work[0].SourceURL != "https://www.youtube.com/watch?v="+choice.VideoID {
		t.Fatalf("source after restart: %+v", work[0])
	}
}
