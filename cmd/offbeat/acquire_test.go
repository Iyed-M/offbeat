package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

type cliRetrieveFunc func(context.Context, string) (*acquisition.Media, error)

func (f cliRetrieveFunc) Retrieve(ctx context.Context, sourceURL string) (*acquisition.Media, error) {
	return f(ctx, sourceURL)
}

type cliResolveFunc func(context.Context, desired.Track) (string, error)

func (f cliResolveFunc) Resolve(ctx context.Context, track desired.Track) (string, error) {
	return f(ctx, track)
}

func TestDecodeAcquisitionResultRejectsInvalidDaemonReply(t *testing.T) {
	for _, result := range []any{
		nil,
		map[string]any{},
		map[string]any{"acquisition_id": 1, "track_uri": "spotify:track:one", "state": "unknown"},
		map[string]any{"acquisition_id": 1, "track_uri": "spotify:playlist:one", "state": "pending"},
		map[string]any{"acquisition_id": 1, "track_uri": "spotify:track:one", "state": "pending", "source_url": "https://example.test/media"},
	} {
		if _, err := decodeAcquisitionResult(result); err == nil {
			t.Errorf("decodeAcquisitionResult(%#v) succeeded", result)
		}
	}
}

func TestDecodeAcquisitionResult(t *testing.T) {
	result, err := decodeAcquisitionResult(map[string]any{
		"acquisition_id": 7,
		"track_uri":      "spotify:track:one",
		"source_kind":    "direct",
		"state":          "failed",
		"error":          "retrieval failed",
	})
	if err != nil {
		t.Fatalf("decodeAcquisitionResult: %v", err)
	}
	if result.ID != 7 || result.State != "failed" || result.Error != "retrieval failed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCLIAcquisitionSubmitStatusAndRetry(t *testing.T) {
	home := t.TempDir()
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("controlled retrieval failure")
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "one")

	out, stderr, err := runCLI(t, home, "acquire", "spotify:track:one", "http://127.0.0.1:8080/owned.wav")
	if err != nil || stderr != "" || out == "" {
		t.Fatalf("acquire = stdout %q stderr %q err %v", out, stderr, err)
	}
	work := waitCLIAcquisitionState(t, d, 1, "failed")

	out, stderr, err = runCLI(t, home, "acquire", "status", fmt.Sprint(work.ID))
	if err != nil || stderr != "" || out != "Acquisition 1: failed (spotify:track:one).\nError: media retrieval failed: controlled retrieval failure\n" {
		t.Fatalf("acquire status = stdout %q stderr %q err %v", out, stderr, err)
	}

	out, stderr, err = runCLI(t, home, "acquire", "retry", fmt.Sprint(work.ID))
	if err != nil || stderr != "" || out == "" {
		t.Fatalf("acquire retry = stdout %q stderr %q err %v", out, stderr, err)
	}
	_ = waitCLIAcquisitionState(t, d, work.ID, "failed")
}

func TestCLIAcquisitionCompletesWithInjectedRetriever(t *testing.T) {
	home := t.TempDir()
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		file, err := os.CreateTemp(t.TempDir(), "controlled-audio-*.wav")
		if err != nil {
			return nil, err
		}
		if _, err := file.Write([]byte("controlled completed audio")); err != nil {
			_ = file.Close()
			return nil, err
		}
		if _, err := file.Seek(0, 0); err != nil {
			_ = file.Close()
			return nil, err
		}
		return &acquisition.Media{File: file, Extension: "wav"}, nil
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "complete")

	out, stderr, err := runCLI(t, home, "acquire", "spotify:track:complete", "http://127.0.0.1:8080/owned.wav")
	if err != nil || stderr != "" || out == "" {
		t.Fatalf("acquire = stdout %q stderr %q err %v", out, stderr, err)
	}
	work := waitCLIAcquisitionState(t, d, 1, "complete")
	out, stderr, err = runCLI(t, home, "acquire", "status", fmt.Sprint(work.ID))
	if err != nil || stderr != "" || out != "Acquisition 1: complete (spotify:track:complete).\n" {
		t.Fatalf("acquire status = stdout %q stderr %q err %v", out, stderr, err)
	}
}

func TestCLIAcquireMissingAndAggregateStatus(t *testing.T) {
	home := t.TempDir()
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("controlled retrieval failure")
	}), cliResolveFunc(func(context.Context, desired.Track) (string, error) {
		return "", acquisition.ErrUnresolved
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "one")

	out, stderr, err := runCLI(t, home, "acquire", "missing")
	if err != nil || stderr != "" || out != "Missing acquisition: 1 queued, 0 active, 0 previously attempted, 0 available.\n" {
		t.Fatalf("acquire missing = stdout %q stderr %q err %v", out, stderr, err)
	}
	waitCLIAcquisitionState(t, d, 1, "unresolved")
	out, stderr, err = runCLI(t, home, "acquire", "status")
	if err != nil || stderr != "" || out != "Acquisitions: 0 pending, 0 running, 1 unresolved, 0 failed, 0 complete.\nAcquisition 1: unresolved (spotify:track:one).\nError: no unique eligible YouTube result\n" {
		t.Fatalf("aggregate status = stdout %q stderr %q err %v", out, stderr, err)
	}
	out, stderr, err = runCLI(t, home, "acquire", "retry", "unresolved")
	if err != nil || stderr != "" || out != "Unresolved acquisition retry: 1 queued, 0 active, 0 available, 0 removed.\n" {
		t.Fatalf("acquire retry unresolved = stdout %q stderr %q err %v", out, stderr, err)
	}
	waitCLIAcquisitionState(t, d, 1, "unresolved")
}

func TestCLIAcquireInspectPrintsMachineReadableFreshReport(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-cli-inspect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	tools := t.TempDir()
	_ = writeCLITool(t, tools, "yt-dlp", `printf '%s\n' '{"entries":[{"id":"aaaaaaaaaaa","title":"Artist - inspect","channel":"Artist","duration":1}]}'`)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	var retrieves atomic.Int32
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		retrieves.Add(1)
		return nil, errors.New("retriever must not run")
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "inspect")
	var beforeWorkCount, beforeManagedCount int
	if err := d.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM acquisition_work`).Scan(&beforeWorkCount); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM managed_tracks`).Scan(&beforeManagedCount); err != nil {
		t.Fatal(err)
	}

	out, stderr, err := runCLI(t, home, "acquire", "inspect", "spotify:track:inspect")
	if err != nil || stderr != "" {
		t.Fatalf("acquire inspect = stdout %q stderr %q err %v", out, stderr, err)
	}
	var report acquisition.ResolutionInspection
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if !report.FreshSearch || report.CapturedAt.IsZero() || report.Track.URI != "spotify:track:inspect" || report.Query.Text != "Artist - inspect" || len(report.Search.RawResults) != 1 || report.Decision.SelectedURL != "https://www.youtube.com/watch?v=aaaaaaaaaaa" {
		t.Fatalf("report = %#v", report)
	}
	var afterWorkCount, afterManagedCount int
	if err := d.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM acquisition_work`).Scan(&afterWorkCount); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM managed_tracks`).Scan(&afterManagedCount); err != nil {
		t.Fatal(err)
	}
	if afterWorkCount != beforeWorkCount || afterManagedCount != beforeManagedCount || retrieves.Load() != 0 {
		t.Fatalf("inspection side effects: work %d->%d managed %d->%d retrieves=%d", beforeWorkCount, afterWorkCount, beforeManagedCount, afterManagedCount, retrieves.Load())
	}
}

func TestCLIAcquireCaptureAndReplayFrozenResults(t *testing.T) {
	home := t.TempDir()
	tools := t.TempDir()
	_ = writeCLITool(t, tools, "yt-dlp", `printf '%s\n' '{"entries":[{"id":"aaaaaaaaaaa","title":"Artist - capture","channel":"Artist","duration":1}]}'`)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("retriever must not run")
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "capture")

	out, stderr, err := runCLI(t, home, "acquire", "capture", "spotify:track:capture")
	if err != nil || stderr != "" {
		t.Fatalf("acquire capture = stdout %q stderr %q err %v", out, stderr, err)
	}
	var corpus acquisition.EvaluationCorpus
	if err := json.Unmarshal([]byte(out), &corpus); err != nil {
		t.Fatalf("capture stdout is not JSON: %v\n%s", err, out)
	}
	if corpus.FormatVersion != acquisition.EvaluationFormatVersion || len(corpus.Captures) != 1 || corpus.Captures[0].Observation == nil || corpus.Captures[0].Observation.RecordedDecision.SelectedURL != "https://www.youtube.com/watch?v=aaaaaaaaaaa" || corpus.Captures[0].Provenance.CapturedBy.Version == "" || corpus.Captures[0].Provenance.Producer == nil || corpus.Captures[0].Provenance.Producer.DecisionTool.Version == "" || corpus.Captures[0].Provenance.Producer.SearchTool.Version == "" {
		t.Fatalf("corpus = %#v", corpus)
	}
	corpus.Captures[0].Annotations.Candidates = []acquisition.CandidateAnnotation{{
		VideoID: "aaaaaaaaaaa", Label: acquisition.CandidateAcceptable,
		Evidence: "independently verified same recording", Provenance: "authorized manual listening audit",
	}}
	corpusPath := filepath.Join(t.TempDir(), "corpus.json")
	data, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corpusPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	out, stderr, err = runCLI(t, home, "acquire", "replay", corpusPath)
	if err != nil || stderr != "" {
		t.Fatalf("acquire replay = stdout %q stderr %q err %v", out, stderr, err)
	}
	var report acquisition.EvaluationReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("replay stdout is not JSON: %v\n%s", err, out)
	}
	if report.Total != 1 || report.Counts.CorrectAutomaticSelections != 1 || report.Cases[0].DecisionMatchesRecorded == nil || !*report.Cases[0].DecisionMatchesRecorded || report.Cases[0].Replay == nil || report.Cases[0].Replay.FreshSearch {
		t.Fatalf("report = %#v", report)
	}
}

func TestCLIAcquireCaptureBatchWritesPrivateReplayableArtifacts(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-batch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	tools := t.TempDir()
	_ = writeCLITool(t, tools, "yt-dlp", `printf '%s\n' '{"entries":[{"id":"aaaaaaaaaaa","title":"Artist - batch","channel":"Artist","duration":1},{"id":"bbbbbbbbbbb","title":"Artist - batch (Official Audio)","channel":"Artist","duration":1}]}'`)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	var retrieves atomic.Int32
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		retrieves.Add(1)
		return nil, errors.New("retriever must not run")
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "batch")
	if _, err := d.DB.ExecContext(context.Background(), `INSERT INTO acquisition_work(track_uri, source_kind, state, error, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`, "spotify:track:batch", db.AcquisitionSourceYouTube, db.AcquisitionUnresolved, time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(t.TempDir(), "captures")

	out, stderr, err := runCLI(t, home, "acquire", "capture-batch", "--output-dir", outputDir, "--limit", "1")
	if err != nil || stderr != "" || out != "Batch capture: 1 attempted, 1 captured, 0 reused, 0 skipped, 0 search failures, 0 retrieval failures.\n" {
		t.Fatalf("capture batch = stdout %q stderr %q err %v", out, stderr, err)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("output entries = %v, want capture, corpus, and summary", entries)
	}
	for _, name := range []string{"corpus.json", "summary.json"} {
		info, err := os.Stat(filepath.Join(outputDir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, err %v", name, info.Mode().Perm(), err)
		}
	}
	var capturePath string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "capture-") {
			capturePath = filepath.Join(outputDir, entry.Name())
		}
	}
	if capturePath == "" {
		t.Fatal("batch did not write an independent capture")
	}
	captureData, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	var capture acquisition.EvaluationCorpus
	if err := json.Unmarshal(captureData, &capture); err != nil || acquisition.ValidateEvaluationCorpus(capture) != nil || len(capture.Captures) != 1 || capture.Captures[0].Observation.Track.URI != "spotify:track:batch" || len(capture.Captures[0].Annotations.Candidates) != 0 {
		t.Fatalf("capture = %#v, decode err %v", capture, err)
	}
	corpusData, err := os.ReadFile(filepath.Join(outputDir, "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus acquisition.EvaluationCorpus
	if err := json.Unmarshal(corpusData, &corpus); err != nil || acquisition.ValidateEvaluationCorpus(corpus) != nil || len(corpus.Captures) != 1 {
		t.Fatalf("combined corpus = %#v, decode err %v", corpus, err)
	}
	summaryData, err := os.ReadFile(filepath.Join(outputDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary captureBatchSummary
	if err := json.Unmarshal(summaryData, &summary); err != nil || summary.DiagnosticReasons["ambiguous"] != 1 || summary.EligibleCandidateDistribution["2"] != 1 || len(summary.ScoreGapDistribution) == 0 || bytes.Contains(summaryData, []byte("correct_automatic")) || bytes.Contains(summaryData, []byte("avoidable")) {
		t.Fatalf("summary = %#v, decode err %v", summary, err)
	}
	combinedWithAnnotation := corpus
	combinedWithAnnotation.Captures[0].Annotations.Candidates = []acquisition.CandidateAnnotation{{
		VideoID: "aaaaaaaaaaa", Label: acquisition.CandidateAcceptable,
		Evidence: "combined-only annotation", Provenance: "manual audit",
	}}
	protectedData, err := json.MarshalIndent(combinedWithAnnotation, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	protectedData = append(protectedData, '\n')
	if err := os.WriteFile(filepath.Join(outputDir, "corpus.json"), protectedData, 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err = runCLI(t, home, "acquire", "capture-batch", "--output-dir", outputDir, "--limit", "1")
	if err == nil || out != "" || !strings.Contains(stderr, "human annotations") {
		t.Fatalf("combined annotation protection = stdout %q stderr %q err %v", out, stderr, err)
	}
	stillProtected, err := os.ReadFile(filepath.Join(outputDir, "corpus.json"))
	if err != nil || !bytes.Equal(stillProtected, protectedData) {
		t.Fatalf("combined annotations were overwritten: equal=%v err=%v", bytes.Equal(stillProtected, protectedData), err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "corpus.json"), corpusData, 0o600); err != nil {
		t.Fatal(err)
	}
	if retrieves.Load() != 0 {
		t.Fatalf("retriever calls = %d", retrieves.Load())
	}
	work, err := d.DB.Acquisition(context.Background(), 1)
	if err != nil || work.State != db.AcquisitionUnresolved || work.SourceURL != "" {
		t.Fatalf("acquisition changed: %#v, %v", work, err)
	}
	capture.Captures[0].Annotations.Candidates = []acquisition.CandidateAnnotation{{
		VideoID: "aaaaaaaaaaa", Label: acquisition.CandidateAcceptable,
		Evidence: "independently verified", Provenance: "manual audit",
	}}
	annotated, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	annotated = append(annotated, '\n')
	if err := os.WriteFile(capturePath, annotated, 0o600); err != nil {
		t.Fatal(err)
	}

	out, stderr, err = runCLI(t, home, "acquire", "capture-batch", "--output-dir", outputDir, "--limit", "1")
	if err != nil || stderr != "" || out != "Batch capture: 0 attempted, 0 captured, 1 reused, 0 skipped, 0 search failures, 0 retrieval failures.\n" {
		t.Fatalf("resumed capture batch = stdout %q stderr %q err %v", out, stderr, err)
	}
	afterResume, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterResume, annotated) {
		t.Fatal("resume overwrote the annotated independent capture")
	}
	corpusData, err = os.ReadFile(filepath.Join(outputDir, "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(corpusData, &corpus); err != nil || len(corpus.Captures) != 1 || len(corpus.Captures[0].Annotations.Candidates) != 1 {
		t.Fatalf("resumed combined corpus = %#v, decode err %v", corpus, err)
	}
	out, stderr, err = runCLI(t, home, "acquire", "capture-batch", "--output-dir", outputDir, "--limit", "1")
	if err != nil || stderr != "" || out != "Batch capture: 0 attempted, 0 captured, 1 reused, 0 skipped, 0 search failures, 0 retrieval failures.\n" {
		t.Fatalf("deterministic resume = stdout %q stderr %q err %v", out, stderr, err)
	}
	deterministicCorpus, err := os.ReadFile(filepath.Join(outputDir, "corpus.json"))
	if err != nil || !bytes.Equal(deterministicCorpus, corpusData) {
		t.Fatalf("combined corpus changed on deterministic resume: equal=%v err=%v", bytes.Equal(deterministicCorpus, corpusData), err)
	}
}

func TestCLIAcquireCaptureBatchPaginatesDeduplicatesAndAuditsCompletedYouTubeWork(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-batch-pages-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	tools := t.TempDir()
	_ = writeCLITool(t, tools, "yt-dlp", `printf '%s\n' '{"entries":[]}'`)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("retriever must not run")
	}))
	defer stop()
	tracks := make([]desired.CandidateEntry, 0, 129)
	for index := 0; index < 127; index++ {
		track := desired.Track{URI: fmt.Sprintf("spotify:track:padding%03d", index), Name: "Padding", Artists: []desired.NamedURI{{Name: "Artist"}}, DurationMS: 1000}
		tracks = append(tracks, desired.CandidateEntry{Position: index, Kind: desired.EntrySupported, Track: &track})
	}
	for _, id := range []string{"audit", "target"} {
		track := desired.Track{URI: "spotify:track:" + id, Name: id, Artists: []desired.NamedURI{{Name: "Artist"}}, DurationMS: 1000}
		tracks = append(tracks, desired.CandidateEntry{Position: len(tracks), Kind: desired.EntrySupported, Track: &track})
	}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: tracks}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := d.DB.ExecContext(context.Background(), `INSERT INTO acquisition_work(track_uri, source_kind, state, error, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`, "spotify:track:audit", db.AcquisitionSourceYouTube, db.AcquisitionComplete, now, now); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 127; index++ {
		if _, err := d.DB.ExecContext(context.Background(), `INSERT INTO acquisition_work(track_uri, source_kind, source_url, state, error, created_at, updated_at) VALUES (?, ?, ?, ?, '', ?, ?)`, fmt.Sprintf("spotify:track:padding%03d", index), db.AcquisitionSourceDirect, "https://example.test/audio", db.AcquisitionComplete, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := d.DB.ExecContext(context.Background(), `INSERT INTO acquisition_work(track_uri, source_kind, state, error, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`, "spotify:track:target", db.AcquisitionSourceYouTube, db.AcquisitionUnresolved, now, now); err != nil {
			t.Fatal(err)
		}
	}

	unresolvedDir := filepath.Join(t.TempDir(), "unresolved")
	out, stderr, err := runCLI(t, home, "acquire", "capture-batch", "--output-dir", unresolvedDir)
	if err != nil || stderr != "" || !strings.HasPrefix(out, "Batch capture: 1 attempted, 1 captured") {
		t.Fatalf("unresolved batch = stdout %q stderr %q err %v", out, stderr, err)
	}
	var unresolved acquisition.EvaluationCorpus
	data, err := os.ReadFile(filepath.Join(unresolvedDir, "corpus.json"))
	if err != nil || json.Unmarshal(data, &unresolved) != nil || len(unresolved.Captures) != 1 || unresolved.Captures[0].Observation.Track.URI != "spotify:track:target" {
		t.Fatalf("unresolved corpus = %#v, read err %v", unresolved, err)
	}

	auditDir := filepath.Join(t.TempDir(), "audit")
	out, stderr, err = runCLI(t, home, "acquire", "capture-batch", "--state", "complete", "--output-dir", auditDir, "--limit", "1")
	if err != nil || stderr != "" || !strings.HasPrefix(out, "Batch capture: 1 attempted, 1 captured") {
		t.Fatalf("complete batch = stdout %q stderr %q err %v", out, stderr, err)
	}
	var audit acquisition.EvaluationCorpus
	data, err = os.ReadFile(filepath.Join(auditDir, "corpus.json"))
	if err != nil || json.Unmarshal(data, &audit) != nil || len(audit.Captures) != 1 || audit.Captures[0].Observation.Track.URI != "spotify:track:audit" {
		t.Fatalf("audit corpus = %#v, read err %v", audit, err)
	}
}

func TestCLIAcquireCaptureBatchIsolatesAndExplicitlyRetriesSearchFailures(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-batch-fail-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	tools := t.TempDir()
	allowBad := filepath.Join(tools, "allow-bad")
	toolBody := fmt.Sprintf(`if [ "${1:-}" = "--version" ]; then
  printf '%%s\n' 'test-version'
elif printf '%%s' "$*" | grep -q 'bad' && [ ! -f %q ]; then
  exit 1
else
  printf '%%s\n' '{"entries":[]}'
fi`, allowBad)
	_ = writeCLITool(t, tools, "yt-dlp", toolBody)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("retriever must not run")
	}))
	defer stop()
	good := desired.Track{URI: "spotify:track:good", Name: "good", Artists: []desired.NamedURI{{Name: "Artist"}}, DurationMS: 1000}
	bad := desired.Track{URI: "spotify:track:bad", Name: "bad", Artists: []desired.NamedURI{{Name: "Artist"}}, DurationMS: 1000}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: []desired.CandidateEntry{{Position: 0, Kind: desired.EntrySupported, Track: &good}, {Position: 1, Kind: desired.EntrySupported, Track: &bad}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, uri := range []string{bad.URI, good.URI} {
		if _, err := d.DB.ExecContext(context.Background(), `INSERT INTO acquisition_work(track_uri, source_kind, state, error, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`, uri, db.AcquisitionSourceYouTube, db.AcquisitionUnresolved, now, now); err != nil {
			t.Fatal(err)
		}
	}
	outputDir := filepath.Join(t.TempDir(), "captures")

	out, stderr, err := runCLI(t, home, "acquire", "capture-batch", "--output-dir", outputDir)
	if err != nil || stderr != "" || out != "Batch capture: 2 attempted, 1 captured, 0 reused, 0 skipped, 1 search failures, 0 retrieval failures.\n" {
		t.Fatalf("failed batch = stdout %q stderr %q err %v", out, stderr, err)
	}
	badPath := filepath.Join(outputDir, captureBatchFilename(bad.URI))
	badData, err := os.ReadFile(badPath)
	if err != nil {
		t.Fatal(err)
	}
	var failed acquisition.EvaluationCorpus
	if err := json.Unmarshal(badData, &failed); err != nil || acquisition.ValidateEvaluationCorpus(failed) != nil || failed.Captures[0].Failure == nil || failed.Captures[0].Failure.Stage != acquisition.FailureSearch {
		t.Fatalf("failure capture = %#v, decode err %v", failed, err)
	}
	failed.Captures[0].Annotations.Notes = "manual failure note"
	badData, err = json.MarshalIndent(failed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	badData = append(badData, '\n')
	if err := os.WriteFile(badPath, badData, 0o600); err != nil {
		t.Fatal(err)
	}
	var combined acquisition.EvaluationCorpus
	combinedData, err := os.ReadFile(filepath.Join(outputDir, "corpus.json"))
	if err != nil || json.Unmarshal(combinedData, &combined) != nil || len(combined.Captures) != 2 {
		t.Fatalf("combined after failure = %#v, read err %v", combined, err)
	}

	if err := os.WriteFile(allowBad, []byte("allowed"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err = runCLI(t, home, "acquire", "capture-batch", "--output-dir", outputDir)
	if err != nil || stderr != "" || out != "Batch capture: 0 attempted, 0 captured, 1 reused, 1 skipped, 0 search failures, 0 retrieval failures.\n" {
		t.Fatalf("default failure resume = stdout %q stderr %q err %v", out, stderr, err)
	}
	if after, err := os.ReadFile(badPath); err != nil || !bytes.Equal(after, badData) {
		t.Fatalf("default resume replaced failure: equal=%v err=%v", bytes.Equal(after, badData), err)
	}

	out, stderr, err = runCLI(t, home, "acquire", "capture-batch", "--output-dir", outputDir, "--retry-failed")
	if err != nil || stderr != "" || out != "Batch capture: 1 attempted, 1 captured, 1 reused, 0 skipped, 0 search failures, 0 retrieval failures.\n" {
		t.Fatalf("explicit failure retry = stdout %q stderr %q err %v", out, stderr, err)
	}
	badData, err = os.ReadFile(badPath)
	failed = acquisition.EvaluationCorpus{}
	if err != nil || json.Unmarshal(badData, &failed) != nil || failed.Captures[0].Observation == nil || failed.Captures[0].Failure != nil || failed.Captures[0].Annotations.Notes != "manual failure note" {
		t.Fatalf("retried capture = %#v, read err %v", failed, err)
	}
}

func TestCLIAcquireCaptureBatchSplitsLargeCorpusWithoutScanningUnrelatedFiles(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-batch-chunks-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("retriever must not run")
	}))
	defer stop()
	outputDir := filepath.Join(t.TempDir(), "captures")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "unrelated.json"), []byte(`{"do_not_include":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index := 0; index < 9; index++ {
		uri := fmt.Sprintf("spotify:track:large%02d", index)
		if _, err := d.DB.ExecContext(context.Background(), `INSERT INTO acquisition_work(track_uri, source_kind, state, error, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`, uri, db.AcquisitionSourceYouTube, db.AcquisitionUnresolved, now, now); err != nil {
			t.Fatal(err)
		}
		videoID := fmt.Sprintf("video%06d", index)
		corpus := acquisition.EvaluationCorpus{FormatVersion: acquisition.EvaluationFormatVersion, Captures: []acquisition.EvaluationCapture{{
			CaptureID: uri,
			Provenance: acquisition.CaptureProvenance{
				CapturedBy: acquisition.CaptureTool{Name: "offbeat", Version: "test"},
				Producer: &acquisition.InspectionProducer{
					DecisionTool: acquisition.CaptureTool{Name: "offbeatd", Version: "test"},
					SearchTool:   acquisition.CaptureTool{Name: "yt-dlp", Version: "test"},
					Environment:  acquisition.CaptureEnvironment{OS: "test", Architecture: "test"},
				},
			},
			Observation: &acquisition.ResolutionObservation{
				CapturedAt: now,
				Track:      acquisition.InspectionTrack{URI: uri, Title: "Song", Artists: []acquisition.InspectionNamedURI{{Name: "Artist"}}, DurationMS: 1000},
				Query:      acquisition.YouTubeQuery{Text: "Artist - Song", DurationMS: 1000, VersionMarkers: []string{}},
				Search:     acquisition.InspectionSearch{CandidateLimit: acquisition.MaxYouTubeSearchCandidates, RawResults: []acquisition.YouTubeSearchResult{{ID: videoID, Title: strings.Repeat("x", 1_000_000), Duration: 1}}},
			},
			Annotations: acquisition.HumanAnnotations{Candidates: []acquisition.CandidateAnnotation{}},
		}}}
		data, err := json.Marshal(corpus)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outputDir, captureBatchFilename(uri)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out, stderr, err := runCLI(t, home, "acquire", "capture-batch", "--output-dir", outputDir)
	if err != nil || stderr != "" || out != "Batch capture: 0 attempted, 0 captured, 9 reused, 0 skipped, 0 search failures, 0 retrieval failures.\n" {
		t.Fatalf("chunked batch = stdout %q stderr %q err %v", out, stderr, err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "corpus.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized corpus.json exists: %v", err)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	var chunks []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "corpus-") && strings.HasSuffix(entry.Name(), ".json") {
			chunks = append(chunks, entry.Name())
		}
	}
	if len(chunks) < 2 {
		t.Fatalf("chunk files = %v", chunks)
	}
	for _, chunk := range chunks {
		info, err := os.Stat(filepath.Join(outputDir, chunk))
		if err != nil || info.Size() > acquisition.MaxEvaluationCorpusBytes {
			t.Fatalf("chunk %s size=%d err=%v", chunk, info.Size(), err)
		}
		data, err := os.ReadFile(filepath.Join(outputDir, chunk))
		if err != nil {
			t.Fatal(err)
		}
		var corpus acquisition.EvaluationCorpus
		if err := json.Unmarshal(data, &corpus); err != nil || acquisition.ValidateEvaluationCorpus(corpus) != nil {
			t.Fatalf("chunk %s is not replayable: %v", chunk, err)
		}
		for _, capture := range corpus.Captures {
			if capture.CaptureID == "unrelated" {
				t.Fatal("unrelated output file was included")
			}
		}
	}
}

func TestCLIAcquireCaptureBatchInterruptCancelsActiveSearchBeforeNextTrack(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-batch-cancel-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	tools := t.TempDir()
	started := filepath.Join(tools, "started")
	finished := filepath.Join(tools, "finished")
	calls := filepath.Join(tools, "calls")
	body := fmt.Sprintf(`if [ "${1:-}" = "--version" ]; then
  printf '%%s\n' 'test-version'
  exit 0
fi
printf 'search\n' >> %q
printf '%%s' "$$" > %q
sleep 30
touch %q
printf '%%s\n' '{"entries":[]}'`, calls, started, finished)
	_ = writeCLITool(t, tools, "yt-dlp", body)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("retriever must not run")
	}))
	defer stop()
	entries := make([]desired.CandidateEntry, 0, 2)
	now := time.Now().UTC()
	for index, id := range []string{"cancel-a", "cancel-b"} {
		track := desired.Track{URI: "spotify:track:" + id, Name: id, Artists: []desired.NamedURI{{Name: "Artist"}}, DurationMS: 1000}
		entries = append(entries, desired.CandidateEntry{Position: index, Kind: desired.EntrySupported, Track: &track})
	}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: entries}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cancel-a", "cancel-b"} {
		if _, err := d.DB.ExecContext(context.Background(), `INSERT INTO acquisition_work(track_uri, source_kind, state, error, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`, "spotify:track:"+id, db.AcquisitionSourceYouTube, db.AcquisitionUnresolved, now, now); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(offbeatPath, "-home", home, "acquire", "capture-batch", "--output-dir", filepath.Join(t.TempDir(), "captures"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("batch search did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	interruptedAt := time.Now()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatalf("interrupted batch succeeded: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if elapsed := time.Since(interruptedAt); elapsed > 3*time.Second {
		t.Fatalf("interrupted batch took %v", elapsed)
	}
	pidData, err := os.ReadFile(started)
	if err != nil {
		t.Fatal(err)
	}
	toolPID, err := strconv.Atoi(string(pidData))
	if err != nil {
		t.Fatal(err)
	}
	toolDeadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(toolPID, 0) == nil && time.Now().Before(toolDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(toolPID, 0); err == nil {
		t.Fatalf("active search process %d survived CLI cancellation", toolPID)
	}
	if _, err := os.Stat(finished); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active search was not canceled: %v", err)
	}
	callData, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(callData), "search\n") != 1 {
		t.Fatalf("search calls = %q, err %v", callData, err)
	}
}

func TestCLIAcquireCaptureBatchSkipsTrackRemovedFromDesiredState(t *testing.T) {
	home, err := os.MkdirTemp("", "offbeat-batch-removed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("retriever must not run")
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "removed")
	now := time.Now().UTC()
	if _, err := d.DB.ExecContext(context.Background(), `INSERT INTO acquisition_work(track_uri, source_kind, state, error, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`, "spotify:track:removed", db.AcquisitionSourceYouTube, db.AcquisitionUnresolved, now, now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{}); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(t.TempDir(), "captures")

	out, stderr, err := runCLI(t, home, "acquire", "capture-batch", "--output-dir", outputDir)
	if err != nil || stderr != "" || out != "Batch capture: 1 attempted, 0 captured, 0 reused, 1 skipped, 0 search failures, 0 retrieval failures.\n" {
		t.Fatalf("removed batch = stdout %q stderr %q err %v", out, stderr, err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, captureBatchFilename("spotify:track:removed"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed track produced capture: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outputDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary captureBatchSummary
	if err := json.Unmarshal(data, &summary); err != nil || summary.SkipReasons["not_currently_desired"] != 1 || summary.SearchFailures != 0 {
		t.Fatalf("removed summary = %#v, decode err %v", summary, err)
	}
}

func TestCLIAcquireDoesNotRetryUnknownOutcome(t *testing.T) {
	home := t.TempDir()
	socketDir := filepath.Join(home, "control")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".config", "offbeat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[paths]\nsocket_dir = %q\n", socketDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", app.SocketPath(socketDir))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan int, 1)
	go func() {
		count := 0
		conn, err := listener.Accept()
		if err == nil {
			count++
			_, _ = ipc.ReadFrame(bufio.NewReader(conn))
			_ = conn.Close() // Deliberately drop the response after receiving the mutation.
		}
		_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(200 * time.Millisecond))
		if conn, err := listener.Accept(); err == nil {
			count++
			_ = conn.Close()
		}
		done <- count
	}()

	out, stderr, err := runCLI(t, home, "acquire", "spotify:track:one", "http://127.0.0.1:8080/owned.wav")
	if err == nil || out != "" || !strings.Contains(stderr, "unknown outcome") {
		t.Fatalf("acquire = stdout %q stderr %q err %v", out, stderr, err)
	}
	if count := <-done; count != 1 {
		t.Fatalf("control requests = %d, want one", count)
	}
}

func startCLIAcquisitionDaemon(t *testing.T, home string, retriever acquisition.Retriever, resolver ...acquisition.Resolver) (*app.Daemon, func()) {
	t.Helper()
	port := reserveCLIAdapterPort(t)
	configPath := filepath.Join(home, ".config", "offbeat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[spotify_adapter]\nport = %d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	options := app.Options{HomeDir: home, Version: "test-daemon", AdapterCredential: "test-credential", Retriever: retriever}
	if len(resolver) > 0 {
		options.Resolver = resolver[0]
		if inspector, ok := resolver[0].(acquisition.Inspector); ok {
			options.Inspector = inspector
		}
	}
	d, err := app.NewDaemon(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, app.RunOptions{SignalCh: make(chan os.Signal)}) }()
	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		cancel()
		t.Fatal(err)
	}
	return d, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	}
}

func seedCLIAcquisitionTrack(t *testing.T, d *app.Daemon, id string) {
	t.Helper()
	track := desired.Track{URI: "spotify:track:" + id, Name: id, Artists: []desired.NamedURI{{URI: "spotify:artist:one", Name: "Artist"}}, Album: desired.NamedURI{URI: "spotify:album:one", Name: "Album"}, DurationMS: 1000}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
}

func waitCLIAcquisitionState(t *testing.T, d *app.Daemon, id int64, state string) db.AcquisitionWork {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		work, err := d.DB.Acquisition(context.Background(), id)
		if err == nil && work.State == state {
			return work
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("acquisition %d did not become %s", id, state)
	return db.AcquisitionWork{}
}

func writeCLITool(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
