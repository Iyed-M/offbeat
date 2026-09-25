package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

const batchSummaryVersion = 1

type captureBatchOptions struct {
	OutputDir   string
	State       string
	Limit       int
	RetryFailed bool
}

type captureBatchSummary struct {
	Version                       int            `json:"version"`
	State                         string         `json:"state"`
	Limit                         int            `json:"limit,omitempty"`
	Attempted                     int            `json:"attempted"`
	Captured                      int            `json:"captured"`
	Reused                        int            `json:"reused"`
	Skipped                       int            `json:"skipped"`
	SearchFailures                int            `json:"search_failures"`
	RetrievalFailures             int            `json:"retrieval_failures"`
	SearchFailureReasons          map[string]int `json:"search_failure_reasons"`
	DiagnosticReasons             map[string]int `json:"diagnostic_reasons"`
	EligibleCandidateDistribution map[string]int `json:"eligible_candidate_distribution"`
	ScoreGapDistribution          map[string]int `json:"score_gap_distribution"`
	SkipReasons                   map[string]int `json:"skip_reasons"`
	CorpusFiles                   []string       `json:"corpus_files"`
}

func runAcquireCaptureBatch(configPath, homeDir string, args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return runAcquireCaptureBatchContext(ctx, configPath, homeDir, args)
}

func runAcquireCaptureBatchContext(ctx context.Context, configPath, homeDir string, args []string) int {
	options, err := parseCaptureBatchOptions(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: %v\n", err)
		return 2
	}
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: config: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(options.OutputDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: create output directory: %v\n", err)
		return 1
	}
	if err := os.Chmod(options.OutputDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: protect output directory: %v\n", err)
		return 1
	}

	socket := app.SocketPath(bootstrap.SocketDir)
	work, err := listAcquisitionWork(ctx, socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: list acquisition work: %v\n", err)
		return 1
	}
	trackURIs := matchingBatchTrackURIs(work, options.State)
	summary := captureBatchSummary{
		Version:                       batchSummaryVersion,
		State:                         options.State,
		Limit:                         options.Limit,
		DiagnosticReasons:             map[string]int{},
		SearchFailureReasons:          map[string]int{},
		EligibleCandidateDistribution: map[string]int{},
		ScoreGapDistribution:          map[string]int{},
		SkipReasons:                   map[string]int{},
		CorpusFiles:                   []string{},
	}
	captures := make([]acquisition.EvaluationCapture, 0, len(trackURIs))
	for _, trackURI := range trackURIs {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "offbeat acquire capture-batch: canceled")
			return 1
		}
		path := filepath.Join(options.OutputDir, captureBatchFilename(trackURI))
		existing, found, readErr := readBatchCapture(path, trackURI)
		if found && readErr != nil {
			summary.Skipped++
			summary.SkipReasons["invalid_existing_capture"]++
			continue
		}
		if found && readErr == nil && existing.Observation != nil {
			summary.Reused++
			captures = append(captures, existing)
			addBatchDiagnostic(&summary, acquisition.ReplayYouTubeObservation(*existing.Observation))
			continue
		}
		if found && !options.RetryFailed {
			summary.Skipped++
			summary.SkipReasons["existing_failure"]++
			captures = append(captures, existing)
			continue
		}
		annotations := acquisition.HumanAnnotations{Candidates: []acquisition.CandidateAnnotation{}}
		if found {
			annotations = existing.Annotations
		}
		if options.Limit > 0 && summary.Attempted >= options.Limit {
			summary.Skipped++
			summary.SkipReasons["limit"]++
			continue
		}
		summary.Attempted++
		report, inspectErr := fetchAcquisitionInspectionContext(ctx, socket, trackURI)
		if inspectErr != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "offbeat acquire capture-batch: canceled")
				return 1
			}
			if isNoLongerDesiredInspection(inspectErr) {
				summary.Skipped++
				summary.SkipReasons["not_currently_desired"]++
				continue
			}
			reason := batchInspectionFailureReason(inspectErr)
			summary.SearchFailures++
			summary.SearchFailureReasons[reason]++
			failure := newBatchFailureCorpus(trackURI, reason, annotations)
			if err := writePrivateJSON(path, failure); err != nil {
				fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: write %s: %v\n", filepath.Base(path), err)
				return 1
			}
			captures = append(captures, failure.Captures[0])
			continue
		}
		corpus, err := acquisition.NewEvaluationCorpus(trackURI, version, report)
		if err != nil {
			reason := "capture_validation_error"
			summary.SearchFailures++
			summary.SearchFailureReasons[reason]++
			failure := newBatchFailureCorpus(trackURI, reason, annotations)
			if err := writePrivateJSON(path, failure); err != nil {
				fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: write %s: %v\n", filepath.Base(path), err)
				return 1
			}
			captures = append(captures, failure.Captures[0])
			continue
		}
		corpus.Captures[0].Annotations = annotations
		if err := writePrivateJSON(path, corpus); err != nil {
			fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: write %s: %v\n", filepath.Base(path), err)
			return 1
		}
		summary.Captured++
		capture := corpus.Captures[0]
		captures = append(captures, capture)
		addBatchDiagnostic(&summary, report)
	}
	if len(captures) > 0 {
		corpusFiles, err := writeBatchCorpora(options.OutputDir, captures)
		if err != nil {
			fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: write corpus: %v\n", err)
			return 1
		}
		summary.CorpusFiles = corpusFiles
	}
	if err := writePrivateJSON(filepath.Join(options.OutputDir, "summary.json"), summary); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire capture-batch: write summary: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Batch capture: %d attempted, %d captured, %d reused, %d skipped, %d search failures, %d retrieval failures.\n", summary.Attempted, summary.Captured, summary.Reused, summary.Skipped, summary.SearchFailures, summary.RetrievalFailures)
	return 0
}

func newBatchFailureCorpus(trackURI, reason string, annotations acquisition.HumanAnnotations) acquisition.EvaluationCorpus {
	return acquisition.EvaluationCorpus{FormatVersion: acquisition.EvaluationFormatVersion, Captures: []acquisition.EvaluationCapture{{
		CaptureID:   trackURI,
		Provenance:  acquisition.CaptureProvenance{CapturedBy: acquisition.CaptureTool{Name: "offbeat", Version: version}},
		Failure:     &acquisition.CapturedFailure{Stage: acquisition.FailureSearch, Message: reason},
		Annotations: annotations,
	}}}
}

func writeBatchCorpora(outputDir string, captures []acquisition.EvaluationCapture) ([]string, error) {
	sorted := append([]acquisition.EvaluationCapture(nil), captures...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CaptureID < sorted[j].CaptureID })
	var chunks [][]acquisition.EvaluationCapture
	current := make([]acquisition.EvaluationCapture, 0, len(sorted))
	for _, capture := range sorted {
		trial := append(append([]acquisition.EvaluationCapture(nil), current...), capture)
		corpus := acquisition.EvaluationCorpus{FormatVersion: acquisition.EvaluationFormatVersion, Description: "Offbeat batch capture", Captures: trial}
		data, err := marshalPrivateJSON(corpus)
		if err != nil {
			return nil, err
		}
		if len(data) <= acquisition.MaxEvaluationCorpusBytes {
			current = trial
			continue
		}
		if len(current) == 0 {
			return nil, fmt.Errorf("capture %q exceeds the replay corpus size limit", capture.CaptureID)
		}
		chunks = append(chunks, current)
		current = []acquisition.EvaluationCapture{capture}
		single := acquisition.EvaluationCorpus{FormatVersion: acquisition.EvaluationFormatVersion, Description: "Offbeat batch capture", Captures: current}
		data, err = marshalPrivateJSON(single)
		if err != nil {
			return nil, err
		}
		if len(data) > acquisition.MaxEvaluationCorpusBytes {
			return nil, fmt.Errorf("capture %q exceeds the replay corpus size limit", capture.CaptureID)
		}
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	files := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		name := "corpus.json"
		if len(chunks) > 1 {
			name = fmt.Sprintf("corpus-%04d.json", index+1)
		}
		corpus := acquisition.EvaluationCorpus{FormatVersion: acquisition.EvaluationFormatVersion, Description: "Offbeat batch capture", Captures: chunk}
		path := filepath.Join(outputDir, name)
		if err := ensureCombinedAnnotationsPreserved(path, corpus); err != nil {
			return nil, err
		}
		if err := writePrivateJSON(path, corpus); err != nil {
			return nil, err
		}
		files = append(files, name)
	}
	return files, nil
}

func ensureCombinedAnnotationsPreserved(path string, proposed acquisition.EvaluationCorpus) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() > acquisition.MaxEvaluationCorpusBytes {
		return errors.New("existing combined corpus is invalid; refusing to overwrite possible human annotations")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var existing acquisition.EvaluationCorpus
	if err := ipc.Decode(data, &existing); err != nil || acquisition.ValidateEvaluationCorpus(existing) != nil {
		return errors.New("existing combined corpus is invalid; refusing to overwrite possible human annotations")
	}
	wanted := make(map[string]acquisition.HumanAnnotations, len(proposed.Captures))
	for _, capture := range proposed.Captures {
		wanted[capture.CaptureID] = capture.Annotations
	}
	for _, capture := range existing.Captures {
		if len(capture.Annotations.Candidates) == 0 && capture.Annotations.Notes == "" {
			continue
		}
		if annotations, ok := wanted[capture.CaptureID]; !ok || !reflect.DeepEqual(capture.Annotations, annotations) {
			return fmt.Errorf("existing combined corpus has human annotations for %q that are absent from its independent capture", capture.CaptureID)
		}
	}
	return nil
}

func isNoLongerDesiredInspection(err error) bool {
	var daemonErr *acquisitionInspectionDaemonError
	return errors.As(err, &daemonErr) && daemonErr.Code == ipc.CodeFailedPrecondition && daemonErr.Message == "track is not currently desired"
}

func batchInspectionFailureReason(err error) string {
	var daemonErr *acquisitionInspectionDaemonError
	if errors.As(err, &daemonErr) {
		switch {
		case containsFold(daemonErr.Message, "timed out"):
			return "timeout"
		case containsFold(daemonErr.Message, "canceled"):
			return "canceled"
		case containsFold(daemonErr.Message, "response limit"):
			return "oversized_report"
		default:
			return "resolver_or_tool_error"
		}
	}
	return "control_error"
}

func containsFold(value, fragment string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(fragment))
}

func readBatchCapture(path, trackURI string) (acquisition.EvaluationCapture, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return acquisition.EvaluationCapture{}, false, nil
	}
	if err != nil {
		return acquisition.EvaluationCapture{}, true, err
	}
	if info.Size() > acquisition.MaxEvaluationCorpusBytes {
		return acquisition.EvaluationCapture{}, true, errors.New("capture exceeds evaluation size limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return acquisition.EvaluationCapture{}, true, err
	}
	var corpus acquisition.EvaluationCorpus
	if err := ipc.Decode(data, &corpus); err != nil {
		return acquisition.EvaluationCapture{}, true, err
	}
	if err := acquisition.ValidateEvaluationCorpus(corpus); err != nil {
		return acquisition.EvaluationCapture{}, true, err
	}
	if len(corpus.Captures) != 1 || corpus.Captures[0].CaptureID != trackURI {
		return acquisition.EvaluationCapture{}, true, errors.New("capture identity does not match track")
	}
	capture := corpus.Captures[0]
	if capture.Observation != nil && capture.Observation.Track.URI != trackURI {
		return acquisition.EvaluationCapture{}, true, errors.New("capture observation does not match track")
	}
	return capture, true, nil
}

func parseCaptureBatchOptions(args []string) (captureBatchOptions, error) {
	options := captureBatchOptions{State: "unresolved"}
	flags := flag.NewFlagSet("acquire capture-batch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.OutputDir, "output-dir", "", "directory for capture artifacts")
	flags.StringVar(&options.State, "state", options.State, "acquisition state: unresolved or complete")
	flags.IntVar(&options.Limit, "limit", 0, "maximum number of new captures")
	flags.BoolVar(&options.RetryFailed, "retry-failed", false, "retry prior failed capture artifacts")
	if err := flags.Parse(args); err != nil {
		return options, errors.New("usage: offbeat acquire capture-batch --output-dir <dir> [--state unresolved|complete] [--limit N] [--retry-failed]")
	}
	if flags.NArg() != 0 || options.OutputDir == "" || (options.State != "unresolved" && options.State != "complete") || options.Limit < 0 {
		return options, errors.New("usage: offbeat acquire capture-batch --output-dir <dir> [--state unresolved|complete] [--limit N] [--retry-failed]")
	}
	return options, nil
}

func listAcquisitionWork(ctx context.Context, socket string) ([]ipc.AcquisitionResult, error) {
	req := ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.status"}
	var all []ipc.AcquisitionResult
	for {
		resp, err := requestControlMessageContext(ctx, socket, req, controlReadTimeout)
		if err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("daemon error: %s: %s", resp.Error.Code, resp.Error.Message)
		}
		if resp.Version != ipc.ProtocolVersion {
			return nil, fmt.Errorf("unexpected daemon reply: unsupported protocol version %d", resp.Version)
		}
		raw, err := ipc.Encode(resp.Result)
		if err != nil {
			return nil, errors.New("unexpected daemon reply")
		}
		var page ipc.AcquisitionStatusResult
		if err := ipc.Decode(raw, &page); err != nil {
			return nil, errors.New("unexpected daemon reply")
		}
		for _, item := range page.Work {
			if item.ID <= 0 || ipc.ValidateAcquisitionTrackURI(item.TrackURI) != nil || !validAcquisitionSourceKind(item.SourceKind) || !validAcquisitionState(item.State) {
				return nil, errors.New("unexpected daemon reply")
			}
		}
		all = append(all, page.Work...)
		if page.NextAfterID == 0 {
			return all, nil
		}
		if len(page.Work) == 0 || page.NextAfterID != page.Work[len(page.Work)-1].ID {
			return nil, errors.New("invalid acquisition status continuation")
		}
		req.AcquisitionList = &ipc.AcquisitionListRequest{AfterID: page.NextAfterID}
	}
}

func matchingBatchTrackURIs(work []ipc.AcquisitionResult, state string) []string {
	seen := make(map[string]bool)
	var uris []string
	for _, item := range work {
		if item.State == state && item.SourceKind == "youtube" && !seen[item.TrackURI] {
			seen[item.TrackURI] = true
			uris = append(uris, item.TrackURI)
		}
	}
	sort.Strings(uris)
	return uris
}

func captureBatchFilename(trackURI string) string {
	digest := sha256.Sum256([]byte(trackURI))
	return "capture-" + hex.EncodeToString(digest[:8]) + ".json"
}

func addBatchDiagnostic(summary *captureBatchSummary, report acquisition.ResolutionInspection) {
	reason := string(report.Decision.UnresolvedReason)
	if reason == "" {
		reason = "selected"
	}
	summary.DiagnosticReasons[reason]++
	summary.EligibleCandidateDistribution[fmt.Sprint(report.Diagnostic.Eligible)]++
	if report.Decision.ScoreGap != nil {
		summary.ScoreGapDistribution[fmt.Sprint(*report.Decision.ScoreGap)]++
	}
}

func writePrivateJSON(path string, value any) error {
	data, err := marshalPrivateJSON(value)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".offbeat-capture-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func marshalPrivateJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
