package db

import (
	"context"
	"database/sql"
	"fmt"
)

type ManualYouTubeMapping struct {
	TrackURI   string `json:"track_uri"`
	VideoID    string `json:"video_id"`
	Provenance string `json:"provenance"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	WorkState  string `json:"work_state,omitempty"`
	WorkError  string `json:"work_error,omitempty"`
}

const mappingColumns = `m.track_uri, m.video_id, m.provenance, m.created_at, m.updated_at,
 COALESCE(w.state, ''), CASE WHEN w.source_url = 'https://www.youtube.com/watch?v=' || m.video_id THEN w.error ELSE '' END`
const mappingFrom = ` FROM manual_youtube_mappings m LEFT JOIN acquisition_work w ON w.id =
 (SELECT id FROM acquisition_work WHERE track_uri = m.track_uri AND source_kind = 'youtube' ORDER BY id DESC LIMIT 1)`

func scanManualMapping(row interface{ Scan(...any) error }) (ManualYouTubeMapping, error) {
	var m ManualYouTubeMapping
	err := row.Scan(&m.TrackURI, &m.VideoID, &m.Provenance, &m.CreatedAt, &m.UpdatedAt, &m.WorkState, &m.WorkError)
	return m, err
}

func (d *DB) ManualYouTubeMapping(ctx context.Context, uri string) (ManualYouTubeMapping, error) {
	return scanManualMapping(d.QueryRowContext(ctx, `SELECT `+mappingColumns+mappingFrom+` WHERE m.track_uri = ?`, uri))
}

func (d *DB) ManualYouTubeMappingsAfter(ctx context.Context, after string, limit int) ([]ManualYouTubeMapping, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+mappingColumns+mappingFrom+` WHERE m.track_uri > ? ORDER BY m.track_uri LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list manual mappings: %w", err)
	}
	defer rows.Close()
	items := make([]ManualYouTubeMapping, 0, limit)
	for rows.Next() {
		m, err := scanManualMapping(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}

// A mapping is independent of Desired Spotify state and is never deleted by sync.
func (d *DB) SetManualYouTubeMapping(ctx context.Context, uri, id string) (ManualYouTubeMapping, error) {
	now := acquisitionTime()
	_, err := d.ExecContext(ctx, `INSERT INTO manual_youtube_mappings(track_uri, video_id, provenance, created_at, updated_at)
 VALUES (?, ?, 'manual', ?, ?) ON CONFLICT(track_uri) DO UPDATE SET video_id=excluded.video_id, updated_at=excluded.updated_at`, uri, id, now, now)
	if err != nil {
		return ManualYouTubeMapping{}, fmt.Errorf("save manual mapping: %w", err)
	}
	return d.ManualYouTubeMapping(ctx, uri)
}

func (d *DB) RemoveManualYouTubeMapping(ctx context.Context, uri string) error {
	result, err := d.ExecContext(ctx, `DELETE FROM manual_youtube_mappings WHERE track_uri = ?`, uri)
	if err != nil {
		return fmt.Errorf("remove manual mapping: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
