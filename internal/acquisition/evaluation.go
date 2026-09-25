package acquisition

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
)

const (
	EvaluationFormatVersion  = 1
	MaxEvaluationCorpusBytes = 8 << 20
)

type CandidateLabel string

const (
	CandidateAcceptable CandidateLabel = "acceptable"
	CandidateIncorrect  CandidateLabel = "incorrect"
	CandidateUnknown    CandidateLabel = "unknown"
)

type FailureStage string

const (
	FailureSearch    FailureStage = "search"
	FailureRetrieval FailureStage = "retrieval"
)

type EvaluationOutcome string

const (
	OutcomeCorrectAutomatic      EvaluationOutcome = "correct_automatic_selection"
	OutcomeIncorrectAutomatic    EvaluationOutcome = "incorrect_automatic_selection"
	OutcomeAppropriateUnresolved EvaluationOutcome = "appropriate_unresolved"
	OutcomeAvoidableUnresolved   EvaluationOutcome = "avoidable_unresolved"
	OutcomeUnknownOrUnlabeled    EvaluationOutcome = "unknown_or_unlabeled"
	OutcomeSearchFailure         EvaluationOutcome = "search_failure"
	OutcomeRetrievalFailure      EvaluationOutcome = "retrieval_failure"
)

// EvaluationCorpus is a user-controlled offline benchmark artifact. It is
// deliberately independent of Acquisition work and is never persisted by the
// daemon owner.
type EvaluationCorpus struct {
	FormatVersion int                 `json:"format_version"`
	Description   string              `json:"description,omitempty"`
	Captures      []EvaluationCapture `json:"captures"`
}

type EvaluationCapture struct {
	CaptureID   string                 `json:"capture_id"`
	Provenance  CaptureProvenance      `json:"provenance"`
	Observation *ResolutionObservation `json:"observation,omitempty"`
	Failure     *CapturedFailure       `json:"failure,omitempty"`
	Annotations HumanAnnotations       `json:"annotations"`
}

type CaptureProvenance struct {
	CapturedBy CaptureTool         `json:"captured_by"`
	Producer   *InspectionProducer `json:"producer,omitempty"`
	Notes      string              `json:"notes,omitempty"`
}

type CaptureTool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type CaptureEnvironment struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Notes        string `json:"notes,omitempty"`
}

type ResolutionObservation struct {
	CapturedAt       time.Time          `json:"captured_at"`
	Track            InspectionTrack    `json:"track"`
	Query            YouTubeQuery       `json:"query"`
	Search           InspectionSearch   `json:"search"`
	RecordedDecision ResolutionDecision `json:"recorded_decision"`
}

type CapturedFailure struct {
	Stage   FailureStage `json:"stage"`
	Message string       `json:"message,omitempty"`
}

type HumanAnnotations struct {
	Candidates []CandidateAnnotation `json:"candidates"`
	Notes      string                `json:"notes,omitempty"`
}

type CandidateAnnotation struct {
	VideoID    string         `json:"video_id"`
	Label      CandidateLabel `json:"label"`
	Evidence   string         `json:"evidence"`
	Provenance string         `json:"provenance"`
}

type EvaluationCounts struct {
	CorrectAutomaticSelections   int `json:"correct_automatic_selections"`
	IncorrectAutomaticSelections int `json:"incorrect_automatic_selections"`
	AppropriateUnresolved        int `json:"appropriate_unresolved"`
	AvoidableUnresolved          int `json:"avoidable_unresolved"`
	UnknownOrUnlabeled           int `json:"unknown_or_unlabeled"`
	SearchFailures               int `json:"search_failures"`
	RetrievalFailures            int `json:"retrieval_failures"`
}

type EvaluationReport struct {
	FormatVersion int                    `json:"format_version"`
	PolicyVersion int                    `json:"policy_version"`
	Total         int                    `json:"total"`
	Counts        EvaluationCounts       `json:"counts"`
	Cases         []EvaluationCaseResult `json:"cases"`
}

type EvaluationCaseResult struct {
	CaptureID               string                `json:"capture_id"`
	Outcome                 EvaluationOutcome     `json:"outcome"`
	DecisionMatchesRecorded *bool                 `json:"decision_matches_recorded,omitempty"`
	Replay                  *ResolutionInspection `json:"replay,omitempty"`
	Failure                 *CapturedFailure      `json:"failure,omitempty"`
}

// NewEvaluationCorpus records a successful fresh inspection without adding
// any human judgment. The evaluator must fill annotations independently.
func NewEvaluationCorpus(captureID, toolVersion string, report ResolutionInspection) (EvaluationCorpus, error) {
	if !report.FreshSearch || report.CapturedAt.IsZero() {
		return EvaluationCorpus{}, errors.New("capture requires a timestamped fresh inspection")
	}
	if report.Producer == nil {
		return EvaluationCorpus{}, errors.New("capture requires inspection producer provenance")
	}
	if captureID == "" {
		captureID = report.Track.URI
	}
	producer := *report.Producer
	corpus := EvaluationCorpus{FormatVersion: EvaluationFormatVersion, Captures: []EvaluationCapture{{
		CaptureID: captureID,
		Provenance: CaptureProvenance{
			CapturedBy: CaptureTool{Name: "offbeat", Version: toolVersion},
			Producer:   &producer,
		},
		Observation: &ResolutionObservation{
			CapturedAt:       report.CapturedAt,
			Track:            report.Track,
			Query:            report.Query,
			Search:           report.Search,
			RecordedDecision: report.Decision,
		},
		Annotations: HumanAnnotations{Candidates: []CandidateAnnotation{}},
	}}}
	if err := ValidateEvaluationCorpus(corpus); err != nil {
		return EvaluationCorpus{}, err
	}
	return corpus, nil
}

// ReplayYouTubeObservation evaluates frozen results through the current
// production selection policy. FreshSearch is false by definition.
func ReplayYouTubeObservation(observation ResolutionObservation) ResolutionInspection {
	report := inspectYouTubeCandidates(inspectionDesiredTrack(observation.Track), observation.Query, observation.Search.RawResults, observation.CapturedAt)
	report.FreshSearch = false
	return report
}

func inspectionDesiredTrack(track InspectionTrack) desired.Track {
	artists := make([]desired.NamedURI, 0, len(track.Artists))
	for _, artist := range track.Artists {
		artists = append(artists, desired.NamedURI{URI: artist.URI, Name: artist.Name})
	}
	return desired.Track{
		URI:        track.URI,
		Name:       track.Title,
		Artists:    artists,
		Album:      desired.NamedURI{URI: track.Album.URI, Name: track.Album.Name},
		DurationMS: track.DurationMS,
	}
}

func ReplayEvaluationCorpus(corpus EvaluationCorpus) (EvaluationReport, error) {
	if err := ValidateEvaluationCorpus(corpus); err != nil {
		return EvaluationReport{}, err
	}
	report := EvaluationReport{
		FormatVersion: EvaluationFormatVersion,
		PolicyVersion: ResolutionInspectionVersion,
		Total:         len(corpus.Captures),
		Cases:         make([]EvaluationCaseResult, 0, len(corpus.Captures)),
	}
	for _, capture := range corpus.Captures {
		result := EvaluationCaseResult{CaptureID: capture.CaptureID}
		if capture.Failure != nil {
			failure := *capture.Failure
			result.Failure = &failure
			switch failure.Stage {
			case FailureSearch:
				result.Outcome = OutcomeSearchFailure
				report.Counts.SearchFailures++
			case FailureRetrieval:
				result.Outcome = OutcomeRetrievalFailure
				report.Counts.RetrievalFailures++
			}
			report.Cases = append(report.Cases, result)
			continue
		}
		replay := ReplayYouTubeObservation(*capture.Observation)
		result.Replay = &replay
		matches := reflect.DeepEqual(replay.Decision, capture.Observation.RecordedDecision)
		result.DecisionMatchesRecorded = &matches
		result.Outcome = classifyEvaluationOutcome(replay, capture.Annotations)
		switch result.Outcome {
		case OutcomeCorrectAutomatic:
			report.Counts.CorrectAutomaticSelections++
		case OutcomeIncorrectAutomatic:
			report.Counts.IncorrectAutomaticSelections++
		case OutcomeAppropriateUnresolved:
			report.Counts.AppropriateUnresolved++
		case OutcomeAvoidableUnresolved:
			report.Counts.AvoidableUnresolved++
		default:
			report.Counts.UnknownOrUnlabeled++
		}
		report.Cases = append(report.Cases, result)
	}
	return report, nil
}

func classifyEvaluationOutcome(replay ResolutionInspection, annotations HumanAnnotations) EvaluationOutcome {
	labels := make(map[string]CandidateLabel, len(annotations.Candidates))
	for _, annotation := range annotations.Candidates {
		labels[annotation.VideoID] = annotation.Label
	}
	if replay.Decision.SelectedURL != "" {
		switch labels[replay.Decision.WinnerVideoID] {
		case CandidateAcceptable:
			return OutcomeCorrectAutomatic
		case CandidateIncorrect:
			return OutcomeIncorrectAutomatic
		default:
			return OutcomeUnknownOrUnlabeled
		}
	}
	uniqueIDs := make(map[string]struct{}, len(replay.Search.RawResults))
	for _, candidate := range replay.Search.RawResults {
		uniqueIDs[candidate.ID] = struct{}{}
	}
	if len(uniqueIDs) == 0 {
		return OutcomeAppropriateUnresolved
	}
	allIncorrect := true
	hasAcceptable := false
	hasUnknown := false
	for id := range uniqueIDs {
		switch labels[id] {
		case CandidateAcceptable:
			hasAcceptable = true
			allIncorrect = false
		case CandidateIncorrect:
		default:
			allIncorrect = false
			hasUnknown = true
		}
	}
	if hasUnknown {
		return OutcomeUnknownOrUnlabeled
	}
	if hasAcceptable {
		return OutcomeAvoidableUnresolved
	}
	if allIncorrect {
		return OutcomeAppropriateUnresolved
	}
	return OutcomeUnknownOrUnlabeled
}

func ValidateEvaluationCorpus(corpus EvaluationCorpus) error {
	if corpus.FormatVersion != EvaluationFormatVersion {
		return fmt.Errorf("unsupported evaluation format_version %d", corpus.FormatVersion)
	}
	if len(corpus.Captures) == 0 {
		return errors.New("evaluation corpus requires at least one capture")
	}
	seenCaptures := make(map[string]struct{}, len(corpus.Captures))
	for index, capture := range corpus.Captures {
		prefix := fmt.Sprintf("captures[%d]", index)
		if strings.TrimSpace(capture.CaptureID) == "" {
			return fmt.Errorf("%s requires capture_id", prefix)
		}
		if _, exists := seenCaptures[capture.CaptureID]; exists {
			return fmt.Errorf("duplicate capture_id %q", capture.CaptureID)
		}
		seenCaptures[capture.CaptureID] = struct{}{}
		if strings.TrimSpace(capture.Provenance.CapturedBy.Name) == "" || strings.TrimSpace(capture.Provenance.CapturedBy.Version) == "" {
			return fmt.Errorf("%s requires capture tool name and version", prefix)
		}
		if (capture.Observation == nil) == (capture.Failure == nil) {
			return fmt.Errorf("%s requires exactly one observation or failure", prefix)
		}
		if capture.Failure != nil && capture.Failure.Stage != FailureSearch && capture.Failure.Stage != FailureRetrieval {
			return fmt.Errorf("%s has unsupported failure stage %q", prefix, capture.Failure.Stage)
		}
		if capture.Observation != nil {
			producer := capture.Provenance.Producer
			if producer == nil || strings.TrimSpace(producer.DecisionTool.Name) == "" || strings.TrimSpace(producer.DecisionTool.Version) == "" {
				return fmt.Errorf("%s observation requires decision tool name and version", prefix)
			}
			if strings.TrimSpace(producer.SearchTool.Name) == "" || strings.TrimSpace(producer.SearchTool.Version) == "" {
				return fmt.Errorf("%s observation requires search tool name and version", prefix)
			}
			if strings.TrimSpace(producer.Environment.OS) == "" || strings.TrimSpace(producer.Environment.Architecture) == "" {
				return fmt.Errorf("%s observation requires producer OS and architecture", prefix)
			}
			observation := capture.Observation
			if observation.CapturedAt.IsZero() {
				return fmt.Errorf("%s observation requires capture timestamp", prefix)
			}
			if observation.Track.URI == "" || observation.Track.Title == "" || len(observation.Track.Artists) == 0 || observation.Track.DurationMS <= 0 {
				return fmt.Errorf("%s observation requires exact desired track metadata", prefix)
			}
			if observation.Query.Text == "" || observation.Query.DurationMS <= 0 {
				return fmt.Errorf("%s observation requires the captured query", prefix)
			}
			if observation.Search.CandidateLimit != MaxYouTubeSearchCandidates || len(observation.Search.RawResults) > observation.Search.CandidateLimit {
				return fmt.Errorf("%s observation has an invalid bounded result set", prefix)
			}
		}
		capturedIDs := make(map[string]struct{})
		if capture.Observation != nil {
			for _, candidate := range capture.Observation.Search.RawResults {
				capturedIDs[candidate.ID] = struct{}{}
			}
		}
		seenAnnotations := make(map[string]struct{}, len(capture.Annotations.Candidates))
		for annotationIndex, annotation := range capture.Annotations.Candidates {
			annotationPrefix := fmt.Sprintf("%s.annotations.candidates[%d]", prefix, annotationIndex)
			if !youtubeIDPattern.MatchString(annotation.VideoID) {
				return fmt.Errorf("%s has invalid video_id", annotationPrefix)
			}
			if _, exists := seenAnnotations[annotation.VideoID]; exists {
				return fmt.Errorf("%s duplicates video_id %q", annotationPrefix, annotation.VideoID)
			}
			seenAnnotations[annotation.VideoID] = struct{}{}
			if _, captured := capturedIDs[annotation.VideoID]; !captured {
				return fmt.Errorf("%s video_id is not in the frozen result set", annotationPrefix)
			}
			if annotation.Label != CandidateAcceptable && annotation.Label != CandidateIncorrect && annotation.Label != CandidateUnknown {
				return fmt.Errorf("%s has unsupported label %q", annotationPrefix, annotation.Label)
			}
			if strings.TrimSpace(annotation.Evidence) == "" || strings.TrimSpace(annotation.Provenance) == "" {
				return fmt.Errorf("%s requires evidence and provenance", annotationPrefix)
			}
		}
	}
	return nil
}
