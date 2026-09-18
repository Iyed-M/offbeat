package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
)

type stateQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// SpotifySyncSummary counts entry occurrences rather than distinct tracks.
type SpotifySyncSummary struct {
	PlaylistCount               int
	PlaylistEntryCount          int
	LikedSongsEntryCount        int
	SupportedEntryOccurrences   int
	UnsupportedEntryOccurrences int
}

// ApplyDesiredSpotifyState atomically reconciles one validated candidate with
// current Desired Spotify state. Equivalent candidates are successful no-ops.
func (d *DB) ApplyDesiredSpotifyState(ctx context.Context, candidate desired.Candidate) (desired.Metadata, SpotifySyncSummary, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, false, fmt.Errorf("begin desired state apply: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	current, metadata, err := readDesiredSpotifyState(ctx, tx)
	if err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, false, fmt.Errorf("read current desired state: %w", err)
	}
	want := stateFromCandidate(candidate)
	summary := summarizeCandidate(candidate)
	if metadata.Revision != 0 && reflect.DeepEqual(current, want) {
		if err := tx.Commit(); err != nil {
			return desired.Metadata{}, SpotifySyncSummary{}, false, fmt.Errorf("commit desired state read: %w", err)
		}
		committed = true
		return metadata, summary, false, nil
	}

	if err := upsertTracks(ctx, tx, current.Tracks, want.Tracks); err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, false, err
	}
	if err := reconcilePlaylists(ctx, tx, current.Playlists, candidate.Playlists); err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, false, err
	}
	if !entriesEqual(current.LikedSongs, candidate.LikedSongs) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM liked_entries`); err != nil {
			return desired.Metadata{}, SpotifySyncSummary{}, false, fmt.Errorf("delete liked entries: %w", err)
		}
		if err := insertCandidateEntries(ctx, tx, `INSERT INTO liked_entries(position, kind, track_uri, source_uri) VALUES (?, ?, ?, ?)`, "", candidate.LikedSongs); err != nil {
			return desired.Metadata{}, SpotifySyncSummary{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM spotify_tracks WHERE uri NOT IN (SELECT track_uri FROM playlist_entries WHERE track_uri IS NOT NULL UNION SELECT track_uri FROM liked_entries WHERE track_uri IS NOT NULL)`); err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, false, fmt.Errorf("prune unreferenced tracks: %w", err)
	}
	committedAt := time.Now().UTC()
	metadata.Revision++
	if _, err := tx.ExecContext(ctx, `UPDATE state_metadata SET revision = ?, last_committed_at = ? WHERE singleton_id = 1`, metadata.Revision, committedAt.Format(time.RFC3339Nano)); err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, false, fmt.Errorf("update desired state metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, false, fmt.Errorf("commit desired state: %w", err)
	}
	committed = true
	return desired.Metadata{Revision: metadata.Revision, LastCommittedAt: &committedAt}, summary, true, nil
}

func stateFromCandidate(candidate desired.Candidate) desired.State {
	tracks := make(map[string]desired.Track)
	collectTracks := func(entries []desired.CandidateEntry) {
		for _, entry := range entries {
			if entry.Kind == desired.EntrySupported {
				tracks[entry.Track.URI] = *entry.Track
			}
		}
	}
	state := desired.State{Tracks: make([]desired.Track, 0), Playlists: make([]desired.Playlist, 0, len(candidate.Playlists)), LikedSongs: candidateEntriesToEntries(candidate.LikedSongs)}
	for _, playlist := range candidate.Playlists {
		collectTracks(playlist.Entries)
		state.Playlists = append(state.Playlists, desired.Playlist{URI: playlist.URI, Name: playlist.Name, Position: playlist.Position, Entries: candidateEntriesToEntries(playlist.Entries)})
	}
	collectTracks(candidate.LikedSongs)
	for _, track := range tracks {
		state.Tracks = append(state.Tracks, track)
	}
	sort.Slice(state.Tracks, func(i, j int) bool { return state.Tracks[i].URI < state.Tracks[j].URI })
	sort.Slice(state.Playlists, func(i, j int) bool { return state.Playlists[i].Position < state.Playlists[j].Position })
	return state
}

func candidateEntriesToEntries(entries []desired.CandidateEntry) []desired.Entry {
	result := make([]desired.Entry, 0, len(entries))
	for _, entry := range entries {
		converted := desired.Entry{Position: entry.Position, Kind: entry.Kind, SourceURI: entry.SourceURI}
		if entry.Kind == desired.EntrySupported {
			converted.TrackURI = entry.Track.URI
		}
		result = append(result, converted)
	}
	return result
}

func upsertTracks(ctx context.Context, tx *sql.Tx, current, wanted []desired.Track) error {
	known := make(map[string]desired.Track, len(current))
	for _, track := range current {
		known[track.URI] = track
	}
	for _, track := range wanted {
		if existing, ok := known[track.URI]; ok && reflect.DeepEqual(existing, track) {
			continue
		}
		artists, err := json.Marshal(track.Artists)
		if err != nil {
			return fmt.Errorf("encode track artists: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO spotify_tracks(uri, name, artists_json, album_uri, album_name, duration_ms) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(uri) DO UPDATE SET name = excluded.name, artists_json = excluded.artists_json, album_uri = excluded.album_uri, album_name = excluded.album_name, duration_ms = excluded.duration_ms`, track.URI, track.Name, string(artists), track.Album.URI, track.Album.Name, track.DurationMS); err != nil {
			return fmt.Errorf("upsert supported track: %w", err)
		}
	}
	return nil
}

func reconcilePlaylists(ctx context.Context, tx *sql.Tx, current []desired.Playlist, wanted []desired.CandidatePlaylist) error {
	known := make(map[string]desired.Playlist, len(current))
	wantedByURI := make(map[string]desired.CandidatePlaylist, len(wanted))
	for _, playlist := range current {
		known[playlist.URI] = playlist
	}
	for _, playlist := range wanted {
		wantedByURI[playlist.URI] = playlist
	}
	for _, playlist := range current {
		if _, ok := wantedByURI[playlist.URI]; !ok {
			if _, err := tx.ExecContext(ctx, `DELETE FROM playlists WHERE uri = ?`, playlist.URI); err != nil {
				return fmt.Errorf("delete playlist: %w", err)
			}
		}
	}
	offset := len(current) + len(wanted) + 1
	for _, playlist := range wanted {
		if existing, ok := known[playlist.URI]; ok && existing.Position != playlist.Position {
			if _, err := tx.ExecContext(ctx, `UPDATE playlists SET position = ? WHERE uri = ?`, existing.Position+offset, playlist.URI); err != nil {
				return fmt.Errorf("move playlist to temporary position: %w", err)
			}
		}
	}
	for _, playlist := range wanted {
		existing, ok := known[playlist.URI]
		if !ok {
			if _, err := tx.ExecContext(ctx, `INSERT INTO playlists(uri, name, position) VALUES (?, ?, ?)`, playlist.URI, playlist.Name, playlist.Position); err != nil {
				return fmt.Errorf("insert playlist: %w", err)
			}
		} else if existing.Name != playlist.Name || existing.Position != playlist.Position {
			if _, err := tx.ExecContext(ctx, `UPDATE playlists SET name = ?, position = ? WHERE uri = ?`, playlist.Name, playlist.Position, playlist.URI); err != nil {
				return fmt.Errorf("update playlist: %w", err)
			}
		}
		if !ok || !entriesEqual(existing.Entries, playlist.Entries) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM playlist_entries WHERE playlist_uri = ?`, playlist.URI); err != nil {
				return fmt.Errorf("delete playlist entries: %w", err)
			}
			if err := insertCandidateEntries(ctx, tx, `INSERT INTO playlist_entries(playlist_uri, position, kind, track_uri, source_uri) VALUES (?, ?, ?, ?, ?)`, playlist.URI, playlist.Entries); err != nil {
				return err
			}
		}
	}
	return nil
}

func entriesEqual(current []desired.Entry, wanted []desired.CandidateEntry) bool {
	return reflect.DeepEqual(current, candidateEntriesToEntries(wanted))
}

func summarizeCandidate(candidate desired.Candidate) SpotifySyncSummary {
	summary := SpotifySyncSummary{PlaylistCount: len(candidate.Playlists), LikedSongsEntryCount: len(candidate.LikedSongs)}
	for _, playlist := range candidate.Playlists {
		summary.PlaylistEntryCount += len(playlist.Entries)
		countEntryKinds(&summary, playlist.Entries)
	}
	countEntryKinds(&summary, candidate.LikedSongs)
	return summary
}

func countEntryKinds(summary *SpotifySyncSummary, entries []desired.CandidateEntry) {
	for _, entry := range entries {
		if entry.Kind == desired.EntrySupported {
			summary.SupportedEntryOccurrences++
		} else {
			summary.UnsupportedEntryOccurrences++
		}
	}
}

func insertCandidateEntries(ctx context.Context, tx *sql.Tx, query, playlistURI string, entries []desired.CandidateEntry) error {
	for _, entry := range entries {
		var trackURI, sourceURI any
		if entry.Kind == desired.EntrySupported {
			trackURI = entry.Track.URI
		} else if entry.SourceURI != "" {
			sourceURI = entry.SourceURI
		}
		var err error
		if playlistURI == "" {
			_, err = tx.ExecContext(ctx, query, entry.Position, entry.Kind, trackURI, sourceURI)
		} else {
			_, err = tx.ExecContext(ctx, query, playlistURI, entry.Position, entry.Kind, trackURI, sourceURI)
		}
		if err != nil {
			return fmt.Errorf("insert desired state entry: %w", err)
		}
	}
	return nil
}

// ReadDesiredSpotifyState returns the complete current Desired Spotify state.
func (d *DB) ReadDesiredSpotifyState(ctx context.Context) (desired.State, desired.Metadata, error) {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("begin desired state read: %w", err)
	}
	defer tx.Rollback()
	state, metadata, err := readDesiredSpotifyState(ctx, tx)
	if err != nil {
		return desired.State{}, desired.Metadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("commit desired state read: %w", err)
	}
	return state, metadata, nil
}

// readDesiredSpotifyState reads from one query source, including a caller's
// existing write transaction when current state must be compared before apply.
func readDesiredSpotifyState(ctx context.Context, q stateQuerier) (desired.State, desired.Metadata, error) {
	state := desired.State{Tracks: []desired.Track{}, Playlists: []desired.Playlist{}, LikedSongs: []desired.Entry{}}
	metadata, err := readStateMetadata(ctx, q)
	if err != nil {
		return desired.State{}, desired.Metadata{}, err
	}

	rows, err := q.QueryContext(ctx, `SELECT uri, name, artists_json, album_uri, album_name, duration_ms FROM spotify_tracks ORDER BY uri`)
	if err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("query tracks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var track desired.Track
		var artistsJSON string
		if err := rows.Scan(&track.URI, &track.Name, &artistsJSON, &track.Album.URI, &track.Album.Name, &track.DurationMS); err != nil {
			return desired.State{}, desired.Metadata{}, fmt.Errorf("scan track: %w", err)
		}
		if err := json.Unmarshal([]byte(artistsJSON), &track.Artists); err != nil {
			return desired.State{}, desired.Metadata{}, fmt.Errorf("decode artists for %q: %w", track.URI, err)
		}
		state.Tracks = append(state.Tracks, track)
	}
	if err := rows.Err(); err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("iterate tracks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("close tracks: %w", err)
	}

	playlists, err := q.QueryContext(ctx, `SELECT uri, name, position FROM playlists ORDER BY position`)
	if err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("query playlists: %w", err)
	}
	playlistRows := make([]desired.Playlist, 0)
	for playlists.Next() {
		playlist := desired.Playlist{Entries: []desired.Entry{}}
		if err := playlists.Scan(&playlist.URI, &playlist.Name, &playlist.Position); err != nil {
			return desired.State{}, desired.Metadata{}, fmt.Errorf("scan playlist: %w", err)
		}
		playlistRows = append(playlistRows, playlist)
	}
	if err := playlists.Err(); err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("iterate playlists: %w", err)
	}
	if err := playlists.Close(); err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("close playlists: %w", err)
	}
	for _, playlist := range playlistRows {
		playlist.Entries, err = readEntries(ctx, q, `SELECT position, kind, track_uri, source_uri FROM playlist_entries WHERE playlist_uri = ? ORDER BY position`, playlist.URI)
		if err != nil {
			return desired.State{}, desired.Metadata{}, fmt.Errorf("read playlist %q entries: %w", playlist.URI, err)
		}
		state.Playlists = append(state.Playlists, playlist)
	}

	state.LikedSongs, err = readEntries(ctx, q, `SELECT position, kind, track_uri, source_uri FROM liked_entries ORDER BY position`)
	if err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("read liked entries: %w", err)
	}
	return state, metadata, nil
}

func readStateMetadata(ctx context.Context, q stateQuerier) (desired.Metadata, error) {
	var metadata desired.Metadata
	var committed sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT revision, last_committed_at FROM state_metadata WHERE singleton_id = 1`).Scan(&metadata.Revision, &committed); err != nil {
		return desired.Metadata{}, fmt.Errorf("query state metadata: %w", err)
	}
	if committed.Valid {
		value, err := time.Parse(time.RFC3339Nano, committed.String)
		if err != nil {
			return desired.Metadata{}, fmt.Errorf("parse last committed timestamp: %w", err)
		}
		metadata.LastCommittedAt = &value
	}
	return metadata, nil
}

func readEntries(ctx context.Context, q stateQuerier, query string, args ...any) ([]desired.Entry, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []desired.Entry{}
	for rows.Next() {
		var entry desired.Entry
		var trackURI, sourceURI sql.NullString
		if err := rows.Scan(&entry.Position, &entry.Kind, &trackURI, &sourceURI); err != nil {
			return nil, err
		}
		if trackURI.Valid {
			entry.TrackURI = trackURI.String
		}
		if sourceURI.Valid {
			entry.SourceURI = sourceURI.String
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
