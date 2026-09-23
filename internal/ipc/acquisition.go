package ipc

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

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
	ID       int64  `json:"acquisition_id"`
	TrackURI string `json:"track_uri"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
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
