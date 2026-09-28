package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Iyed-M/offbeat/internal/desired"
)

func TestManualMappingMigrationCRUDAndSpotifyRetention(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mapping.db")
	d, err := OpenFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// Upgrade an existing database, without requiring a current Desired track.
	migrations, err := LoadMigrations(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, MigrationsTableSchema); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:5] {
		if err := d.applyOne(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if versions, err := d.Migrate(ctx, nil, ""); err != nil || !reflect.DeepEqual(versions, []int{6, 7, 8, 9}) {
		t.Fatalf("upgrade: %v, %v", versions, err)
	}
	uri := "spotify:track:one"
	m, err := d.SetManualYouTubeMapping(ctx, uri, "abcdefghijk")
	if err != nil || m.Provenance != "manual" || m.CreatedAt == "" || m.UpdatedAt == "" {
		t.Fatalf("mapping: %#v %v", m, err)
	}
	updated, err := d.SetManualYouTubeMapping(ctx, uri, "ZYXWvu_987-")
	if err != nil || updated.CreatedAt != m.CreatedAt || updated.VideoID != "ZYXWvu_987-" {
		t.Fatalf("replacement: %#v %v", updated, err)
	}
	for _, id := range []string{"short", "abcdefghijk/", "abcdefghij?"} {
		if _, err := d.SetManualYouTubeMapping(ctx, uri, id); err == nil {
			t.Fatalf("invalid id %q accepted", id)
		}
	}
	if _, err := d.SetManualYouTubeMapping(ctx, "spotify:episode:one", "abcdefghijk"); err == nil {
		t.Fatal("invalid track accepted")
	}
	track := acquisitionTrack(uri)
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, desired.Candidate{}); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = OpenFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatal(err)
	}
	m, err = d.ManualYouTubeMapping(ctx, uri)
	if err != nil || m.VideoID != "ZYXWvu_987-" {
		t.Fatalf("persisted removed-track choice: %#v %v", m, err)
	}
	for i := range 130 {
		if _, err := d.SetManualYouTubeMapping(ctx, fmt.Sprintf("spotify:track:page%03d", i), "abcdefghijk"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := d.ManualYouTubeMappingsAfter(ctx, "", 128)
	if err != nil || len(first) != 128 {
		t.Fatalf("page: %d %v", len(first), err)
	}
	second, err := d.ManualYouTubeMappingsAfter(ctx, first[len(first)-1].TrackURI, 128)
	if err != nil || len(second) != 3 {
		t.Fatalf("next page: %d %v", len(second), err)
	}
	if err := d.RemoveManualYouTubeMapping(ctx, uri); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ManualYouTubeMapping(ctx, uri); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed: %v", err)
	}
	if err := d.RemoveManualYouTubeMapping(ctx, uri); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("remove twice: %v", err)
	}
}
