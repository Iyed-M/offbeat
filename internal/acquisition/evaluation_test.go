package acquisition

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func evaluationTestProvenance() CaptureProvenance {
	return CaptureProvenance{
		CapturedBy: CaptureTool{Name: "offbeat", Version: "test"},
		Producer: &InspectionProducer{
			DecisionTool: CaptureTool{Name: "offbeatd", Version: "test"},
			SearchTool:   CaptureTool{Name: "yt-dlp", Version: "test"},
			Environment:  CaptureEnvironment{OS: "test", Architecture: "test"},
		},
	}
}

func TestReplayEvaluationCorpusAccountsForVerifiedAndUnknownOutcomes(t *testing.T) {
	capturedAt := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	track := InspectionTrack{
		URI:        "spotify:track:evaluation",
		Title:      "Song",
		Artists:    []InspectionNamedURI{{URI: "spotify:artist:one", Name: "Artist"}},
		Album:      InspectionNamedURI{URI: "spotify:album:one", Name: "Album"},
		DurationMS: 200_000,
	}
	candidate := func(id string) YouTubeSearchResult {
		return YouTubeSearchResult{ID: id, Title: "Artist - Song", Uploader: "Artist", Duration: 200}
	}
	observation := func(results ...YouTubeSearchResult) ResolutionInspection {
		return ReplayYouTubeObservation(ResolutionObservation{
			CapturedAt: capturedAt,
			Track:      track,
			Query:      YouTubeQuery{Text: "Artist - Song", DurationMS: 200_000},
			Search:     InspectionSearch{CandidateLimit: MaxYouTubeSearchCandidates, RawResults: results},
		})
	}
	withDecision := func(captureID string, report ResolutionInspection, labels ...CandidateAnnotation) EvaluationCapture {
		return EvaluationCapture{
			CaptureID:  captureID,
			Provenance: evaluationTestProvenance(),
			Observation: &ResolutionObservation{
				CapturedAt:       report.CapturedAt,
				Track:            report.Track,
				Query:            report.Query,
				Search:           report.Search,
				RecordedDecision: report.Decision,
			},
			Annotations: HumanAnnotations{Candidates: labels},
		}
	}

	acceptableOne := candidate("acceptable1")
	acceptableTwo := candidate("acceptable2")
	incorrect := candidate("incorrect01")
	unknown := candidate("unknown0001")
	corpus := EvaluationCorpus{FormatVersion: EvaluationFormatVersion, Captures: []EvaluationCapture{
		withDecision("correct-multiple-acceptable", observation(acceptableOne, YouTubeSearchResult{ID: acceptableTwo.ID, Title: "Song", Uploader: "Label", Duration: 200}),
			CandidateAnnotation{VideoID: acceptableOne.ID, Label: CandidateAcceptable, Evidence: "independently verified recording", Provenance: "manual audit"},
			CandidateAnnotation{VideoID: acceptableTwo.ID, Label: CandidateAcceptable, Evidence: "independently verified recording", Provenance: "manual audit"},
		),
		withDecision("incorrect-selection", observation(incorrect),
			CandidateAnnotation{VideoID: incorrect.ID, Label: CandidateIncorrect, Evidence: "different recording", Provenance: "manual audit"},
		),
		withDecision("unknown-selection", observation(unknown),
			CandidateAnnotation{VideoID: unknown.ID, Label: CandidateUnknown, Evidence: "identity could not be established", Provenance: "manual audit"},
		),
		withDecision("avoidable-unresolved", observation(acceptableOne, acceptableTwo),
			CandidateAnnotation{VideoID: acceptableOne.ID, Label: CandidateAcceptable, Evidence: "same recording", Provenance: "manual audit"},
			CandidateAnnotation{VideoID: acceptableTwo.ID, Label: CandidateAcceptable, Evidence: "same recording", Provenance: "manual audit"},
		),
		withDecision("unknown-alternative-unresolved", observation(acceptableOne, unknown),
			CandidateAnnotation{VideoID: acceptableOne.ID, Label: CandidateAcceptable, Evidence: "same recording", Provenance: "manual audit"},
			CandidateAnnotation{VideoID: unknown.ID, Label: CandidateUnknown, Evidence: "identity could not be established", Provenance: "manual audit"},
		),
		withDecision("appropriate-unresolved", observation(
			YouTubeSearchResult{ID: "wrongmeta01", Title: "Other - Song", Uploader: "Other", Duration: 200},
		), CandidateAnnotation{VideoID: "wrongmeta01", Label: CandidateIncorrect, Evidence: "different recording", Provenance: "manual audit"}),
		withDecision("unlabeled-unresolved", observation(candidate("unlabeled01"))),
		{CaptureID: "search-failure", Provenance: evaluationTestProvenance(), Failure: &CapturedFailure{Stage: FailureSearch, Message: "tool unavailable"}},
		{CaptureID: "retrieval-failure", Provenance: evaluationTestProvenance(), Failure: &CapturedFailure{Stage: FailureRetrieval, Message: "retrieval failed"}},
	}}

	report, err := ReplayEvaluationCorpus(corpus)
	if err != nil {
		t.Fatalf("ReplayEvaluationCorpus: %v", err)
	}
	want := EvaluationCounts{
		CorrectAutomaticSelections:   1,
		IncorrectAutomaticSelections: 1,
		AppropriateUnresolved:        1,
		AvoidableUnresolved:          1,
		UnknownOrUnlabeled:           3,
		SearchFailures:               1,
		RetrievalFailures:            1,
	}
	if report.Counts != want || report.Total != 9 {
		t.Fatalf("counts = %#v total=%d, want %#v total=9", report.Counts, report.Total, want)
	}
	if report.Cases[0].Outcome != OutcomeCorrectAutomatic || report.Cases[0].DecisionMatchesRecorded == nil || !*report.Cases[0].DecisionMatchesRecorded {
		t.Fatalf("correct case = %#v", report.Cases[0])
	}
	if report.Cases[3].Outcome != OutcomeAvoidableUnresolved || report.Cases[3].Replay == nil || report.Cases[3].Replay.FreshSearch {
		t.Fatalf("avoidable replay = %#v", report.Cases[3])
	}
}

func TestValidateEvaluationCorpusRejectsUncapturedAnnotation(t *testing.T) {
	corpus := EvaluationCorpus{FormatVersion: EvaluationFormatVersion, Captures: []EvaluationCapture{{
		CaptureID:  "invalid-annotation",
		Provenance: evaluationTestProvenance(),
		Observation: &ResolutionObservation{
			CapturedAt: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC),
			Track:      InspectionTrack{URI: "spotify:track:one", Title: "Song", Artists: []InspectionNamedURI{{Name: "Artist"}}, DurationMS: 200_000},
			Query:      YouTubeQuery{Text: "Artist - Song", DurationMS: 200_000},
			Search:     InspectionSearch{CandidateLimit: MaxYouTubeSearchCandidates, RawResults: []YouTubeSearchResult{{ID: "captured001", Title: "Artist - Song", Duration: 200}}},
		},
		Annotations: HumanAnnotations{Candidates: []CandidateAnnotation{{VideoID: "notcapture1", Label: CandidateAcceptable, Evidence: "claim", Provenance: "source"}}},
	}}}
	if err := ValidateEvaluationCorpus(corpus); err == nil {
		t.Fatal("ValidateEvaluationCorpus accepted an annotation for an ID outside the frozen result set")
	}
}

func TestReplayEvaluationCorpusKeepsDistinctMatchingMetadataLabelsIndependent(t *testing.T) {
	track := InspectionTrack{URI: "spotify:track:one", Title: "Song", Artists: []InspectionNamedURI{{Name: "Artist"}}, DurationMS: 200_000}
	results := []YouTubeSearchResult{
		{ID: "aaaaaaaaaaa", Title: "Artist - Song", Uploader: "Artist", Duration: 200},
		{ID: "bbbbbbbbbbb", Title: "Artist - Song", Uploader: "Artist", Duration: 200},
	}
	capturedAt := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	recorded := ReplayYouTubeObservation(ResolutionObservation{CapturedAt: capturedAt, Track: track, Query: YouTubeQuery{Text: "Artist - Song", DurationMS: 200_000}, Search: InspectionSearch{CandidateLimit: MaxYouTubeSearchCandidates, RawResults: results}})
	corpus := EvaluationCorpus{FormatVersion: EvaluationFormatVersion, Captures: []EvaluationCapture{{
		CaptureID:   "same-metadata-different-recordings",
		Provenance:  evaluationTestProvenance(),
		Observation: &ResolutionObservation{CapturedAt: capturedAt, Track: track, Query: recorded.Query, Search: recorded.Search, RecordedDecision: recorded.Decision},
		Annotations: HumanAnnotations{Candidates: []CandidateAnnotation{
			{VideoID: "aaaaaaaaaaa", Label: CandidateAcceptable, Evidence: "verified same recording", Provenance: "manual audit"},
			{VideoID: "bbbbbbbbbbb", Label: CandidateIncorrect, Evidence: "verified different recording", Provenance: "manual audit"},
		}},
	}}}

	report, err := ReplayEvaluationCorpus(corpus)
	if err != nil {
		t.Fatalf("ReplayEvaluationCorpus: %v", err)
	}
	if report.Counts.AvoidableUnresolved != 1 || report.Cases[0].Outcome != OutcomeAvoidableUnresolved {
		t.Fatalf("report = %#v", report)
	}
}

func TestReplayEvaluationSyntheticFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "youtube-evaluation-synthetic-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus EvaluationCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	report, err := ReplayEvaluationCorpus(corpus)
	if err != nil {
		t.Fatalf("ReplayEvaluationCorpus: %v", err)
	}
	want := EvaluationCounts{
		IncorrectAutomaticSelections: 1,
		AppropriateUnresolved:        1,
		AvoidableUnresolved:          2,
		UnknownOrUnlabeled:           1,
		SearchFailures:               1,
		RetrievalFailures:            1,
	}
	if report.Total != 7 || report.Counts != want {
		t.Fatalf("fixture report = %#v, want total=7 counts=%#v", report, want)
	}
	for _, result := range report.Cases {
		if result.Failure == nil && (result.DecisionMatchesRecorded == nil || !*result.DecisionMatchesRecorded) {
			t.Errorf("%s did not reproduce its recorded decision: %#v", result.CaptureID, result.Replay.Decision)
		}
	}
}

func TestEvaluationFailureReportOmitsReplayAndDecisionComparison(t *testing.T) {
	corpus := EvaluationCorpus{FormatVersion: EvaluationFormatVersion, Captures: []EvaluationCapture{{
		CaptureID:  "search-failure",
		Provenance: CaptureProvenance{CapturedBy: CaptureTool{Name: "offbeat", Version: "test"}},
		Failure:    &CapturedFailure{Stage: FailureSearch},
	}}}
	report, err := ReplayEvaluationCorpus(corpus)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report.Cases[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"capture_id":"search-failure","outcome":"search_failure","failure":{"stage":"search"}}` {
		t.Fatalf("failure result = %s", data)
	}
}
