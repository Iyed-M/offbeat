package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func (d *Daemon) handleYouTubeSelection(ctx context.Context, req ipc.Request) (any, error) {
	c := req.AcquisitionChoice
	if c == nil {
		return nil, ipc.NewError(ipc.CodeInvalidRequest, "acquire.select requires acquisition_choice")
	}
	if err := ipc.ValidateManualMappingTrackURI(c.TrackURI); err != nil {
		return nil, ipc.NewError(ipc.CodeInvalidRequest, err.Error())
	}
	if err := ipc.ValidateYouTubeVideoID(c.VideoID); err != nil {
		return nil, ipc.NewError(ipc.CodeInvalidRequest, err.Error())
	}
	if c.ExpectedTitle == "" || len(c.ExpectedArtists) == 0 || len(c.ExpectedArtistURIs) != len(c.ExpectedArtists) || c.ExpectedDurationMS <= 0 {
		return nil, ipc.NewError(ipc.CodeInvalidRequest, "selection requires inspected title, artists and duration")
	}
	// The fresh search (including fixture inspectors) is outside managedMu.
	inspection, err := d.handleAcquisitionInspection(ctx, ipc.Request{AcquisitionInspect: &ipc.AcquisitionTrackRequest{TrackURI: c.TrackURI}})
	if err != nil {
		return nil, err
	}
	report := inspection.(acquisition.ResolutionInspection)
	if !report.FreshSearch || report.Track.URI != c.TrackURI {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "inspection is stale; refresh candidates")
	}
	var reason string
	found := false
	for _, candidate := range report.Candidates {
		if candidate.VideoID == c.VideoID {
			if !candidate.Eligible && candidate.RejectionReason == "" {
				return nil, ipc.NewError(ipc.CodeFailedPrecondition, "candidate eligibility is unknown; refresh inspection")
			}
			found = true
			reason = string(candidate.RejectionReason)
			break
		}
	}
	if !found {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "video ID is not in the fresh inspection; refresh candidates")
	}
	if reason != c.RejectionReason {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "candidate eligibility changed; refresh inspection")
	}
	if reason != "" && c.AcknowledgeRejection != reason {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "candidate rejected: "+reason+"; acknowledge this exact rejection reason")
	}
	if reason == "" && c.AcknowledgeRejection != "" {
		return nil, ipc.NewError(ipc.CodeInvalidRequest, "eligible candidate does not require rejection acknowledgment")
	}

	d.managedMu.Lock()
	defer d.managedMu.Unlock()
	if d.DB == nil || d.managedFiles == nil {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "managed library not ready")
	}
	track, err := d.DB.DesiredTrack(ctx, c.TrackURI)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "track is not currently desired; refresh inspection")
	}
	if err != nil {
		return nil, ipc.NewError(ipc.CodeInternal, "could not read desired track")
	}
	if track.Name != c.ExpectedTitle || track.DurationMS != c.ExpectedDurationMS || track.Album.Name != c.ExpectedAlbum || track.Album.URI != c.ExpectedAlbumURI || len(track.Artists) != len(c.ExpectedArtists) || report.Track.Title != track.Name || report.Track.DurationMS != track.DurationMS || report.Track.Album.Name != track.Album.Name || report.Track.Album.URI != track.Album.URI || len(report.Track.Artists) != len(track.Artists) {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "Spotify metadata changed; refresh inspection")
	}
	for i, artist := range track.Artists {
		if artist.Name != c.ExpectedArtists[i] || artist.URI != c.ExpectedArtistURIs[i] || report.Track.Artists[i].Name != artist.Name || report.Track.Artists[i].URI != artist.URI {
			return nil, ipc.NewError(ipc.CodeFailedPrecondition, "Spotify metadata changed; refresh inspection")
		}
	}
	if err := d.requireMissingTrack(ctx, c.TrackURI); err != nil {
		return nil, err
	}
	work, err := d.DB.SelectYouTube(ctx, c.TrackURI, c.VideoID)
	if errors.Is(err, db.ErrAcquisitionConflict) {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, err.Error())
	}
	if err != nil {
		return nil, d.acquisitionError(err)
	}
	return acquisitionResult(work), nil
}
