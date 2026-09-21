package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Iyed-M/offbeat/internal/desired"
	"golang.org/x/text/unicode/norm"
)

const likedSongsPlaylistFilename = "Liked Songs.m3u8"

const maxPlaylistBasenameBytes = 200

// materializePlaylistsLocked projects current Desired Spotify state and live
// Managed track availability. The caller holds managedMu so the two database
// reads and all live file checks describe one serialized application state.
func (d *Daemon) materializePlaylistsLocked(ctx context.Context) error {
	state, _, err := d.DB.ReadDesiredSpotifyState(ctx)
	if err != nil {
		return fmt.Errorf("read Desired Spotify state: %w", err)
	}
	managedTracks, err := d.DB.DesiredManagedTracks(ctx)
	if err != nil {
		return fmt.Errorf("read Managed tracks: %w", err)
	}
	available := make(map[string]string, len(managedTracks))
	for _, track := range managedTracks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.managedFiles.Available(track.Track.URI, track.RelativePath) {
			available[track.Track.URI] = track.RelativePath
		}
	}

	desiredPlaylists := make(map[string][]byte, len(state.Playlists)+1)
	desiredPlaylists[likedSongsPlaylistFilename] = renderM3U8(state.LikedSongs, available)
	filenames := playlistFilenames(state.Playlists)
	for i, playlist := range state.Playlists {
		if err := ctx.Err(); err != nil {
			return err
		}
		desiredPlaylists[filenames[i]] = renderM3U8(playlist.Entries, available)
	}
	if err := d.managedFiles.ReconcilePlaylists(desiredPlaylists); err != nil {
		return fmt.Errorf("reconcile desktop playlists: %w", err)
	}
	return nil
}

// reconcilePlaylistsAfterCommitLocked keeps an authoritative database commit
// independent from its derived filesystem projection. Callers already hold
// managedMu and must invoke this only after the source commit has succeeded.
func (d *Daemon) reconcilePlaylistsAfterCommitLocked(ctx context.Context, source string) {
	if err := d.materializePlaylistsLocked(ctx); err != nil {
		// Filesystem errors can contain user-controlled paths. Keep the durable
		// failure signal useful without copying those paths into daemon logs.
		d.Logger.Error("reconcile desktop playlists after " + source + " commit")
	}
}

func playlistFilenames(playlists []desired.Playlist) []string {
	basenames := make([]string, len(playlists))
	collisionKeys := make(map[string]int, len(playlists))
	for i, playlist := range playlists {
		basenames[i] = safePlaylistBasename(playlist.Name)
		collisionKeys[playlistFilenameCollisionKey(basenames[i])]++
	}

	indices := make([]int, len(playlists))
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(i, j int) bool {
		left, right := playlists[indices[i]], playlists[indices[j]]
		if left.URI != right.URI {
			return left.URI < right.URI
		}
		return basenames[indices[i]] < basenames[indices[j]]
	})

	filenames := make([]string, len(playlists))
	reservedKey := playlistFilenameCollisionKey(strings.TrimSuffix(likedSongsPlaylistFilename, ".m3u8"))
	used := map[string]struct{}{reservedKey: {}}
	for _, i := range indices {
		playlist := playlists[i]
		basename := basenames[i]
		key := playlistFilenameCollisionKey(basename)
		digest := sha256.Sum256([]byte(playlist.URI))
		suffix := fmt.Sprintf("%x", digest[:6])
		candidate := basename
		if collisionKeys[key] > 1 || key == reservedKey {
			candidate = basename + "~" + suffix
		}
		candidateKey := playlistFilenameCollisionKey(candidate)
		if _, exists := used[candidateKey]; exists {
			for attempt := 2; ; attempt++ {
				candidate = basename + "~" + suffix + "-" + strconv.Itoa(attempt)
				candidateKey = playlistFilenameCollisionKey(candidate)
				if _, exists := used[candidateKey]; !exists {
					break
				}
			}
		}
		used[candidateKey] = struct{}{}
		filenames[i] = candidate + ".m3u8"
	}
	return filenames
}

func safePlaylistBasename(name string) string {
	if !utf8.ValidString(name) {
		return "Playlist"
	}
	var safe strings.Builder
	spacePending := false
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '/' || r == '\\' {
			spacePending = safe.Len() > 0
			continue
		}
		if spacePending {
			safe.WriteByte(' ')
			spacePending = false
		}
		safe.WriteRune(r)
	}
	basename := strings.Trim(safe.String(), " .")
	if basename == "" || basename == "." || basename == ".." {
		basename = "Playlist"
	}
	return truncateUTF8(basename, maxPlaylistBasenameBytes)
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func playlistFilenameCollisionKey(basename string) string {
	return strings.ToLower(norm.NFC.String(basename))
}

func renderM3U8(entries []desired.Entry, available map[string]string) []byte {
	var rendered bytes.Buffer
	rendered.WriteString("#EXTM3U\n")
	for _, entry := range entries {
		if entry.Kind != desired.EntrySupported {
			continue
		}
		relativePath, ok := available[entry.TrackURI]
		if !ok {
			continue
		}
		rendered.WriteString(path.Join("..", relativePath))
		rendered.WriteByte('\n')
	}
	return rendered.Bytes()
}
