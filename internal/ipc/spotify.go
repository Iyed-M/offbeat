package ipc

// SpotifySyncResult is the current Desired Spotify state summary returned by
// a successful spotify.sync request.
type SpotifySyncResult struct {
	Changed                     bool  `json:"changed"`
	StateRevision               int64 `json:"state_revision"`
	PlaylistCount               int   `json:"playlist_count"`
	PlaylistEntryCount          int   `json:"playlist_entry_count"`
	LikedSongsEntryCount        int   `json:"liked_songs_entry_count"`
	SupportedEntryOccurrences   int   `json:"supported_entry_occurrences"`
	UnsupportedEntryOccurrences int   `json:"unsupported_entry_occurrences"`
}
