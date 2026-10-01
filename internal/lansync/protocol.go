// Package lansync defines the read-only current-state HTTPS protocol.
package lansync

import (
	"crypto/sha256"
	"fmt"
	"net/url"
)

const (
	Version          = 1
	ManifestRoute    = "/v1/sync/manifest"
	FilesRoute       = "/v1/sync/files/"
	LikedSongsID     = "offbeat:liked-songs"
	MaxManifestBytes = 8 << 20
	MaxEntries       = 4096
	MaxFileBytes     = 512 << 20
)

type File struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	ContentVersion string `json:"content_version"`
	Download       string `json:"download"`
}

type Manifest struct {
	Version        int    `json:"version"`
	ContentVersion string `json:"content_version"`
	StateRevision  int64  `json:"state_revision"`
	Tracks         []File `json:"tracks"`
	Playlists      []File `json:"playlists"`
}

func ContentVersion(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func DownloadReference(kind, id, version string) string {
	return FilesRoute + kind + "/" + ContentVersion([]byte(id)) + "?version=" + url.QueryEscape(version)
}
