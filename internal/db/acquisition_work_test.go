package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/Iyed-M/offbeat/internal/desired"
)

func TestAcquisitionWorkTransitionsAndDuplicateActiveWork(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatal(err)
	}
	track := acquisitionTrack("spotify:track:one")
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}

	queued, err := d.EnqueueAcquisition(ctx, track.URI, "https://authorized.example/one")
	if err != nil || queued.ID == 0 || queued.State != AcquisitionPending || queued.Error != "" || queued.CreatedAt == "" || queued.UpdatedAt == "" {
		t.Fatalf("enqueue = %#v, %v", queued, err)
	}
	again, err := d.EnqueueAcquisition(ctx, track.URI, queued.SourceURL)
	if err != nil || again.ID != queued.ID {
		t.Fatalf("idempotent enqueue = %#v, %v", again, err)
	}
	if _, err := d.EnqueueAcquisition(ctx, track.URI, "https://authorized.example/other"); !errors.Is(err, ErrAcquisitionConflict) {
		t.Fatalf("different active source error = %v", err)
	}

	running, err := d.ClaimAcquisition(ctx)
	if err != nil || running.ID != queued.ID || running.State != AcquisitionRunning {
		t.Fatalf("claim = %#v, %v", running, err)
	}
	if err := d.FailAcquisition(ctx, queued.ID, "tool exited 1"); err != nil {
		t.Fatal(err)
	}
	failed, err := d.Acquisition(ctx, queued.ID)
	if err != nil || failed.State != AcquisitionFailed || failed.Error != "tool exited 1" {
		t.Fatalf("failed work = %#v, %v", failed, err)
	}
	retried, err := d.RetryAcquisition(ctx, queued.ID)
	if err != nil || retried.State != AcquisitionPending || retried.Error != "" {
		t.Fatalf("retry = %#v, %v", retried, err)
	}
	if _, err := d.ClaimAcquisition(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteAcquisition(ctx, queued.ID, "tracks/one.m4a"); err != nil {
		t.Fatal(err)
	}
	complete, err := d.Acquisition(ctx, queued.ID)
	if err != nil || complete.State != AcquisitionComplete || complete.Error != "" {
		t.Fatalf("complete work = %#v, %v", complete, err)
	}
	var path string
	if err := d.QueryRowContext(ctx, `SELECT relative_path FROM managed_tracks WHERE track_uri = ?`, track.URI).Scan(&path); err != nil || path != "tracks/one.m4a" {
		t.Fatalf("managed mapping = %q, %v", path, err)
	}
	if err := d.FailAcquisition(ctx, queued.ID, "again"); !errors.Is(err, ErrAcquisitionPrecondition) {
		t.Fatalf("terminal failure transition error = %v", err)
	}
	if _, err := d.Acquisition(ctx, queued.ID+1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing acquisition error = %v", err)
	}
}

func TestRecoverAcquisitionsAndCompleteRollback(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatal(err)
	}
	track := acquisitionTrack("spotify:track:one")
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
	work, err := d.EnqueueAcquisition(ctx, track.URI, "https://authorized.example/one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimAcquisition(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.RecoverAcquisitions(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := d.Acquisition(ctx, work.ID)
	if err != nil || recovered.State != AcquisitionPending || recovered.Error != "" {
		t.Fatalf("recovered work = %#v, %v", recovered, err)
	}
	if _, err := d.ClaimAcquisition(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `CREATE TRIGGER fail_acquired_mapping BEFORE INSERT ON managed_tracks BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteAcquisition(ctx, work.ID, "tracks/one.m4a"); err == nil {
		t.Fatal("completion unexpectedly succeeded")
	}
	after, err := d.Acquisition(ctx, work.ID)
	if err != nil || after.State != AcquisitionRunning {
		t.Fatalf("failed completion split work state: %#v, %v", after, err)
	}
	var mappings int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_tracks WHERE track_uri = ?`, track.URI).Scan(&mappings); err != nil || mappings != 0 {
		t.Fatalf("failed completion mapping count = %d, %v", mappings, err)
	}
}

func TestAcquisitionRequiresCurrentlyDesiredTrack(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.EnqueueAcquisition(ctx, "spotify:track:missing", "https://authorized.example/missing"); !errors.Is(err, ErrAcquisitionPrecondition) {
		t.Fatalf("enqueue missing track error = %v", err)
	}
	track := acquisitionTrack("spotify:track:one")
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
	work, err := d.EnqueueAcquisition(ctx, track.URI, "https://authorized.example/one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimAcquisition(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, desired.Candidate{}); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteAcquisition(ctx, work.ID, "tracks/one.m4a"); !errors.Is(err, ErrAcquisitionPrecondition) {
		t.Fatalf("complete removed track error = %v", err)
	}
}

func TestRetryAcquisitionRejectsNewerActiveWorkAndOversizedFailure(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatal(err)
	}
	track := acquisitionTrack("spotify:track:one")
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
	old, err := d.EnqueueAcquisition(ctx, track.URI, "https://authorized.example/old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimAcquisition(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.FailAcquisition(ctx, old.ID, "failed"); err != nil {
		t.Fatal(err)
	}
	newer, err := d.EnqueueAcquisition(ctx, track.URI, "https://authorized.example/new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RetryAcquisition(ctx, old.ID); !errors.Is(err, ErrAcquisitionConflict) {
		t.Fatalf("retry against newer active work error = %v", err)
	}
	if _, err := d.ClaimAcquisition(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.FailAcquisition(ctx, newer.ID, strings.Repeat("x", maxAcquisitionErrorBytes+1)); !errors.Is(err, ErrAcquisitionPrecondition) {
		t.Fatalf("oversized failure error = %v", err)
	}
}

func acquisitionTrack(uri string) desired.Track {
	return desired.Track{URI: uri, Name: "One", Artists: []desired.NamedURI{{URI: "spotify:artist:one", Name: "Artist"}}, Album: desired.NamedURI{URI: "spotify:album:one", Name: "Album"}, DurationMS: 1000}
}
