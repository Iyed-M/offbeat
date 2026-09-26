package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Iyed-M/offbeat/internal/ipc"
)

// ReviewCandidatesAfter scans a bounded window of desired tracks whose most
// recent YouTube work is unresolved or failed. Filesystem availability is
// checked by the daemon, never inferred from the managed_tracks row alone.
func (d *DB) ReviewCandidatesAfter(ctx context.Context, after string, limit int) ([]ipc.ReviewTrack, []string, error) {
	rows, err := d.QueryContext(ctx, `SELECT s.uri, s.name, s.artists_json, s.duration_ms, w.state, w.error, COALESCE(m.relative_path, '')
		FROM spotify_tracks s
		JOIN acquisition_work w ON w.id = (SELECT MAX(id) FROM acquisition_work WHERE track_uri = s.uri AND source_kind = 'youtube')
		LEFT JOIN managed_tracks m ON m.track_uri = s.uri
		WHERE s.uri > ? AND w.state IN ('unresolved', 'failed')
		AND NOT EXISTS (SELECT 1 FROM acquisition_work a WHERE a.track_uri = s.uri AND a.state IN ('pending', 'running'))
		ORDER BY s.uri LIMIT ?`, after, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("query review candidates: %w", err)
	}
	defer rows.Close()
	items := make([]ipc.ReviewTrack, 0, limit)
	paths := make([]string, 0, limit)
	for rows.Next() {
		var item ipc.ReviewTrack
		var artists, path string
		if err := rows.Scan(&item.TrackURI, &item.Title, &artists, &item.DurationMS, &item.WorkState, &item.WorkError, &path); err != nil {
			return nil, nil, err
		}
		var named []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(artists), &named); err != nil {
			return nil, nil, fmt.Errorf("decode review artists: %w", err)
		}
		item.Artists = make([]string, 0, len(named))
		for _, artist := range named {
			item.Artists = append(item.Artists, artist.Name)
		}
		items = append(items, item)
		paths = append(paths, path)
	}
	return items, paths, rows.Err()
}
