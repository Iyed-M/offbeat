package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
)

// ReadDesiredSpotifyState returns the complete current Desired Spotify state.
func (d *DB) ReadDesiredSpotifyState(ctx context.Context) (desired.State, desired.Metadata, error) {
	state := desired.State{Tracks: []desired.Track{}, Playlists: []desired.Playlist{}, LikedSongs: []desired.Entry{}}
	metadata, err := d.readStateMetadata(ctx)
	if err != nil {
		return desired.State{}, desired.Metadata{}, err
	}

	rows, err := d.QueryContext(ctx, `SELECT uri, name, artists_json, album_uri, album_name, duration_ms FROM spotify_tracks ORDER BY uri`)
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

	playlists, err := d.QueryContext(ctx, `SELECT uri, name, rootlist_position FROM playlists ORDER BY rootlist_position`)
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
		playlist.Entries, err = d.readEntries(ctx, `SELECT position, kind, track_uri, source_uri FROM playlist_entries WHERE playlist_uri = ? ORDER BY position`, playlist.URI)
		if err != nil {
			return desired.State{}, desired.Metadata{}, fmt.Errorf("read playlist %q entries: %w", playlist.URI, err)
		}
		state.Playlists = append(state.Playlists, playlist)
	}

	state.LikedSongs, err = d.readEntries(ctx, `SELECT position, kind, track_uri, source_uri FROM liked_entries ORDER BY position`)
	if err != nil {
		return desired.State{}, desired.Metadata{}, fmt.Errorf("read liked entries: %w", err)
	}
	return state, metadata, nil
}

func (d *DB) readStateMetadata(ctx context.Context) (desired.Metadata, error) {
	var metadata desired.Metadata
	var committed sql.NullString
	if err := d.QueryRowContext(ctx, `SELECT revision, last_committed_at FROM state_metadata WHERE singleton_id = 1`).Scan(&metadata.Revision, &committed); err != nil {
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

func (d *DB) readEntries(ctx context.Context, query string, args ...any) ([]desired.Entry, error) {
	rows, err := d.QueryContext(ctx, query, args...)
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
