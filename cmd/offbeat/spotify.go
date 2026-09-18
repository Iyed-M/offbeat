package main

import (
	"fmt"
	"os"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func runSpotifySync(configPath, homeDir string) int {
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat spotify sync: config: %v\n", err)
		return 1
	}
	resp, err := requestControlWithTimeout(app.SocketPath(bootstrap.SocketDir), "spotify.sync", spotifySyncReadTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat spotify sync: daemon-unavailable: %v\n", err)
		return 1
	}
	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "offbeat spotify sync: daemon error: %s: %s\n", resp.Error.Code, resp.Error.Message)
		return 1
	}
	if resp.Version != ipc.ProtocolVersion {
		fmt.Fprintf(os.Stderr, "offbeat spotify sync: unexpected daemon reply: unsupported protocol version %d\n", resp.Version)
		return 1
	}
	result, err := decodeSpotifySyncResult(resp.Result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat spotify sync: unexpected daemon reply: %v\n", err)
		return 1
	}
	status := "committed"
	if !result.Changed {
		status = "unchanged"
	}
	fmt.Fprintf(os.Stdout, "Spotify desired state %s (revision %d): %d playlists, %d playlist entries, %d Liked Songs entries, %d supported entries, %d unsupported entries.\n", status, result.StateRevision, result.PlaylistCount, result.PlaylistEntryCount, result.LikedSongsEntryCount, result.SupportedEntryOccurrences, result.UnsupportedEntryOccurrences)
	return 0
}

func decodeSpotifySyncResult(result any) (ipc.SpotifySyncResult, error) {
	raw, err := ipc.Encode(result)
	if err != nil {
		return ipc.SpotifySyncResult{}, err
	}
	var decoded ipc.SpotifySyncResult
	if err := ipc.Decode(raw, &decoded); err != nil {
		return ipc.SpotifySyncResult{}, err
	}
	if decoded.StateRevision < 1 || decoded.PlaylistCount < 0 || decoded.PlaylistEntryCount < 0 || decoded.LikedSongsEntryCount < 0 || decoded.SupportedEntryOccurrences < 0 || decoded.UnsupportedEntryOccurrences < 0 {
		return ipc.SpotifySyncResult{}, fmt.Errorf("invalid Spotify sync result")
	}
	return decoded, nil
}
