package main

import (
	"encoding/json"
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
	data, err := json.MarshalIndent(resp.Result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat spotify sync: invalid daemon snapshot: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Spotify snapshot:\n%s\n", data)
	return 0
}
