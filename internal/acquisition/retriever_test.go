package acquisition

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/config"
)

func TestRetrieveStagesOneValidatedAudioFile(t *testing.T) {
	tools := t.TempDir()
	ytdlp := writeTool(t, tools, "yt-dlp", `
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output" ]; then out="$2"; shift 2; continue; fi
  shift
done
out="${out%.%(ext)s}.mp3"
printf audio > "$out"
`)
	ffprobe := writeTool(t, tools, "ffprobe", "printf '%s\\n' '{\"streams\":[{\"codec_type\":\"audio\",\"disposition\":{\"attached_pic\":0}}]}'")
	r := NewRetriever(config.Downloader{YTDLPPath: ytdlp, FFprobePath: ffprobe})
	media, err := r.Retrieve(context.Background(), "https://media.example/owned.mp3")
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Dir(media.File.Name())
	if media.Extension != "mp3" {
		t.Fatalf("extension = %q", media.Extension)
	}
	if err := media.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("stage remains: %v", err)
	}
}

func TestRetrieveRejectsBadOrMultipleOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"none", "exit 0"},
		{"multiple", `
out=""
while [ "$#" -gt 0 ]; do if [ "$1" = "--output" ]; then out="$2"; shift 2; continue; fi; shift; done
printf x > "${out%.%(ext)s}.mp3"
printf x > "${out%.%(ext)s}.ogg"
`},
		{"partial", `
out=""
while [ "$#" -gt 0 ]; do if [ "$1" = "--output" ]; then out="$2"; shift 2; continue; fi; shift; done
printf x > "${out%.%(ext)s}.part"
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := t.TempDir()
			r := NewRetriever(config.Downloader{YTDLPPath: writeTool(t, tools, "yt-dlp", tc.body), FFprobePath: writeTool(t, tools, "ffprobe", "printf '%s\\n' '{\"streams\":[{\"codec_type\":\"audio\",\"disposition\":{\"attached_pic\":0}}]}'")})
			if _, err := r.Retrieve(context.Background(), "https://media.example/a"); err == nil {
				t.Fatal("Retrieve succeeded")
			}
		})
	}
}

func TestRetrieveCancellationAndSanitizedProcessFailure(t *testing.T) {
	tools := t.TempDir()
	r := NewRetriever(config.Downloader{YTDLPPath: writeTool(t, tools, "yt-dlp", "sleep 30"), FFprobePath: writeTool(t, tools, "ffprobe", "printf '%s\\n' '{\"streams\":[{\"codec_type\":\"audio\",\"disposition\":{\"attached_pic\":0}}]}'")})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := r.Retrieve(ctx, "https://private.example/token"); err == nil || strings.Contains(err.Error(), "private.example") {
		t.Fatalf("error = %v, want canceled sanitized failure", err)
	}
	failed := NewRetriever(config.Downloader{YTDLPPath: writeTool(t, tools, "fail", "echo bad >&2; exit 7"), FFprobePath: writeTool(t, tools, "ffprobe", "printf '%s\\n' '{\"streams\":[{\"codec_type\":\"audio\",\"disposition\":{\"attached_pic\":0}}]}'")})
	if _, err := failed.Retrieve(context.Background(), "https://private.example/token"); err == nil || strings.Contains(err.Error(), "private.example") {
		t.Fatalf("error = %v, want sanitized failure", err)
	}
}

func TestRetrieveCancellationKillsProcessGroup(t *testing.T) {
	tools, childPID := t.TempDir(), filepath.Join(t.TempDir(), "child.pid")
	ytdlp := writeTool(t, tools, "yt-dlp", "sleep 30 & echo $! > '"+childPID+"'; wait")
	r := NewRetriever(config.Downloader{YTDLPPath: ytdlp, FFprobePath: writeTool(t, tools, "ffprobe", "printf '%s\\n' '{\"streams\":[{\"codec_type\":\"audio\",\"disposition\":{\"attached_pic\":0}}]}'")})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := r.Retrieve(ctx, "https://media.example/a"); err == nil {
		t.Fatal("Retrieve succeeded")
	}
	data, err := os.ReadFile(childPID)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil || pid <= 0 {
		t.Fatalf("child PID = %q, %v", data, err)
	}
	if err := syscall.Kill(pid, 0); err != nil && err != syscall.ESRCH {
		t.Fatalf("check child process: %v", err)
	}
	// A killed orphan can remain as a zombie briefly until init reaps it; it is
	// no longer executing and therefore proves the process group was stopped.
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil && !strings.Contains(string(stat), ") Z ") {
		t.Fatalf("child process %d remains running: %q", pid, stat)
	}
}

func TestValidateSourceURL(t *testing.T) {
	for _, value := range []string{"https://example.test/a", "http://127.0.0.1:8080/a"} {
		if err := validateSourceURL(value); err != nil {
			t.Fatalf("validate %q: %v", value, err)
		}
	}
	for _, value := range []string{"file:///tmp/a", "https://user@example.test/a", "https://example.test/a#x", "--output=x", "https://example.test/a\n--x"} {
		if err := validateSourceURL(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestValidateProbeOutputAllowsArtworkButRejectsVideo(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want bool
	}{
		{"audio", `{"streams":[{"codec_type":"audio","disposition":{"attached_pic":0}}]}`, true},
		{"audio with artwork", `{"streams":[{"codec_type":"audio","disposition":{"attached_pic":0}},{"codec_type":"video","disposition":{"attached_pic":1}}]}`, true},
		{"real video", `{"streams":[{"codec_type":"audio","disposition":{"attached_pic":0}},{"codec_type":"video","disposition":{"attached_pic":0}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProbeOutput([]byte(tc.json))
			if (err == nil) != tc.want {
				t.Fatalf("validateProbeOutput() = %v, want success=%v", err, tc.want)
			}
		})
	}
}

// This smoke test is deliberately opt-in. It proves the documented yt-dlp
// invocation works end-to-end without fetching Internet media.
func TestRetrieveWithRealToolsFromControlledHTTP(t *testing.T) {
	if os.Getenv("OFFBEAT_TEST_REAL_TOOLS") != "1" {
		t.Skip("set OFFBEAT_TEST_REAL_TOOLS=1 to run real downloader smoke test")
	}
	ytdlp, err := exec.LookPath("yt-dlp")
	if err != nil {
		t.Skip("yt-dlp not installed")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(testWAV())
	}))
	defer server.Close()
	media, err := NewRetriever(config.Downloader{YTDLPPath: ytdlp, FFmpegPath: ffmpeg, FFprobePath: ffprobe}).Retrieve(context.Background(), server.URL+"/owned.wav")
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	info, err := media.File.Stat()
	if err != nil || info.Size() == 0 || media.Extension != "wav" {
		t.Fatalf("media = extension %q size %d err %v", media.Extension, info.Size(), err)
	}
}

func testWAV() []byte {
	data := make([]byte, 44+16)
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
	binary.LittleEndian.PutUint32(data[40:], 16)
	return data
}

func writeTool(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
