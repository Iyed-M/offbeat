// Package acquisition provides the narrow process boundary used to retrieve
// one explicitly authorized media URL into private staging.
package acquisition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/Iyed-M/offbeat/internal/config"
)

// Retriever fetches exactly one media item into private staging. It receives a
// URL rather than a destination so managed-library paths never reach yt-dlp.
type Retriever interface {
	Retrieve(ctx context.Context, sourceURL string) (*Media, error)
}

// Media owns a private staging directory. Close closes the file and removes
// all staged output; callers must publish the open file before closing it.
type Media struct {
	File      *os.File
	Extension string
	stage     string
	once      sync.Once
	closeErr  error
}

func (m *Media) Close() error {
	if m == nil {
		return nil
	}
	m.once.Do(func() {
		if m.File != nil {
			m.closeErr = m.File.Close()
		}
		if err := os.RemoveAll(m.stage); err != nil && m.closeErr == nil {
			m.closeErr = err
		}
	})
	return m.closeErr
}

// YTDLP invokes configured yt-dlp and ffprobe executables with a fixed,
// non-interactive argument set.
type YTDLP struct {
	ytdlp   string
	ffmpeg  string
	ffprobe string
	command func(context.Context, string, ...string) *exec.Cmd
}

func NewRetriever(cfg config.Downloader) Retriever {
	return &YTDLP{ytdlp: cfg.YTDLPPath, ffmpeg: cfg.FFmpegPath, ffprobe: cfg.FFprobePath, command: exec.CommandContext}
}

func (r *YTDLP) Retrieve(ctx context.Context, sourceURL string) (_ *Media, retErr error) {
	if r == nil || r.ytdlp == "" || r.ffprobe == "" {
		return nil, errors.New("acquisition tools are not configured")
	}
	if err := validateSourceURL(sourceURL); err != nil {
		return nil, err
	}
	ytdlp, err := configuredExecutable(r.ytdlp, "yt-dlp")
	if err != nil {
		return nil, err
	}
	ffprobe, err := configuredExecutable(r.ffprobe, "ffprobe")
	if err != nil {
		return nil, err
	}
	ffmpeg := ""
	if r.ffmpeg != "" {
		ffmpeg, err = configuredExecutable(r.ffmpeg, "FFmpeg")
		if err != nil {
			return nil, err
		}
	}
	stage, err := os.MkdirTemp("", "offbeat-acquisition-")
	if err != nil {
		return nil, fmt.Errorf("create acquisition staging: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(stage)
		}
	}()

	// These are documented yt-dlp options: no user config, one item only, a
	// fixed private output template, and extraction to best available audio.
	// The URL is the final argument after -- and is never parsed as an option.
	output := filepath.Join(stage, "media.%(ext)s")
	args := []string{
		"--no-config", "--no-plugin-dirs", "--no-playlist", "--no-progress",
		"--no-cache-dir", "--playlist-items", "1",
		"--format", "bestaudio/best", "--extract-audio", "--audio-format", "best",
	}
	if ffmpeg != "" {
		args = append(args, "--ffmpeg-location", ffmpeg)
	}
	args = append(args, "--output", output, "--", sourceURL)
	if err := runProcess(ctx, r.command(ctx, ytdlp, args...)); err != nil {
		return nil, fmt.Errorf("retrieve authorized media: %w", err)
	}
	file, extension, err := stagedOutput(stage)
	if err != nil {
		return nil, err
	}
	if err := r.validateAudio(ctx, ffprobe, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &Media{File: file, Extension: extension, stage: stage}, nil
}

func validateSourceURL(value string) error {
	u, err := url.ParseRequestURI(value)
	if err != nil || strings.ContainsAny(value, "\r\n#") || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("source must be an HTTP(S) URL")
	}
	return nil
}

func stagedOutput(stage string) (*os.File, string, error) {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return nil, "", fmt.Errorf("read acquisition staging: %w", err)
	}
	var name string
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 || entry.IsDir() || strings.HasSuffix(entry.Name(), ".part") {
			return nil, "", errors.New("retriever produced an invalid staged output")
		}
		if name != "" {
			return nil, "", errors.New("retriever produced multiple staged outputs")
		}
		name = entry.Name()
	}
	if name == "" {
		return nil, "", errors.New("retriever produced no staged output")
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if !supportedExtension(extension) {
		return nil, "", errors.New("retriever produced an unsupported audio format")
	}
	file, err := os.OpenFile(filepath.Join(stage, name), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open staged media: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		_ = file.Close()
		return nil, "", errors.New("retriever produced an invalid staged output")
	}
	return file, extension, nil
}

func supportedExtension(extension string) bool {
	switch extension {
	case "wav", "mp3", "m4a", "opus", "ogg", "flac", "aac":
		return true
	default:
		return false
	}
}

func (r *YTDLP) validateAudio(ctx context.Context, ffprobe string, file *os.File) error {
	cmd := r.command(ctx, ffprobe, "-v", "error", "-show_entries", "stream=codec_type:stream_disposition=attached_pic", "-of", "json", "--", file.Name())
	output, err := runProcessOutput(ctx, cmd, 64<<10)
	if err != nil {
		return errors.New("staged output is not readable audio")
	}
	return validateProbeOutput(output)
}

func configuredExecutable(path, name string) (string, error) {
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("configured %s executable not found", name)
	}
	return resolved, nil
}

func validateProbeOutput(output []byte) error {
	var probe struct {
		Streams []struct {
			CodecType   string `json:"codec_type"`
			Disposition struct {
				AttachedPicture int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &probe); err != nil {
		return errors.New("staged output is not readable audio")
	}
	seenAudio := false
	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "audio":
			seenAudio = true
		case "video":
			if stream.Disposition.AttachedPicture == 0 {
				return errors.New("staged output contains video")
			}
		}
	}
	if !seenAudio {
		return errors.New("staged output contains no audio")
	}
	return nil
}

func runProcess(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("start retrieval process")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// CommandContext invokes Cancel itself when ctx ends. Make that cancellation
	// kill the entire process group so a completed yt-dlp parent cannot leave
	// ffmpeg or another descendant running.
	cmd.Cancel = func() error {
		killProcessGroup(cmd)
		return nil
	}
	if err := cmd.Start(); err != nil {
		return errors.New("start retrieval process")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ctx.Err() != nil {
			killProcessGroup(cmd)
			return ctx.Err()
		}
		if err != nil {
			return errors.New("retrieval process failed")
		}
		return nil
	case <-ctx.Done():
		killProcessGroup(cmd)
		<-done
		return ctx.Err()
	}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

type boundedOutput struct {
	data     []byte
	limit    int
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if remaining := b.limit - len(b.data); remaining > 0 {
		if len(p) > remaining {
			b.data = append(b.data, p[:remaining]...)
			b.overflow = true
		} else {
			b.data = append(b.data, p...)
		}
	} else {
		b.overflow = true
	}
	return len(p), nil
}

func runProcessOutput(ctx context.Context, cmd *exec.Cmd, limit int) ([]byte, error) {
	output := &boundedOutput{limit: limit}
	cmd.Stdout = output
	cmd.Stderr = nil
	if err := runProcess(ctx, cmd); err != nil {
		return nil, err
	}
	if output.overflow {
		return nil, errors.New("process output exceeds limit")
	}
	return output.data, nil
}
