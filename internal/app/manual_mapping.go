package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func mappingResult(m db.ManualYouTubeMapping) ipc.ManualMappingResult {
	return ipc.ManualMappingResult{TrackURI: m.TrackURI, VideoID: m.VideoID, Provenance: m.Provenance,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, WorkState: m.WorkState, WorkError: m.WorkError}
}

func (d *Daemon) handleManualMapping(ctx context.Context, req ipc.Request) (any, error) {
	d.managedMu.Lock()
	defer d.managedMu.Unlock()
	if d.DB == nil {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "database not ready")
	}
	var m db.ManualYouTubeMapping
	var err error
	switch req.Command {
	case "acquire.mapping.set", "acquire.mapping.show", "acquire.mapping.remove":
		if req.ManualMapping == nil {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, "mapping requires track_uri")
		}
		uri := req.ManualMapping.TrackURI
		if err := ipc.ValidateManualMappingTrackURI(uri); err != nil {
			return nil, ipc.NewError(ipc.CodeInvalidRequest, err.Error())
		}
		if req.ManualMapping.ExpectedVideoID != "" {
			if req.Command == "acquire.mapping.show" || ipc.ValidateYouTubeVideoID(req.ManualMapping.ExpectedVideoID) != nil {
				return nil, ipc.NewError(ipc.CodeInvalidRequest, "invalid expected video ID")
			}
			current, lookupErr := d.DB.ManualYouTubeMapping(ctx, uri)
			if errors.Is(lookupErr, sql.ErrNoRows) || (lookupErr == nil && current.VideoID != req.ManualMapping.ExpectedVideoID) {
				return nil, ipc.NewError(ipc.CodeFailedPrecondition, "mapping changed; refresh mapping")
			}
			if lookupErr != nil {
				return nil, ipc.NewError(ipc.CodeInternal, "could not read manual mapping")
			}
		}
		switch req.Command {
		case "acquire.mapping.set":
			if err := ipc.ValidateYouTubeVideoID(req.ManualMapping.VideoID); err != nil {
				return nil, ipc.NewError(ipc.CodeInvalidRequest, err.Error())
			}
			m, err = d.DB.SetManualYouTubeMapping(ctx, uri, req.ManualMapping.VideoID)
		case "acquire.mapping.show":
			if req.ManualMapping.VideoID != "" {
				return nil, ipc.NewError(ipc.CodeInvalidRequest, "video_id is only valid for mapping set")
			}
			m, err = d.DB.ManualYouTubeMapping(ctx, uri)
		case "acquire.mapping.remove":
			if req.ManualMapping.VideoID != "" {
				return nil, ipc.NewError(ipc.CodeInvalidRequest, "video_id is only valid for mapping set")
			}
			err = d.DB.RemoveManualYouTubeMapping(ctx, uri)
			if err == nil {
				return ipc.ManualMappingRequest{TrackURI: uri}, nil
			}
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ipc.NewError(ipc.CodeFailedPrecondition, "manual mapping not found")
		}
		if err != nil {
			return nil, ipc.NewError(ipc.CodeInternal, "could not update or read manual mapping")
		}
		return mappingResult(m), nil
	case "acquire.mapping.list":
		after := ""
		if req.ManualMappingList != nil {
			after = req.ManualMappingList.AfterURI
			if err := ipc.ValidateManualMappingTrackURI(after); err != nil {
				return nil, ipc.NewError(ipc.CodeInvalidRequest, err.Error())
			}
		}
		const pageSize = 128
		items, err := d.DB.ManualYouTubeMappingsAfter(ctx, after, pageSize+1)
		if err != nil {
			return nil, ipc.NewError(ipc.CodeInternal, "could not list manual mappings")
		}
		result := ipc.ManualMappingPage{Mappings: []ipc.ManualMappingResult{}}
		if len(items) > pageSize {
			items = items[:pageSize]
			result.NextAfterURI = items[len(items)-1].TrackURI
		}
		for _, item := range items {
			result.Mappings = append(result.Mappings, mappingResult(item))
		}
		encoded, err := ipc.Encode(ipc.Response{Version: ipc.ProtocolVersion, Result: result})
		if err != nil || len(encoded) > ipc.MaxMessageBytes {
			return nil, ipc.NewError(ipc.CodeInternal, "manual mapping page exceeds Control response limit")
		}
		return result, nil
	}
	return nil, ipc.NewError(ipc.CodeInvalidRequest, "unknown manual mapping command")
}
