package tagging

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
)

func fixture(t *testing.T, extension string) *os.File {
	t.Helper()
	codecs := map[string][2]string{
		"opus": {"libopus", "opus"}, "ogg": {"libvorbis", "ogg"},
		"flac": {"flac", "flac"}, "mp3": {"libmp3lame", "mp3"},
		"m4a": {"aac", "ipod"}, "wav": {"pcm_s16le", "wav"}, "aac": {"aac", "adts"},
	}
	path := filepath.Join(t.TempDir(), "original."+extension)
	c := codecs[extension]
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.2", "-ac", "1", "-ar", "48000", "-c:a", c[0], "-f", c[1], path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v %s", err, out)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func tools(t *testing.T) {
	t.Helper()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s not available", name)
		}
	}
}

func sampleTrack() desired.Track {
	n := 2
	d := 1
	return desired.Track{URI: "spotify:track:fixture", Name: "Fixture Title", Artists: []desired.NamedURI{{URI: "a", Name: "First Artist"}, {URI: "b", Name: "Second Artist"}}, Album: desired.NamedURI{URI: "album", Name: "Fixture Album"}, DurationMS: 200, AlbumArtist: "Album Artist", TrackNumber: &n, DiscNumber: &d, ReleaseDate: "2024-05-03"}
}

func TestStagedTextTagsAndUnchangedAudio(t *testing.T) {
	tools(t)
	if err := exec.Command("python3", "-c", "import mutagen; assert mutagen.version_string == '1.47.0'").Run(); err != nil {
		t.Skip("pinned offline Mutagen interpreter not installed; run through uv offline test environment")
	}
	for _, extension := range []string{"opus", "ogg", "flac", "mp3", "m4a", "wav", "aac"} {
		t.Run(extension, func(t *testing.T) {
			source := fixture(t, extension)
			before, err := os.ReadFile(source.Name())
			if err != nil {
				t.Fatal(err)
			}
			result := Stage(context.Background(), source, extension, sampleTrack(), "ffmpeg", "ffprobe")
			defer result.Close()
			if extension == "wav" || extension == "aac" {
				if result.State != Unsupported || result.File != nil {
					t.Fatalf("pass through: %+v", result)
				}
				return
			}
			if result.State != Tagged || result.File == nil {
				t.Fatalf("tagging outcome: %+v", result)
			}
			after, err := os.ReadFile(source.Name())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("original audio mutated")
			}
			// Independent native-tag readback, separate from the writer protocol.
			inspection := `import json,sys
from mutagen.flac import FLAC
from mutagen.oggopus import OggOpus
from mutagen.oggvorbis import OggVorbis
from mutagen.id3 import ID3
from mutagen.mp4 import MP4
p,fmt=sys.argv[1:]
if fmt in ('opus','ogg','flac'):
 a={'opus':OggOpus,'ogg':OggVorbis,'flac':FLAC}[fmt](p)
 v=[a['title'][0],a['artist'],a['album'][0],a['spotify_uri'][0],a['albumartist'][0],a['tracknumber'][0],a['discnumber'][0],a['date'][0]]
elif fmt=='mp3':
 a=ID3(p)
 v=[a['TIT2'].text[0],a['TPE1'].text,a['TALB'].text[0],a['TXXX:SPOTIFY_URI'].text[0],a['TPE2'].text[0],str(a['TRCK']),str(a['TPOS']),str(a['TDRC'])]
else:
 a=MP4(p)
 v=[a['\u00a9nam'][0],a['\u00a9ART'],a['\u00a9alb'][0],a['----:com.apple.iTunes:SPOTIFY_URI'][0].decode(),a['aART'][0],str(a['trkn'][0][0]),str(a['disk'][0][0]),a['\u00a9day'][0]]
print(json.dumps(v))`
			out, err := exec.Command("python3", "-c", inspection, result.File.Name(), extension).CombinedOutput()
			if err != nil {
				t.Fatalf("inspect native tags: %v %s", err, out)
			}
			var fields []json.RawMessage
			if err := json.Unmarshal(out, &fields); err != nil {
				t.Fatal(err)
			}
			var got, want []any
			_ = json.Unmarshal(out, &got)
			_ = json.Unmarshal([]byte(`["Fixture Title",["First Artist","Second Artist"],"Fixture Album","spotify:track:fixture","Album Artist","2","1","2024-05-03"]`), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("native tags: %s", out)
			}
			if len(fields) != 8 {
				t.Fatal("missing native fields")
			}
		})
	}
}

func TestFailureAndCancellationKeepOriginal(t *testing.T) {
	tools(t)
	source := fixture(t, "flac")
	before, _ := os.ReadFile(source.Name())
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		probe string
	}{
		{"unavailable probe", context.Background(), "nonexistent-offbeat-ffprobe"},
		{"cancel", canceled(), "ffprobe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := Stage(tc.ctx, source, "flac", sampleTrack(), "ffmpeg", tc.probe)
			defer result.Close()
			if result.State != Failed || result.File != nil || strings.Contains(result.Error, source.Name()) {
				t.Fatalf("failure: %+v", result)
			}
			after, _ := os.ReadFile(source.Name())
			if !bytes.Equal(before, after) {
				t.Fatal("original changed on failure")
			}
		})
	}
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestTagProcessStopsOnCancellationAndBoundsOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := run(ctx, exec.CommandContext(ctx, "sh", "-c", "sleep 20 & wait"), 16); err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("canceled helper did not stop promptly")
	}
	if err := run(context.Background(), exec.Command("sh", "-c", "printf 'private/path-or-url'"), 4); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("oversized helper output was returned: %v", err)
	}
}
