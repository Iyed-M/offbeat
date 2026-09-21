package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestChangedSpotifySyncMaterializesPlayableM3U8GoldenDirectory(t *testing.T) {
	d := startAdapterDaemon(t)
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	first := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
	writePlaylistCandidate(t, adapter, requestID, false)
	assertSyncSuccess(t, syncResponse(t, <-first))
	assertFileBytes(t, filepath.Join(d.Cfg.Paths.MusicRoot, "playlists", likedSongsPlaylistFilename), "#EXTM3U\n")

	for _, uri := range []string{"spotify:track:one", "spotify:track:two"} {
		path, err := d.managedFiles.PublishSynthetic(uri)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.DB.RegisterManagedTrack(context.Background(), uri, path); err != nil {
			t.Fatal(err)
		}
	}

	second := sendSync(t, d)
	requestID = assertSnapshotRequest(t, readAdapterMessage(t, adapter))
	writePlaylistCandidate(t, adapter, requestID, true)
	result := decodeSyncResult(t, syncResponse(t, <-second))
	if !result.Changed || result.StateRevision != 2 {
		t.Fatalf("sync result = %#v", result)
	}

	actualDir := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists")
	goldenDir := filepath.Join("testdata", "playlists", "materialized")
	assertGoldenDirectory(t, actualDir, goldenDir)
	assertEveryPlaylistPathReadable(t, actualDir)
}

func writePlaylistCandidate(t *testing.T, conn *websocket.Conn, requestID string, changed bool) {
	t.Helper()
	suffix := ""
	if changed {
		suffix = `,{"position":5,"kind":"unsupported","source_uri":"spotify:episode:changed"}`
	}
	raw := `{
  "kind":"candidate",
  "playlists":[
    {"uri":"spotify:playlist:road","name":"Road Trip","entries":[
      {"position":0,"kind":"supported","track":{"uri":"spotify:track:one","name":"One","artists":[{"uri":"spotify:artist:one","name":"Artist"}],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1000}},
      {"position":1,"kind":"unsupported","source_uri":"spotify:episode:one"},
      {"position":2,"kind":"supported","track":{"uri":"spotify:track:missing","name":"Missing","artists":[{"uri":"spotify:artist:one","name":"Artist"}],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1000}},
      {"position":3,"kind":"supported","track":{"uri":"spotify:track:one","name":"One","artists":[{"uri":"spotify:artist:one","name":"Artist"}],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1000}},
      {"position":4,"kind":"supported","track":{"uri":"spotify:track:two","name":"Two","artists":[{"uri":"spotify:artist:two","name":"Artiste"}],"album":{"uri":"spotify:album:two","name":"Album"},"duration_ms":2000}}` + suffix + `]},
    {"uri":"spotify:playlist:unicode","name":"日本語 믹스","entries":[
      {"position":0,"kind":"supported","track":{"uri":"spotify:track:two","name":"Two","artists":[{"uri":"spotify:artist:two","name":"Artiste"}],"album":{"uri":"spotify:album:two","name":"Album"},"duration_ms":2000}}
    ]},
    {"uri":"spotify:playlist:empty","name":"Empty","entries":[
      {"position":0,"kind":"unsupported"},
      {"position":1,"kind":"supported","track":{"uri":"spotify:track:missing","name":"Missing","artists":[{"uri":"spotify:artist:one","name":"Artist"}],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1000}}
    ]},
    {"uri":"spotify:playlist:collision-a","name":"Collision","entries":[]},
    {"uri":"spotify:playlist:collision-b","name":"Collision","entries":[]},
    {"uri":"spotify:playlist:unsafe","name":"../escape","entries":[]}
  ],
  "liked_songs":{"entries":[
    {"position":0,"kind":"supported","track":{"uri":"spotify:track:two","name":"Two","artists":[{"uri":"spotify:artist:two","name":"Artiste"}],"album":{"uri":"spotify:album:two","name":"Album"},"duration_ms":2000}},
    {"position":1,"kind":"supported","track":{"uri":"spotify:track:one","name":"One","artists":[{"uri":"spotify:artist:one","name":"Artist"}],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1000}},
    {"position":2,"kind":"unsupported"},
    {"position":3,"kind":"supported","track":{"uri":"spotify:track:missing","name":"Missing","artists":[{"uri":"spotify:artist:one","name":"Artist"}],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1000}},
    {"position":4,"kind":"supported","track":{"uri":"spotify:track:one","name":"One","artists":[{"uri":"spotify:artist:one","name":"Artist"}],"album":{"uri":"spotify:album:one","name":"Album"},"duration_ms":1000}}
  ]}
}`
	var snapshot any
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	writeAdapterJSON(t, conn, map[string]any{"version": 1, "type": "snapshot.response", "request_id": requestID, "snapshot": snapshot})
}

func assertGoldenDirectory(t *testing.T, actualDir, goldenDir string) {
	t.Helper()
	actualEntries, err := os.ReadDir(actualDir)
	if err != nil {
		t.Fatal(err)
	}
	goldenEntries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	names := func(entries []os.DirEntry) []string {
		result := make([]string, 0, len(entries))
		for _, entry := range entries {
			result = append(result, entry.Name())
		}
		sort.Strings(result)
		return result
	}
	actualNames, goldenNames := names(actualEntries), names(goldenEntries)
	if strings.Join(actualNames, "\n") != strings.Join(goldenNames, "\n") {
		t.Fatalf("playlist files = %q, want %q", actualNames, goldenNames)
	}
	for _, name := range goldenNames {
		actual, err := os.ReadFile(filepath.Join(actualDir, name))
		if err != nil {
			t.Fatal(err)
		}
		golden, err := os.ReadFile(filepath.Join(goldenDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(actual) != string(golden) {
			t.Fatalf("%s = %q, want %q", name, actual, golden)
		}
	}
}

func assertEveryPlaylistPathReadable(t *testing.T, playlistsDir string) {
	t.Helper()
	entries, err := os.ReadDir(playlistsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(playlistsDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
		if len(lines) == 0 || lines[0] != "#EXTM3U" {
			t.Fatalf("%s has invalid header", entry.Name())
		}
		for _, line := range lines[1:] {
			if strings.Contains(line, `\`) || !strings.HasPrefix(line, "../tracks/") {
				t.Fatalf("%s has non-portable path %q", entry.Name(), line)
			}
			file, err := os.Open(filepath.Join(playlistsDir, filepath.FromSlash(line)))
			if err != nil {
				t.Fatalf("open %s path %q: %v", entry.Name(), line, err)
			}
			var one [1]byte
			if _, err := file.Read(one[:]); err != nil {
				_ = file.Close()
				t.Fatalf("read %s path %q: %v", entry.Name(), line, err)
			}
			_ = file.Close()
		}
	}
}

func assertFileBytes(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestDirectPlaylistFilenameRejectsUnsafeAndReservedNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", `a\\b`, "line\nfeed", "Liked Songs", strings.Repeat("x", 241)} {
		if filename, ok := directPlaylistFilename(name); ok {
			t.Fatalf("directPlaylistFilename(%q) = %q, true", name, filename)
		}
	}
	if filename, ok := directPlaylistFilename("Café 日本語"); !ok || filename != "Café 日本語.m3u8" {
		t.Fatalf("unicode filename = %q, %v", filename, ok)
	}
}
