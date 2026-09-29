// Package managed confines local track-file access to the configured managed root.
package managed

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type Files struct {
	root *os.Root
}

type stagedPlaylist struct {
	temporary string
	final     string
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

// ReconcilePlaylists publishes one complete generated playlist set, then
// removes obsolete generated M3U8 files. Every changed file is fully staged
// before any stale output is removed.
func (f *Files) ReconcilePlaylists(ctx context.Context, desired map[string][]byte) error {
	if err := f.checkDir("playlists"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	names := make([]string, 0, len(desired))
	for filename := range desired {
		if !validPlaylistFilename(filename) {
			return fmt.Errorf("invalid playlist filename")
		}
		names = append(names, filename)
	}
	sort.Strings(names)

	directory, err := f.root.Open("playlists")
	if err != nil {
		return fmt.Errorf("open managed playlists: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return fmt.Errorf("read managed playlists: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close managed playlists: %w", closeErr)
	}
	existing := make(map[string]struct{}, len(entries))
	orphanedTemporary := make([]string, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".publish-") {
			filename := path.Join("playlists", entry.Name())
			info, err := f.root.Lstat(filename)
			if err != nil {
				return fmt.Errorf("inspect managed playlist temporary file: %w", err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("managed playlist temporary file must be a regular file")
			}
			orphanedTemporary = append(orphanedTemporary, filename)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".m3u8") {
			continue
		}
		filename := path.Join("playlists", entry.Name())
		info, err := f.root.Lstat(filename)
		if err != nil {
			return fmt.Errorf("inspect managed playlist: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed playlist destination must be a regular file")
		}
		existing[entry.Name()] = struct{}{}
	}

	staged := make([]stagedPlaylist, 0, len(names))
	defer func() {
		for _, file := range staged {
			_ = f.root.Remove(file.temporary)
		}
	}()
	for _, filename := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		final := path.Join("playlists", filename)
		matches, err := f.playlistMatches(final, desired[filename])
		if err != nil {
			return err
		}
		if matches {
			continue
		}
		temporary := "playlists/.publish-" + rand.Text()
		if err := f.writePlaylistTemporary(ctx, temporary, desired[filename]); err != nil {
			return err
		}
		staged = append(staged, stagedPlaylist{temporary: temporary, final: final})
	}
	for _, filename := range orphanedTemporary {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.root.Remove(filename); err != nil {
			return fmt.Errorf("remove managed playlist temporary file: %w", err)
		}
	}
	for _, file := range staged {
		if err := ctx.Err(); err != nil {
			return err
		}
		if info, err := f.root.Lstat(file.final); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("managed playlist destination must be a regular file")
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect managed playlist: %w", err)
		}
		if err := f.root.Rename(file.temporary, file.final); err != nil {
			return fmt.Errorf("replace managed playlist: %w", err)
		}
	}
	for filename := range existing {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, wanted := desired[filename]; wanted {
			continue
		}
		stale := path.Join("playlists", filename)
		info, err := f.root.Lstat(stale)
		if err != nil {
			return fmt.Errorf("inspect stale managed playlist: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("stale managed playlist must be a regular file")
		}
		if err := f.root.Remove(stale); err != nil {
			return fmt.Errorf("remove stale managed playlist: %w", err)
		}
	}
	return nil
}

func validPlaylistFilename(filename string) bool {
	return filename != "" && filename != "." && filename != ".." && path.Base(filename) == filename && !strings.Contains(filename, `\`) && strings.HasSuffix(filename, ".m3u8")
}

func (f *Files) playlistMatches(name string, content []byte) (bool, error) {
	info, err := f.root.Lstat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect managed playlist: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("managed playlist destination must be a regular file")
	}
	if info.Size() != int64(len(content)) {
		return false, nil
	}
	existing, err := f.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("open managed playlist: %w", err)
	}
	current, readErr := io.ReadAll(existing)
	closeErr := existing.Close()
	if readErr != nil {
		return false, fmt.Errorf("read managed playlist: %w", readErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close managed playlist: %w", closeErr)
	}
	return bytes.Equal(current, content), nil
}

func (f *Files) writePlaylistTemporary(ctx context.Context, name string, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	staged, err := f.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create managed playlist temporary file: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = f.root.Remove(name)
		}
	}()
	if n, err := staged.Write(content); err != nil {
		_ = staged.Close()
		return fmt.Errorf("write managed playlist: %w", err)
	} else if n != len(content) {
		_ = staged.Close()
		return fmt.Errorf("write managed playlist: %w", io.ErrShortWrite)
	}
	if err := ctx.Err(); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return fmt.Errorf("sync managed playlist: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("close managed playlist temporary file: %w", err)
	}
	complete = true
	return nil
}

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

// OpenManaged confines an existing mapping to its canonical regular file.
func (f *Files) OpenManaged(uri, name string) (*os.File, error) {
	if !canonicalTrackPath(uri, name) || f.checkDir("tracks") != nil {
		return nil, fmt.Errorf("invalid managed track mapping")
	}
	file, err := f.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 512<<20 {
		_ = file.Close()
		return nil, fmt.Errorf("managed file is not a bounded regular file")
	}
	return file, nil
}

// Digest reads a bounded open descriptor without trusting its pathname.
func Digest(ctx context.Context, file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 512<<20 {
		return "", fmt.Errorf("invalid managed audio")
	}
	h := sha256.New()
	n, err := io.Copy(h, &contextReader{ctx: ctx, source: io.NewSectionReader(file, 0, info.Size())})
	if err != nil || n != info.Size() {
		return "", fmt.Errorf("could not hash managed audio")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReplaceManaged stages verified media under the root, then exchanges it with
// the destination. The displaced file remains available for an identity and
// digest check; a concurrently replaced destination is exchanged back.
func (f *Files) ReplaceManaged(ctx context.Context, uri, name, staged string, original, replacement *os.File, expectedSHA, replacementSHA string) error {
	if !canonicalTrackPath(uri, name) || !validRefreshTemporary(staged) || f.checkDir("tracks") != nil || original == nil || replacement == nil {
		return fmt.Errorf("invalid managed replacement")
	}
	output, err := f.root.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = f.root.Remove(staged)
		}
	}()
	if _, err = replacement.Seek(0, io.SeekStart); err == nil {
		_, err = io.Copy(output, &contextReader{ctx: ctx, source: replacement})
	}
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	stagedFile, err := f.root.OpenFile(staged, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	stagedSHA, digestErr := Digest(ctx, stagedFile)
	closeErr = stagedFile.Close()
	if digestErr != nil || closeErr != nil || stagedSHA != replacementSHA {
		return fmt.Errorf("staged managed audio changed during copy")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	current, err := f.OpenManaged(uri, name)
	if err != nil {
		return fmt.Errorf("managed destination changed: %w", err)
	}
	defer current.Close()
	before, err := original.Stat()
	if err != nil {
		return err
	}
	actual, err := current.Stat()
	if err != nil || !os.SameFile(before, actual) || before.Size() != actual.Size() || before.ModTime() != actual.ModTime() {
		return fmt.Errorf("managed destination changed")
	}
	sha, err := Digest(ctx, current)
	if err != nil || sha != expectedSHA {
		return fmt.Errorf("managed destination changed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	dir, err := f.root.Open("tracks")
	if err != nil {
		return err
	}
	defer dir.Close()
	// The exchange is atomic, so a concurrent writer between the final check
	// and publication is still detected on the displaced entry.
	if err := unix.Renameat2(int(dir.Fd()), path.Base(staged), int(dir.Fd()), path.Base(name), unix.RENAME_EXCHANGE); err != nil {
		return fmt.Errorf("exchange managed audio: %w", err)
	}
	old, err := f.root.OpenFile(staged, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err == nil {
		var oldInfo os.FileInfo
		oldInfo, err = old.Stat()
		if err == nil && (!oldInfo.Mode().IsRegular() || !os.SameFile(before, oldInfo)) {
			err = fmt.Errorf("managed destination changed")
		}
		if err == nil {
			var oldSHA string
			oldSHA, err = Digest(ctx, old)
			if err == nil && oldSHA != expectedSHA {
				err = fmt.Errorf("managed destination changed")
			}
		}
		_ = old.Close()
	}
	if err != nil {
		if rollbackErr := unix.Renameat2(int(dir.Fd()), path.Base(staged), int(dir.Fd()), path.Base(name), unix.RENAME_EXCHANGE); rollbackErr != nil {
			cleanup = false // preserve the displaced good file for manual recovery
			return fmt.Errorf("managed destination changed; could not restore displaced file: %w", rollbackErr)
		}
		return fmt.Errorf("managed destination changed: %w", err)
	}
	syncErr := dir.Sync()
	if syncErr != nil {
		if rollbackErr := unix.Renameat2(int(dir.Fd()), path.Base(staged), int(dir.Fd()), path.Base(name), unix.RENAME_EXCHANGE); rollbackErr != nil {
			cleanup = false
			return fmt.Errorf("sync managed directory failed and restore failed: %w", rollbackErr)
		}
		return syncErr
	}
	return nil
}

// NewRefreshTemporary generates the root-relative name persisted in the DB
// intent before an exchange. It can be inspected after an interrupted daemon.
func NewRefreshTemporary() string { return "tracks/.publish-" + rand.Text() }

func validRefreshTemporary(name string) bool {
	return strings.HasPrefix(name, "tracks/.publish-") && len(name) == len("tracks/.publish-")+26 && path.Base(name) == strings.TrimPrefix(name, "tracks/")
}

// ReconcileReplacement checks the displaced file if an exchange was
// interrupted. If another valid file was displaced, restore it rather than
// accepting the newly published bytes as an authorized replacement.
func (f *Files) ReconcileReplacement(ctx context.Context, uri, name, temporary, previousSHA string) error {
	if !canonicalTrackPath(uri, name) || !validRefreshTemporary(temporary) || f.checkDir("tracks") != nil {
		return fmt.Errorf("invalid pending replacement")
	}
	old, err := f.root.OpenFile(temporary, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return nil
	} // verified publication removed the displaced copy
	if err != nil {
		return fmt.Errorf("pending displaced file cannot be opened safely")
	}
	defer old.Close()
	sha, err := Digest(ctx, old)
	if err != nil {
		return fmt.Errorf("pending displaced file cannot be inspected safely")
	}
	if sha != previousSHA {
		// The caller has already checked that the current destination is exactly
		// the staged digest, so restoration cannot overwrite a third-party edit.
		if err := f.root.Rename(temporary, name); err != nil {
			return fmt.Errorf("could not restore changed managed file: %w", err)
		}
		dir, err := f.root.Open("tracks")
		if err != nil {
			return err
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("managed destination changed during interrupted replacement; original restored")
	}
	if err := f.root.Remove(temporary); err != nil {
		return err
	}
	dir, err := f.root.Open("tracks")
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	return err
}

// OpenOrphan opens the sole identity-derived track file, if one exists, via
// the managed root. It does not claim the media is playable: callers must
// inspect its actual container/codec and decode it before registering it.
// A malformed or ambiguous candidate blocks recovery rather than allowing a
// new download to overwrite it. The returned descriptor belongs to the caller.
func (f *Files) OpenOrphan(uri string) (*os.File, string, error) {
	if uri == "" || f.checkDir("tracks") != nil {
		return nil, "", fmt.Errorf("invalid managed tracks directory or identity")
	}
	var found string
	var extension string
	for _, ext := range []string{"opus", "ogg", "mp3", "m4a", "flac", "wav", "aac"} {
		name := TrackPath(uri, ext)
		_, err := f.root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("inspect managed orphan: %w", err)
		}
		if found != "" {
			return nil, "", fmt.Errorf("multiple managed orphan candidates")
		}
		found, extension = name, ext
	}
	if found == "" {
		return nil, "", os.ErrNotExist
	}
	file, err := f.root.OpenFile(found, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open managed orphan: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 512<<20 {
		_ = file.Close()
		return nil, "", fmt.Errorf("managed orphan is not a bounded regular file")
	}
	return file, extension, nil
}

// OrphanUnchanged checks that the verified descriptor still names the same
// managed file immediately before its DB mapping is committed.
func (f *Files) OrphanUnchanged(uri, extension string, file *os.File, before os.FileInfo) bool {
	if file == nil || !validExtension(extension) || f.checkDir("tracks") != nil {
		return false
	}
	current, err := f.root.Lstat(TrackPath(uri, extension))
	if err != nil || !current.Mode().IsRegular() {
		return false
	}
	verified, err := file.Stat()
	return err == nil && os.SameFile(current, verified) && os.SameFile(before, verified) &&
		current.Size() == before.Size() && current.ModTime() == before.ModTime()
}

// Publish copies a verified staged audio file into a root-owned temporary
// file, then atomically installs it at its identity-derived managed path. The
// staged file is intentionally accepted as an open descriptor: callers cannot
// direct a managed write with an arbitrary source pathname.
func (f *Files) Publish(uri string, source *os.File, extension string) (string, error) {
	return f.publish(context.Background(), uri, source, extension, false)
}

// PublishNew refuses to replace a preexisting track, including an orphan from
// an interrupted database commit. Orphans are verified separately before any
// retrieval, never implicitly adopted by this publication boundary.
func (f *Files) PublishNew(ctx context.Context, uri string, source *os.File, extension string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return f.publish(ctx, uri, source, extension, true)
}

func (f *Files) publish(ctx context.Context, uri string, source *os.File, extension string, newOnly bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
	if newOnly {
		if _, err := f.root.Lstat(final); err == nil {
			// An orphan exists or a file appeared since the recovery check.
			// Never replace it, even if it is currently unreadable.
			return "", fmt.Errorf("managed track already exists but is unavailable")
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect managed track: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	temp := "tracks/.publish-" + rand.Text()
	destination, err := f.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	defer f.root.Remove(temp)
	if _, err := io.Copy(destination, &contextReader{ctx: ctx, source: source}); err != nil {
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if newOnly {
		// An atomic hard link installs only if the destination is absent.
		// Rename would replace a good track in a race with an outside writer.
		if err := f.root.Link(temp, final); err != nil {
			return "", err
		}
		// Persist the directory entry before the DB points at it. If sync
		// fails, the complete file can be adopted on the next attempt.
		dir, err := f.root.Open("tracks")
		if err != nil {
			return "", err
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil {
			return "", syncErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	} else if err := f.root.Rename(temp, final); err != nil {
		return "", err
	}
	return final, nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
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
