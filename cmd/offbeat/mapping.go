package main

import (
	"fmt"
	"os"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func runAcquireMapping(configPath, homeDir string, args []string) int {
	const usage = "offbeat: usage: offbeat acquire mapping set <spotify-track-uri> <youtube-video-id> | show <spotify-track-uri> | list | remove <spotify-track-uri>"
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	command := "acquire.mapping." + args[0]
	req := ipc.Request{Version: ipc.ProtocolVersion, Command: command}
	switch {
	case command == "acquire.mapping.set" && len(args) == 3:
		if err := ipc.ValidateYouTubeVideoID(args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		req.ManualMapping = &ipc.ManualMappingRequest{TrackURI: args[1], VideoID: args[2]}
	case (command == "acquire.mapping.show" || command == "acquire.mapping.remove") && len(args) == 2:
		req.ManualMapping = &ipc.ManualMappingRequest{TrackURI: args[1]}
	case command == "acquire.mapping.list" && len(args) == 1:
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if req.ManualMapping != nil {
		if err := ipc.ValidateManualMappingTrackURI(req.ManualMapping.TrackURI); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire mapping: config: %v\n", err)
		return 1
	}
	socket := app.SocketPath(bootstrap.SocketDir)
	for {
		resp, err := requestControlMessage(socket, req, controlReadTimeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "offbeat acquire mapping: daemon-unavailable: %v\n", err)
			return 1
		}
		if resp.Error != nil {
			fmt.Fprintf(os.Stderr, "offbeat acquire mapping: daemon error: %s: %s\n", resp.Error.Code, resp.Error.Message)
			return 1
		}
		if resp.Version != ipc.ProtocolVersion {
			fmt.Fprintln(os.Stderr, "offbeat acquire mapping: unexpected daemon reply")
			return 1
		}
		raw, err := ipc.Encode(resp.Result)
		if err != nil {
			return 1
		}
		if command == "acquire.mapping.remove" {
			var removed ipc.ManualMappingRequest
			if ipc.Decode(raw, &removed) != nil || removed.TrackURI != req.ManualMapping.TrackURI {
				fmt.Fprintln(os.Stderr, "offbeat acquire mapping: unexpected daemon reply")
				return 1
			}
			fmt.Fprintf(os.Stdout, "Removed mapping for %s.\n", removed.TrackURI)
			return 0
		}
		if command != "acquire.mapping.list" {
			var item ipc.ManualMappingResult
			if ipc.Decode(raw, &item) != nil || !validMapping(item) || item.TrackURI != req.ManualMapping.TrackURI {
				fmt.Fprintln(os.Stderr, "offbeat acquire mapping: unexpected daemon reply")
				return 1
			}
			printMapping(item)
			return 0
		}
		var page ipc.ManualMappingPage
		if ipc.Decode(raw, &page) != nil || len(page.Mappings) > 128 {
			fmt.Fprintln(os.Stderr, "offbeat acquire mapping: unexpected daemon reply")
			return 1
		}
		for _, item := range page.Mappings {
			if !validMapping(item) {
				fmt.Fprintln(os.Stderr, "offbeat acquire mapping: unexpected daemon reply")
				return 1
			}
			printMapping(item)
		}
		if page.NextAfterURI == "" {
			return 0
		}
		if len(page.Mappings) == 0 || page.NextAfterURI != page.Mappings[len(page.Mappings)-1].TrackURI || (req.ManualMappingList != nil && page.NextAfterURI <= req.ManualMappingList.AfterURI) {
			fmt.Fprintln(os.Stderr, "offbeat acquire mapping: unexpected daemon reply")
			return 1
		}
		req.ManualMappingList = &ipc.ManualMappingListRequest{AfterURI: page.NextAfterURI}
	}
}

func validMapping(m ipc.ManualMappingResult) bool {
	return ipc.ValidateManualMappingTrackURI(m.TrackURI) == nil && ipc.ValidateYouTubeVideoID(m.VideoID) == nil && m.Provenance == "manual" && m.CreatedAt != "" && m.UpdatedAt != ""
}

func printMapping(m ipc.ManualMappingResult) {
	fmt.Fprintf(os.Stdout, "%s -> %s (manual; created %s; updated %s)\n", m.TrackURI, m.VideoID, m.CreatedAt, m.UpdatedAt)
	if m.WorkError != "" {
		fmt.Fprintf(os.Stdout, "Acquisition %s: %s\n", m.WorkState, m.WorkError)
	}
}
