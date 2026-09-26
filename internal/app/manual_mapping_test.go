package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func TestManualMappingPrecedenceFailuresAndExplicitRecovery(t *testing.T) {
	var resolverCalls atomic.Int32
	var retrieved atomic.Value
	home, err := os.MkdirTemp("", "ob-map-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d := acquisitionDaemon(t, home, retrieveFunc(func(_ context.Context, url string) (*acquisition.Media, error) {
		retrieved.Store(url)
		return nil, errors.New("chosen video unavailable")
	}), 1)
	d.resolver = resolveFunc(func(context.Context, desired.Track) (string, error) {
		resolverCalls.Add(1)
		return "https://www.youtube.com/watch?v=resolver000", nil
	})
	seedAcquisitionTracks(t, d, "one", "two")
	runAcquisitionDaemon(t, d)
	uri := "spotify:track:one"
	set := func(id string) ipc.ManualMappingResult {
		t.Helper()
		result, err := d.handleControlRequest(context.Background(), ipc.Request{Command: "acquire.mapping.set", ManualMapping: &ipc.ManualMappingRequest{TrackURI: uri, VideoID: id}})
		if err != nil {
			t.Fatal(err)
		}
		return result.(ipc.ManualMappingResult)
	}
	if m := set("abcdefghijk"); m.VideoID != "abcdefghijk" || m.Provenance != "manual" {
		t.Fatalf("set: %#v", m)
	}
	if batch := acquireBatchControl(t, d); batch.Queued != 2 {
		t.Fatalf("batch: %#v", batch)
	}
	failed := waitAcquisition(t, d, 1, "failed")
	waitAcquisition(t, d, 2, "failed")
	if failed.Error != "media retrieval failed: chosen video unavailable" || resolverCalls.Load() != 1 {
		t.Fatalf("work: %#v resolver calls %d", failed, resolverCalls.Load())
	}
	work, err := d.DB.Acquisition(context.Background(), 1)
	if err != nil || work.SourceURL != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("selected: %#v %v", work, err)
	}
	result, err := d.handleControlRequest(context.Background(), ipc.Request{Command: "acquire.mapping.show", ManualMapping: &ipc.ManualMappingRequest{TrackURI: uri}})
	if err != nil || !strings.Contains(result.(ipc.ManualMappingResult).WorkError, "unavailable") {
		t.Fatalf("visible error: %#v %v", result, err)
	}
	// Replacing a failed choice alone does not queue or erase its failure.
	set("ZYXWvu_987-")
	work, _ = d.DB.Acquisition(context.Background(), 1)
	if work.State != db.AcquisitionFailed || work.SourceURL != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("replacement hijacked prior work: %#v", work)
	}
	acquireControl(t, d, ipc.Request{Command: "acquire.retry", AcquisitionRetry: &ipc.AcquisitionIDRequest{ID: 1}})
	waitAcquisition(t, d, 1, "failed")
	work, _ = d.DB.Acquisition(context.Background(), 1)
	if work.SourceURL != "https://www.youtube.com/watch?v=ZYXWvu_987-" || resolverCalls.Load() != 1 {
		t.Fatalf("replacement retry: %#v calls %d", work, resolverCalls.Load())
	}
	if got := retrieved.Load(); got != work.SourceURL {
		t.Fatalf("retrieved %v want %s", got, work.SourceURL)
	}
	if _, err := d.handleControlRequest(context.Background(), ipc.Request{Command: "acquire.mapping.remove", ManualMapping: &ipc.ManualMappingRequest{TrackURI: uri}}); err != nil {
		t.Fatal(err)
	}
	acquireControl(t, d, ipc.Request{Command: "acquire.retry", AcquisitionRetry: &ipc.AcquisitionIDRequest{ID: 1}})
	waitAcquisition(t, d, 1, "failed")
	work, _ = d.DB.Acquisition(context.Background(), 1)
	if work.SourceURL != "https://www.youtube.com/watch?v=resolver000" || resolverCalls.Load() != 2 {
		t.Fatalf("removed mapping retry: %#v calls %d", work, resolverCalls.Load())
	}
}

func TestManualMappingDoesNotHijackRunningSourceOrStartRemovedOrAvailableWork(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	home, err := os.MkdirTemp("", "ob-active-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d := acquisitionDaemon(t, home, retrieveFunc(func(ctx context.Context, url string) (*acquisition.Media, error) {
		started <- url
		select {
		case <-release:
			return fakeMedia(t)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}), 1)
	seedAcquisitionTracks(t, d, "active", "available", "removed")
	path, err := d.managedFiles.PublishSynthetic("spotify:track:available")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DB.RegisterManagedTrack(context.Background(), "spotify:track:available", path); err != nil {
		t.Fatal(err)
	}
	runAcquisitionDaemon(t, d)
	work := acquireControl(t, d, ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:active", SourceURL: "https://example.test/active"}})
	select {
	case url := <-started:
		if url != "https://example.test/active" {
			t.Fatal(url)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retrieval not started")
	}
	for _, uri := range []string{"active", "available", "removed"} {
		if _, err := d.handleControlRequest(context.Background(), ipc.Request{Command: "acquire.mapping.set", ManualMapping: &ipc.ManualMappingRequest{TrackURI: "spotify:track:" + uri, VideoID: "abcdefghijk"}}); err != nil {
			t.Fatal(err)
		}
	}
	seedAcquisitionTracks(t, d, "active", "available")
	if batch := acquireBatchControl(t, d); batch.Considered != 1 || batch.SkippedActive != 1 || batch.Available != 1 {
		t.Fatalf("batch: %#v", batch)
	}
	close(release)
	waitAcquisition(t, d, work.ID, "complete")
	stored, err := d.DB.Acquisition(context.Background(), work.ID)
	if err != nil || stored.SourceURL != "https://example.test/active" {
		t.Fatalf("hijacked: %#v %v", stored, err)
	}
	for _, uri := range []string{"available", "removed"} {
		if _, err := d.handleAcquisition(context.Background(), ipc.Request{Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: "spotify:track:" + uri, SourceURL: "https://example.test/source"}}); err == nil {
			t.Fatalf("%s was acquired", uri)
		}
	}
}

func TestManualMappingControlValidationAndPaging(t *testing.T) {
	home, err := os.MkdirTemp("", "ob-pages-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d := acquisitionDaemon(t, home, retrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		t.Fatal("mapping must not start retrieval")
		return nil, nil
	}), 1)
	runAcquisitionDaemon(t, d)
	for _, raw := range []string{
		`{"version":1,"command":"acquire.mapping.set","manual_mapping":{"track_uri":"spotify:track:one","video_id":"short"}}`,
		`{"version":1,"command":"acquire.mapping.set","manual_mapping":{"track_uri":"spotify:track:one","video_id":"abcdefghij/"}}`,
		`{"version":1,"command":"acquire.mapping.set","manual_mapping":{"track_uri":"spotify:track:one:extra","video_id":"abcdefghijk"}}`,
		`{"version":1,"command":"acquire.mapping.set","manual_mapping":{"track_uri":"spotify:track:one","video_id":"abcdefghijk","extra":true}}`,
		`{"version":1,"command":"acquire.mapping.show"}`,
		`{"version":1,"command":"acquire.mapping.list","manual_mapping_list":{"after_uri":"bad"}}`,
	} {
		resp := sendRaw(t, SocketPath(d.socketDir), []byte(raw+"\n"))
		if resp.Error == nil || resp.Error.Code != ipc.CodeInvalidRequest {
			t.Fatalf("accepted %s: %#v", raw, resp)
		}
	}
	for i := range 130 {
		uri := fmt.Sprintf("spotify:track:page%03d", i)
		request, _ := ipc.Encode(ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.mapping.set", ManualMapping: &ipc.ManualMappingRequest{TrackURI: uri, VideoID: "abcdefghijk"}})
		if resp := sendRaw(t, SocketPath(d.socketDir), append(request, '\n')); resp.Error != nil {
			t.Fatalf("set %s: %v", uri, resp.Error)
		}
	}
	var after string
	total := 0
	for {
		req := ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.mapping.list"}
		if after != "" {
			req.ManualMappingList = &ipc.ManualMappingListRequest{AfterURI: after}
		}
		data, _ := ipc.Encode(req)
		resp := sendRaw(t, SocketPath(d.socketDir), append(data, '\n'))
		if resp.Error != nil {
			t.Fatal(resp.Error)
		}
		payload, _ := json.Marshal(resp.Result)
		var page ipc.ManualMappingPage
		if err := json.Unmarshal(payload, &page); err != nil || len(page.Mappings) > 128 {
			t.Fatalf("page: %v %v", page, err)
		}
		total += len(page.Mappings)
		if page.NextAfterURI == "" {
			break
		}
		after = page.NextAfterURI
	}
	if total != 130 {
		t.Fatalf("listed %d", total)
	}
}

func TestManualMappingSurvivesRestartAndDoesNotRedirectSelectedRunningYouTubeWork(t *testing.T) {
	home, err := os.MkdirTemp("", "ob-restart-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	started := make(chan string, 1)
	d := acquisitionDaemon(t, home, retrieveFunc(func(ctx context.Context, url string) (*acquisition.Media, error) {
		started <- url
		<-ctx.Done()
		return nil, ctx.Err()
	}), 1)
	d.resolver = resolveFunc(func(context.Context, desired.Track) (string, error) {
		t.Error("resolver called despite mapping")
		return "", acquisition.ErrUnresolved
	})
	seedAcquisitionTracks(t, d, "one")
	if _, err := d.handleManualMapping(context.Background(), ipc.Request{Command: "acquire.mapping.set", ManualMapping: &ipc.ManualMappingRequest{TrackURI: "spotify:track:one", VideoID: "abcdefghijk"}}); err != nil {
		t.Fatal(err)
	}
	stop := runAcquisitionDaemon(t, d)
	if batch := acquireBatchControl(t, d); batch.Queued != 1 {
		t.Fatalf("batch: %#v", batch)
	}
	select {
	case url := <-started:
		if url != "https://www.youtube.com/watch?v=abcdefghijk" {
			t.Fatal(url)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retrieval did not start")
	}
	if _, err := d.handleManualMapping(context.Background(), ipc.Request{Command: "acquire.mapping.set", ManualMapping: &ipc.ManualMappingRequest{TrackURI: "spotify:track:one", VideoID: "ZYXWvu_987-"}}); err != nil {
		t.Fatal(err)
	}
	stop()
	d = acquisitionDaemon(t, home, retrieveFunc(func(_ context.Context, url string) (*acquisition.Media, error) {
		if url != "https://www.youtube.com/watch?v=abcdefghijk" {
			t.Errorf("running work redirected to %s", url)
		}
		return nil, errors.New("unavailable")
	}), 1)
	d.resolver = resolveFunc(func(context.Context, desired.Track) (string, error) {
		t.Error("resolver called on recovery")
		return "", acquisition.ErrUnresolved
	})
	runAcquisitionDaemon(t, d)
	waitAcquisition(t, d, 1, "failed")
	m, err := d.DB.ManualYouTubeMapping(context.Background(), "spotify:track:one")
	if err != nil || m.VideoID != "ZYXWvu_987-" {
		t.Fatalf("mapping after restart: %#v %v", m, err)
	}
	work, err := d.DB.Acquisition(context.Background(), 1)
	if err != nil || work.SourceURL != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("work after restart: %#v %v", work, err)
	}
}

func TestManualMappingReusesUnresolvedWorkOnlyOnExplicitRetry(t *testing.T) {
	home, err := os.MkdirTemp("", "ob-unresolved-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d := acquisitionDaemon(t, home, retrieveFunc(func(_ context.Context, url string) (*acquisition.Media, error) {
		if url != "https://www.youtube.com/watch?v=abcdefghijk" {
			t.Errorf("unexpected URL: %s", url)
		}
		return fakeMedia(t)
	}), 1)
	d.resolver = resolveFunc(func(context.Context, desired.Track) (string, error) { return "", acquisition.ErrUnresolved })
	seedAcquisitionTracks(t, d, "one")
	runAcquisitionDaemon(t, d)
	if batch := acquireBatchControl(t, d); batch.Queued != 1 {
		t.Fatalf("batch: %#v", batch)
	}
	waitAcquisition(t, d, 1, "unresolved")
	if _, err := d.handleManualMapping(context.Background(), ipc.Request{Command: "acquire.mapping.set", ManualMapping: &ipc.ManualMappingRequest{TrackURI: "spotify:track:one", VideoID: "abcdefghijk"}}); err != nil {
		t.Fatal(err)
	}
	if batch := acquireBatchControl(t, d); batch.Queued != 0 || batch.SkippedAttempted != 1 {
		t.Fatalf("mapping unexpectedly queued: %#v", batch)
	}
	if work, err := d.DB.Acquisition(context.Background(), 1); err != nil || work.State != db.AcquisitionUnresolved {
		t.Fatalf("mapping changed unresolved work: %#v %v", work, err)
	}
	if batch := retryUnresolvedControl(t, d); batch.Queued != 1 {
		t.Fatalf("retry: %#v", batch)
	}
	waitAcquisition(t, d, 1, "complete")
}
