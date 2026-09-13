package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

// runConfig connects to the daemon over its control socket, requests the
// sanitized effective configuration, and renders it for people.
//
// The local config file is loaded only to locate the socket; its values
// are never rendered. There is no fallback that prints a local file: if
// the daemon owner is unavailable, the command fails concisely with exit 1
// (ADR 0003).
//
// Exit codes:
//   - 0 on success
//   - 1 when the daemon is unreachable or returns a runtime error
//   - 2 on CLI usage errors (handled by main)
func runConfig(configPath, homeDir string) int {
	cfg, err := loadConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat config: config: %v\n", err)
		return 1
	}

	sockPath := app.SocketPath(cfg.Paths.SocketDir)

	result, err := requestConfig(sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat config: daemon-unavailable: %v\n", err)
		return 1
	}

	printConfig(os.Stdout, result)
	return 0
}

// requestConfig opens the Unix socket, writes one config request, reads
// one response, and closes the connection. It does not retry on failure
// (ADR 0007). Failures before the request bytes are fully written on the
// wire are reported as daemon-unavailable; failures after the request was
// sent on the wire are reported as unknown-outcome.
func requestConfig(sockPath string) (ipc.ConfigResult, error) {
	resp, err := requestControl(sockPath, "config")
	if err != nil {
		return ipc.ConfigResult{}, err
	}
	if resp.Error != nil {
		return ipc.ConfigResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return ipc.ConfigResult{}, fmt.Errorf("unexpected daemon reply: %w", err)
	}
	var cfg ipc.ConfigResult
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ipc.ConfigResult{}, fmt.Errorf("unexpected daemon reply: %w", err)
	}
	return cfg, nil
}

// printConfig renders the ConfigResult as human-readable text. Mirrors
// printStatus so both IPC-backed commands speak the same visual dialect.
func printConfig(w io.Writer, c ipc.ConfigResult) {
	fmt.Fprintln(w, "Offbeat configuration")
	fmt.Fprintln(w, "  paths:")
	fmt.Fprintf(w, "    config_dir : %s\n", c.Paths.ConfigDir)
	fmt.Fprintf(w, "    data_dir   : %s\n", c.Paths.DataDir)
	fmt.Fprintf(w, "    state_dir  : %s\n", c.Paths.StateDir)
	fmt.Fprintf(w, "    cache_dir  : %s\n", c.Paths.CacheDir)
	fmt.Fprintf(w, "    music_root : %s\n", c.Paths.MusicRoot)
	fmt.Fprintf(w, "    database   : %s\n", c.Paths.Database)
	fmt.Fprintf(w, "    socket_dir : %s\n", c.Paths.SocketDir)
	fmt.Fprintf(w, "    certs_dir  : %s\n", c.Paths.CertsDir)
	fmt.Fprintf(w, "    log_file   : %s\n", c.Paths.LogFile)
	fmt.Fprintln(w, "  acquisition:")
	fmt.Fprintf(w, "    concurrency : %d\n", c.AcquisitionConcurrency)
	fmt.Fprintln(w, "  downloader:")
	fmt.Fprintf(w, "    yt_dlp_path  : %s\n", c.DownloaderYTDLPPath)
	fmt.Fprintf(w, "    ffmpeg_path  : %s\n", c.DownloaderFFmpegPath)
	fmt.Fprintf(w, "    ffprobe_path : %s\n", c.DownloaderFFprobePath)
}
