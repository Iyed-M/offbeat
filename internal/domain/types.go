package domain

import (
	"errors"
	"time"
)

type SpotifyTrackID string

func (s SpotifyTrackID) String() string { return string(s) }

func (s SpotifyTrackID) IsZero() bool { return s == "" }

type SpotifyPlaylistID string

func (s SpotifyPlaylistID) String() string { return string(s) }
func (s SpotifyPlaylistID) IsZero() bool   { return s == "" }

type SpotifyUserID string

func (s SpotifyUserID) String() string { return string(s) }

type SnapshotID int64

func (s SnapshotID) IsZero() bool { return s == 0 }

type RevisionID int64

func (r RevisionID) IsZero() bool { return r == 0 }

type AssetID int64

func (a AssetID) IsZero() bool { return a == 0 }

type DeviceID string

func (d DeviceID) IsZero() bool { return d == "" }

type JobID int64

func (j JobID) IsZero() bool { return j == 0 }

type SpotifyTrack struct {
	ID         SpotifyTrackID
	URI        string
	Title      string
	Artists    []string
	Album      string
	DurationMS int
	TrackNo    int
	DiscNo     int
	ArtworkURL string
	ISRC       string
}

type Playlist struct {
	ID        SpotifyPlaylistID
	Name      string
	OwnerID   SpotifyUserID
	TrackIDs  []SpotifyTrackID
	CreatedAt time.Time
	UpdatedAt time.Time
}

type PlaylistEntry struct {
	PlaylistID SpotifyPlaylistID
	Position   int
	TrackID    SpotifyTrackID
}

type LikedSongs struct {
	UserID    SpotifyUserID
	TrackIDs  []SpotifyTrackID
	Collected time.Time
}

type Snapshot struct {
	ID         SnapshotID
	UserID     SpotifyUserID
	CapturedAt time.Time
	Playlists  []Playlist
	LikedSongs LikedSongs
}

type AssetKind string

const (
	AssetAudio    AssetKind = "audio"
	AssetArtwork  AssetKind = "artwork"
	AssetPlaylist AssetKind = "playlist"
)

type LocalAsset struct {
	ID        AssetID
	Kind      AssetKind
	Path      string
	SizeBytes int64
	SHA256    string
	MimeType  string
	CreatedAt time.Time
}

type AssetMapping struct {
	AssetID AssetID
	TrackID SpotifyTrackID
}

type Revision struct {
	ID        RevisionID
	CreatedAt time.Time
	Added     int
	Removed   int
	Notes     string
}

type Device struct {
	ID           DeviceID
	Name         string
	Credential   string
	LastSync     time.Time
	LastRevision RevisionID
	CreatedAt    time.Time
}

type AcquisitionState string

const (
	StatePending      AcquisitionState = "pending"
	StateResolving    AcquisitionState = "resolving"
	StateDownloading  AcquisitionState = "downloading"
	StateNormalizing  AcquisitionState = "normalizing"
	StateReady        AcquisitionState = "ready"
	StateFailedTemp   AcquisitionState = "failed_temporary"
	StateUnresolved   AcquisitionState = "unresolved"
	StateFailedPerm   AcquisitionState = "failed_permanent"
	StateReviewNeeded AcquisitionState = "review_required"
)

func (s AcquisitionState) Valid() bool {
	switch s {
	case StatePending, StateResolving, StateDownloading, StateNormalizing,
		StateReady, StateFailedTemp, StateUnresolved, StateFailedPerm, StateReviewNeeded:
		return true
	}
	return false
}

type AcquisitionJob struct {
	ID          JobID
	TrackID     SpotifyTrackID
	State       AcquisitionState
	Attempts    int
	NextRetryAt time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

var ErrInvalidState = errors.New("invalid state")

func ParseAcquisitionState(s string) (AcquisitionState, error) {
	st := AcquisitionState(s)
	if !st.Valid() {
		return "", ErrInvalidState
	}
	return st, nil
}
