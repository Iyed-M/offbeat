// Package desired defines Offbeat-owned normalized Spotify values.
package desired

import "time"

type EntryKind string

const (
	EntrySupported   EntryKind = "supported"
	EntryUnsupported EntryKind = "unsupported"
)

type NamedURI struct {
	URI  string
	Name string
}

type Track struct {
	URI        string
	Name       string
	Artists    []NamedURI
	Album      NamedURI
	DurationMS int
}

// Candidate is the complete, validated observation supplied by the Adapter.
type Candidate struct {
	Playlists  []CandidatePlaylist
	LikedSongs []CandidateEntry
}

type CandidatePlaylist struct {
	URI      string
	Name     string
	Position int
	Entries  []CandidateEntry
}

type CandidateEntry struct {
	Position  int
	Kind      EntryKind
	Track     *Track
	SourceURI string
}

// State is the current persisted Desired Spotify state.
type State struct {
	Tracks     []Track
	Playlists  []Playlist
	LikedSongs []Entry
}

type Playlist struct {
	URI      string
	Name     string
	Position int
	Entries  []Entry
}

type Entry struct {
	Position  int
	Kind      EntryKind
	TrackURI  string
	SourceURI string
}

type Metadata struct {
	Revision        int64
	LastCommittedAt *time.Time
}
