package app

import (
	"encoding/json"
	"fmt"
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

func TestTrackPresentationContract(t *testing.T) {
	base := `{"uri":"spotify:track:one","name":"One","artists":[{"uri":"spotify:artist:a","name":"First"},{"uri":"spotify:artist:b","name":"Second"}],"album":{"uri":"spotify:album:a","name":"Album"},"duration_ms":1234`
	for _, tc := range []struct {
		suffix string
		valid  bool
	}{
		{``, true},
		{`,"album_artist":"Ensemble","track_number":2,"disc_number":1,"release_date":"2024-02-29","artwork_url":"https://i.scdn.co/image/abc"`, true},
		{`,"album_artist":""`, true},
		{`,"album_artist":null`, false},
		{`,"track_number":null`, false},
		{`,"track_number":0`, false},
		{`,"disc_number":10000`, false},
		{`,"release_date":"2023-02-29"`, false},
		{`,"release_date":"2024-13"`, false},
		{`,"artwork_url":"http://i.scdn.co/image/abc"`, false},
		{`,"artwork_url":"https://user:secret@i.scdn.co/image/abc"`, false},
		{`,"artwork_url":"https://127.0.0.1/image"`, true}, // no retrieval here; network destination validation belongs to the artwork fetcher
		{`,"name":"extra"`, false},
		{`,"name":"line\nfeed"`, false},
		{`,"album_artist":"` + strings.Repeat("a", 1025) + `"`, false},
		{`,"artwork_url":"https://example.com/` + strings.Repeat("a", 2048) + `"`, false},
		{`,"unexpected":1`, false},
	} {
		track, err := materializeTrack(json.RawMessage(base + tc.suffix + `}`))
		if (err == nil) != tc.valid {
			t.Errorf("suffix %q: track=%#v err=%v", tc.suffix, track, err)
		}
		if tc.valid && len(track.Artists) != 2 {
			t.Errorf("artists lost: %#v", track.Artists)
		}
	}
	_, err := materializeTrack(json.RawMessage(fmt.Sprintf(`{"uri":"spotify:track:a","name":"One","artists":[%s],"album":{"uri":"a","name":"Album"},"duration_ms":1}`, strings.Repeat(`{"uri":"a","name":"Artist"},`, 64)+`{"uri":"b","name":"Artist"}`)))
	if err == nil {
		t.Fatal("oversized artist list accepted")
	}
}

func TestCandidateRejectsConflictingOptionalMetadataForSameURI(t *testing.T) {
	track := `{"uri":"spotify:track:one","name":"One","artists":[{"uri":"spotify:artist:a","name":"Artist"}],"album":{"uri":"spotify:album:a","name":"Album"},"duration_ms":1234`
	snapshot := fmt.Sprintf(`{"kind":"candidate","playlists":[],"liked_songs":{"entries":[{"position":0,"kind":"supported","track":%s}},{"position":1,"kind":"supported","track":%s}]}}`, track+`}`, track+`,"release_date":"2024"}`)
	if _, err := parseCandidateSnapshot(json.RawMessage(snapshot)); err == nil {
		t.Fatal("same track URI with conflicting optional metadata accepted")
	}
}

func TestParseCandidateSnapshotRejectsInvalidAndConflictingTracks(t *testing.T) {
	for _, snapshot := range []string{
		`{"kind":"candidate","kind":"candidate","playlists":[],"liked_songs":{"entries":[]}}`,
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
