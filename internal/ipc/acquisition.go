package ipc

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

type ManualMappingRequest struct {
	TrackURI string `json:"track_uri"`
	VideoID  string `json:"video_id,omitempty"`
	// ExpectedVideoID guards edits made from an older mapping view.
	ExpectedVideoID string `json:"expected_video_id,omitempty"`
}

// AcquisitionChoice confirms one candidate from an inspection of the given
// Desired metadata. A rejected candidate requires its exact reported reason.
type AcquisitionChoice struct {
	TrackURI             string   `json:"track_uri"`
	VideoID              string   `json:"video_id"`
	SelectionReceipt     string   `json:"selection_receipt"`
	ExpectedTitle        string   `json:"expected_title"`
	ExpectedArtists      []string `json:"expected_artists"`
	ExpectedArtistURIs   []string `json:"expected_artist_uris"`
	ExpectedAlbum        string   `json:"expected_album"`
	ExpectedAlbumURI     string   `json:"expected_album_uri"`
	ExpectedDurationMS   int      `json:"expected_duration_ms"`
	RejectionReason      string   `json:"rejection_reason,omitempty"`
	AcknowledgeRejection string   `json:"acknowledge_rejection,omitempty"`
}

type ManualMappingListRequest struct {
	AfterURI string `json:"after_uri"`
}

type ManualMappingResult struct {
	TrackURI   string `json:"track_uri"`
	VideoID    string `json:"video_id"`
	Provenance string `json:"provenance"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	WorkState  string `json:"work_state,omitempty"`
	WorkError  string `json:"work_error,omitempty"`
}

type ManualMappingPage struct {
	Mappings     []ManualMappingResult `json:"mappings"`
	NextAfterURI string                `json:"next_after_uri,omitempty"`
}

// ReviewPage is a current-state projection; no search results are persisted.
type ReviewListRequest struct {
	AfterURI string `json:"after_uri"`
}

type ReviewTrack struct {
	TrackURI   string   `json:"track_uri"`
	Title      string   `json:"title"`
	Artists    []string `json:"artists"`
	DurationMS int      `json:"duration_ms"`
	WorkState  string   `json:"work_state"`
	WorkError  string   `json:"work_error,omitempty"`
}

type ReviewPage struct {
	Tracks       []ReviewTrack `json:"tracks"`
	NextAfterURI string        `json:"next_after_uri,omitempty"`
}

func ValidateYouTubeVideoID(id string) error {
	if len(id) != 11 {
		return fmt.Errorf("video_id must be exactly 11 YouTube ID characters")
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return fmt.Errorf("video_id must be exactly 11 YouTube ID characters")
		}
	}
	return nil
}

// Mapping keys use the normalized Spotify track form (not a web URL).
func ValidateManualMappingTrackURI(uri string) error {
	if !strings.HasPrefix(uri, "spotify:track:") {
		return fmt.Errorf("track_uri must be a normalized Spotify track URI")
	}
	id := strings.TrimPrefix(uri, "spotify:track:")
	if len(id) == 0 || len(id) > 22 {
		return fmt.Errorf("track_uri must be a normalized Spotify track URI")
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return fmt.Errorf("track_uri must be a normalized Spotify track URI")
		}
	}
	return nil
}

// AcquireRequest supplies the desired Spotify track and one user-authorized
// HTTP(S) source for a durable acquisition request.
type AcquireRequest struct {
	TrackURI  string `json:"track_uri"`
	SourceURL string `json:"source_url"`
}

// AcquisitionTrackRequest identifies one desired Spotify track for a
// read-only acquisition-related operation.
type AcquisitionTrackRequest struct {
	TrackURI string `json:"track_uri"`
}

// AcquisitionIDRequest addresses one durable acquisition request.
type AcquisitionIDRequest struct {
	ID int64 `json:"acquisition_id"`
}

type AcquisitionListRequest struct {
	AfterID int64 `json:"after_id"`
}

// AcquisitionResult reports the durable acquisition identity and its current
// state. Source URLs are deliberately not returned through the Control API.
type AcquisitionResult struct {
	ID         int64  `json:"acquisition_id"`
	TrackURI   string `json:"track_uri"`
	SourceKind string `json:"source_kind"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
}

// AcquisitionBatchResult is bounded regardless of Missing-set size.
type AcquisitionBatchResult struct {
	Considered       int `json:"considered"`
	Queued           int `json:"queued"`
	SkippedActive    int `json:"skipped_active"`
	SkippedAttempted int `json:"skipped_attempted"`
	Available        int `json:"available"`
}

type UnresolvedRetryBatchResult struct {
	Considered       int `json:"considered"`
	Queued           int `json:"queued"`
	SkippedActive    int `json:"skipped_active"`
	SkippedAvailable int `json:"skipped_available"`
	SkippedRemoved   int `json:"skipped_removed"`
}

type AcquisitionCountsResult struct {
	Pending    int `json:"pending"`
	Running    int `json:"running"`
	Unresolved int `json:"unresolved"`
	Failed     int `json:"failed"`
	Complete   int `json:"complete"`
}

type AcquisitionStatusResult struct {
	Counts      AcquisitionCountsResult `json:"counts"`
	Work        []AcquisitionResult     `json:"work"`
	NextAfterID int64                   `json:"next_after_id,omitempty"`
}

// ValidateAcquisitionTrackURI accepts a normalized Spotify track URI. It
// rejects episode, playlist, and search inputs before they reach acquisition.
func ValidateAcquisitionTrackURI(uri string) error {
	if !strings.HasPrefix(uri, "spotify:track:") {
		return fmt.Errorf("track_uri must be a Spotify track URI")
	}
	id := strings.TrimPrefix(uri, "spotify:track:")
	if id == "" || strings.Contains(id, ":") || hasWhitespaceOrControl(uri) {
		return fmt.Errorf("track_uri must be a Spotify track URI")
	}
	return nil
}

// ValidateAcquisitionSource accepts only absolute HTTP(S) URLs with an
// explicit host. URLs with user info, fragments, or whitespace are rejected,
// so the field remains a literal retrieval location rather than a search
// expression or shell input.
func ValidateAcquisitionSource(raw string) error {
	if raw == "" || strings.Contains(raw, "#") || hasWhitespaceOrControl(raw) {
		return fmt.Errorf("source_url must be an absolute HTTP(S) URL")
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u == nil || u.User != nil || u.Fragment != "" || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("source_url must be an absolute HTTP(S) URL without user info or fragment")
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("source_url must use HTTP or HTTPS")
	}
	return nil
}

func hasWhitespaceOrControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0
}
