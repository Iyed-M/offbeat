package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	AcquisitionPending       = "pending"
	AcquisitionRunning       = "running"
	AcquisitionUnresolved    = "unresolved"
	AcquisitionFailed        = "failed"
	AcquisitionComplete      = "complete"
	AcquisitionSourceDirect  = "direct"
	AcquisitionSourceYouTube = "youtube"
	maxAcquisitionErrorBytes = 1024
)

var (
	// ErrAcquisitionConflict means another active request already owns a track
	// with a different authorized source.
	ErrAcquisitionConflict = errors.New("acquisition work conflicts with active work for track")
	// ErrAcquisitionPrecondition means the requested transition is not valid in
	// the work's current state, or its track is no longer currently desired.
	ErrAcquisitionPrecondition = errors.New("acquisition work precondition failed")
)

// AcquisitionWork is the restart-safe record for one daemon-owned retrieval.
// Timestamps are stored and returned in RFC3339Nano UTC form.
type AcquisitionWork struct {
	ID         int64
	TrackURI   string
	SourceKind string
	SourceURL  string
	State      string
	Error      string
	CreatedAt  string
	UpdatedAt  string
}

type AcquisitionBatch struct {
	Considered       int
	Queued           int
	SkippedActive    int
	SkippedAttempted int
}

type AcquisitionCounts struct {
	Pending    int `json:"pending"`
	Running    int `json:"running"`
	Unresolved int `json:"unresolved"`
	Failed     int `json:"failed"`
	Complete   int `json:"complete"`
}

// EnqueueAcquisition adds an authorized source request for a currently desired
// supported track. Repeating an active request with the same source is
// idempotent; a different source conflicts until the active work reaches a
// terminal state.
func (d *DB) EnqueueAcquisition(ctx context.Context, trackURI, sourceURL string) (AcquisitionWork, error) {
	if trackURI == "" || sourceURL == "" {
		return AcquisitionWork{}, fmt.Errorf("%w: track URI and source URL are required", ErrAcquisitionPrecondition)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return AcquisitionWork{}, fmt.Errorf("begin enqueue acquisition: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := requireDesiredTrack(ctx, tx, trackURI); err != nil {
		return AcquisitionWork{}, err
	}

	active, err := acquisitionByTrackAndStates(ctx, tx, trackURI, AcquisitionPending, AcquisitionRunning)
	if err == nil {
		if active.SourceURL != sourceURL {
			return AcquisitionWork{}, fmt.Errorf("%w: track %q already has work %d", ErrAcquisitionConflict, trackURI, active.ID)
		}
		if err := tx.Commit(); err != nil {
			return AcquisitionWork{}, fmt.Errorf("commit duplicate acquisition: %w", err)
		}
		committed = true
		return active, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AcquisitionWork{}, err
	}

	now := acquisitionTime()
	result, err := tx.ExecContext(ctx, `INSERT INTO acquisition_work(track_uri, source_kind, source_url, state, error, created_at, updated_at) VALUES (?, ?, ?, ?, '', ?, ?)`, trackURI, AcquisitionSourceDirect, sourceURL, AcquisitionPending, now, now)
	if err != nil {
		return AcquisitionWork{}, fmt.Errorf("insert acquisition work: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AcquisitionWork{}, fmt.Errorf("read acquisition work ID: %w", err)
	}
	work, err := acquisition(ctx, tx, id)
	if err != nil {
		return AcquisitionWork{}, err
	}
	if err := tx.Commit(); err != nil {
		return AcquisitionWork{}, fmt.Errorf("commit acquisition work: %w", err)
	}
	committed = true
	return work, nil
}

// EnqueueMissingAcquisitions atomically creates one built-in YouTube
// resolution request for each supplied Missing track that has never had one.
// Existing active work and prior YouTube outcomes are idempotently skipped.
func (d *DB) EnqueueMissingAcquisitions(ctx context.Context, trackURIs []string) (AcquisitionBatch, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return AcquisitionBatch{}, fmt.Errorf("begin enqueue missing acquisitions: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	result := AcquisitionBatch{Considered: len(trackURIs)}
	for _, trackURI := range trackURIs {
		if err := requireDesiredTrack(ctx, tx, trackURI); err != nil {
			return AcquisitionBatch{}, err
		}
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acquisition_work WHERE track_uri = ? AND state IN (?, ?))`, trackURI, AcquisitionPending, AcquisitionRunning).Scan(&active); err != nil {
			return AcquisitionBatch{}, fmt.Errorf("check active acquisition: %w", err)
		}
		if active {
			result.SkippedActive++
			continue
		}
		var attempted bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acquisition_work WHERE track_uri = ? AND source_kind = ?)`, trackURI, AcquisitionSourceYouTube).Scan(&attempted); err != nil {
			return AcquisitionBatch{}, fmt.Errorf("check previous YouTube acquisition: %w", err)
		}
		if attempted {
			result.SkippedAttempted++
			continue
		}
		now := acquisitionTime()
		if _, err := tx.ExecContext(ctx, `INSERT INTO acquisition_work(track_uri, source_kind, source_url, state, error, created_at, updated_at) VALUES (?, ?, NULL, ?, '', ?, ?)`, trackURI, AcquisitionSourceYouTube, AcquisitionPending, now, now); err != nil {
			return AcquisitionBatch{}, fmt.Errorf("insert YouTube acquisition work: %w", err)
		}
		result.Queued++
	}
	if err := tx.Commit(); err != nil {
		return AcquisitionBatch{}, fmt.Errorf("commit missing acquisitions: %w", err)
	}
	committed = true
	return result, nil
}

// Acquisition reads a persisted acquisition record by its durable ID.
func (d *DB) Acquisition(ctx context.Context, id int64) (AcquisitionWork, error) {
	return acquisition(ctx, d, id)
}

func (d *DB) AcquisitionCounts(ctx context.Context) (AcquisitionCounts, error) {
	var counts AcquisitionCounts
	err := d.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(state = 'pending'), 0), COALESCE(SUM(state = 'running'), 0),
		COALESCE(SUM(state = 'unresolved'), 0), COALESCE(SUM(state = 'failed'), 0),
		COALESCE(SUM(state = 'complete'), 0) FROM acquisition_work`).Scan(
		&counts.Pending, &counts.Running, &counts.Unresolved, &counts.Failed, &counts.Complete)
	if err != nil {
		return AcquisitionCounts{}, fmt.Errorf("count acquisition work: %w", err)
	}
	return counts, nil
}

func (d *DB) AcquisitionsAfter(ctx context.Context, afterID int64, limit int) ([]AcquisitionWork, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, track_uri, source_kind, source_url, state, error, created_at, updated_at FROM acquisition_work WHERE id > ? ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list acquisition work: %w", err)
	}
	defer rows.Close()
	work := make([]AcquisitionWork, 0, limit)
	for rows.Next() {
		var item AcquisitionWork
		var sourceURL sql.NullString
		if err := rows.Scan(&item.ID, &item.TrackURI, &item.SourceKind, &sourceURL, &item.State, &item.Error, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.SourceURL = sourceURL.String
		work = append(work, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return work, nil
}

// ClaimAcquisition atomically changes the oldest pending work to running.
// It returns sql.ErrNoRows when there is no pending work.
func (d *DB) ClaimAcquisition(ctx context.Context) (AcquisitionWork, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return AcquisitionWork{}, fmt.Errorf("begin claim acquisition: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM acquisition_work WHERE state = ? ORDER BY id LIMIT 1`, AcquisitionPending).Scan(&id); err != nil {
		return AcquisitionWork{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE acquisition_work SET state = ?, error = '', updated_at = ? WHERE id = ? AND state = ?`, AcquisitionRunning, acquisitionTime(), id, AcquisitionPending)
	if err != nil {
		return AcquisitionWork{}, fmt.Errorf("claim acquisition work: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return AcquisitionWork{}, err
	}
	if changed != 1 {
		return AcquisitionWork{}, fmt.Errorf("%w: acquisition %d is no longer pending", ErrAcquisitionPrecondition, id)
	}
	work, err := acquisition(ctx, tx, id)
	if err != nil {
		return AcquisitionWork{}, err
	}
	if err := tx.Commit(); err != nil {
		return AcquisitionWork{}, fmt.Errorf("commit claim acquisition: %w", err)
	}
	committed = true
	return work, nil
}

// RecoverAcquisitions returns work interrupted by daemon shutdown or a crash
// to pending, so it can be claimed after restart.
func (d *DB) RecoverAcquisitions(ctx context.Context) error {
	_, err := d.ExecContext(ctx, `UPDATE acquisition_work SET state = ?, error = '', updated_at = ? WHERE state = ?`, AcquisitionPending, acquisitionTime(), AcquisitionRunning)
	if err != nil {
		return fmt.Errorf("recover acquisition work: %w", err)
	}
	return nil
}

// RetryAcquisition makes a failed request pending again. Automatic retry is
// intentionally absent; callers must request this transition explicitly.
func (d *DB) RetryAcquisition(ctx context.Context, id int64) (AcquisitionWork, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return AcquisitionWork{}, fmt.Errorf("begin retry acquisition: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	work, err := acquisition(ctx, tx, id)
	if err != nil {
		return AcquisitionWork{}, err
	}
	if work.State != AcquisitionFailed {
		return AcquisitionWork{}, invalidAcquisitionState(id, work.State, AcquisitionFailed)
	}
	if err := requireDesiredTrack(ctx, tx, work.TrackURI); err != nil {
		return AcquisitionWork{}, err
	}
	active, err := acquisitionByTrackAndStates(ctx, tx, work.TrackURI, AcquisitionPending, AcquisitionRunning)
	if err == nil && active.ID != id {
		return AcquisitionWork{}, fmt.Errorf("%w: track %q already has work %d", ErrAcquisitionConflict, work.TrackURI, active.ID)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AcquisitionWork{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE acquisition_work SET state = ?, error = '', updated_at = ? WHERE id = ?`, AcquisitionPending, acquisitionTime(), id); err != nil {
		return AcquisitionWork{}, fmt.Errorf("retry acquisition work: %w", err)
	}
	work, err = acquisition(ctx, tx, id)
	if err != nil {
		return AcquisitionWork{}, err
	}
	if err := tx.Commit(); err != nil {
		return AcquisitionWork{}, fmt.Errorf("commit retry acquisition: %w", err)
	}
	committed = true
	return work, nil
}

// FailAcquisition records a tool failure for running work.
func (d *DB) FailAcquisition(ctx context.Context, id int64, message string) error {
	if message == "" {
		return fmt.Errorf("%w: failure message is required", ErrAcquisitionPrecondition)
	}
	if len(message) > maxAcquisitionErrorBytes {
		return fmt.Errorf("%w: failure message exceeds %d bytes", ErrAcquisitionPrecondition, maxAcquisitionErrorBytes)
	}
	return d.transitionAcquisition(ctx, id, AcquisitionRunning, AcquisitionFailed, message)
}

// SetAcquisitionSource persists a resolver-selected URL before retrieval.
func (d *DB) SetAcquisitionSource(ctx context.Context, id int64, sourceURL string) error {
	if sourceURL == "" {
		return fmt.Errorf("%w: selected source URL is required", ErrAcquisitionPrecondition)
	}
	result, err := d.ExecContext(ctx, `UPDATE acquisition_work SET source_url = ?, updated_at = ? WHERE id = ? AND source_kind = ? AND state = ? AND source_url IS NULL`, sourceURL, acquisitionTime(), id, AcquisitionSourceYouTube, AcquisitionRunning)
	if err != nil {
		return fmt.Errorf("persist selected YouTube source: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: acquisition %d cannot accept a selected source", ErrAcquisitionPrecondition, id)
	}
	return nil
}

// UnresolveAcquisition records the expected terminal outcome where no unique
// eligible candidate was found.
func (d *DB) UnresolveAcquisition(ctx context.Context, id int64, message string) error {
	if message == "" || len(message) > maxAcquisitionErrorBytes {
		return fmt.Errorf("%w: unresolved message is invalid", ErrAcquisitionPrecondition)
	}
	return d.transitionAcquisition(ctx, id, AcquisitionRunning, AcquisitionUnresolved, message)
}

// CompleteAcquisition atomically records both the managed-track mapping and
// terminal work state. The track must still be currently desired.
func (d *DB) CompleteAcquisition(ctx context.Context, id int64, relativePath string) error {
	if relativePath == "" {
		return fmt.Errorf("%w: managed relative path is required", ErrAcquisitionPrecondition)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete acquisition: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	work, err := acquisition(ctx, tx, id)
	if err != nil {
		return err
	}
	if work.State != AcquisitionRunning {
		return invalidAcquisitionState(id, work.State, AcquisitionRunning)
	}
	if err := requireDesiredTrack(ctx, tx, work.TrackURI); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO managed_tracks(track_uri, relative_path) SELECT uri, ? FROM spotify_tracks WHERE uri = ? ON CONFLICT(track_uri) DO UPDATE SET relative_path = excluded.relative_path`, relativePath, work.TrackURI)
	if err != nil {
		return fmt.Errorf("register acquired managed track: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: track %q is no longer currently desired", ErrAcquisitionPrecondition, work.TrackURI)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE acquisition_work SET state = ?, error = '', updated_at = ? WHERE id = ?`, AcquisitionComplete, acquisitionTime(), id); err != nil {
		return fmt.Errorf("complete acquisition work: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit complete acquisition: %w", err)
	}
	committed = true
	return nil
}

type acquisitionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func acquisition(ctx context.Context, q acquisitionQuerier, id int64) (AcquisitionWork, error) {
	var work AcquisitionWork
	var sourceURL sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, track_uri, source_kind, source_url, state, error, created_at, updated_at FROM acquisition_work WHERE id = ?`, id).Scan(&work.ID, &work.TrackURI, &work.SourceKind, &sourceURL, &work.State, &work.Error, &work.CreatedAt, &work.UpdatedAt)
	work.SourceURL = sourceURL.String
	if err != nil {
		return AcquisitionWork{}, err
	}
	return work, nil
}

func acquisitionByTrackAndStates(ctx context.Context, q acquisitionQuerier, trackURI string, states ...string) (AcquisitionWork, error) {
	if len(states) != 2 {
		panic("acquisitionByTrackAndStates requires exactly two states")
	}
	var work AcquisitionWork
	var sourceURL sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, track_uri, source_kind, source_url, state, error, created_at, updated_at FROM acquisition_work WHERE track_uri = ? AND state IN (?, ?) ORDER BY id LIMIT 1`, trackURI, states[0], states[1]).Scan(&work.ID, &work.TrackURI, &work.SourceKind, &sourceURL, &work.State, &work.Error, &work.CreatedAt, &work.UpdatedAt)
	work.SourceURL = sourceURL.String
	if err != nil {
		return AcquisitionWork{}, err
	}
	return work, nil
}

func requireDesiredTrack(ctx context.Context, q acquisitionQuerier, trackURI string) error {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spotify_tracks WHERE uri = ?)`, trackURI).Scan(&exists); err != nil {
		return fmt.Errorf("check desired acquisition track: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: track %q is not currently desired", ErrAcquisitionPrecondition, trackURI)
	}
	return nil
}

func (d *DB) transitionAcquisition(ctx context.Context, id int64, from, to, message string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transition acquisition: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	work, err := acquisition(ctx, tx, id)
	if err != nil {
		return err
	}
	if work.State != from {
		return invalidAcquisitionState(id, work.State, from)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE acquisition_work SET state = ?, error = ?, updated_at = ? WHERE id = ?`, to, message, acquisitionTime(), id); err != nil {
		return fmt.Errorf("transition acquisition work: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transition acquisition: %w", err)
	}
	committed = true
	return nil
}

func invalidAcquisitionState(id int64, got, want string) error {
	return fmt.Errorf("%w: acquisition %d is %q, expected %q", ErrAcquisitionPrecondition, id, got, want)
}

func acquisitionTime() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
