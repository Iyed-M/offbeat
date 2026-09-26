package app

import (
	"context"
	"database/sql"
	"errors"

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
	evidence, err := d.verifySelection(c.SelectionReceipt)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, err.Error())
	}
	if evidence.Track.URI != c.TrackURI || evidence.VideoID != c.VideoID {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "selection differs from inspected candidate")
	}
	reason := string(evidence.Reason)
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
	if track.Name != c.ExpectedTitle || track.DurationMS != c.ExpectedDurationMS || track.Album.Name != c.ExpectedAlbum || track.Album.URI != c.ExpectedAlbumURI || len(track.Artists) != len(c.ExpectedArtists) || evidence.Track.Title != track.Name || evidence.Track.DurationMS != track.DurationMS || evidence.Track.Album.Name != track.Album.Name || evidence.Track.Album.URI != track.Album.URI || len(evidence.Track.Artists) != len(track.Artists) {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "Spotify metadata changed; refresh inspection")
	}
	for i, artist := range track.Artists {
		if artist.Name != c.ExpectedArtists[i] || artist.URI != c.ExpectedArtistURIs[i] || evidence.Track.Artists[i].Name != artist.Name || evidence.Track.Artists[i].URI != artist.URI {
			return nil, ipc.NewError(ipc.CodeFailedPrecondition, "Spotify metadata changed; refresh inspection")
		}
	}
	if err := d.requireMissingTrack(ctx, c.TrackURI); err != nil {
		return nil, err
	}
	work, err := d.DB.SelectYouTube(ctx, c.TrackURI, c.VideoID, evidence.Revision)
	if errors.Is(err, db.ErrAcquisitionConflict) {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, err.Error())
	}
	if err != nil {
		return nil, d.acquisitionError(err)
	}
	return acquisitionResult(work), nil
}
