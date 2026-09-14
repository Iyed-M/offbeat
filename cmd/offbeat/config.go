package main

import (
	"bytes"
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
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat config: config: %v\n", err)
		return 1
	}

	sockPath := app.SocketPath(bootstrap.SocketDir)

	resp, err := requestControl(sockPath, "config")
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat config: daemon-unavailable: %v\n", err)
		return 1
	}
	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "offbeat config: daemon error: %s: %s\n",
			resp.Error.Code, resp.Error.Message)
		return 1
	}
	if resp.Version != ipc.ProtocolVersion {
		fmt.Fprintf(os.Stderr, "offbeat config: unexpected daemon reply: unsupported protocol version %d\n", resp.Version)
		return 1
	}

	result, err := decodeConfigResult(resp.Result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat config: unexpected daemon reply: %v\n", err)
		return 1
	}

	printConfig(os.Stdout, result)
	return 0
}

func decodeConfigResult(result any) (ipc.ConfigResult, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return ipc.ConfigResult{}, fmt.Errorf("unexpected daemon reply: %w", err)
	}
	var decoded *ipc.ConfigResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return ipc.ConfigResult{}, fmt.Errorf("unexpected daemon reply: %w", err)
	}
	if decoded == nil {
		return ipc.ConfigResult{}, fmt.Errorf("unexpected daemon reply: result is null")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ipc.ConfigResult{}, fmt.Errorf("unexpected daemon reply: %w", err)
	}
	for _, field := range []string{"paths", "logging", "spotify_adapter", "downloader", "acquisition", "sync"} {
		if _, ok := fields[field]; !ok {
			return ipc.ConfigResult{}, fmt.Errorf("unexpected daemon reply: missing %q", field)
		}
	}
	return *decoded, nil
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
	fmt.Fprintln(w, "  logging:")
	fmt.Fprintf(w, "    level  : %s\n", c.Logging.Level)
	fmt.Fprintf(w, "    format : %s\n", c.Logging.Format)
	fmt.Fprintln(w, "  spotify_adapter:")
	fmt.Fprintf(w, "    bind_address : %s\n", c.SpotifyAdapter.BindAddress)
	fmt.Fprintf(w, "    port         : %d\n", c.SpotifyAdapter.Port)
	fmt.Fprintln(w, "  acquisition:")
	fmt.Fprintf(w, "    concurrency        : %d\n", c.Acquisition.Concurrency)
	fmt.Fprintf(w, "    temp_retry_backoff : %s\n", c.Acquisition.TempRetryBackoff)
	fmt.Fprintf(w, "    max_temp_retries   : %d\n", c.Acquisition.MaxTempRetries)
	fmt.Fprintln(w, "  downloader:")
	fmt.Fprintf(w, "    yt_dlp_path  : %s\n", c.Downloader.YTDLPPath)
	fmt.Fprintf(w, "    ffmpeg_path  : %s\n", c.Downloader.FFmpegPath)
	fmt.Fprintf(w, "    ffprobe_path : %s\n", c.Downloader.FFprobePath)
	fmt.Fprintln(w, "  sync:")
	fmt.Fprintf(w, "    https_port       : %d\n", c.Sync.HTTPSPort)
	fmt.Fprintf(w, "    lan_bind_address : %s\n", c.Sync.LANBindAddress)
	fmt.Fprintf(w, "    pairing_timeout  : %s\n", c.Sync.PairingTimeout)
}
