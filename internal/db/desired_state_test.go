package db

import (
	"context"
	"encoding/json"
	"path/filepath"
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
	if _, err := d.ExecContext(ctx, `INSERT INTO playlists(uri, name, position) VALUES ('spotify:playlist:two', 'Two', 1), ('spotify:playlist:one', 'One', 0)`); err != nil {
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

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	got, metadata, err = readDesiredSpotifyState(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("read desired state in transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transaction state = %#v, want %#v", got, want)
	}
	if metadata.Revision != 7 || metadata.LastCommittedAt == nil || !metadata.LastCommittedAt.Equal(committed) {
		t.Fatalf("transaction metadata = %#v", metadata)
	}
}

func TestCurrentStateSchemaConstrainsEntryRepresentation(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO playlists(uri, name, position) VALUES ('spotify:playlist:one', 'One', 0)`); err != nil {
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

func TestApplyDesiredSpotifyStateReconcilesAndSkipsEquivalentCandidate(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	candidateA, wantA := desiredCandidateFixture()
	metadata, summary, changed, err := d.ApplyDesiredSpotifyState(ctx, candidateA)
	if err != nil {
		t.Fatalf("apply A: %v", err)
	}
	if !changed || metadata.Revision != 1 || metadata.LastCommittedAt == nil || metadata.LastCommittedAt.IsZero() || metadata.LastCommittedAt.Location() != time.UTC {
		t.Fatalf("metadata = %#v", metadata)
	}
	if summary != (SpotifySyncSummary{PlaylistCount: 2, PlaylistEntryCount: 4, LikedSongsEntryCount: 3, SupportedEntryOccurrences: 4, UnsupportedEntryOccurrences: 3}) {
		t.Fatalf("summary = %#v", summary)
	}
	got, persistedMetadata, err := d.ReadDesiredSpotifyState(ctx)
	if err != nil {
		t.Fatalf("read committed state: %v", err)
	}
	if !reflect.DeepEqual(got, wantA) {
		t.Fatalf("state = %#v, want %#v", got, wantA)
	}
	if persistedMetadata.Revision != 1 || persistedMetadata.LastCommittedAt == nil || !persistedMetadata.LastCommittedAt.Equal(*metadata.LastCommittedAt) {
		t.Fatalf("persisted metadata = %#v", persistedMetadata)
	}

	candidateB, wantB := desiredCandidateBFixture()
	metadata, summary, changed, err = d.ApplyDesiredSpotifyState(ctx, candidateB)
	if err != nil {
		t.Fatalf("apply B: %v", err)
	}
	if !changed || metadata.Revision != 2 || summary != (SpotifySyncSummary{PlaylistCount: 2, PlaylistEntryCount: 4, LikedSongsEntryCount: 4, SupportedEntryOccurrences: 6, UnsupportedEntryOccurrences: 2}) {
		t.Fatalf("B result = metadata %#v summary %#v changed %v", metadata, summary, changed)
	}
	got, persistedMetadata, err = d.ReadDesiredSpotifyState(ctx)
	if err != nil || !reflect.DeepEqual(got, wantB) || persistedMetadata.Revision != 2 {
		t.Fatalf("B state = %#v metadata %#v err %v", got, persistedMetadata, err)
	}
	committedAt := *persistedMetadata.LastCommittedAt
	metadata, summary, changed, err = d.ApplyDesiredSpotifyState(ctx, candidateB)
	if err != nil || changed || metadata.Revision != 2 || metadata.LastCommittedAt == nil || !metadata.LastCommittedAt.Equal(committedAt) {
		t.Fatalf("equivalent B result = metadata %#v summary %#v changed %v err %v", metadata, summary, changed, err)
	}
}

func TestApplyDesiredSpotifyStateRollsBackSQLiteFailure(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	candidateA, _ := desiredCandidateFixture()
	candidateB, wantB := desiredCandidateBFixture()
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, candidateA); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	metadata, _, _, err := d.ApplyDesiredSpotifyState(ctx, candidateB)
	if err != nil {
		t.Fatalf("apply B: %v", err)
	}
	committedAt := *metadata.LastCommittedAt
	if _, err := d.ExecContext(ctx, `CREATE TRIGGER fail_liked_entry BEFORE INSERT ON liked_entries BEGIN SELECT RAISE(ABORT, 'forced failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	candidateC := candidateB
	candidateC.LikedSongs = append(candidateC.LikedSongs, desired.CandidateEntry{Position: 4, Kind: desired.EntryUnsupported, SourceURI: "spotify:episode:failure"})
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, candidateC); err == nil {
		t.Fatal("apply succeeded despite SQLite failure")
	}
	state, metadata, err := d.ReadDesiredSpotifyState(ctx)
	if err != nil {
		t.Fatalf("read state after rollback: %v", err)
	}
	if !reflect.DeepEqual(state, wantB) {
		t.Fatalf("state after rollback = %#v, want %#v", state, wantB)
	}
	if metadata.Revision != 2 || metadata.LastCommittedAt == nil || !metadata.LastCommittedAt.Equal(committedAt) {
		t.Fatalf("metadata after rollback = %#v", metadata)
	}
}

func TestApplyDesiredSpotifyStateReordersExistingPlaylistPositions(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	candidate, _ := desiredCandidateFixture()
	if _, _, _, err := d.ApplyDesiredSpotifyState(ctx, candidate); err != nil {
		t.Fatalf("apply initial candidate: %v", err)
	}
	for index := range candidate.Playlists {
		candidate.Playlists[index].Position = 1 - candidate.Playlists[index].Position
	}
	metadata, _, changed, err := d.ApplyDesiredSpotifyState(ctx, candidate)
	if err != nil || !changed || metadata.Revision != 2 {
		t.Fatalf("reorder result = metadata %#v changed %v err %v", metadata, changed, err)
	}
	state, _, err := d.ReadDesiredSpotifyState(ctx)
	if err != nil || !reflect.DeepEqual(state, stateFromCandidate(candidate)) {
		t.Fatalf("reordered state = %#v err %v", state, err)
	}
}

func TestDesiredSpotifyStateSurvivesDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "offbeat.db")
	d, err := OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	candidate, want := desiredCandidateBFixture()
	metadata, _, _, err := d.ApplyDesiredSpotifyState(ctx, candidate)
	if err != nil {
		t.Fatalf("apply desired state: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	d, err = OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatalf("migrate reopened database: %v", err)
	}
	state, reopenedMetadata, err := d.ReadDesiredSpotifyState(ctx)
	if err != nil || !reflect.DeepEqual(state, want) || reopenedMetadata.Revision != metadata.Revision || reopenedMetadata.LastCommittedAt == nil || !reopenedMetadata.LastCommittedAt.Equal(*metadata.LastCommittedAt) {
		t.Fatalf("reopened state = %#v metadata %#v err %v", state, reopenedMetadata, err)
	}
}

func desiredCandidateFixture() (desired.Candidate, desired.State) {
	trackOne := desired.Track{URI: "spotify:track:one", Name: "One", Artists: []desired.NamedURI{{URI: "spotify:artist:one", Name: "Artist One"}}, Album: desired.NamedURI{URI: "spotify:album:one", Name: "Album One"}, DurationMS: 1000}
	trackTwo := desired.Track{URI: "spotify:track:two", Name: "Two", Artists: []desired.NamedURI{{URI: "spotify:artist:two", Name: "Artist Two"}}, Album: desired.NamedURI{URI: "spotify:album:two", Name: "Album Two"}, DurationMS: 2000}
	candidate := desired.Candidate{Playlists: []desired.CandidatePlaylist{
		{URI: "spotify:playlist:two", Name: "Two", Position: 1, Entries: []desired.CandidateEntry{{Position: 0, Kind: desired.EntrySupported, Track: &trackTwo}}},
		{URI: "spotify:playlist:one", Name: "One", Position: 0, Entries: []desired.CandidateEntry{{Position: 0, Kind: desired.EntrySupported, Track: &trackOne}, {Position: 1, Kind: desired.EntryUnsupported, SourceURI: "spotify:episode:one"}, {Position: 2, Kind: desired.EntrySupported, Track: &trackOne}}},
	}, LikedSongs: []desired.CandidateEntry{{Position: 0, Kind: desired.EntryUnsupported}, {Position: 1, Kind: desired.EntrySupported, Track: &trackTwo}, {Position: 2, Kind: desired.EntryUnsupported, SourceURI: "spotify:episode:two"}}}
	state := desired.State{Tracks: []desired.Track{trackOne, trackTwo}, Playlists: []desired.Playlist{
		{URI: "spotify:playlist:one", Name: "One", Position: 0, Entries: []desired.Entry{{Position: 0, Kind: desired.EntrySupported, TrackURI: trackOne.URI}, {Position: 1, Kind: desired.EntryUnsupported, SourceURI: "spotify:episode:one"}, {Position: 2, Kind: desired.EntrySupported, TrackURI: trackOne.URI}}},
		{URI: "spotify:playlist:two", Name: "Two", Position: 1, Entries: []desired.Entry{{Position: 0, Kind: desired.EntrySupported, TrackURI: trackTwo.URI}}},
	}, LikedSongs: []desired.Entry{{Position: 0, Kind: desired.EntryUnsupported}, {Position: 1, Kind: desired.EntrySupported, TrackURI: trackTwo.URI}, {Position: 2, Kind: desired.EntryUnsupported, SourceURI: "spotify:episode:two"}}}
	return candidate, state
}

func desiredCandidateBFixture() (desired.Candidate, desired.State) {
	trackOne := desired.Track{URI: "spotify:track:one", Name: "One (remastered)", Artists: []desired.NamedURI{{URI: "spotify:artist:one", Name: "Artist One"}, {URI: "spotify:artist:guest", Name: "Guest"}}, Album: desired.NamedURI{URI: "spotify:album:one", Name: "Album One"}, DurationMS: 1100}
	trackThree := desired.Track{URI: "spotify:track:three", Name: "Three", Artists: []desired.NamedURI{{URI: "spotify:artist:three", Name: "Artist Three"}}, Album: desired.NamedURI{URI: "spotify:album:three", Name: "Album Three"}, DurationMS: 3000}
	candidate := desired.Candidate{Playlists: []desired.CandidatePlaylist{
		{URI: "spotify:playlist:two", Name: "Two renamed", Position: 0, Entries: []desired.CandidateEntry{{Position: 0, Kind: desired.EntrySupported, Track: &trackThree}, {Position: 1, Kind: desired.EntrySupported, Track: &trackOne}, {Position: 2, Kind: desired.EntrySupported, Track: &trackOne}}},
		{URI: "spotify:playlist:three", Name: "Three", Position: 1, Entries: []desired.CandidateEntry{{Position: 0, Kind: desired.EntryUnsupported, SourceURI: "spotify:episode:three"}}},
	}, LikedSongs: []desired.CandidateEntry{{Position: 0, Kind: desired.EntrySupported, Track: &trackOne}, {Position: 1, Kind: desired.EntryUnsupported}, {Position: 2, Kind: desired.EntrySupported, Track: &trackThree}, {Position: 3, Kind: desired.EntrySupported, Track: &trackOne}}}
	return candidate, stateFromCandidate(candidate)
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
