package acquisition

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyOrphanFormatMatrixAndCorruptAudio(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	formats := map[string][2]string{
		"opus": {"libopus", "opus"}, "ogg": {"libvorbis", "ogg"},
		"flac": {"flac", "flac"}, "mp3": {"libmp3lame", "mp3"},
		"m4a": {"aac", "ipod"}, "wav": {"pcm_s16le", "wav"}, "aac": {"aac", "adts"},
	}
	for extension, format := range formats {
		t.Run(extension, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audio."+extension)
			cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.2", "-ac", "1", "-ar", "48000", "-c:a", format[0], "-f", format[1], path)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generate: %v %s", err, out)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := VerifyOrphan(context.Background(), file, extension, "ffprobe", "ffmpeg"); err != nil {
				t.Fatalf("valid audio rejected: %v", err)
			}
			if extension != "flac" {
				if err := VerifyOrphan(context.Background(), file, "flac", "ffprobe", "ffmpeg"); err == nil {
					t.Fatal("wrong extension accepted")
				}
			}
		})
	}
	path := filepath.Join(t.TempDir(), "corrupt.wav")
	if err := os.WriteFile(path, []byte("nonempty but not audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = VerifyOrphan(context.Background(), file, "wav", "ffprobe", "ffmpeg")
	if err == nil || strings.Contains(err.Error(), path) {
		t.Fatalf("corrupt audio diagnostic = %v", err)
	}
}
