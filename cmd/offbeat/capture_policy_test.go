package main

import (
	"testing"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/config"
)

func TestBatchSearchFailureIdentifiesPolicyInReplay(t *testing.T) {
	corpus := newBatchFailureCorpus("spotify:track:one", "timeout", config.AmbiguityAutoBest, acquisition.HumanAnnotations{Candidates: []acquisition.CandidateAnnotation{}})
	report, err := acquisition.ReplayEvaluationCorpus(corpus)
	if err != nil {
		t.Fatal(err)
	}
	if report.Policy != config.AmbiguityAutoBest || report.Cases[0].Outcome != acquisition.OutcomeSearchFailure {
		t.Fatalf("report = %#v", report)
	}
}
