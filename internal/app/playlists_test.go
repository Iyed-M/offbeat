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

func TestSpotifySyncMaterializesIdentitySafePlaylistFilenameGoldenDirectory(t *testing.T) {
	d := startAdapterDaemon(t)
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	playlists := []map[string]any{
		{"uri": "spotify:playlist:dup-a", "name": "Mix", "entries": []any{}},
		{"uri": "spotify:playlist:dup-b", "name": "Mix", "entries": []any{}},
		{"uri": "spotify:playlist:suffix-natural", "name": "Mix~a1cdca483047", "entries": []any{}},
		{"uri": "spotify:playlist:slash", "name": "A/B", "entries": []any{}},
		{"uri": "spotify:playlist:backslash", "name": `A\B`, "entries": []any{}},
		{"uri": "spotify:playlist:empty", "name": "\u0001\u0002", "entries": []any{}},
		{"uri": "spotify:playlist:traversal", "name": "../", "entries": []any{}},
		{"uri": "spotify:playlist:liked", "name": "Liked Songs", "entries": []any{}},
		{"uri": "spotify:playlist:case-a", "name": "Case", "entries": []any{}},
		{"uri": "spotify:playlist:case-b", "name": "case", "entries": []any{}},
		{"uri": "spotify:playlist:norm-a", "name": "Café", "entries": []any{}},
		{"uri": "spotify:playlist:norm-b", "name": "Cafe\u0301", "entries": []any{}},
		{"uri": "spotify:playlist:unicode", "name": "日本語 믹스", "entries": []any{}},
		{"uri": "spotify:playlist:long", "name": strings.Repeat("界", 200), "entries": []any{}},
	}
	done := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
	writeAdapterJSON(t, adapter, map[string]any{
		"version": 1, "type": "snapshot.response", "request_id": requestID,
		"snapshot": map[string]any{"kind": "candidate", "playlists": playlists, "liked_songs": map[string]any{"entries": []any{}}},
	})
	assertSyncSuccess(t, syncResponse(t, <-done))

	want := []string{
		"A B~c4240c7ad048.m3u8",
		"A B~f35622adcef1.m3u8",
		"Café~cfb73c578f50.m3u8",
		"Café~356d4ee54dd5.m3u8",
		"Case~16858d8352f6.m3u8",
		"Liked Songs.m3u8",
		"Liked Songs~600b6cf08d9d.m3u8",
		"Mix~73731f5da9d0.m3u8",
		"Mix~a1cdca483047.m3u8",
		"Mix~a1cdca483047~3b5ed37b98cc-2.m3u8",
		"Playlist~a64409cf70a0.m3u8",
		"Playlist~3c21fd2fa5b5.m3u8",
		"case~361ea07f47ea.m3u8",
		strings.Repeat("界", 66) + ".m3u8",
		"日本語 믹스.m3u8",
	}
	playlistsDir := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists")
	assertHeaderOnlyPlaylistDirectory(t, playlistsDir, want)
	for left, right := 0, len(playlists)-1; left < right; left, right = left+1, right-1 {
		playlists[left], playlists[right] = playlists[right], playlists[left]
	}
	syncEmptyPlaylists(t, d, adapter, playlists)
	assertHeaderOnlyPlaylistDirectory(t, playlistsDir, want)
}

func TestSpotifySyncReconcilesRenamedAndDeletedPlaylistGoldenDirectory(t *testing.T) {
	d := startAdapterDaemon(t)
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	syncEmptyPlaylists(t, d, adapter, []map[string]any{
		{"uri": "spotify:playlist:rename", "name": "Before", "entries": []any{}},
		{"uri": "spotify:playlist:delete", "name": "Deleted", "entries": []any{}},
	})
	playlistsDir := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists")
	if err := os.WriteFile(filepath.Join(playlistsDir, "notes.txt"), []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playlistsDir, "stale.m3u8"), []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trackSentinel := filepath.Join(d.Cfg.Paths.MusicRoot, "tracks", "sentinel")
	if err := os.WriteFile(trackSentinel, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}

	syncEmptyPlaylists(t, d, adapter, []map[string]any{
		{"uri": "spotify:playlist:rename", "name": "After", "entries": []any{}},
	})
	assertHeaderOnlyPlaylistDirectory(t, playlistsDir, []string{"After.m3u8", "Liked Songs.m3u8", "notes.txt"})
	if got, err := os.ReadFile(trackSentinel); err != nil || string(got) != "untouched" {
		t.Fatalf("managed audio sentinel = %q, %v", got, err)
	}
}

func TestSpotifySyncRefusesUnsafePlaylistDestinationsBeforeStaleCleanup(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			d := startAdapterDaemon(t)
			adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
			syncEmptyPlaylists(t, d, adapter, []map[string]any{
				{"uri": "spotify:playlist:old", "name": "Old", "entries": []any{}},
			})

			playlistsDir := filepath.Join(d.Cfg.Paths.MusicRoot, "playlists")
			blocked := filepath.Join(playlistsDir, "Blocked.m3u8")
			outside := filepath.Join(t.TempDir(), "outside.m3u8")
			if err := os.WriteFile(outside, []byte("untouched"), 0o644); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(outside, blocked); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(blocked, 0o755); err != nil {
				t.Fatal(err)
			}

			syncEmptyPlaylists(t, d, adapter, []map[string]any{
				{"uri": "spotify:playlist:blocked", "name": "Blocked", "entries": []any{}},
			})
			if _, err := os.Stat(filepath.Join(playlistsDir, "Old.m3u8")); err != nil {
				t.Fatalf("stale output removed before replacement was safe: %v", err)
			}
			if got, err := os.ReadFile(outside); err != nil || string(got) != "untouched" {
				t.Fatalf("outside destination = %q, %v", got, err)
			}
			state, metadata, err := d.DB.ReadDesiredSpotifyState(context.Background())
			if err != nil || metadata.Revision != 2 || len(state.Playlists) != 1 || state.Playlists[0].URI != "spotify:playlist:blocked" {
				t.Fatalf("committed desired state = %#v, %#v, %v", state, metadata, err)
			}
		})
	}
}

func syncEmptyPlaylists(t *testing.T, d *Daemon, adapter *websocket.Conn, playlists []map[string]any) {
	t.Helper()
	done := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
	writeAdapterJSON(t, adapter, map[string]any{
		"version": 1, "type": "snapshot.response", "request_id": requestID,
		"snapshot": map[string]any{"kind": "candidate", "playlists": playlists, "liked_songs": map[string]any{"entries": []any{}}},
	})
	assertSyncSuccess(t, syncResponse(t, <-done))
}

func assertHeaderOnlyPlaylistDirectory(t *testing.T, dir string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
		if entry.Name() == "notes.txt" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || string(content) != "#EXTM3U\n" {
			t.Fatalf("%s = %q, %v", entry.Name(), content, err)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("playlist files = %q, want %q", got, want)
	}
}

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
