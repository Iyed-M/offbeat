package acquisition

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// VerifyOrphan checks an already-published, root-opened descriptor before it
// can become a Managed track mapping. The child sees only its inherited file
// descriptor; neither its path nor any process output reaches diagnostics.
// It is deliberately stricter than the retriever's initial audio probe:
// adoption requires a matching container/codec and a complete audio decode.
func VerifyOrphan(ctx context.Context, file *os.File, extension, ffprobe, ffmpeg string) error {
	if file == nil || ffprobe == "" || ffmpeg == "" {
		return errors.New("orphan verification tools unavailable; configure ffprobe and ffmpeg")
	}
	probe, err := configuredExecutable(ffprobe, "ffprobe")
	if err != nil {
		return errors.New("orphan verification unavailable; configure ffprobe")
	}
	decode, err := configuredExecutable(ffmpeg, "FFmpeg")
	if err != nil {
		return errors.New("orphan verification unavailable; configure ffmpeg")
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	const descriptor = "/proc/self/fd/3" // ExtraFiles[0], not a reopenable managed path.
	cmd := exec.CommandContext(bounded, probe, "-v", "error", "-show_entries", "stream=codec_type,codec_name:stream_disposition=attached_pic:format=format_name", "-of", "json", "--", descriptor)
	cmd.ExtraFiles = []*os.File{file}
	output, err := runProcessOutput(bounded, cmd, 64<<10)
	if bounded.Err() != nil {
		return errors.New("orphan verification timed out or was canceled; retry when media tools are responsive")
	}
	if err != nil || !orphanProbeMatches(output, extension) {
		return errors.New("orphan audio is unreadable or its container/codec does not match its extension; repair or remove it before retry")
	}
	cmd = exec.CommandContext(bounded, decode, "-nostdin", "-xerror", "-err_detect", "explode", "-v", "error", "-i", descriptor, "-map", "0:a:0", "-f", "s16le", "-acodec", "pcm_s16le", "pipe:1")
	cmd.ExtraFiles = []*os.File{file}
	decoded := &decodedBytes{limit: 2 << 30}
	cmd.Stdout = decoded
	err = runProcess(bounded, cmd)
	if bounded.Err() != nil {
		return errors.New("orphan verification timed out or was canceled; retry when media tools are responsive")
	}
	if err != nil || decoded.count == 0 || decoded.exceeded {
		return errors.New("orphan audio cannot be fully decoded within verification limits; repair or remove it before retry")
	}
	return nil
}

func orphanProbeMatches(output []byte, extension string) bool {
	var data struct {
		Streams []struct {
			CodecType   string `json:"codec_type"`
			CodecName   string `json:"codec_name"`
			Disposition struct {
				AttachedPicture int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
		Format struct {
			Name string `json:"format_name"`
		} `json:"format"`
	}
	if json.Unmarshal(output, &data) != nil {
		return false
	}
	containers := map[string]string{
		"opus": "ogg", "ogg": "ogg", "mp3": "mp3", "m4a": "mov,mp4,m4a,3gp,3g2,mj2",
		"flac": "flac", "wav": "wav", "aac": "aac",
	}
	codecs := map[string]string{
		"opus": "opus", "ogg": "vorbis", "mp3": "mp3", "m4a": "aac", "flac": "flac", "aac": "aac",
	}
	if containers[extension] == "" || data.Format.Name != containers[extension] {
		return false
	}
	audio := 0
	for _, stream := range data.Streams {
		switch stream.CodecType {
		case "audio":
			audio++
			if extension == "wav" {
				if !strings.HasPrefix(stream.CodecName, "pcm_") {
					return false
				}
			} else if stream.CodecName != codecs[extension] {
				return false
			}
		case "video":
			if stream.Disposition.AttachedPicture != 1 {
				return false
			}
		}
	}
	return audio == 1
}

type decodedBytes struct {
	count, limit int64
	exceeded     bool
}

func (b *decodedBytes) Write(p []byte) (int, error) {
	if int64(len(p)) > b.limit-b.count {
		b.exceeded = true
		return 0, errors.New("decoded audio exceeds limit")
	}
	b.count += int64(len(p))
	return len(p), nil
}
