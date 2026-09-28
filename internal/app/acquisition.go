package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/artwork"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/Iyed-M/offbeat/internal/managed"
	"github.com/Iyed-M/offbeat/internal/tagging"
)

func acquisitionResult(work db.AcquisitionWork) ipc.AcquisitionResult {
	return ipc.AcquisitionResult{ID: work.ID, TrackURI: work.TrackURI, SourceKind: work.SourceKind, State: work.State, Error: work.Error}
}

const youtubeInspectionTimeout = 2 * time.Minute

func (d *Daemon) handleAcquisitionInspection(ctx context.Context, req ipc.Request) (any, error) {
	if req.AcquisitionInspect == nil {
		return nil, ipc.NewError(ipc.CodeInvalidRequest, "acquire.inspect requires track_uri")
	}
	if err := ipc.ValidateAcquisitionTrackURI(req.AcquisitionInspect.TrackURI); err != nil {
		return nil, ipc.NewError(ipc.CodeInvalidRequest, err.Error())
	}

	// Desired Spotify state changes under managedMu. Copy the small immutable
	// metadata value, then release the mutex before starting the external search.
	d.managedMu.Lock()
	if d.DB == nil || d.inspector == nil {
		d.managedMu.Unlock()
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "YouTube inspection is not ready")
	}
	track, err := d.DB.DesiredTrack(ctx, req.AcquisitionInspect.TrackURI)
	d.managedMu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "track is not currently desired")
	}
	if err != nil {
		return nil, ipc.NewError(ipc.CodeInternal, "could not read desired track")
	}

	searchCtx, cancel := context.WithTimeout(ctx, youtubeInspectionTimeout)
	defer cancel()
	report, err := d.inspector.Inspect(searchCtx, track)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, ipc.NewError(ipc.CodeInternal, "YouTube inspection timed out")
		}
		if errors.Is(err, context.Canceled) {
			return nil, ipc.NewError(ipc.CodeInternal, "YouTube inspection canceled")
		}
		return nil, ipc.NewError(ipc.CodeInternal, "YouTube inspection failed: "+err.Error())
	}
	if report.Producer == nil {
		report.Producer = &acquisition.InspectionProducer{}
	}
	report.Producer.DecisionTool = acquisition.CaptureTool{Name: "offbeatd", Version: d.version}
	report.Producer.Environment = acquisition.CaptureEnvironment{OS: runtime.GOOS, Architecture: runtime.GOARCH}
	if report.ReportVersion != acquisition.ResolutionInspectionVersion || !report.FreshSearch || report.Track.URI != track.URI || len(report.Search.RawResults) > acquisition.MaxYouTubeSearchCandidates || len(report.Candidates) > acquisition.MaxYouTubeSearchCandidates || report.Track.Title != track.Name || report.Track.DurationMS != track.DurationMS || report.Track.Album.Name != track.Album.Name || report.Track.Album.URI != track.Album.URI || len(report.Track.Artists) != len(track.Artists) {
		return nil, ipc.NewError(ipc.CodeInternal, "invalid YouTube inspection")
	}
	for i, artist := range track.Artists {
		if report.Track.Artists[i].Name != artist.Name || report.Track.Artists[i].URI != artist.URI {
			return nil, ipc.NewError(ipc.CodeInternal, "invalid YouTube inspection")
		}
	}
	// Read the mapping state after the search. A concurrent edit after this
	// snapshot is rejected by the transactional revision check at selection.
	d.managedMu.Lock()
	revision, err := d.DB.MappingRevision(ctx, track.URI)
	d.managedMu.Unlock()
	if err != nil {
		return nil, ipc.NewError(ipc.CodeInternal, "could not read mapping state")
	}
	for i := range report.Candidates {
		candidate := &report.Candidates[i]
		if ipc.ValidateYouTubeVideoID(candidate.VideoID) != nil || (candidate.Eligible && candidate.RejectionReason != "") || (!candidate.Eligible && candidate.RejectionReason == "") {
			continue
		}
		// Only sign IDs actually present in the bounded raw observation.
		found := false
		for _, raw := range report.Search.RawResults {
			if raw.ID == candidate.VideoID {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		candidate.SelectionReceipt, err = d.signSelection(selectionEvidence{Track: report.Track, VideoID: candidate.VideoID, Reason: candidate.RejectionReason, Revision: revision, Issued: time.Now().Unix()})
		if err != nil {
			return nil, ipc.NewError(ipc.CodeInternal, "could not issue inspection receipt")
		}
	}
	encoded, err := ipc.Encode(ipc.Response{Version: ipc.ProtocolVersion, Result: report})
	if err != nil || len(encoded)+1 > ipc.MaxMessageBytes {
		return nil, ipc.NewError(ipc.CodeInternal, "YouTube inspection report exceeds the Control protocol response limit")
	}
	return report, nil
}

func (d *Daemon) handleAcquisition(ctx context.Context, req ipc.Request) (any, error) {
	d.managedMu.Lock()
	defer d.managedMu.Unlock()
	if d.DB == nil || d.managedFiles == nil {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "managed library not ready")
	}
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
		work, err := d.DB.EnqueueAcquisition(ctx, req.Acquire.TrackURI, req.Acquire.SourceURL)
		if err != nil {
			return nil, d.acquisitionError(err)
		}
		return acquisitionResult(work), nil
	case "acquire.missing":
		missing, available, err := d.currentMissingTrackURIs(ctx)
		if err != nil {
			return nil, err
		}
		batch, err := d.DB.EnqueueMissingAcquisitions(ctx, missing)
		if err != nil {
			return nil, d.acquisitionError(err)
		}
		return ipc.AcquisitionBatchResult{Considered: batch.Considered, Queued: batch.Queued, SkippedActive: batch.SkippedActive, SkippedAttempted: batch.SkippedAttempted, Available: available}, nil
	case "acquire.status":
		if req.AcquisitionStatus == nil {
			counts, err := d.DB.AcquisitionCounts(ctx)
			if err != nil {
				return nil, d.acquisitionError(err)
			}
			after := int64(0)
			if req.AcquisitionList != nil {
				after = req.AcquisitionList.AfterID
			}
			const pageSize = 128
			work, err := d.DB.AcquisitionsAfter(ctx, after, pageSize+1)
			if err != nil {
				return nil, d.acquisitionError(err)
			}
			result := ipc.AcquisitionStatusResult{Counts: ipc.AcquisitionCountsResult{Pending: counts.Pending, Running: counts.Running, Unresolved: counts.Unresolved, Failed: counts.Failed, Complete: counts.Complete}, Work: []ipc.AcquisitionResult{}}
			if len(work) > pageSize {
				result.NextAfterID = work[pageSize-1].ID
				work = work[:pageSize]
			}
			for _, item := range work {
				result.Work = append(result.Work, acquisitionResult(item))
			}
			encoded, err := ipc.Encode(ipc.Response{Version: ipc.ProtocolVersion, Result: result})
			if err != nil || len(encoded) > ipc.MaxMessageBytes {
				return nil, ipc.NewError(ipc.CodeInternal, "acquisition status page exceeds the Control protocol response limit")
			}
			return result, nil
		}
		if req.AcquisitionStatus.ID < 1 {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, "status requires a positive acquisition_id")
		}
		work, err := d.DB.Acquisition(ctx, req.AcquisitionStatus.ID)
		if err != nil {
			return nil, d.acquisitionError(err)
		}
		return acquisitionResult(work), nil
	case "acquire.retry":
		if req.AcquisitionRetry == nil || req.AcquisitionRetry.ID < 1 {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, "retry requires acquisition_id")
		}
		work, err := d.DB.Acquisition(ctx, req.AcquisitionRetry.ID)
		if err == nil {
			if err := d.requireMissingTrack(ctx, work.TrackURI); err != nil {
				return nil, err
			}
			work, err = d.DB.RetryAcquisition(ctx, work.ID)
		}
		if err != nil {
			return nil, d.acquisitionError(err)
		}
		return acquisitionResult(work), nil
	case "acquire.retry.unresolved":
		missing, _, err := d.currentMissingTrackURIs(ctx)
		if err != nil {
			return nil, err
		}
		batch, err := d.DB.RetryUnresolvedAcquisitions(ctx, missing)
		if err != nil {
			return nil, d.acquisitionError(err)
		}
		return ipc.UnresolvedRetryBatchResult{
			Considered:       batch.Considered,
			Queued:           batch.Queued,
			SkippedActive:    batch.SkippedActive,
			SkippedAvailable: batch.SkippedAvailable,
			SkippedRemoved:   batch.SkippedRemoved,
		}, nil
	}
	return nil, ipc.NewError(ipc.CodeInvalidRequest, "unknown acquisition command")
}

func (d *Daemon) currentMissingTrackURIs(ctx context.Context) ([]string, int, error) {
	tracks, err := d.DB.DesiredManagedTracks(ctx)
	if err != nil {
		return nil, 0, d.acquisitionError(err)
	}
	missing := make([]string, 0, len(tracks))
	available := 0
	for _, track := range tracks {
		if d.managedFiles.Available(track.Track.URI, track.RelativePath) {
			available++
		} else {
			missing = append(missing, track.Track.URI)
		}
	}
	return missing, available, nil
}

func (d *Daemon) acquisitionError(err error) error {
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ipc.NewError(ipc.CodeFailedPrecondition, "acquisition not found")
		}
		if errors.Is(err, db.ErrAcquisitionConflict) || errors.Is(err, db.ErrAcquisitionPrecondition) {
			return ipc.NewError(ipc.CodeFailedPrecondition, "acquisition conflicts with existing work or is not eligible for this operation")
		}
		d.Logger.Error("acquisition request failed")
		return ipc.NewError(ipc.CodeInternal, "could not update or read acquisition work")
	}
	return nil
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
	if err != nil {
		d.managedMu.Unlock()
		fail("track is no longer desired or missing")
		return
	}
	// Publication may have succeeded immediately before the DB commit failed.
	// Recover that exact root-confined orphan before resolving or retrieving a
	// source again. An invalid orphan blocks work rather than being overwritten.
	orphan, extension, orphanErr := d.managedFiles.OpenOrphan(work.TrackURI)
	if orphanErr == nil {
		defer orphan.Close()
		before, statErr := orphan.Stat()
		if statErr != nil {
			d.managedMu.Unlock()
			fail("managed orphan cannot be inspected safely; repair or remove it before retry")
			return
		}
		if err := acquisition.VerifyOrphan(ctx, orphan, extension, d.Cfg.Downloader.FFprobePath, d.Cfg.Downloader.FFmpegPath); err != nil {
			d.managedMu.Unlock()
			if ctx.Err() == nil {
				fail(err.Error())
			}
			return
		}
		if ctx.Err() != nil {
			d.managedMu.Unlock()
			return
		}
		if !d.managedFiles.OrphanUnchanged(work.TrackURI, extension, orphan, before) {
			d.managedMu.Unlock()
			fail("managed orphan changed during verification; retry after repairing it")
			return
		}
		path := managed.TrackPath(work.TrackURI, extension)
		if err := d.DB.CompleteTaggedAcquisition(ctx, work.ID, path, "pending", ""); err != nil {
			d.managedMu.Unlock()
			fail("could not commit recovered managed audio; retry acquisition")
			return
		}
		d.reconcilePlaylistsAfterCommitLocked(ctx, "Managed track")
		d.managedMu.Unlock()
		return
	}
	d.managedMu.Unlock()
	if !errors.Is(orphanErr, os.ErrNotExist) {
		fail("managed orphan cannot be opened safely; repair or remove it before retry")
		return
	}
	if work.SourceKind == db.AcquisitionSourceYouTube && work.SourceURL == "" {
		track, err := d.DB.DesiredTrack(ctx, work.TrackURI)
		if err != nil {
			fail("track is no longer desired")
			return
		}
		mapping, mappingErr := d.DB.ManualYouTubeMapping(ctx, work.TrackURI)
		var selected string
		if mappingErr == nil {
			selected = fmt.Sprintf("https://www.youtube.com/watch?v=%s", mapping.VideoID)
		} else if errors.Is(mappingErr, sql.ErrNoRows) {
			selected, err = d.resolver.Resolve(ctx, track)
		} else {
			fail("could not read manual YouTube mapping")
			return
		}
		if errors.Is(err, acquisition.ErrUnresolved) {
			if ctx.Err() == nil {
				if recordErr := d.DB.UnresolveAcquisition(ctx, work.ID, err.Error()); recordErr != nil {
					d.Logger.Error("could not record unresolved acquisition", "acquisition_id", work.ID)
				}
			}
			return
		}
		if err != nil {
			fail("YouTube resolution failed: " + err.Error())
			return
		}
		if err := d.DB.SetAcquisitionSource(ctx, work.ID, selected); err != nil {
			fail("could not persist selected YouTube source")
			return
		}
		work.SourceURL = selected
	}
	media, err := d.retriever.Retrieve(ctx, work.SourceURL)
	if err != nil {
		fail("media retrieval failed: " + err.Error())
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
	track, err := d.DB.DesiredTrack(ctx, work.TrackURI)
	if err != nil {
		fail("could not read Spotify metadata")
		return
	}
	artState, artError := "unavailable", ""
	var picture *artwork.Image
	if media.Extension == "wav" || media.Extension == "aac" {
		artState = "unsupported"
	} else if track.ArtworkURL != "" {
		artState = "failed"
		cover, fetchErr := acquisitionArtwork.Get(ctx, track.Album.URI, track.ArtworkURL)
		if fetchErr != nil {
			artError = "artwork fetch or validation failed; retry metadata refresh"
		} else {
			picture = &cover
		}
	}
	tags := tagging.StageWithArtwork(ctx, media.File, media.Extension, track, d.Cfg.Downloader.FFmpegPath, d.Cfg.Downloader.FFprobePath, picture)
	if picture != nil {
		if tags.State == tagging.Tagged {
			artState = "embedded"
		} else if ctx.Err() == nil {
			artError = "artwork embedding failed; retry metadata refresh"
			tags.Close()
			tags = tagging.Stage(ctx, media.File, media.Extension, track, d.Cfg.Downloader.FFmpegPath, d.Cfg.Downloader.FFprobePath)
		}
	}
	defer tags.Close()
	if ctx.Err() != nil {
		return
	}
	source := media.File
	if tags.File != nil {
		source = tags.File
	}
	path, err := d.managedFiles.PublishNew(ctx, work.TrackURI, source, media.Extension)
	if err != nil {
		fail("could not publish managed audio")
		return
	}
	if err := d.DB.CompletePresentedAcquisition(ctx, work.ID, path, tags.State, tags.Error, artState, artError); err != nil {
		fail("could not commit managed audio; retry acquisition")
		return
	}
	d.reconcilePlaylistsAfterCommitLocked(ctx, "Managed track")
}

var acquisitionArtwork = artwork.New()
