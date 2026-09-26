package app

import (
	"context"

	"github.com/Iyed-M/offbeat/internal/ipc"
)

const reviewPageSize = 32

func (d *Daemon) handleReviewList(ctx context.Context, req ipc.Request) (any, error) {
	d.managedMu.Lock()
	defer d.managedMu.Unlock()
	if d.DB == nil || d.managedFiles == nil {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "managed library not ready")
	}
	result := ipc.ReviewPage{Tracks: []ipc.ReviewTrack{}}
	after := ""
	if req.ReviewList != nil {
		after = req.ReviewList.AfterURI
		if err := ipc.ValidateManualMappingTrackURI(after); err != nil {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, err.Error())
		}
	}
	for {
		items, paths, err := d.DB.ReviewCandidatesAfter(ctx, after, 128)
		if err != nil {
			return nil, ipc.NewError(ipc.CodeInternal, "could not list review tracks")
		}
		for i, item := range items {
			if d.managedFiles.Available(item.TrackURI, paths[i]) {
				continue
			}
			if len(result.Tracks) == reviewPageSize {
				result.NextAfterURI = result.Tracks[reviewPageSize-1].TrackURI
				return boundedReviewPage(result)
			}
			result.Tracks = append(result.Tracks, item)
		}
		if len(items) < 128 {
			return boundedReviewPage(result)
		}
		after = items[len(items)-1].TrackURI
	}
}

func boundedReviewPage(page ipc.ReviewPage) (any, error) {
	encoded, err := ipc.Encode(ipc.Response{Version: ipc.ProtocolVersion, Result: page})
	if err != nil || len(encoded)+1 > ipc.MaxMessageBytes {
		return nil, ipc.NewError(ipc.CodeInternal, "review page exceeds Control response limit")
	}
	return page, nil
}
