package app

import (
	"context"
	"strings"
	"testing"

	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func TestSyntheticFixturesRequireExplicitOptIn(t *testing.T) {
	d := startAdapterDaemon(t)
	if _, err := d.RegisterSyntheticTrackFixture(context.Background(), "spotify:track:one"); err == nil {
		t.Fatal("synthetic fixtures enabled by default")
	}
	response := sendRaw(t, SocketPath(d.socketDir), []byte(`{"version":1,"command":"managed.fixture"}`+"\n"))
	if response.Error == nil || response.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("fixture mechanism exposed over Control protocol: %#v", response)
	}
}

func TestMissingContinuationValidationAndStateChange(t *testing.T) {
	d := startAdapterDaemon(t)
	for _, raw := range []string{
		`{"version":1,"command":"missing","missing":null}`,
		`{"version":1,"command":"missing","missing":{}}`,
		`{"version":1,"command":"missing","missing":{"after_uri":"track"}}`,
		`{"version":1,"command":"missing","missing":{"after_uri":"track","state_revision":-1}}`,
		`{"version":1,"command":"missing","missing":{"after_uri":"track","state_revision":0,"extra":true}}`,
		`{"version":1,"command":"status","missing":{"after_uri":"track","state_revision":0}}`,
	} {
		response := sendRaw(t, SocketPath(d.socketDir), []byte(raw+"\n"))
		if response.Error == nil || response.Error.Code != ipc.CodeInvalidRequest {
			t.Fatalf("request %s: %#v", raw, response)
		}
	}
	// A continuation from state 0 must be rejected after a state 1 commit.
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{}); err != nil {
		t.Fatal(err)
	}
	response := sendRaw(t, SocketPath(d.socketDir), []byte(`{"version":1,"command":"missing","missing":{"after_uri":"spotify:track:one","state_revision":0}}`+"\n"))
	if response.Error == nil || response.Error.Code != ipc.CodeFailedPrecondition || !strings.Contains(response.Error.Message, "state changed") {
		t.Fatalf("mixed-state continuation: %#v", response)
	}
}

func TestMissingOversizedMetadataReturnsStructuredError(t *testing.T) {
	d := startAdapterDaemon(t)
	track := desired.Track{URI: "spotify:track:one", Name: strings.Repeat("x", ipc.MaxMessageBytes), Artists: []desired.NamedURI{{URI: "artist", Name: "Artist"}}, Album: desired.NamedURI{URI: "album", Name: "Album"}, DurationMS: 1000}
	if _, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{LikedSongs: []desired.CandidateEntry{{Kind: desired.EntrySupported, Track: &track}}}); err != nil {
		t.Fatal(err)
	}
	response := sendRaw(t, SocketPath(d.socketDir), []byte(`{"version":1,"command":"missing"}`+"\n"))
	if response.Error == nil || response.Error.Code != ipc.CodeInternal || !strings.Contains(response.Error.Message, "metadata limit") {
		t.Fatalf("oversized metadata: %#v", response)
	}
}
