package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func runMissing(configPath, homeDir string) int {
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat missing: config: %v\n", err)
		return 1
	}
	result, err := collectMissing(app.SocketPath(bootstrap.SocketDir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat missing: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Missing tracks: %d (%d available of %d desired).\n", len(result.Tracks), result.AvailableCount, result.DesiredCount)
	for _, track := range result.Tracks {
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", singleLine(track.URI), singleLine(track.Name), singleLine(strings.Join(track.Artists, ", ")))
	}
	return 0
}

func collectMissing(socket string) (ipc.MissingResult, error) {
	result := ipc.MissingResult{Tracks: []ipc.MissingTrack{}}
	var continuation *ipc.MissingRequest
	for {
		resp, err := requestControlMessage(socket, ipc.Request{Version: ipc.ProtocolVersion, Command: "missing", Missing: continuation}, controlReadTimeout)
		if err != nil {
			return ipc.MissingResult{}, fmt.Errorf("daemon-unavailable: %w", err)
		}
		if resp.Error != nil {
			return ipc.MissingResult{}, fmt.Errorf("daemon error: %s: %s", resp.Error.Code, resp.Error.Message)
		}
		if resp.Version != ipc.ProtocolVersion {
			return ipc.MissingResult{}, fmt.Errorf("unexpected daemon reply: unsupported protocol version %d", resp.Version)
		}
		page, err := decodeMissingResult(resp.Result)
		if err != nil {
			return ipc.MissingResult{}, fmt.Errorf("unexpected daemon reply: %w", err)
		}
		after := ""
		if continuation != nil {
			after = continuation.AfterURI
			if page.StateRevision != continuation.StateRevision {
				return ipc.MissingResult{}, fmt.Errorf("unexpected daemon reply: state revision changed")
			}
		}
		if (page.NextAfterURI != "" && (page.NextAfterURI <= after || page.DesiredCount == 0)) || (len(page.Tracks) > 0 && (page.Tracks[0].URI <= after || (page.NextAfterURI != "" && page.Tracks[len(page.Tracks)-1].URI > page.NextAfterURI))) {
			return ipc.MissingResult{}, fmt.Errorf("unexpected daemon reply: invalid missing continuation")
		}
		result.DesiredCount += page.DesiredCount
		result.AvailableCount += page.AvailableCount
		result.Tracks = append(result.Tracks, page.Tracks...)
		result.StateRevision = page.StateRevision
		if page.NextAfterURI == "" {
			return result, nil
		}
		continuation = &ipc.MissingRequest{AfterURI: page.NextAfterURI, StateRevision: page.StateRevision}
	}
}

func singleLine(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, value)
}

func decodeMissingResult(result any) (ipc.MissingResult, error) {
	raw, err := ipc.Encode(result)
	if err != nil {
		return ipc.MissingResult{}, err
	}
	var decoded ipc.MissingResult
	if err := ipc.Decode(raw, &decoded); err != nil {
		return ipc.MissingResult{}, err
	}
	if decoded.Tracks == nil || decoded.StateRevision < 0 || decoded.AvailableCount < 0 || decoded.DesiredCount != decoded.AvailableCount+len(decoded.Tracks) {
		return ipc.MissingResult{}, fmt.Errorf("invalid missing result")
	}
	for i, track := range decoded.Tracks {
		if track.URI == "" || track.Name == "" || len(track.Artists) == 0 || (i > 0 && decoded.Tracks[i-1].URI >= track.URI) {
			return ipc.MissingResult{}, fmt.Errorf("invalid missing track")
		}
	}
	return decoded, nil
}
