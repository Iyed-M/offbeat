package db

import (
	"context"
	"reflect"
	"testing"

	"github.com/Iyed-M/offbeat/internal/desired"
)

func TestManagedMappingConstraintsAndRetention(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	// Start at M4, populate desired state, then exercise the upgrade in place.
	if _, err := d.ExecContext(ctx, MigrationsTableSchema); err != nil {
		t.Fatal(err)
	}
	migrations, err := LoadMigrations(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:2] {
		if err := d.applyOne(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	one := desired.Track{URI: "spotify:track:one", Name: "One", Artists: []desired.NamedURI{{URI: "artist", Name: "Artist"}}, Album: desired.NamedURI{URI: "album", Name: "Album"}, DurationMS: 1000}
	two := one
	two.URI = "spotify:track:two"
	candidate := desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &one}, {Position: 1, Kind: desired.EntrySupported, Track: &two}}}
	before, _, _, err := d.ApplyDesiredSpotifyState(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := d.Migrate(ctx, nil, ""); err != nil || !reflect.DeepEqual(applied, []int{3, 4, 5}) {
		t.Fatalf("M4 upgrade = %v %v", applied, err)
	}
	if err := d.RegisterManagedTrack(ctx, one.URI, "tracks/one.wav"); err != nil {
		t.Fatal(err)
	}
	if err := d.RegisterManagedTrack(ctx, two.URI, "tracks/one.wav"); err == nil {
		t.Fatal("two tracks share a file")
	}
	if err := d.RegisterManagedTrack(ctx, "not-desired", "tracks/other.wav"); err == nil {
		t.Fatal("registered unknown track")
	}
	if _, err := d.Exec(`INSERT INTO managed_tracks VALUES (?, ?)`, one.URI, "tracks/duplicate.wav"); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	if err := d.RegisterManagedTrack(ctx, one.URI, "tracks/replacement.wav"); err != nil {
		t.Fatal(err)
	}
	_, after, err := d.ReadDesiredSpotifyState(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("managed change advanced desired state: %#v %#v %v", before, after, err)
	}
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, desired.Candidate{}); err != nil {
		t.Fatal(err)
	}
	if got, err := d.DesiredManagedTracks(ctx); err != nil || len(got) != 0 {
		t.Fatalf("removed tracks still desired: %v %v", got, err)
	}
	one.Name = "Renamed"
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	got, err := d.DesiredManagedTracks(ctx)
	if err != nil || len(got) != 2 || got[0].RelativePath != "tracks/replacement.wav" || got[0].Track.Name != "Renamed" || got[1].RelativePath != "" {
		t.Fatalf("retained mapping = %#v %v", got, err)
	}
}
