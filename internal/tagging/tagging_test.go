package tagging

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/artwork"
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
			if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
				t.Fatalf("required tagging check needs %s: %v", name, err)
			}
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
		if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
			t.Fatal("pinned Mutagen 1.47.0 unavailable: provision Python3 offline before required native-tag check")
		}
		t.Skip("pinned offline Mutagen interpreter not installed; run the required native-tag check")
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

func TestMissingTagRuntimeProducesActionablePrivateDiagnostic(t *testing.T) {
	tools(t)
	source := fixture(t, "flac")
	path := source.Name()
	t.Setenv("PATH", t.TempDir())
	result := Stage(context.Background(), source, "flac", sampleTrack(), "/usr/bin/ffmpeg", "/usr/bin/ffprobe")
	defer result.Close()
	if result.State != Failed || !strings.Contains(result.Error, "Python3") || strings.Contains(result.Error, path) {
		t.Fatalf("missing runtime diagnostic = %q", result.Error)
	}
}

func TestStagedNativeArtworkAndTextTags(t *testing.T) {
	tools(t)
	if err := exec.Command("python3", "-c", "import mutagen; assert mutagen.version_string == '1.47.0'").Run(); err != nil {
		if os.Getenv("OFFBEAT_REQUIRE_TAGGING") == "1" {
			t.Fatal("pinned Mutagen unavailable")
		}
		t.Skip("pinned Mutagen unavailable")
	}
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, im); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"opus", "ogg", "flac", "mp3", "m4a"} {
		t.Run(ext, func(t *testing.T) {
			source := fixture(t, ext)
			result := StageWithArtwork(context.Background(), source, ext, sampleTrack(), "ffmpeg", "ffprobe", &artwork.Image{Data: encoded.Bytes(), MIME: "image/png"})
			defer result.Close()
			if result.State != Tagged {
				t.Fatalf("stage: %+v", result)
			}
			// Independent reader checks native picture bytes, title and artist order.
			inspect := `import sys,base64
from mutagen.flac import FLAC,Picture
from mutagen.oggopus import OggOpus
from mutagen.oggvorbis import OggVorbis
from mutagen.id3 import ID3
from mutagen.mp4 import MP4
p,ext,picture=sys.argv[1:]
if ext in ('opus','ogg','flac'):
 a={'opus':OggOpus,'ogg':OggVorbis,'flac':FLAC}[ext](p)
 assert a['title']==['Fixture Title'] and a['artist']==['First Artist','Second Artist']
 cover=a.pictures[0] if ext=='flac' else Picture(base64.b64decode(a['metadata_block_picture'][0]))
 data=cover.data
 assert cover.type==3 and cover.mime=='image/png'
elif ext=='mp3':
 a=ID3(p); assert a['TIT2'].text==['Fixture Title'] and a['TPE1'].text==['First Artist','Second Artist']
 data=a.getall('APIC')[0].data
else:
 a=MP4(p); assert a['\u00a9nam']==['Fixture Title'] and a['\u00a9ART']==['First Artist','Second Artist']
 data=bytes(a['covr'][0])
assert data==open(picture,'rb').read()`
			picture := filepath.Join(t.TempDir(), "cover.png")
			if err := os.WriteFile(picture, encoded.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("python3", "-c", inspect, result.File.Name(), ext, picture).CombinedOutput()
			if err != nil {
				t.Fatalf("native picture: %v %s", err, out)
			}
		})
	}
}
