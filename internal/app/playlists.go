package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Iyed-M/offbeat/internal/desired"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const likedSongsPlaylistFilename = "Liked Songs.m3u8"

const maxPlaylistBasenameBytes = 200

// materializePlaylists serializes projections. Probe a snapshot without holding
// managedMu, then read current state and recheck root-confined live files under
// managedMu through publication. A concurrent commit cannot publish a stale
// projection after its own reconciliation.
func (d *Daemon) materializePlaylists(ctx context.Context) error {
	d.playlistMu.Lock()
	defer d.playlistMu.Unlock()
	d.managedMu.Lock()
	managedTracks, err := d.DB.DesiredManagedTracks(ctx)
	if err != nil {
		d.managedMu.Unlock()
		return fmt.Errorf("read Managed tracks: %w", err)
	}
	snapshot := make(map[string]playlistTrack, len(managedTracks))
	for _, track := range managedTracks {
		if err := ctx.Err(); err != nil {
			d.managedMu.Unlock()
			return err
		}
		if d.managedFiles.Available(track.Track.URI, track.RelativePath) {
			snapshot[track.Track.URI] = playlistTrack{path: track.RelativePath, track: track.Track}
		}
	}
	d.managedMu.Unlock()
	for uri, item := range snapshot {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := d.managedFiles.OpenManaged(uri, item.path)
		if err == nil {
			item.info, err = file.Stat()
			if err == nil && completePlaylistDisplay(item.track) {
				item.duration = probePlaylistDuration(ctx, file, d.Cfg.Downloader.FFprobePath)
			}
			_ = file.Close()
			snapshot[uri] = item
		}
	}
	d.managedMu.Lock()
	defer d.managedMu.Unlock()
	state, _, err := d.DB.ReadDesiredSpotifyState(ctx)
	if err != nil {
		return fmt.Errorf("read Desired Spotify state: %w", err)
	}
	managedTracks, err = d.DB.DesiredManagedTracks(ctx)
	if err != nil {
		return fmt.Errorf("read Managed tracks: %w", err)
	}
	available := make(map[string]playlistTrack, len(managedTracks))
	for _, current := range managedTracks {
		if err := ctx.Err(); err != nil {
			return err
		}
		uri, name := current.Track.URI, current.RelativePath
		if !d.managedFiles.Available(uri, name) {
			continue
		}
		item := playlistTrack{path: name, track: current.Track}
		if probed, ok := snapshot[uri]; ok && probed.path == name && probed.info != nil {
			file, err := d.managedFiles.OpenManaged(uri, name)
			if err != nil {
				continue
			}
			live, err := file.Stat()
			_ = file.Close()
			if err != nil || !os.SameFile(probed.info, live) || probed.info.Size() != live.Size() || !probed.info.ModTime().Equal(live.ModTime()) {
				continue // changed since the probe; let a later reconciliation retry
			}
			item.duration = probed.duration
		}
		available[uri] = item
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
	if err := d.managedFiles.ReconcilePlaylists(ctx, desiredPlaylists); err != nil {
		return fmt.Errorf("reconcile desktop playlists: %w", err)
	}
	return nil
}

// Call only after releasing managedMu so probing cannot block other commits.
func (d *Daemon) reconcilePlaylistsAfterCommit(ctx context.Context, source string) bool {
	if err := d.materializePlaylists(ctx); err != nil {
		// Filesystem errors can contain user-controlled paths. Keep the durable
		// failure signal useful without copying those paths into daemon logs.
		d.Logger.Error("reconcile desktop playlists after " + source + " commit")
		return false
	}
	return true
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
	return norm.NFC.String(cases.Fold().String(basename))
}

type playlistTrack struct {
	path     string
	track    desired.Track
	duration string
	info     os.FileInfo
}

func completePlaylistDisplay(track desired.Track) bool {
	if !utf8.ValidString(track.Name) || sanitizePlaylistText(track.Name) == "" || len(track.Artists) == 0 {
		return false
	}
	for _, artist := range track.Artists {
		if !utf8.ValidString(artist.Name) || sanitizePlaylistText(artist.Name) == "" {
			return false
		}
	}
	return true
}

func sanitizePlaylistText(text string) string {
	var safe strings.Builder
	space := false
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			space = safe.Len() > 0
			continue
		}
		if space {
			safe.WriteByte(' ')
			space = false
		}
		safe.WriteRune(r)
	}
	return safe.String()
}

// Probe the open root-confined descriptor, not its pathname: a replaced path
// cannot redirect ffprobe. A failed, slow or malformed probe means bare path.
func probePlaylistDuration(ctx context.Context, file *os.File, ffprobe string) string {
	if file == nil || ffprobe == "" {
		return ""
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "json", "-i", "/proc/self/fd/3")
	cmd.ExtraFiles = []*os.File{file}
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	var output limitedPlaylistOutput
	cmd.Stdout = &output
	if cmd.Run() != nil || bounded.Err() != nil || output.overflow {
		return ""
	}
	var data struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(output.data, &data) != nil {
		return ""
	}
	return formatPlaylistDuration(data.Format.Duration)
}

func formatPlaylistDuration(value string) string {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return ""
	}
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}

type limitedPlaylistOutput struct {
	data     []byte
	overflow bool
}

func (o *limitedPlaylistOutput) Write(p []byte) (int, error) {
	if len(p) > 4096-len(o.data) {
		o.overflow = true
	} else {
		o.data = append(o.data, p...)
	}
	return len(p), nil
}

func renderM3U8(entries []desired.Entry, available map[string]playlistTrack) []byte {
	var rendered bytes.Buffer
	rendered.WriteString("#EXTM3U\n")
	for _, entry := range entries {
		if entry.Kind != desired.EntrySupported {
			continue
		}
		item, ok := available[entry.TrackURI]
		if !ok {
			continue
		}
		if item.duration != "" && completePlaylistDisplay(item.track) {
			artists := make([]string, 0, len(item.track.Artists))
			for _, artist := range item.track.Artists {
				artists = append(artists, sanitizePlaylistText(artist.Name))
			}
			rendered.WriteString("#EXTINF:" + item.duration + "," + strings.Join(artists, ", ") + " - " + sanitizePlaylistText(item.track.Name) + "\n")
		}
		rendered.WriteString(path.Join("..", item.path))
		rendered.WriteByte('\n')
	}
	return rendered.Bytes()
}
