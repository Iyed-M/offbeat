package app

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/Iyed-M/offbeat/internal/artwork"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/Iyed-M/offbeat/internal/managed"
	"github.com/Iyed-M/offbeat/internal/tagging"
)

func (d *Daemon) handleMetadataRefresh(ctx context.Context) (any, error) {
	d.managedMu.Lock()
	if d.DB == nil || d.managedFiles == nil {
		d.managedMu.Unlock()
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "managed library not ready")
	}
	changedFiles := false
	defer func() {
		d.managedMu.Unlock()
		if changedFiles {
			d.metadataPlaylistPending.Store(true)
		}
		if changedFiles || d.metadataPlaylistPending.Load() {
			// A disconnected client may interrupt a batch after a file was
			// published. Give derived playlists a bounded chance to catch up;
			// failure never rolls back the authoritative file/DB commit.
			projectionCtx := ctx
			if ctx.Err() != nil {
				var cancel context.CancelFunc
				projectionCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
			}
			if d.reconcilePlaylistsAfterCommit(projectionCtx, "metadata refresh") {
				d.metadataPlaylistPending.Store(false)
			}
		}
	}()
	tracks, err := d.DB.DesiredManagedTracks(ctx)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeInternal, "could not read managed tracks")
	}
	result := ipc.MetadataRefreshResult{Diagnostics: []ipc.MetadataDiagnostic{}}
	for _, item := range tracks {
		if ctx.Err() != nil {
			return nil, ipc.NewError(ipc.CodeInternal, "metadata refresh interrupted; repeat to retry remaining tracks")
		}
		result.Considered++
		if !d.managedFiles.Available(item.Track.URI, item.RelativePath) {
			result.Missing++
			continue
		}
		if item.Track.AlbumArtist == "" || item.Track.TrackNumber == nil || item.Track.DiscNumber == nil || item.Track.ReleaseDate == "" || item.Track.ArtworkURL == "" {
			result.MissingOptional++
		}
		changed, reason := d.refreshTrack(ctx, item)
		// An intent may describe a file exchanged before a previous DB commit;
		// reconciling that record can also leave the prior playlist stale.
		if changed || item.RefreshIntent != "" {
			changedFiles = true
		}
		if changed {
			result.Changed++
		}
		if reason != "" {
			result.Failed++
			if changed {
				result.Partial++
			}
			if len(result.Diagnostics) < 32 {
				result.Diagnostics = append(result.Diagnostics, ipc.MetadataDiagnostic{TrackURI: item.Track.URI, Reason: reason})
			} else {
				result.Omitted++
			}
		} else if !changed {
			result.Skipped++
		}
	}
	return result, nil
}

func (d *Daemon) refreshTrack(ctx context.Context, item db.ManagedTrack) (bool, string) {
	uri, name := item.Track.URI, item.RelativePath
	file, err := d.managedFiles.OpenManaged(uri, name)
	if err != nil {
		return false, "managed audio cannot be opened safely"
	}
	defer file.Close()
	sha, err := managed.Digest(ctx, file)
	if err != nil {
		return false, "managed audio cannot be inspected safely"
	}
	if item.RefreshIntent != "" {
		var intent db.RefreshIntent
		if json.Unmarshal([]byte(item.RefreshIntent), &intent) != nil || len(intent.SHA256) != 64 {
			return false, "invalid pending replacement; inspect managed file"
		}
		if sha == intent.SHA256 {
			if err := d.managedFiles.ReconcileReplacement(ctx, uri, name, intent.Temporary, intent.PreviousSHA); err != nil {
				return false, "interrupted replacement displaced a changed file; inspect managed audio"
			}
			if err := d.DB.FinishMetadataReplacement(ctx, uri, name, intent); err != nil {
				return false, "could not reconcile published metadata; retry"
			}
			item.TagState, item.ArtworkState = intent.TagState, intent.ArtworkState
			item.TagFingerprint, item.ArtworkFingerprint, item.FileSHA256 = intent.TagFingerprint, intent.ArtworkFingerprint, intent.SHA256
			item.RefreshIntent = ""
		} else if sha != intent.PreviousSHA {
			return false, "managed file changed during interrupted replacement; inspect before retry"
		} else if err := d.managedFiles.DiscardUnpublishedReplacement(ctx, uri, name, intent.Temporary, intent.PreviousSHA, intent.SHA256); err != nil {
			return false, "pending replacement contains unexpected audio; inspect before retry"
		}
	}
	if item.FileSHA256 != "" && sha != item.FileSHA256 {
		return false, "managed file changed outside Offbeat; inspect before retry"
	}
	textHash, artHash := db.PresentationFingerprint(item.Track)
	ext := strings.TrimPrefix(path.Ext(name), ".")
	unsupported := ext == "wav" || ext == "aac"
	textCurrent := item.TagFingerprint == textHash && (item.TagState == "tagged" || item.TagState == "unsupported")
	artCurrent := item.ArtworkFingerprint == artHash && (item.ArtworkState == "embedded" || item.ArtworkState == "unavailable" || item.ArtworkState == "unsupported")
	if textCurrent && artCurrent {
		return false, ""
	}
	tagState, tagError, artState, artError := item.TagState, "", item.ArtworkState, ""
	if unsupported {
		tagState, tagError, artState, artError = tagging.Unsupported, "native text tags unavailable for format", "unsupported", ""
		if err := d.DB.RecordMetadataOutcome(ctx, uri, name, tagState, tagError, artState, artError, textHash, artHash, sha); err != nil {
			return false, "could not record unsupported format"
		}
		return false, "unsupported format (WAV/raw AAC); playable audio retained"
	}
	var picture *artwork.Image
	if item.Track.ArtworkURL == "" {
		artState = "unavailable"
	} else if !artCurrent {
		cover, fetchErr := d.artworkFetcher.Get(ctx, item.Track.Album.URI, item.Track.ArtworkURL)
		if fetchErr != nil {
			artState, artError = "failed", "artwork fetch or validation failed; retry metadata refresh"
		} else {
			picture = &cover
		}
	}
	if ctx.Err() != nil {
		return false, "metadata refresh interrupted; retry"
	}
	clearPicture := item.Track.ArtworkURL == "" && !artCurrent
	if textCurrent && picture == nil && !clearPicture {
		if err := d.DB.RecordMetadataOutcome(ctx, uri, name, tagState, tagError, artState, artError, textHash, artHash, sha); err != nil {
			return false, "could not record artwork outcome"
		}
		if artState == "failed" {
			return false, artError
		}
		return false, ""
	}
	tags := tagging.StageRefresh(ctx, file, ext, item.Track, d.Cfg.Downloader.FFmpegPath, d.Cfg.Downloader.FFprobePath, picture, clearPicture)
	if picture != nil && tags.State != tagging.Tagged && ctx.Err() == nil {
		tags.Close()
		artState, artError = "failed", "artwork embedding failed; retry metadata refresh"
		tags = tagging.StageRefresh(ctx, file, ext, item.Track, d.Cfg.Downloader.FFmpegPath, d.Cfg.Downloader.FFprobePath, nil, false)
	}
	defer tags.Close()
	if ctx.Err() != nil {
		return false, "metadata refresh interrupted; retry"
	}
	if tags.State != tagging.Tagged {
		if err := d.DB.RecordMetadataOutcome(ctx, uri, name, tagging.Failed, tags.Error, artState, artError, "", artHash, sha); err != nil {
			return false, "could not record tagging failure"
		}
		return false, tags.Error
	}
	if picture != nil {
		artState = "embedded"
	}
	newSHA, err := managed.Digest(ctx, tags.File)
	if err != nil {
		return false, "could not inspect staged tags"
	}
	intent := db.RefreshIntent{SHA256: newSHA, PreviousSHA: sha, Temporary: managed.NewRefreshTemporary(), TagFingerprint: textHash, ArtworkFingerprint: artHash, TagState: tagging.Tagged, ArtworkState: artState, ArtworkError: artError}
	if err := d.DB.BeginMetadataReplacement(ctx, uri, name, item.FileSHA256, intent); err != nil {
		return false, "could not prepare metadata replacement"
	}
	if err := d.managedFiles.ReplaceManaged(ctx, uri, name, intent.Temporary, file, tags.File, sha, newSHA); err != nil {
		return false, "managed file changed or replacement interrupted; retry metadata refresh"
	}
	if err := d.DB.FinishMetadataReplacement(ctx, uri, name, intent); err != nil {
		return true, "metadata published but bookkeeping interrupted; retry to reconcile"
	}
	if artState == "failed" {
		return true, artError
	}
	return true, ""
}
