package managed_test

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"

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
