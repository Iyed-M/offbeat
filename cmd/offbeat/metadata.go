package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func runMetadataRefresh(configPath, homeDir string) int {
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat metadata refresh: config: %v\n", err)
		return 1
	}
	// The command is an explicit synchronous batch; each file can need an
	// artwork request and two bounded audio decodes.
	resp, err := requestControlMessage(app.SocketPath(bootstrap.SocketDir), ipc.Request{Version: ipc.ProtocolVersion, Command: "metadata.refresh"}, 24*time.Hour)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat metadata refresh: daemon-unavailable: %v\n", err)
		return 1
	}
	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "offbeat metadata refresh: daemon error: %s: %s\n", resp.Error.Code, resp.Error.Message)
		return 1
	}
	if resp.Version != ipc.ProtocolVersion {
		fmt.Fprintln(os.Stderr, "offbeat metadata refresh: unsupported daemon reply")
		return 1
	}
	raw, err := ipc.Encode(resp.Result)
	if err != nil {
		return 1
	}
	var result ipc.MetadataRefreshResult
	if err := ipc.Decode(raw, &result); err != nil || !validMetadataRefreshResult(result) {
		fmt.Fprintln(os.Stderr, "offbeat metadata refresh: invalid daemon reply")
		return 1
	}
	printMetadataRefresh(os.Stdout, result)
	if result.Failed > 0 {
		return 1
	}
	return 0
}

func printMetadataRefresh(out io.Writer, result ipc.MetadataRefreshResult) {
	fmt.Fprintf(out, "Metadata refresh: %d considered, %d changed (%d with outstanding failures), %d skipped, %d failed, %d missing.\n", result.Considered, result.Changed, result.Partial, result.Skipped, result.Failed, result.Missing)
	if result.MissingOptional > 0 {
		fmt.Fprintf(out, "%d available tracks have absent optional Spotify fields (album artist, numbers, date or artwork URL); no values were invented.\n", result.MissingOptional)
	}
	for _, diagnostic := range result.Diagnostics {
		fmt.Fprintf(out, "%s: %s\n", diagnostic.TrackURI, diagnostic.Reason)
	}
	if result.Omitted > 0 {
		fmt.Fprintf(out, "%d additional diagnostics omitted.\n", result.Omitted)
	}
}

func validMetadataRefreshResult(result ipc.MetadataRefreshResult) bool {
	return result.Considered >= 0 && result.Changed >= 0 && result.Partial >= 0 && result.Partial <= result.Changed && result.Partial <= result.Failed && result.Skipped >= 0 && result.Failed >= 0 && result.Missing >= 0 && result.MissingOptional >= 0 && result.MissingOptional <= result.Considered-result.Missing && result.Omitted >= 0 && result.Changed+result.Skipped+result.Failed+result.Missing == result.Considered+result.Partial && len(result.Diagnostics) <= 32 && len(result.Diagnostics)+result.Omitted <= result.Failed
}
