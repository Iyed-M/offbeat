package app

import (
	"context"
	"fmt"

	"github.com/Iyed-M/offbeat/internal/ipc"
)

func (d *Daemon) handleMissing(ctx context.Context, page *ipc.MissingRequest) (any, error) {
	d.managedMu.Lock()
	defer d.managedMu.Unlock()
	if d.DB == nil || d.managedFiles == nil {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "managed library not ready")
	}
	after := ""
	if page != nil {
		after = page.AfterURI
	}
	const pageSize = 128
	tracks, metadata, err := d.DB.DesiredManagedTrackPage(ctx, after, pageSize)
	if err != nil {
		d.Logger.Error("read managed tracks", "err", err)
		return nil, ipc.NewError(ipc.CodeInternal, "could not read managed tracks")
	}
	if page != nil && metadata.Revision != page.StateRevision {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "Desired Spotify state changed while listing missing tracks; run offbeat missing again")
	}
	result := ipc.MissingResult{StateRevision: metadata.Revision, Tracks: []ipc.MissingTrack{}}
	// Reserve half the frame for the envelope/cursor. Track metadata can be
	// unusually large, so bound encoded bytes as well as the number of tracks.
	bytesUsed := 0
	for i, managedTrack := range tracks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		track := managedTrack.Track
		if d.managedFiles.Available(track.URI, managedTrack.RelativePath) {
			result.AvailableCount++
		} else {
			item := ipc.MissingTrack{URI: track.URI, Name: track.Name, Artists: []string{}}
			for _, artist := range track.Artists {
				item.Artists = append(item.Artists, artist.Name)
			}
			encoded, err := ipc.Encode(item)
			if err != nil {
				return nil, err
			}
			if bytesUsed+len(encoded)+1 > ipc.MaxMessageBytes/2 {
				if result.DesiredCount == 0 {
					return nil, ipc.NewError(ipc.CodeInternal, "one missing track exceeds the Control protocol metadata limit")
				}
				break
			}
			bytesUsed += len(encoded) + 1
			result.Tracks = append(result.Tracks, item)
		}
		result.DesiredCount++
		result.NextAfterURI = track.URI
		if i == len(tracks)-1 && len(tracks) < pageSize {
			result.NextAfterURI = ""
		}
	}
	encoded, err := ipc.Encode(ipc.Response{Version: ipc.ProtocolVersion, Result: result})
	if err != nil {
		return nil, err
	}
	if len(encoded) > ipc.MaxMessageBytes {
		return nil, ipc.NewError(ipc.CodeInternal, "missing track page exceeds the Control protocol response limit")
	}
	return result, nil
}

// RegisterSyntheticTrackFixture is an opt-in in-process acceptance-test seam.
// The daemon generates controlled audio; neither arbitrary paths nor external
// audio can be supplied. The returned path is relative to the managed root.
func (d *Daemon) RegisterSyntheticTrackFixture(ctx context.Context, uri string) (string, error) {
	if !d.syntheticFixtures {
		return "", fmt.Errorf("synthetic track fixtures are disabled")
	}
	d.managedMu.Lock()
	defer d.managedMu.Unlock()
	if d.DB == nil || d.managedFiles == nil {
		return "", fmt.Errorf("managed library not ready")
	}
	desired, err := d.DB.IsDesiredTrack(ctx, uri)
	if err != nil {
		return "", err
	}
	if !desired {
		return "", fmt.Errorf("track is not currently desired")
	}
	path, err := d.managedFiles.PublishSynthetic(uri)
	if err != nil {
		return "", err
	}
	if err := d.DB.RegisterManagedTrack(ctx, uri, path); err != nil {
		return "", err
	}
	return path, nil
}
