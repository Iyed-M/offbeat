package managed_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/managed"
)

func TestManagedFilesRejectInvalidFiles(t *testing.T) {
	for _, kind := range []string{"absent", "empty", "unreadable", "directory", "fifo", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			files, err := managed.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer files.Close()
			uri := "spotify:track:one"
			path := managed.FixturePath(uri)
			full := filepath.Join(root, path)
			switch kind {
			case "empty":
				err = os.WriteFile(full, nil, 0o644)
			case "unreadable":
				err = os.WriteFile(full, []byte("audio"), 0o000)
			case "directory":
				err = os.Mkdir(full, 0o755)
			case "fifo":
				err = syscall.Mkfifo(full, 0o600)
			case "symlink":
				target := filepath.Join(t.TempDir(), "outside.wav")
				if err := os.WriteFile(target, []byte("outside audio"), 0o644); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(target, full)
			}
			if err != nil {
				t.Fatal(err)
			}
			if files.Available(uri, path) {
				t.Fatal("invalid file reported available")
			}
		})
	}
}

func TestManagedPublicationIsConfinedAndAtomic(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	files, err := managed.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	// Identity input is opaque; punctuation and traversal cannot become paths.
	uri := "spotify:track:../../outside/\x00?*"
	path, err := files.PublishSynthetic(uri)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || len(content) != 1644 || string(content[:4]) != "RIFF" || string(content[8:12]) != "WAVE" {
		t.Fatalf("invalid synthetic WAV, len=%d err=%v", len(content), err)
	}
	if !files.Available(uri, path) {
		t.Fatal("published fixture not available")
	}
	if files.Available(uri, "../outside.wav") || files.Available(uri, filepath.Join(outside, "outside.wav")) {
		t.Fatal("escaped mapping is available")
	}
	// Replacing a target symlink must not write to its external referent.
	outsideFile := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(outsideFile, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, path)); err != nil {
		t.Fatal(err)
	}
	if _, err := files.PublishSynthetic(uri); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outsideFile)
	if err != nil || !bytes.Equal(got, []byte("untouched")) {
		t.Fatalf("external file changed: %q %v", got, err)
	}
	// A failed rename must remove its temporary file and preserve the target.
	blocked := managed.FixturePath("spotify:track:blocked")
	if err := os.Mkdir(filepath.Join(root, blocked), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := files.PublishSynthetic("spotify:track:blocked"); err == nil {
		t.Fatal("published over directory")
	}
	entries, err := os.ReadDir(filepath.Join(root, "tracks"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("partial files leaked: %v %v", entries, err)
	}
	// A swapped directory cannot redirect subsequent mutation or reads outside.
	if err := os.Rename(filepath.Join(root, "tracks"), filepath.Join(root, "old-tracks")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "tracks")); err != nil {
		t.Fatal(err)
	}
	if _, err := files.PublishSynthetic("spotify:track:escape"); err == nil {
		t.Fatal("published through symlink directory")
	}
	if files.Available(uri, path) {
		t.Fatal("symlink directory reported available")
	}
	entries, err = os.ReadDir(outside)
	if err != nil || len(entries) != 1 {
		t.Fatalf("external directory changed: %v %v", entries, err)
	}
}

func TestPublishCopiesOnlyValidatedStagedMedia(t *testing.T) {
	root, staging := t.TempDir(), t.TempDir()
	files, err := managed.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	stagedPath := filepath.Join(staging, "source.mp3")
	if err := os.WriteFile(stagedPath, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged, err := os.Open(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	uri := "spotify:track:published"
	published, err := files.Publish(uri, staged, "mp3")
	if err != nil {
		t.Fatal(err)
	}
	if published != managed.TrackPath(uri, "mp3") || !files.Available(uri, published) {
		t.Fatalf("published = %q, available=%v", published, files.Available(uri, published))
	}
	if got, err := os.ReadFile(filepath.Join(root, published)); err != nil || !bytes.Equal(got, []byte("audio")) {
		t.Fatalf("published content = %q, %v", got, err)
	}
	replacementPath := filepath.Join(staging, "replacement.mp3")
	if err := os.WriteFile(replacementPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Open(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if _, err := files.Publish(uri, replacement, "mp3"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, published)); err != nil || !bytes.Equal(got, []byte("replacement")) {
		t.Fatalf("replacement content = %q, %v", got, err)
	}
	for _, extension := range []string{"", "exe", "MP3"} {
		if _, err := files.Publish(uri, staged, extension); err == nil {
			t.Fatalf("accepted extension %q", extension)
		}
	}
	empty, err := os.CreateTemp(staging, "empty-")
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if _, err := files.Publish(uri, empty, "mp3"); err == nil {
		t.Fatal("accepted empty staged file")
	}
}

func TestManagedRootRejectsSymlinkDirectories(t *testing.T) {
	for _, name := range []string{"tracks", "playlists"} {
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			if err := os.Symlink(outside, filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			if files, err := managed.Open(root); err == nil {
				files.Close()
				t.Fatal("accepted symlink directory")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("external mutation: %v %v", entries, err)
			}
		})
	}
}

func TestPlaylistPublicationIsConfinedAtomicAndSkipsIdenticalBytes(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	files, err := managed.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	content := []byte("#EXTM3U\n../tracks/example.wav\n")
	changed, err := files.PublishPlaylist("Mix.m3u8", content)
	if err != nil || !changed {
		t.Fatalf("first publish = %v, %v", changed, err)
	}
	playlistPath := filepath.Join(root, "playlists", "Mix.m3u8")
	before, err := os.Stat(playlistPath)
	if err != nil {
		t.Fatal(err)
	}
	// Filesystems with coarse timestamps can hide a rewrite, so compare the
	// inode as well as requiring the publication boundary to report a no-op.
	time.Sleep(time.Millisecond)
	changed, err = files.PublishPlaylist("Mix.m3u8", append([]byte(nil), content...))
	if err != nil || changed {
		t.Fatalf("identical publish = %v, %v", changed, err)
	}
	after, err := os.Stat(playlistPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("identical playlist bytes were rewritten")
	}

	outsideFile := filepath.Join(outside, "sentinel.m3u8")
	if err := os.WriteFile(outsideFile, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "playlists", "Linked.m3u8")); err != nil {
		t.Fatal(err)
	}
	if _, err := files.PublishPlaylist("Linked.m3u8", content); err == nil {
		t.Fatal("published over destination symlink")
	}
	if got, err := os.ReadFile(outsideFile); err != nil || string(got) != "untouched" {
		t.Fatalf("outside destination changed: %q, %v", got, err)
	}

	for _, filename := range []string{"../escape.m3u8", `/escape.m3u8`, `dir\\escape.m3u8`, "not-a-playlist"} {
		if _, err := files.PublishPlaylist(filename, content); err == nil {
			t.Fatalf("accepted unsafe filename %q", filename)
		}
	}
	blocked := filepath.Join(root, "playlists", "Blocked.m3u8")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := files.PublishPlaylist("Blocked.m3u8", content); err == nil {
		t.Fatal("published over non-regular destination")
	}
	playlistEntries, err := os.ReadDir(filepath.Join(root, "playlists"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range playlistEntries {
		if strings.HasPrefix(entry.Name(), ".publish-") {
			t.Fatalf("temporary playlist leaked after failure: %s", entry.Name())
		}
	}

	if err := os.Rename(filepath.Join(root, "playlists"), filepath.Join(root, "old-playlists")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "playlists")); err != nil {
		t.Fatal(err)
	}
	if _, err := files.PublishPlaylist("Escape.m3u8", content); err == nil {
		t.Fatal("published through playlist-directory symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outside directory changed: %v, %v", entries, err)
	}
	oldEntries, err := os.ReadDir(filepath.Join(root, "old-playlists"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range oldEntries {
		if strings.HasPrefix(entry.Name(), ".publish-") {
			t.Fatalf("temporary playlist leaked: %s", entry.Name())
		}
	}
}
