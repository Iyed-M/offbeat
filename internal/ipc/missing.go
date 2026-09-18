package ipc

// MissingRequest continues a listing after the last examined desired identity.
// StateRevision prevents mixing pages from different Desired Spotify states.
type MissingRequest struct {
	AfterURI      string `json:"after_uri"`
	StateRevision int64  `json:"state_revision"`
}

// MissingResult describes one page of distinct supported desired tracks.
// Counts apply to this page, including available tracks omitted from Tracks.
type MissingResult struct {
	StateRevision  int64          `json:"state_revision"`
	NextAfterURI   string         `json:"next_after_uri,omitempty"`
	DesiredCount   int            `json:"desired_count"`
	AvailableCount int            `json:"available_count"`
	Tracks         []MissingTrack `json:"tracks"`
}

type MissingTrack struct {
	URI     string   `json:"uri"`
	Name    string   `json:"name"`
	Artists []string `json:"artists"`
}
