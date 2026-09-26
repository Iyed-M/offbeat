package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func selectionFixture(t *testing.T) (*Daemon, *ipc.AcquisitionChoice, string) {
	t.Helper()
	home, err := os.MkdirTemp("", "ob-select-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d := acquisitionDaemon(t, home, retrieveFunc(func(context.Context, string) (*acquisition.Media, error) { return nil, errors.New("unavailable") }), 1)
	seedAcquisitionTracks(t, d, "one")
	track, err := d.DB.DesiredTrack(context.Background(), "spotify:track:one")
	if err != nil {
		t.Fatal(err)
	}
	d.inspector = inspectFunc(func(_ context.Context, got desired.Track) (acquisition.ResolutionInspection, error) {
		report := acquisition.ReplayYouTubeObservation(acquisition.ResolutionObservation{
			CapturedAt: time.Now(), Track: acquisition.InspectionTrack{URI: got.URI, Title: got.Name, DurationMS: got.DurationMS,
				Artists: []acquisition.InspectionNamedURI{{URI: got.Artists[0].URI, Name: got.Artists[0].Name}},
				Album:   acquisition.InspectionNamedURI{URI: got.Album.URI, Name: got.Album.Name}},
			Query: acquisition.YouTubeQuery{Text: "Artist one", DurationMS: got.DurationMS},
			Search: acquisition.InspectionSearch{CandidateLimit: acquisition.MaxYouTubeSearchCandidates, RawResults: []acquisition.YouTubeSearchResult{
				{ID: "abcdefghijk", Title: "Artist - one", Channel: "Artist", Duration: 1},
				{ID: "ZYXWvu_987-", Title: "wrong video", Channel: "Other", Duration: 1},
			}},
		})
		report.FreshSearch = true // The fixture inspector simulates a live bounded search.
		return report, nil
	})
	c := &ipc.AcquisitionChoice{TrackURI: track.URI, VideoID: "abcdefghijk", ExpectedTitle: track.Name,
		ExpectedArtists: []string{track.Artists[0].Name}, ExpectedArtistURIs: []string{track.Artists[0].URI},
		ExpectedAlbum: track.Album.Name, ExpectedAlbumURI: track.Album.URI, ExpectedDurationMS: track.DurationMS}
	return d, c, home
}

func TestSelectYouTubeReusesUnresolvedAndFailedWithDurableSource(t *testing.T) {
	d, c, _ := selectionFixture(t)
	ctx := context.Background()
	batch, err := d.DB.EnqueueMissingAcquisitions(ctx, []string{c.TrackURI})
	if err != nil || batch.Queued != 1 {
		t.Fatalf("enqueue: %v %#v", err, batch)
	}
	work, err := d.DB.ClaimAcquisition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DB.UnresolveAcquisition(ctx, work.ID, "ambiguous"); err != nil {
		t.Fatal(err)
	}
	selectChoice := func() db.AcquisitionWork {
		t.Helper()
		result, err := d.handleControlRequest(ctx, ipc.Request{Command: "acquire.select", AcquisitionChoice: c})
		if err != nil {
			t.Fatal(err)
		}
		stored, err := d.DB.Acquisition(ctx, result.(ipc.AcquisitionResult).ID)
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	selected := selectChoice()
	if selected.ID != work.ID || selected.State != db.AcquisitionPending || selected.SourceURL != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("selected %#v", selected)
	}
	if selected = selectChoice(); selected.ID != work.ID {
		t.Fatalf("duplicate: %#v", selected)
	}
	if _, err := d.DB.ClaimAcquisition(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.FailAcquisition(ctx, work.ID, "unavailable"); err != nil {
		t.Fatal(err)
	}
	c.VideoID = "ZYXWvu_987-"
	report, err := d.handleAcquisitionInspection(ctx, ipc.Request{AcquisitionInspect: &ipc.AcquisitionTrackRequest{TrackURI: c.TrackURI}})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range report.(acquisition.ResolutionInspection).Candidates {
		if candidate.VideoID == c.VideoID {
			c.RejectionReason = string(candidate.RejectionReason)
		}
	}
	if c.RejectionReason == "" {
		t.Fatal("fixture did not reject replacement")
	}
	if _, err := d.handleYouTubeSelection(ctx, ipc.Request{AcquisitionChoice: c}); err == nil || !strings.Contains(err.Error(), c.RejectionReason) {
		t.Fatalf("missing acknowledgment: %v", err)
	}
	c.AcknowledgeRejection = c.RejectionReason
	selected = selectChoice()
	if selected.ID != work.ID || selected.SourceURL != "https://www.youtube.com/watch?v=ZYXWvu_987-" || selected.State != db.AcquisitionPending {
		t.Fatalf("replacement: %#v", selected)
	}
	m, err := d.DB.ManualYouTubeMapping(ctx, c.TrackURI)
	if err != nil || m.VideoID != c.VideoID {
		t.Fatalf("mapping: %#v %v", m, err)
	}
	if err := d.DB.RecoverAcquisitions(ctx); err != nil {
		t.Fatal(err)
	}
	selected, _ = d.DB.Acquisition(ctx, work.ID)
	if selected.SourceURL != "https://www.youtube.com/watch?v=ZYXWvu_987-" {
		t.Fatalf("recovery: %#v", selected)
	}
}

func TestSelectYouTubeConflictsAndStaleChecksDoNotSaveMapping(t *testing.T) {
	d, c, _ := selectionFixture(t)
	ctx := context.Background()
	checkAbsent := func() {
		t.Helper()
		if _, err := d.DB.ManualYouTubeMapping(ctx, c.TrackURI); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unexpected mapping: %v", err)
		}
	}
	c.ExpectedTitle = "old title"
	if _, err := d.handleYouTubeSelection(ctx, ipc.Request{AcquisitionChoice: c}); err == nil || !strings.Contains(err.Error(), "metadata changed") {
		t.Fatalf("stale: %v", err)
	}
	checkAbsent()
	c.ExpectedTitle = "one"
	work, err := d.DB.EnqueueAcquisition(ctx, c.TrackURI, "https://example.test/direct")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleYouTubeSelection(ctx, ipc.Request{AcquisitionChoice: c}); err == nil || !strings.Contains(err.Error(), "active pending") {
		t.Fatalf("conflict: %v", err)
	}
	checkAbsent()
	stored, _ := d.DB.Acquisition(ctx, work.ID)
	if stored.SourceURL != "https://example.test/direct" {
		t.Fatalf("hijacked direct: %#v", stored)
	}
	if _, err := d.DB.ClaimAcquisition(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.FailAcquisition(ctx, work.ID, "failed"); err != nil {
		t.Fatal(err)
	}
	path, err := d.managedFiles.PublishSynthetic(c.TrackURI)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DB.RegisterManagedTrack(ctx, c.TrackURI, path); err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleYouTubeSelection(ctx, ipc.Request{AcquisitionChoice: c}); err == nil || !strings.Contains(err.Error(), "available") {
		t.Fatalf("available: %v", err)
	}
	checkAbsent()
	seedAcquisitionTracks(t, d)
	if _, err := d.handleYouTubeSelection(ctx, ipc.Request{AcquisitionChoice: c}); err == nil || !strings.Contains(err.Error(), "not currently desired") {
		t.Fatalf("removed: %v", err)
	}
	checkAbsent()
}

func TestConcurrentYouTubeSelectionSingleWork(t *testing.T) {
	d, c, _ := selectionFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.handleYouTubeSelection(context.Background(), ipc.Request{AcquisitionChoice: c})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent select: %v", err)
		}
	}
	work, err := d.DB.AcquisitionsAfter(context.Background(), 0, 100)
	if err != nil || len(work) != 1 || work[0].State != db.AcquisitionPending {
		t.Fatalf("work: %#v %v", work, err)
	}
}

func TestSelectYouTubeControlConcurrentAndRestartRecovery(t *testing.T) {
	d, c, home := selectionFixture(t)
	stop := runAcquisitionDaemon(t, d)
	// The retriever fails quickly; every wire request must either reuse the
	// chosen active work or deliberately recover that failed YouTube work.
	var wg sync.WaitGroup
	results := make(chan ipc.Response, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := ipc.Encode(ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.select", AcquisitionChoice: c})
			results <- sendRaw(t, SocketPath(d.socketDir), append(raw, '\n'))
		}()
	}
	wg.Wait()
	close(results)
	for response := range results {
		if response.Error != nil {
			t.Fatalf("wire select: %v", response.Error)
		}
		encoded, _ := json.Marshal(response.Result)
		var work ipc.AcquisitionResult
		if err := json.Unmarshal(encoded, &work); err != nil || work.ID != 1 {
			t.Fatalf("response: %#v %v", response, err)
		}
	}
	work, err := d.DB.AcquisitionsAfter(context.Background(), 0, 10)
	if err != nil || len(work) != 1 || work[0].SourceURL != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("work: %#v %v", work, err)
	}
	stop()
	// A new daemon must use the persisted selection without invoking a resolver.
	d = acquisitionDaemon(t, home, retrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("still unavailable")
	}), 1)
	d.resolver = resolveFunc(func(context.Context, desired.Track) (string, error) {
		t.Error("resolver ran for manually selected work")
		return "", acquisition.ErrUnresolved
	})
	runAcquisitionDaemon(t, d)
	work, err = d.DB.AcquisitionsAfter(context.Background(), 0, 10)
	if err != nil || len(work) != 1 || work[0].SourceURL != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("restarted: %#v %v", work, err)
	}
	mapping, err := d.DB.ManualYouTubeMapping(context.Background(), c.TrackURI)
	if err != nil || mapping.VideoID != c.VideoID {
		t.Fatalf("restarted mapping: %#v %v", mapping, err)
	}
}

func TestSelectYouTubeRechecksStateAfterSearchAndRollsBackOnFailure(t *testing.T) {
	d, c, _ := selectionFixture(t)
	base := d.inspector
	d.inspector = inspectFunc(func(ctx context.Context, track desired.Track) (acquisition.ResolutionInspection, error) {
		// Simulate a Spotify sync while the external search is in progress.
		seedAcquisitionTracks(t, d)
		return base.Inspect(ctx, track)
	})
	_, err := d.handleYouTubeSelection(context.Background(), ipc.Request{AcquisitionChoice: c})
	if err == nil || !strings.Contains(err.Error(), "not currently desired") {
		t.Fatalf("removed mid-search: %v", err)
	}
	if _, err := d.DB.ManualYouTubeMapping(context.Background(), c.TrackURI); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mapping saved: %v", err)
	}
	seedAcquisitionTracks(t, d, "one")
	d.inspector = base
	if _, err := d.DB.Exec(`CREATE TRIGGER reject_choice BEFORE INSERT ON manual_youtube_mappings BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleYouTubeSelection(context.Background(), ipc.Request{AcquisitionChoice: c}); err == nil {
		t.Fatal("accepted failed mapping write")
	}
	work, err := d.DB.AcquisitionsAfter(context.Background(), 0, 10)
	if err != nil || len(work) != 0 {
		t.Fatalf("work committed without mapping: %#v %v", work, err)
	}
}
