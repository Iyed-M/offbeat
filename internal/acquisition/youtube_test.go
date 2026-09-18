package acquisition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/Iyed-M/offbeat/internal/config"
	"github.com/Iyed-M/offbeat/internal/desired"
)

func youtubeTrack(name string, duration int) desired.Track {
	return desired.Track{Name: name, Artists: []desired.NamedURI{{Name: "Massive Attack"}}, DurationMS: duration}
}

func TestYouTubeResolverRealAuthorizedFixture(t *testing.T) {
	if os.Getenv("OFFBEAT_TEST_REAL_YOUTUBE") != "1" {
		t.Skip("set OFFBEAT_TEST_REAL_YOUTUBE=1 with authorized fixture metadata")
	}
	title, artist, rawDuration, expectedID := os.Getenv("OFFBEAT_TEST_YOUTUBE_TITLE"), os.Getenv("OFFBEAT_TEST_YOUTUBE_ARTIST"), os.Getenv("OFFBEAT_TEST_YOUTUBE_DURATION_MS"), os.Getenv("OFFBEAT_TEST_YOUTUBE_ID")
	duration, err := strconv.Atoi(rawDuration)
	if err != nil || title == "" || artist == "" || !youtubeIDPattern.MatchString(expectedID) {
		t.Fatal("set OFFBEAT_TEST_YOUTUBE_TITLE, _ARTIST, _DURATION_MS, and _ID for a user-authorized fixture")
	}
	for _, tool := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("required tool %s: %v", tool, err)
		}
	}
	cfg := config.Downloader{YTDLPPath: "yt-dlp", FFmpegPath: "ffmpeg", FFprobePath: "ffprobe"}
	track := desired.Track{Name: title, Artists: []desired.NamedURI{{Name: artist}}, DurationMS: duration}
	url, err := NewYouTubeResolver(cfg).Resolve(context.Background(), track)
	if err != nil || url != "https://www.youtube.com/watch?v="+expectedID {
		t.Fatalf("resolution = %q, %v", url, err)
	}
	media, err := NewRetriever(cfg).Retrieve(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	if media.File == nil || media.Extension == "" {
		t.Fatal("authorized fixture produced no audio")
	}
}

func TestBuildYouTubeQueryIsStable(t *testing.T) {
	track := youtubeTrack("  Teardrop (Live) ", 330_000)
	track.Artists = append(track.Artists, desired.NamedURI{Name: " Elizabeth Fraser "})
	got := BuildYouTubeQuery(track)
	if want := "Massive Attack - Elizabeth Fraser - Teardrop (Live)"; got.Text != want || got.DurationMS != 330_000 || len(got.VersionMarkers) != 1 || got.VersionMarkers[0] != "live" {
		t.Fatalf("query = %#v, want text %q, duration and live marker", got, want)
	}
}

func TestSelectYouTubeCandidateUniqueConservativeMatch(t *testing.T) {
	track := youtubeTrack("Teardrop", 330_000)
	candidates := []youtubeCandidate{
		{ID: "aaaaaaaaaaa", Title: "Massive Attack - Teardrop (Official Audio)", Channel: "Massive Attack", Duration: 329},
		{ID: "bbbbbbbbbbb", Title: "Teardrop cover", Channel: "Massive Fan", Duration: 330},
		{ID: "ccccccccccc", Title: "Massive Attack - Teardrop", Channel: "Massive Attack", Duration: 390},
	}
	url, err := selectYouTubeCandidate(track, candidates)
	if err != nil || url != "https://www.youtube.com/watch?v=aaaaaaaaaaa" {
		t.Fatalf("selection = %q, %v", url, err)
	}
}

func TestYTDLPResolverUsesBoundedSearchAndCanonicalURL(t *testing.T) {
	tools, captured := t.TempDir(), t.TempDir()+"/args"
	body := fmt.Sprintf(`printf '%%s\n' "$@" > '%s'
printf '%%s\n' '{"entries":[{"id":"aaaaaaaaaaa","title":"Massive Attack - Teardrop","channel":"Massive Attack","duration":330}]}'`, captured)
	resolver := NewYouTubeResolver(config.Downloader{YTDLPPath: writeTool(t, tools, "yt-dlp-search", body)})
	url, err := resolver.Resolve(context.Background(), youtubeTrack("Teardrop", 330_000))
	if err != nil || url != "https://www.youtube.com/watch?v=aaaaaaaaaaa" {
		t.Fatalf("Resolve = %q, %v", url, err)
	}
	args, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	want := "--no-config\n--no-plugin-dirs\n--dump-single-json\n--playlist-end\n5\n--no-warnings\n--\nytsearch5:Massive Attack - Teardrop\n"
	if string(args) != want {
		t.Fatalf("args = %q, want %q", args, want)
	}
}

func TestSelectYouTubeCandidateLeavesUnsafeCasesUnresolved(t *testing.T) {
	tests := []struct {
		name       string
		track      desired.Track
		candidates []youtubeCandidate
	}{
		{name: "no result", track: youtubeTrack("Teardrop", 330_000)},
		{name: "version conflict", track: youtubeTrack("Teardrop (Live)", 330_000), candidates: []youtubeCandidate{{ID: "aaaaaaaaaaa", Title: "Massive Attack - Teardrop (Official Audio)", Channel: "Massive Attack", Duration: 330}}},
		{name: "duration", track: youtubeTrack("Teardrop", 330_000), candidates: []youtubeCandidate{{ID: "aaaaaaaaaaa", Title: "Massive Attack - Teardrop", Channel: "Massive Attack", Duration: 350}}},
		{name: "tie", track: youtubeTrack("Teardrop", 330_000), candidates: []youtubeCandidate{
			{ID: "aaaaaaaaaaa", Title: "Massive Attack - Teardrop", Channel: "Massive Attack", Duration: 330},
			{ID: "bbbbbbbbbbb", Title: "Massive Attack - Teardrop", Channel: "Massive Attack", Duration: 330},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := selectYouTubeCandidate(tt.track, tt.candidates); !errors.Is(err, ErrUnresolved) {
				t.Fatalf("error = %v, want ErrUnresolved", err)
			}
		})
	}
}
