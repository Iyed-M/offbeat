package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Iyed-M/offbeat/internal/desired"
)

func TestParseCandidateSnapshotMaterializesNormalizedCandidate(t *testing.T) {
	candidate, err := parseCandidateSnapshot(json.RawMessage(`{"kind":"candidate","playlists":[{"uri":"spotify:playlist:one","name":"One","entries":[{"position":0,"kind":"supported","track":{"uri":"spotify:track:one","name":"Track One","artists":[{"uri":"spotify:artist:one","name":"Artist One"}],"album":{"uri":"spotify:album:one","name":"Album One"},"duration_ms":1234}},{"position":1,"kind":"unsupported","source_uri":"spotify:episode:one"},{"position":2,"kind":"supported","track":{"uri":"spotify:track:one","name":"Track One","artists":[{"uri":"spotify:artist:one","name":"Artist One"}],"album":{"uri":"spotify:album:one","name":"Album One"},"duration_ms":1234}}]}],"liked_songs":{"entries":[{"position":0,"kind":"unsupported"}]}}`))
	if err != nil {
		t.Fatalf("parse candidate: %v", err)
	}
	if len(candidate.Playlists) != 1 || candidate.Playlists[0].Position != 0 || len(candidate.Playlists[0].Entries) != 3 {
		t.Fatalf("playlists = %#v", candidate.Playlists)
	}
	entry := candidate.Playlists[0].Entries[0]
	if entry.Kind != desired.EntrySupported || entry.Track == nil || entry.Track.URI != "spotify:track:one" || entry.Track.Artists[0].URI != "spotify:artist:one" {
		t.Fatalf("supported entry = %#v", entry)
	}
	placeholder := candidate.Playlists[0].Entries[1]
	if placeholder.Kind != desired.EntryUnsupported || placeholder.SourceURI != "spotify:episode:one" || placeholder.Track != nil {
		t.Fatalf("unsupported entry = %#v", placeholder)
	}
}

func TestParseCandidateSnapshotRejectsInvalidAndConflictingTracks(t *testing.T) {
	for _, snapshot := range []string{
		`{"kind":"candidate","playlists":[],"liked_songs":{"entries":[{"position":1,"kind":"unsupported"}]}}`,
		`{"kind":"candidate","playlists":[],"liked_songs":{"entries":[{"position":0,"kind":"supported","track":{"uri":"spotify:track:one","name":"One","artists":[],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1}}]}}`,
		`{"kind":"candidate","playlists":[],"liked_songs":{"entries":[{"position":0,"kind":"supported","track":{"uri":"spotify:track:one","name":"One","artists":[{"uri":"spotify:artist:one","name":"Artist"}],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1}},{"position":1,"kind":"supported","track":{"uri":"spotify:track:one","name":"Renamed","artists":[{"uri":"spotify:artist:one","name":"Artist"}],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1}}]}}`,
	} {
		if _, err := parseCandidateSnapshot(json.RawMessage(snapshot)); err == nil {
			t.Fatal("parse succeeded for invalid candidate")
		}
	}
	_, err := parseCandidateSnapshot(json.RawMessage(`{"kind":"candidate","playlists":[],"liked_songs":{"entries":[{"position":0,"kind":"unsupported","source_uri":""}]}}`))
	if err == nil || !strings.Contains(err.Error(), "source URI") {
		t.Fatalf("error = %v, want invalid source URI", err)
	}
}
