package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func acquisitionResult(work db.AcquisitionWork) ipc.AcquisitionResult {
	return ipc.AcquisitionResult{ID: work.ID, TrackURI: work.TrackURI, State: work.State, Error: work.Error}
}

func (d *Daemon) handleAcquisition(ctx context.Context, req ipc.Request) (any, error) {
	d.managedMu.Lock()
	defer d.managedMu.Unlock()
	if d.DB == nil || d.managedFiles == nil {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "managed library not ready")
	}
	var work db.AcquisitionWork
	var err error
	switch req.Command {
	case "acquire":
		if req.Acquire == nil {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, "acquire requires track_uri and source_url")
		}
		if err := ipc.ValidateAcquisitionTrackURI(req.Acquire.TrackURI); err != nil {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, err.Error())
		}
		if err := ipc.ValidateAcquisitionSource(req.Acquire.SourceURL); err != nil {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, err.Error())
		}
		if err := d.requireMissingTrack(ctx, req.Acquire.TrackURI); err != nil {
			return nil, err
		}
		work, err = d.DB.EnqueueAcquisition(ctx, req.Acquire.TrackURI, req.Acquire.SourceURL)
	case "acquire.status":
		if req.AcquisitionStatus == nil || req.AcquisitionStatus.ID < 1 {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, "status requires acquisition_id")
		}
		work, err = d.DB.Acquisition(ctx, req.AcquisitionStatus.ID)
	case "acquire.retry":
		if req.AcquisitionRetry == nil || req.AcquisitionRetry.ID < 1 {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, "retry requires acquisition_id")
		}
		work, err = d.DB.Acquisition(ctx, req.AcquisitionRetry.ID)
		if err == nil {
			if err := d.requireMissingTrack(ctx, work.TrackURI); err != nil {
				return nil, err
			}
			work, err = d.DB.RetryAcquisition(ctx, work.ID)
		}
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ipc.NewError(ipc.CodeFailedPrecondition, "acquisition not found")
		}
		if errors.Is(err, db.ErrAcquisitionConflict) || errors.Is(err, db.ErrAcquisitionPrecondition) {
			return nil, ipc.NewError(ipc.CodeFailedPrecondition, "acquisition conflicts with existing work or is not eligible for this operation")
		}
		d.Logger.Error("acquisition request failed")
		return nil, ipc.NewError(ipc.CodeInternal, "could not update or read acquisition work")
	}
	return acquisitionResult(work), nil
}

func (d *Daemon) requireMissingTrack(ctx context.Context, uri string) error {
	var path string
	err := d.DB.QueryRowContext(ctx, `SELECT COALESCE(m.relative_path, '') FROM spotify_tracks s LEFT JOIN managed_tracks m ON m.track_uri=s.uri WHERE s.uri=?`, uri).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return ipc.NewError(ipc.CodeFailedPrecondition, "track is not currently desired")
	}
	if err != nil {
		return ipc.NewError(ipc.CodeInternal, "could not read managed track")
	}
	if d.managedFiles.Available(uri, path) {
		return ipc.NewError(ipc.CodeFailedPrecondition, "track already has an available managed file")
	}
	return nil
}

func (d *Daemon) startAcquisitionWork(ctx context.Context) error {
	if err := d.DB.RecoverAcquisitions(ctx); err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	d.acquisitionCancel = cancel
	for range d.Cfg.Acquisition.Concurrency {
		d.acquisitionWorkers.Add(1)
		go func() {
			defer d.acquisitionWorkers.Done()
			// Polling also recovers from a missed wakeup without a second queue in memory.
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				if workerCtx.Err() != nil {
					return
				}
				work, err := d.DB.ClaimAcquisition(workerCtx)
				if err == nil {
					d.runAcquisition(workerCtx, work)
					continue
				}
				if !errors.Is(err, sql.ErrNoRows) && workerCtx.Err() == nil {
					d.Logger.Error("could not claim acquisition work")
				}
				select {
				case <-workerCtx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	return nil
}

func (d *Daemon) stopAcquisitionWork() {
	if d.acquisitionCancel != nil {
		d.acquisitionCancel()
		d.acquisitionWorkers.Wait()
		d.acquisitionCancel = nil
	}
}

func (d *Daemon) runAcquisition(ctx context.Context, work db.AcquisitionWork) {
	fail := func(message string) {
		// Shutdown leaves running work durable for startup recovery.
		if ctx.Err() != nil {
			return
		}
		if err := d.DB.FailAcquisition(ctx, work.ID, message); err != nil {
			d.Logger.Error("could not record acquisition failure", "acquisition_id", work.ID)
		}
	}
	d.managedMu.Lock()
	err := d.requireMissingTrack(ctx, work.TrackURI)
	d.managedMu.Unlock()
	if err != nil {
		fail("track is no longer desired or missing")
		return
	}
	media, err := d.retriever.Retrieve(ctx, work.SourceURL)
	if err != nil {
		fail("media retrieval failed; check source and configured yt-dlp/FFmpeg tools, then retry")
		return
	}
	if media == nil {
		fail("media retrieval returned no audio")
		return
	}
	defer media.Close()
	if ctx.Err() != nil {
		return
	}
	d.managedMu.Lock()
	defer d.managedMu.Unlock()
	if err := d.requireMissingTrack(ctx, work.TrackURI); err != nil {
		fail("track is no longer desired or missing")
		return
	}
	path, err := d.managedFiles.Publish(work.TrackURI, media.File, media.Extension)
	if err != nil {
		fail("could not publish managed audio")
		return
	}
	if err := d.DB.CompleteAcquisition(ctx, work.ID, path); err != nil {
		fail("could not commit managed audio; retry acquisition")
		return
	}
}
