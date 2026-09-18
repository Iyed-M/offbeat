package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func runAcquire(configPath, homeDir, trackURI, sourceURL string) int {
	if err := ipc.ValidateAcquisitionTrackURI(trackURI); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire: %v\n", err)
		return 2
	}
	if err := ipc.ValidateAcquisitionSource(sourceURL); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire: %v\n", err)
		return 2
	}
	return runAcquisitionRequest(configPath, homeDir, "acquire", &ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire", Acquire: &ipc.AcquireRequest{TrackURI: trackURI, SourceURL: sourceURL}})
}

func runAcquireStatus(configPath, homeDir, rawID string) int {
	return runAcquisitionByID(configPath, homeDir, rawID, "acquire.status")
}

func runAcquireRetry(configPath, homeDir, rawID string) int {
	return runAcquisitionByID(configPath, homeDir, rawID, "acquire.retry")
}

func runAcquisitionByID(configPath, homeDir, rawID, command string) int {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintf(os.Stderr, "offbeat acquire: acquisition id must be a positive integer\n")
		return 2
	}
	req := &ipc.Request{Version: ipc.ProtocolVersion, Command: command}
	if command == "acquire.status" {
		req.AcquisitionStatus = &ipc.AcquisitionIDRequest{ID: id}
	} else {
		req.AcquisitionRetry = &ipc.AcquisitionIDRequest{ID: id}
	}
	return runAcquisitionRequest(configPath, homeDir, "acquire", req)
}

func runAcquisitionRequest(configPath, homeDir, label string, req *ipc.Request) int {
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: config: %v\n", label, err)
		return 1
	}
	resp, err := requestControlMessage(app.SocketPath(bootstrap.SocketDir), *req, controlReadTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: daemon-unavailable: %v\n", label, err)
		return 1
	}
	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: daemon error: %s: %s\n", label, resp.Error.Code, resp.Error.Message)
		return 1
	}
	if resp.Version != ipc.ProtocolVersion {
		fmt.Fprintf(os.Stderr, "offbeat %s: unexpected daemon reply: unsupported protocol version %d\n", label, resp.Version)
		return 1
	}
	result, err := decodeAcquisitionResult(resp.Result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: unexpected daemon reply: %v\n", label, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Acquisition %d: %s (%s).\n", result.ID, result.State, result.TrackURI)
	if result.Error != "" {
		fmt.Fprintf(os.Stdout, "Error: %s\n", result.Error)
	}
	return 0
}

func decodeAcquisitionResult(result any) (ipc.AcquisitionResult, error) {
	raw, err := ipc.Encode(result)
	if err != nil {
		return ipc.AcquisitionResult{}, err
	}
	var decoded ipc.AcquisitionResult
	if err := ipc.Decode(raw, &decoded); err != nil {
		return ipc.AcquisitionResult{}, err
	}
	if decoded.ID <= 0 || ipc.ValidateAcquisitionTrackURI(decoded.TrackURI) != nil || !validAcquisitionState(decoded.State) {
		return ipc.AcquisitionResult{}, fmt.Errorf("invalid acquisition result")
	}
	return decoded, nil
}

func validAcquisitionState(state string) bool {
	switch state {
	case "pending", "running", "failed", "complete":
		return true
	default:
		return false
	}
}
