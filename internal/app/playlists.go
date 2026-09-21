package app

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Iyed-M/offbeat/internal/desired"
)

const likedSongsPlaylistFilename = "Liked Songs.m3u8"

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

	if _, err := d.managedFiles.PublishPlaylist(likedSongsPlaylistFilename, renderM3U8(state.LikedSongs, available)); err != nil {
		return fmt.Errorf("publish Liked Songs playlist: %w", err)
	}

	filenameCounts := make(map[string]int, len(state.Playlists))
	filenames := make([]string, len(state.Playlists))
	for i, playlist := range state.Playlists {
		filename, ok := directPlaylistFilename(playlist.Name)
		if !ok {
			continue
		}
		filenames[i] = filename
		filenameCounts[filename]++
	}
	for i, playlist := range state.Playlists {
		filename := filenames[i]
		if filename == "" || filenameCounts[filename] != 1 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := d.managedFiles.PublishPlaylist(filename, renderM3U8(playlist.Entries, available)); err != nil {
			return fmt.Errorf("publish Spotify playlist: %w", err)
		}
	}
	return nil
}

func directPlaylistFilename(name string) (string, bool) {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || len(name) > 240 || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	filename := name + ".m3u8"
	if filename == likedSongsPlaylistFilename {
		return "", false
	}
	return filename, true
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
