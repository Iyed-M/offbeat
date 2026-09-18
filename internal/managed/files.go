// Package managed confines local track-file access to the configured managed root.
package managed

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"syscall"
)

type Files struct {
	root *os.Root
}

func Open(path string) (*Files, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("managed root must be a directory, not a symlink: %s", path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	files := &Files{root: root}
	for _, dir := range []string{"tracks", "playlists"} {
		if err := root.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
			_ = root.Close()
			return nil, err
		}
		if err := files.checkDir(dir); err != nil {
			_ = root.Close()
			return nil, err
		}
	}
	return files, nil
}

func (f *Files) Close() error { return f.root.Close() }

// FixturePath is stable across metadata changes and safe for any normalized URI.
// Full SHA-256 identities avoid filename collisions and filesystem metacharacters.
func FixturePath(uri string) string {
	return TrackPath(uri, "wav")
}

// TrackPath returns the only path at which a track may be published in the
// managed library. The extension is deliberately restricted to the small set
// of containers that v1 accepts from the acquisition boundary.
func TrackPath(uri, extension string) string {
	return fmt.Sprintf("tracks/%x.%s", sha256.Sum256([]byte(uri)), extension)
}

func validExtension(extension string) bool {
	switch extension {
	case "wav", "mp3", "m4a", "opus", "ogg", "flac", "aac":
		return true
	default:
		return false
	}
}

func canonicalTrackPath(uri, name string) bool {
	extension := strings.TrimPrefix(path.Ext(name), ".")
	return validExtension(extension) && name == TrackPath(uri, extension)
}

func (f *Files) checkDir(name string) error {
	info, err := f.root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("managed %s must be a directory, not a symlink", name)
	}
	return nil
}

// Available performs a shallow live check, never an audio decode or integrity scan.
func (f *Files) Available(uri, path string) bool {
	if !canonicalTrackPath(uri, path) || f.checkDir("tracks") != nil {
		return false
	}
	// NONBLOCK prevents a replaced FIFO from hanging a Control request; NOFOLLOW
	// rejects symlink files. Root also prevents ancestor symlink escapes.
	file, err := f.root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Mode().Perm()&0o444 == 0 {
		return false
	}
	var first [1]byte
	n, err := file.Read(first[:])
	return err == nil && n == 1
}

// Publish copies a verified staged audio file into a root-owned temporary
// file, then atomically installs it at its identity-derived managed path. The
// staged file is intentionally accepted as an open descriptor: callers cannot
// direct a managed write with an arbitrary source pathname.
func (f *Files) Publish(uri string, source *os.File, extension string) (string, error) {
	if uri == "" {
		return "", fmt.Errorf("empty track URI")
	}
	if source == nil {
		return "", fmt.Errorf("nil staged media")
	}
	if !validExtension(extension) {
		return "", fmt.Errorf("unsupported audio extension %q", extension)
	}
	if err := f.checkDir("tracks"); err != nil {
		return "", err
	}
	info, err := source.Stat()
	if err != nil {
		return "", fmt.Errorf("stat staged media: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("staged media must be a non-empty regular file")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind staged media: %w", err)
	}
	final := TrackPath(uri, extension)
	temp := "tracks/.publish-" + rand.Text()
	destination, err := f.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	defer f.root.Remove(temp)
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		return "", fmt.Errorf("copy staged media: %w", err)
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		return "", fmt.Errorf("sync staged media: %w", err)
	}
	if err := destination.Close(); err != nil {
		return "", err
	}
	if err := f.root.Rename(temp, final); err != nil {
		return "", err
	}
	return final, nil
}

// PublishSynthetic writes a short PCM WAV silence fixture. It never reads an
// external media path. Callers serialize publication and persist its mapping
// only after success; a failed database write may leave an unregistered file.
func (f *Files) PublishSynthetic(uri string) (string, error) {
	if uri == "" {
		return "", fmt.Errorf("empty track URI")
	}
	if err := f.checkDir("tracks"); err != nil {
		return "", err
	}
	path := FixturePath(uri)
	if f.Available(uri, path) {
		return path, nil
	}
	temp := "tracks/.fixture-" + rand.Text()
	file, err := f.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	defer f.root.Remove(temp)
	defer file.Close()
	if _, err := file.Write(syntheticWAV()); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := f.root.Rename(temp, path); err != nil {
		return "", err
	}
	return path, nil
}

func syntheticWAV() []byte {
	// 100ms of mono, 16-bit PCM silence at 8kHz.
	data := make([]byte, 44+1600)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 16000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], 1600)
	return data
}
