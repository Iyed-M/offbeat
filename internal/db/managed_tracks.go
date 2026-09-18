package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Iyed-M/offbeat/internal/desired"
)

type ManagedTrack struct {
	Track        desired.Track
	RelativePath string
}

// DesiredManagedTracks returns only distinct currently desired supported tracks,
// including their optional managed mapping, from one consistent query.
func (d *DB) DesiredManagedTracks(ctx context.Context) ([]ManagedTrack, error) {
	tracks, _, err := d.DesiredManagedTrackPage(ctx, "", -1)
	return tracks, err
}

// DesiredManagedTrackPage reads tracks and their state revision in one snapshot.
// A negative limit means all remaining tracks (for in-process callers).
func (d *DB) DesiredManagedTrackPage(ctx context.Context, after string, limit int) ([]ManagedTrack, desired.Metadata, error) {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, desired.Metadata{}, err
	}
	defer tx.Rollback()
	metadata, err := readStateMetadata(ctx, tx)
	if err != nil {
		return nil, desired.Metadata{}, err
	}
	tracks, err := readManagedTracks(ctx, tx, after, limit)
	if err != nil {
		return nil, desired.Metadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, desired.Metadata{}, err
	}
	return tracks, metadata, nil
}

func readManagedTracks(ctx context.Context, q stateQuerier, after string, limit int) ([]ManagedTrack, error) {
	rows, err := q.QueryContext(ctx, `SELECT s.uri, s.name, s.artists_json, s.album_uri, s.album_name, s.duration_ms, COALESCE(m.relative_path, '') FROM spotify_tracks s LEFT JOIN managed_tracks m ON m.track_uri = s.uri WHERE s.uri > ? ORDER BY s.uri LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("query desired managed tracks: %w", err)
	}
	defer rows.Close()
	tracks := []ManagedTrack{}
	for rows.Next() {
		var item ManagedTrack
		var artists string
		if err := rows.Scan(&item.Track.URI, &item.Track.Name, &artists, &item.Track.Album.URI, &item.Track.Album.Name, &item.Track.DurationMS, &item.RelativePath); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(artists), &item.Track.Artists); err != nil {
			return nil, err
		}
		tracks = append(tracks, item)
	}
	return tracks, rows.Err()
}

func (d *DB) IsDesiredTrack(ctx context.Context, uri string) (bool, error) {
	var exists bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spotify_tracks WHERE uri = ?)`, uri).Scan(&exists)
	return exists, err
}

// RegisterManagedTrack retains at most one file per Spotify identity. It does
// not change Desired Spotify state or its revision.
func (d *DB) RegisterManagedTrack(ctx context.Context, uri, relativePath string) error {
	result, err := d.ExecContext(ctx, `INSERT INTO managed_tracks(track_uri, relative_path) SELECT uri, ? FROM spotify_tracks WHERE uri = ? ON CONFLICT(track_uri) DO UPDATE SET relative_path = excluded.relative_path`, relativePath, uri)
	if err != nil {
		return fmt.Errorf("register managed track: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("track is not currently desired")
	}
	return nil
}
