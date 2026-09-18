package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
)

type stateQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ErrDesiredSpotifyStateAlreadyCommitted marks the intentionally narrow M4
// first-commit boundary. Reconciliation belongs to the following ticket.
var ErrDesiredSpotifyStateAlreadyCommitted = errors.New("desired Spotify state is already committed")

// SpotifySyncSummary counts entry occurrences rather than distinct tracks.
type SpotifySyncSummary struct {
	PlaylistCount               int
	PlaylistEntryCount          int
	LikedSongsEntryCount        int
	SupportedEntryOccurrences   int
	UnsupportedEntryOccurrences int
}

// ApplyInitialDesiredSpotifyState atomically commits the first validated
// candidate. It deliberately rejects later applies rather than anticipating
// reconciliation policy.
func (d *DB) ApplyInitialDesiredSpotifyState(ctx context.Context, candidate desired.Candidate) (desired.Metadata, SpotifySyncSummary, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, fmt.Errorf("begin desired state apply: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	_, metadata, err := readDesiredSpotifyState(ctx, tx)
	if err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, fmt.Errorf("read current desired state: %w", err)
	}
	if metadata.Revision != 0 {
		return desired.Metadata{}, SpotifySyncSummary{}, ErrDesiredSpotifyStateAlreadyCommitted
	}

	summary := summarizeCandidate(candidate)
	tracks := make(map[string]desired.Track)
	for _, playlist := range candidate.Playlists {
		for _, entry := range playlist.Entries {
			if entry.Kind == desired.EntrySupported {
				tracks[entry.Track.URI] = *entry.Track
			}
		}
	}
	for _, entry := range candidate.LikedSongs {
		if entry.Kind == desired.EntrySupported {
			tracks[entry.Track.URI] = *entry.Track
		}
	}
	for _, track := range tracks {
		artists, err := json.Marshal(track.Artists)
		if err != nil {
			return desired.Metadata{}, SpotifySyncSummary{}, fmt.Errorf("encode track artists: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO spotify_tracks(uri, name, artists_json, album_uri, album_name, duration_ms) VALUES (?, ?, ?, ?, ?, ?)`, track.URI, track.Name, string(artists), track.Album.URI, track.Album.Name, track.DurationMS); err != nil {
			return desired.Metadata{}, SpotifySyncSummary{}, fmt.Errorf("insert supported track: %w", err)
		}
	}
	for _, playlist := range candidate.Playlists {
		if _, err := tx.ExecContext(ctx, `INSERT INTO playlists(uri, name, position) VALUES (?, ?, ?)`, playlist.URI, playlist.Name, playlist.Position); err != nil {
			return desired.Metadata{}, SpotifySyncSummary{}, fmt.Errorf("insert playlist: %w", err)
		}
		if err := insertCandidateEntries(ctx, tx, `INSERT INTO playlist_entries(playlist_uri, position, kind, track_uri, source_uri) VALUES (?, ?, ?, ?, ?)`, playlist.URI, playlist.Entries); err != nil {
			return desired.Metadata{}, SpotifySyncSummary{}, err
		}
	}
	if err := insertCandidateEntries(ctx, tx, `INSERT INTO liked_entries(position, kind, track_uri, source_uri) VALUES (?, ?, ?, ?)`, "", candidate.LikedSongs); err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, err
	}
	committedAt := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE state_metadata SET revision = 1, last_committed_at = ? WHERE singleton_id = 1`, committedAt.Format(time.RFC3339Nano)); err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, fmt.Errorf("update desired state metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return desired.Metadata{}, SpotifySyncSummary{}, fmt.Errorf("commit desired state: %w", err)
	}
	committed = true
	return desired.Metadata{Revision: 1, LastCommittedAt: &committedAt}, summary, nil
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
