package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SelectYouTube atomically saves the choice and queues one YouTube acquisition
// (or returns identical active work) with its source already durable. Caller holds the
// daemon's managed-state lock and has verified the filesystem Missing state.
func (d *DB) SelectYouTube(ctx context.Context, uri, videoID string) (AcquisitionWork, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return AcquisitionWork{}, err
	}
	defer tx.Rollback()
	if err := requireDesiredTrack(ctx, tx, uri); err != nil {
		return AcquisitionWork{}, err
	}
	url := "https://www.youtube.com/watch?v=" + videoID
	active, err := acquisitionByTrackAndStates(ctx, tx, uri, AcquisitionPending, AcquisitionRunning)
	if err == nil {
		// An already selected identical source is idempotent. In particular an
		// unselected pending resolver or direct URL must never be redirected.
		if active.SourceKind != AcquisitionSourceYouTube || active.SourceURL != url {
			return AcquisitionWork{}, fmt.Errorf("%w: active %s work %d owns track", ErrAcquisitionConflict, active.State, active.ID)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AcquisitionWork{}, err
	}
	if active.ID == 0 {
		// Prefer the most recent reusable YouTube attempt. A completed work
		// item stays historical even if its managed file later disappears.
		err = tx.QueryRowContext(ctx, `SELECT id FROM acquisition_work WHERE track_uri=? AND source_kind='youtube' AND state IN ('unresolved','failed') ORDER BY id DESC LIMIT 1`, uri).Scan(&active.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return AcquisitionWork{}, err
		}
		if active.ID != 0 {
			var result sql.Result
			result, err = tx.ExecContext(ctx, `UPDATE acquisition_work SET source_url=?, state='pending', error='', updated_at=? WHERE id=? AND state IN ('unresolved','failed')`, url, acquisitionTime(), active.ID)
			if err == nil {
				var changed int64
				changed, err = result.RowsAffected()
				if err == nil && changed != 1 {
					return AcquisitionWork{}, fmt.Errorf("%w: acquisition %d changed state", ErrAcquisitionConflict, active.ID)
				}
			}
		} else {
			result, insertErr := tx.ExecContext(ctx, `INSERT INTO acquisition_work(track_uri,source_kind,source_url,state,error,created_at,updated_at) VALUES (?, 'youtube', ?, 'pending', '', ?, ?)`, uri, url, acquisitionTime(), acquisitionTime())
			if insertErr != nil {
				return AcquisitionWork{}, insertErr
			}
			active.ID, err = result.LastInsertId()
		}
		if err != nil {
			return AcquisitionWork{}, fmt.Errorf("queue selected YouTube source: %w", err)
		}
	}
	now := acquisitionTime()
	if _, err := tx.ExecContext(ctx, `INSERT INTO manual_youtube_mappings(track_uri,video_id,provenance,created_at,updated_at) VALUES (?, ?, 'manual', ?, ?) ON CONFLICT(track_uri) DO UPDATE SET video_id=excluded.video_id, updated_at=excluded.updated_at`, uri, videoID, now, now); err != nil {
		return AcquisitionWork{}, fmt.Errorf("save selected mapping: %w", err)
	}
	work, err := acquisition(ctx, tx, active.ID)
	if err != nil {
		return AcquisitionWork{}, err
	}
	if err := tx.Commit(); err != nil {
		return AcquisitionWork{}, err
	}
	return work, nil
}
