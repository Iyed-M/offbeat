// Package tagging writes Spotify text tags on a private copy of new audio.
package tagging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
)

//go:embed write.py
var helper embed.FS

const maxMediaBytes int64 = 512 << 20

const (
	Tagged      = "tagged"
	Unsupported = "unsupported"
	Failed      = "failed"
)

// Result owns the optional tagged descriptor. Close removes the private copy.
type Result struct {
	File  *os.File
	State string
	Error string
	dir   string
}

func (r *Result) Close() {
	if r.File != nil {
		_ = r.File.Close()
	}
	if r.dir != "" {
		_ = os.RemoveAll(r.dir)
	}
}

func failure(reason string) Result { return Result{State: Failed, Error: reason} }

// Stage never modifies source. A failed tag/verification returns a sanitized
// outcome and the caller can publish the original descriptor instead.
func Stage(ctx context.Context, source *os.File, extension string, track desired.Track, ffmpeg, ffprobe string) Result {
	if extension == "wav" || extension == "aac" {
		return Result{State: Unsupported, Error: "native text tags unavailable for format"}
	}
	if extension != "opus" && extension != "ogg" && extension != "flac" && extension != "mp3" && extension != "m4a" {
		return failure("unsupported tagging format")
	}
	if desired.ValidateTrack(track) != nil || source == nil {
		return failure("invalid Spotify metadata or staged audio")
	}
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxMediaBytes {
		return failure("staged audio exceeds tagging limits")
	}
	stage, err := os.MkdirTemp("", "offbeat-tags-")
	if err != nil {
		return failure("could not stage tags")
	}
	result := Result{dir: stage}
	defer func() {
		if result.File == nil {
			result.Close()
		}
	}()
	name := filepath.Join(stage, "audio."+extension)
	copyFile, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return failure("could not stage tags")
	}
	// Copy from the descriptor rather than reopening its path. Enforce the size
	// even if the input grows after Stat; never follow a replacement pathname.
	if err := ctx.Err(); err != nil {
		_ = copyFile.Close()
		return failure("tagging canceled")
	}
	n, err := io.Copy(copyFile, &cancellableReader{ctx: ctx, reader: io.LimitReader(io.NewSectionReader(source, 0, info.Size()), maxMediaBytes+1)})
	if err != nil || n != info.Size() || n > maxMediaBytes {
		_ = copyFile.Close()
		return failure("could not copy staged audio")
	}
	if err := copyFile.Close(); err != nil {
		return failure("could not close staged audio")
	}
	artists := make([]string, 0, len(track.Artists))
	for _, artist := range track.Artists {
		artists = append(artists, artist.Name)
	}
	// Use explicit snake_case names for the private JSON protocol.
	payload, _ := json.Marshal(map[string]any{"title": track.Name, "album": track.Album.Name, "uri": track.URI, "artists": artists, "album_artist": track.AlbumArtist, "track_number": track.TrackNumber, "disc_number": track.DiscNumber, "release_date": track.ReleaseDate})
	if len(payload) > 128<<10 {
		return failure("tag metadata exceeds limit")
	}
	script, _ := helper.ReadFile("write.py")
	scriptPath := filepath.Join(stage, "write.py")
	if os.WriteFile(scriptPath, script, 0o600) != nil {
		return failure("could not stage tag writer")
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, "python3", "-I", scriptPath, name, extension)
	cmd.Stdin = bytes.NewReader(payload)
	if err := run(bounded, cmd, 4096); err != nil {
		var exit *helperExit
		switch {
		case errors.As(err, &exit) && exit.code == 2:
			return failure("Mutagen unavailable; provision Python3 and Mutagen 1.47.0 offline")
		case errors.As(err, &exit) && exit.code == 3:
			return failure("Mutagen version mismatch; provision Mutagen 1.47.0 offline")
		case errors.Is(err, exec.ErrNotFound):
			return failure("Python3 unavailable; provision Python3 and Mutagen 1.47.0 offline")
		default:
			return failure("native tag writer failed; original audio retained")
		}
	}
	if verify(bounded, source.Name(), name, ffmpeg, ffprobe) != nil {
		return failure("tagged audio verification failed")
	}
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return failure("could not open tagged audio")
	}
	if tagged, err := file.Stat(); err != nil || !tagged.Mode().IsRegular() || tagged.Size() < 1 || tagged.Size() > maxMediaBytes {
		_ = file.Close()
		return failure("tagged audio exceeds limits")
	}
	result.File, result.State = file, Tagged
	return result
}

func run(ctx context.Context, cmd *exec.Cmd, limit int) error {
	output := &limitedOutput{limit: limit}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return exec.ErrNotFound
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return &helperExit{code: exit.ExitCode()}
		}
		return errors.New("tagging process failed")
	}
	if ctx.Err() != nil || output.overflow {
		return errors.New("tagging process failed")
	}
	return nil
}

type helperExit struct{ code int }

func (e *helperExit) Error() string { return "tagging helper exited" }

type limitedOutput struct {
	limit    int
	used     int
	overflow bool
}

func (o *limitedOutput) Write(p []byte) (int, error) {
	if o.used <= o.limit {
		o.used += len(p)
	}
	if o.used > o.limit {
		o.overflow = true
	}
	return len(p), nil
}

type audioInfo struct {
	Streams []struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		SampleRate  string `json:"sample_rate"`
		Channels    int    `json:"channels"`
		Disposition struct {
			AttachedPicture int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
}

func verify(ctx context.Context, before, after, ffmpeg, ffprobe string) error {
	if ffmpeg == "" || ffprobe == "" {
		return errors.New("audio verification tools unavailable")
	}
	var baseline []string
	for i, name := range []string{before, after} {
		cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,sample_rate,channels:stream_disposition=attached_pic", "-of", "json", "--", name)
		var out bytes.Buffer
		// FFprobe is capped by the caller's short timeout; output remains bounded.
		if runProbe(ctx, cmd, &out) != nil {
			return errors.New("audio probe failed")
		}
		var info audioInfo
		if json.Unmarshal(out.Bytes(), &info) != nil {
			return errors.New("invalid audio probe")
		}
		var streams []string
		for _, s := range info.Streams {
			if s.CodecType == "audio" {
				streams = append(streams, s.CodecName+"/"+s.SampleRate+"/"+strconv.Itoa(s.Channels))
			}
			if s.CodecType == "video" && s.Disposition.AttachedPicture != 1 {
				return errors.New("unexpected video")
			}
		}
		if len(streams) == 0 {
			return errors.New("no audio")
		}
		if i == 0 {
			baseline = streams
		} else if !equal(baseline, streams) {
			return errors.New("audio codec changed")
		}
	}
	var digests [2][32]byte
	for i, name := range []string{before, after} {
		cmd := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", name, "-map", "0:a:0", "-f", "s16le", "-acodec", "pcm_s16le", "pipe:1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			return nil
		}
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = io.Discard
		if cmd.Start() != nil {
			return errors.New("audio decode failed")
		}
		h := sha256.New()
		n, readErr := io.Copy(h, io.LimitReader(pipe, 2<<30+1))
		if readErr != nil || n == 0 || n > 2<<30 {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return errors.New("audio decode exceeds limit")
		}
		if cmd.Wait() != nil || ctx.Err() != nil {
			return errors.New("audio decode failed")
		}
		copy(digests[i][:], h.Sum(nil))
	}
	if digests[0] != digests[1] {
		return errors.New("decoded audio changed")
	}
	return nil
}

func runProbe(ctx context.Context, cmd *exec.Cmd, out *bytes.Buffer) error {
	// Capture at most 64 KiB, without exposing probe diagnostics.
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	if cmd.Start() != nil {
		return errors.New("probe failed")
	}
	n, err := io.Copy(out, io.LimitReader(pipe, 64<<10+1))
	if err != nil || n > 64<<10 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("probe output exceeds limit")
	}
	if cmd.Wait() != nil || ctx.Err() != nil {
		return errors.New("probe failed")
	}
	return nil
}

func equal(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

type cancellableReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *cancellableReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
