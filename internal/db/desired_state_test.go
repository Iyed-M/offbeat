package db

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
)

func TestReadDesiredSpotifyStateReturnsExactCurrentState(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	trackOne := desired.Track{URI: "spotify:track:one", Name: "One", Artists: []desired.NamedURI{{URI: "spotify:artist:one", Name: "Artist One"}}, Album: desired.NamedURI{URI: "spotify:album:one", Name: "Album One"}, DurationMS: 1000}
	trackTwo := desired.Track{URI: "spotify:track:two", Name: "Two", Artists: []desired.NamedURI{{URI: "spotify:artist:two", Name: "Artist Two"}}, Album: desired.NamedURI{URI: "spotify:album:two", Name: "Album Two"}, DurationMS: 2000}
	insertTrack(t, d, trackOne)
	insertTrack(t, d, trackTwo)
	if _, err := d.ExecContext(ctx, `INSERT INTO playlists(uri, name, rootlist_position) VALUES ('spotify:playlist:two', 'Two', 1), ('spotify:playlist:one', 'One', 0)`); err != nil {
		t.Fatalf("insert playlists: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO playlist_entries(playlist_uri, position, kind, track_uri, source_uri) VALUES
        ('spotify:playlist:one', 0, 'supported', 'spotify:track:one', NULL),
        ('spotify:playlist:one', 1, 'unsupported', NULL, 'spotify:episode:one'),
        ('spotify:playlist:one', 2, 'supported', 'spotify:track:one', NULL),
        ('spotify:playlist:two', 0, 'supported', 'spotify:track:two', NULL)`); err != nil {
		t.Fatalf("insert playlist entries: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO liked_entries(position, kind, track_uri, source_uri) VALUES
        (0, 'unsupported', NULL, NULL), (1, 'supported', 'spotify:track:two', NULL)`); err != nil {
		t.Fatalf("insert liked entries: %v", err)
	}
	committed := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	if _, err := d.ExecContext(ctx, `UPDATE state_metadata SET revision = 7, last_committed_at = ? WHERE singleton_id = 1`, committed.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("update metadata: %v", err)
	}

	got, metadata, err := d.ReadDesiredSpotifyState(ctx)
	if err != nil {
		t.Fatalf("read desired state: %v", err)
	}
	want := desired.State{
		Tracks: []desired.Track{trackOne, trackTwo},
		Playlists: []desired.Playlist{
			{URI: "spotify:playlist:one", Name: "One", Position: 0, Entries: []desired.Entry{{Position: 0, Kind: desired.EntrySupported, TrackURI: trackOne.URI}, {Position: 1, Kind: desired.EntryUnsupported, SourceURI: "spotify:episode:one"}, {Position: 2, Kind: desired.EntrySupported, TrackURI: trackOne.URI}}},
			{URI: "spotify:playlist:two", Name: "Two", Position: 1, Entries: []desired.Entry{{Position: 0, Kind: desired.EntrySupported, TrackURI: trackTwo.URI}}},
		},
		LikedSongs: []desired.Entry{{Position: 0, Kind: desired.EntryUnsupported}, {Position: 1, Kind: desired.EntrySupported, TrackURI: trackTwo.URI}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
	if metadata.Revision != 7 || metadata.LastCommittedAt == nil || !metadata.LastCommittedAt.Equal(committed) {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestCurrentStateSchemaConstrainsEntryRepresentation(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO playlists(uri, name, rootlist_position) VALUES ('spotify:playlist:one', 'One', 0)`); err != nil {
		t.Fatalf("insert playlist: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO playlist_entries(playlist_uri, position, kind, track_uri) VALUES ('spotify:playlist:one', 0, 'supported', NULL)`,
		`INSERT INTO playlist_entries(playlist_uri, position, kind, track_uri) VALUES ('spotify:playlist:one', 0, 'unsupported', 'spotify:track:missing')`,
		`INSERT INTO playlist_entries(playlist_uri, position, kind, source_uri) VALUES ('spotify:playlist:one', 0, 'unsupported', '')`,
		`INSERT INTO liked_entries(position, kind, track_uri) VALUES (0, 'other', NULL)`,
		`INSERT INTO spotify_tracks(uri, name, artists_json, album_uri, album_name, duration_ms) VALUES ('spotify:track:bad', 'Bad', '{}', 'spotify:album:bad', 'Bad', 1)`,
	} {
		if _, err := d.ExecContext(ctx, query); err == nil {
			t.Fatalf("invalid entry insert succeeded: %s", query)
		}
	}
}

func insertTrack(t *testing.T, d *DB, track desired.Track) {
	t.Helper()
	artists, err := json.Marshal(track.Artists)
	if err != nil {
		t.Fatalf("marshal artists: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO spotify_tracks(uri, name, artists_json, album_uri, album_name, duration_ms) VALUES (?, ?, ?, ?, ?, ?)`, track.URI, track.Name, string(artists), track.Album.URI, track.Album.Name, track.DurationMS); err != nil {
		t.Fatalf("insert track: %v", err)
	}
}
