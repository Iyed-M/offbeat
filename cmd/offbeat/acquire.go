package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/Iyed-M/offbeat/internal/acquisition"
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

func runAcquireMissing(configPath, homeDir string) int {
	return runAcquisitionSummaryRequest(configPath, homeDir, "acquire missing", ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.missing"})
}

func runAcquireInspect(configPath, homeDir, trackURI string) int {
	const label = "acquire inspect"
	if err := ipc.ValidateAcquisitionTrackURI(trackURI); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: %v\n", label, err)
		return 2
	}
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: config: %v\n", label, err)
		return 1
	}
	req := ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.inspect", AcquisitionInspect: &ipc.AcquisitionTrackRequest{TrackURI: trackURI}}
	resp, err := requestControlMessage(app.SocketPath(bootstrap.SocketDir), req, youtubeInspectReadTimeout)
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
	raw, err := ipc.Encode(resp.Result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: unexpected daemon reply\n", label)
		return 1
	}
	var report acquisition.ResolutionInspection
	if err := ipc.Decode(raw, &report); err != nil || report.ReportVersion != acquisition.ResolutionInspectionVersion || !report.FreshSearch || report.CapturedAt.IsZero() || report.Track.URI != trackURI || len(report.Search.RawResults) > acquisition.MaxYouTubeSearchCandidates || len(report.Candidates) > acquisition.MaxYouTubeSearchCandidates {
		fmt.Fprintf(os.Stderr, "offbeat %s: unexpected daemon reply\n", label)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: write report: %v\n", label, err)
		return 1
	}
	return 0
}

func runAcquireStatusAll(configPath, homeDir string) int {
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire status: config: %v\n", err)
		return 1
	}
	req := ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.status"}
	first := true
	for {
		resp, err := requestControlMessage(app.SocketPath(bootstrap.SocketDir), req, controlReadTimeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "offbeat acquire status: daemon-unavailable: %v\n", err)
			return 1
		}
		if resp.Error != nil {
			fmt.Fprintf(os.Stderr, "offbeat acquire status: daemon error: %s: %s\n", resp.Error.Code, resp.Error.Message)
			return 1
		}
		if resp.Version != ipc.ProtocolVersion {
			fmt.Fprintf(os.Stderr, "offbeat acquire status: unexpected daemon reply: unsupported protocol version %d\n", resp.Version)
			return 1
		}
		raw, _ := ipc.Encode(resp.Result)
		var result ipc.AcquisitionStatusResult
		if err := ipc.Decode(raw, &result); err != nil {
			fmt.Fprintln(os.Stderr, "offbeat acquire status: unexpected daemon reply")
			return 1
		}
		if result.Counts.Pending < 0 || result.Counts.Running < 0 || result.Counts.Unresolved < 0 || result.Counts.Failed < 0 || result.Counts.Complete < 0 {
			fmt.Fprintln(os.Stderr, "offbeat acquire status: unexpected daemon reply")
			return 1
		}
		if first {
			fmt.Fprintf(os.Stdout, "Acquisitions: %d pending, %d running, %d unresolved, %d failed, %d complete.\n", result.Counts.Pending, result.Counts.Running, result.Counts.Unresolved, result.Counts.Failed, result.Counts.Complete)
			first = false
		}
		for _, work := range result.Work {
			if work.ID <= 0 || ipc.ValidateAcquisitionTrackURI(work.TrackURI) != nil || !validAcquisitionState(work.State) {
				fmt.Fprintln(os.Stderr, "offbeat acquire status: unexpected daemon reply")
				return 1
			}
			fmt.Fprintf(os.Stdout, "Acquisition %d: %s (%s).\n", work.ID, work.State, work.TrackURI)
			if work.Error != "" {
				fmt.Fprintf(os.Stdout, "Error: %s\n", work.Error)
			}
		}
		if result.NextAfterID == 0 {
			return 0
		}
		if len(result.Work) == 0 || result.NextAfterID != result.Work[len(result.Work)-1].ID {
			fmt.Fprintln(os.Stderr, "offbeat acquire status: unexpected daemon reply")
			return 1
		}
		req.AcquisitionList = &ipc.AcquisitionListRequest{AfterID: result.NextAfterID}
	}
}

func runAcquireStatus(configPath, homeDir, rawID string) int {
	return runAcquisitionByID(configPath, homeDir, rawID, "acquire.status")
}

func runAcquireRetry(configPath, homeDir, rawID string) int {
	return runAcquisitionByID(configPath, homeDir, rawID, "acquire.retry")
}

func runAcquireRetryUnresolved(configPath, homeDir string) int {
	const label = "acquire retry unresolved"
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: config: %v\n", label, err)
		return 1
	}
	resp, err := requestControlMessage(app.SocketPath(bootstrap.SocketDir), ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.retry.unresolved"}, controlReadTimeout)
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
	raw, err := ipc.Encode(resp.Result)
	if err != nil {
		return 1
	}
	var result ipc.UnresolvedRetryBatchResult
	if err := ipc.Decode(raw, &result); err != nil || result.Considered < 0 || result.Queued < 0 || result.SkippedActive < 0 || result.SkippedAvailable < 0 || result.SkippedRemoved < 0 || result.Queued+result.SkippedActive+result.SkippedAvailable+result.SkippedRemoved != result.Considered {
		fmt.Fprintf(os.Stderr, "offbeat %s: unexpected daemon reply\n", label)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Unresolved acquisition retry: %d queued, %d active, %d available, %d removed.\n", result.Queued, result.SkippedActive, result.SkippedAvailable, result.SkippedRemoved)
	return 0
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

func runAcquisitionSummaryRequest(configPath, homeDir, label string, req ipc.Request) int {
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat %s: config: %v\n", label, err)
		return 1
	}
	resp, err := requestControlMessage(app.SocketPath(bootstrap.SocketDir), req, controlReadTimeout)
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
	raw, err := ipc.Encode(resp.Result)
	if err != nil {
		return 1
	}
	var result ipc.AcquisitionBatchResult
	if err := ipc.Decode(raw, &result); err != nil || result.Considered < 0 || result.Queued < 0 || result.SkippedActive < 0 || result.SkippedAttempted < 0 || result.Available < 0 || result.Queued+result.SkippedActive+result.SkippedAttempted != result.Considered {
		fmt.Fprintf(os.Stderr, "offbeat %s: unexpected daemon reply\n", label)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Missing acquisition: %d queued, %d active, %d previously attempted, %d available.\n", result.Queued, result.SkippedActive, result.SkippedAttempted, result.Available)
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
	case "pending", "running", "unresolved", "failed", "complete":
		return true
	default:
		return false
	}
}
