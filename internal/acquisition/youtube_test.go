package acquisition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/config"
	"github.com/Iyed-M/offbeat/internal/desired"
)

func youtubeTrack(name string, duration int) desired.Track {
	return desired.Track{Name: name, Artists: []desired.NamedURI{{Name: "Massive Attack"}}, DurationMS: duration}
}

func TestInspectYouTubeCandidatesSeparatesRawEvidenceFromSharedDecision(t *testing.T) {
	track := youtubeTrack("Teardrop", 330_000)
	capturedAt := time.Date(2026, 9, 23, 12, 30, 0, 0, time.UTC)
	candidates := []youtubeCandidate{
		{ID: "ambiguous01", Title: "Massive Attack - Teardrop", Channel: "Massive Attack", Duration: 330},
		{ID: "ambiguous01", Title: "duplicate raw result", Channel: "Other", Duration: 1},
		{ID: "wronglive01", Title: "Massive Attack - Teardrop (Live)", Uploader: "Massive Attack", Duration: 330},
		{ID: "ambiguous02", Title: "Massive Attack - Teardrop (Official Audio)", Uploader: "Massive Attack", Duration: 329},
	}

	report := inspectYouTubeCandidates(track, BuildYouTubeQuery(track), candidates, capturedAt)
	if !report.FreshSearch || !report.CapturedAt.Equal(capturedAt) || report.Search.CandidateLimit != youtubeCandidateLimit {
		t.Fatalf("report provenance = %#v", report)
	}
	if len(report.Search.RawResults) != 4 || report.Search.RawResults[1].Title != "duplicate raw result" {
		t.Fatalf("raw results = %#v", report.Search.RawResults)
	}
	if len(report.Candidates) != 3 || report.Candidates[0].FirstSearchPosition != 1 || len(report.Candidates[0].DuplicateSearchPositions) != 1 || report.Candidates[0].DuplicateSearchPositions[0] != 2 {
		t.Fatalf("unique candidates = %#v", report.Candidates)
	}
	if !report.Candidates[0].Eligible || report.Candidates[0].Score == nil || report.Candidates[0].TitleScore == nil || report.Candidates[0].ArtistScore == nil || report.Candidates[0].DurationScore == nil {
		t.Fatalf("eligible evidence = %#v", report.Candidates[0])
	}
	if report.Candidates[1].Eligible || report.Candidates[1].RejectionReason != "version_conflict" || len(report.Candidates[1].CandidateVersionMarkers) != 1 || report.Candidates[1].CandidateVersionMarkers[0] != "live" {
		t.Fatalf("rejected evidence = %#v", report.Candidates[1])
	}
	if report.Decision.UnresolvedReason != ResolutionAmbiguous || report.Decision.SelectedURL != "" || report.Decision.AcceptanceFloor != youtubeAcceptanceFloor || report.Decision.RunnerUpMargin != youtubeRunnerUpMargin || report.Decision.FloorPassed == nil || !*report.Decision.FloorPassed || report.Decision.MarginPassed == nil || *report.Decision.MarginPassed {
		t.Fatalf("decision = %#v", report.Decision)
	}
	if _, err := selectYouTubeCandidate(track, candidates); err == nil || err.Error() != report.Diagnostic.Error() {
		t.Fatalf("Resolve decision error = %v, report diagnostic = %#v", err, report.Diagnostic)
	}
}

func TestInspectYouTubeCandidatesEnforcesSearchBound(t *testing.T) {
	candidates := make([]youtubeCandidate, youtubeCandidateLimit+1)
	for i := range candidates {
		candidates[i] = youtubeCandidate{ID: fmt.Sprintf("bounded%04d", i), Title: "Artist - Song", Channel: "Artist", Duration: 100}
	}
	track := desired.Track{Name: "Song", Artists: []desired.NamedURI{{Name: "Artist"}}, DurationMS: 100_000}
	report := inspectYouTubeCandidates(track, BuildYouTubeQuery(track), candidates, time.Now())
	if len(report.Search.RawResults) != youtubeCandidateLimit || len(report.Candidates) != youtubeCandidateLimit {
		t.Fatalf("bounded report has %d raw and %d unique candidates", len(report.Search.RawResults), len(report.Candidates))
	}
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
	want := "--no-config\n--no-plugin-dirs\n--flat-playlist\n--dump-single-json\n--playlist-end\n10\n--no-warnings\n--\nytsearch10:Massive Attack - Teardrop\n"
	if string(args) != want {
		t.Fatalf("args = %q, want %q", args, want)
	}
}

func TestYTDLPResolverReportsMissingConfiguredExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := NewYouTubeResolver(config.Downloader{YTDLPPath: "missing-yt-dlp"}).Resolve(context.Background(), youtubeTrack("Teardrop", 330_000))
	if err == nil || err.Error() != "configured yt-dlp executable not found" {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestYTDLPInspectionSearchHonorsCancellation(t *testing.T) {
	tools := t.TempDir()
	startedFile := tools + "/started"
	resolver := NewYouTubeResolver(config.Downloader{YTDLPPath: writeTool(t, tools, "yt-dlp-search", fmt.Sprintf("touch %q\nsleep 30", startedFile))})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := resolver.Inspect(ctx, youtubeTrack("Teardrop", 330_000))
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(startedFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inspection tool did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	started := time.Now()
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled inspection took %v", elapsed)
	}
}

func TestYTDLPInspectionAndResolveAgreeOnCapturedCandidates(t *testing.T) {
	tools := t.TempDir()
	body := `printf '%s\n' '{"entries":[{"id":"aaaaaaaaaaa","title":"Massive Attack - Teardrop","channel":"Massive Attack","duration":330},{"id":"bbbbbbbbbbb","title":"Massive Attack - Teardrop (Live)","channel":"Massive Attack","duration":330}]}'`
	resolver := NewYouTubeResolver(config.Downloader{YTDLPPath: writeTool(t, tools, "yt-dlp-search", body)})
	track := youtubeTrack("Teardrop", 330_000)
	report, err := resolver.Inspect(context.Background(), track)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := resolver.Resolve(context.Background(), track)
	if err != nil || selected != report.Decision.SelectedURL || selected != "https://www.youtube.com/watch?v=aaaaaaaaaaa" {
		t.Fatalf("Resolve = %q, %v; inspection decision = %#v", selected, err, report.Decision)
	}
}

func TestSelectYouTubeCandidateExplainsMissingCandidates(t *testing.T) {
	_, err := selectYouTubeCandidate(youtubeTrack("Teardrop", 330_000), nil)
	var diagnostic *ResolutionDiagnostic
	if !errors.As(err, &diagnostic) {
		t.Fatalf("error = %v, want ResolutionDiagnostic", err)
	}
	if !errors.Is(err, ErrUnresolved) || diagnostic.Reason != ResolutionNoCandidates || diagnostic.Candidates != 0 {
		t.Fatalf("diagnostic = %#v, error = %v", diagnostic, err)
	}
}

func TestSelectYouTubeCandidateScoredFixtureCorpus(t *testing.T) {
	tests := []struct {
		name       string
		track      desired.Track
		candidates []youtubeCandidate
		wantID     string
		wantReason ResolutionReason
	}{
		{
			name:       "punctuation and separators",
			track:      desired.Track{Name: "Sweet Dreams (Are Made of This)", Artists: []desired.NamedURI{{Name: "Eurythmics"}}, DurationMS: 216_000},
			candidates: []youtubeCandidate{{ID: "punctuation", Title: "Eurythmics - Sweet Dreams [Are Made Of This] (Official Video)", Uploader: "Eurythmics", Duration: 216}},
			wantID:     "punctuation",
		},
		{
			name:       "apostrophe formatting",
			track:      desired.Track{Name: "Don't Speak", Artists: []desired.NamedURI{{Name: "No Doubt"}}, DurationMS: 263_000},
			candidates: []youtubeCandidate{{ID: "apostrophe1", Title: "No Doubt - Dont Speak", Uploader: "No Doubt", Duration: 263}},
			wantID:     "apostrophe1",
		},
		{
			name:       "reordered artists",
			track:      desired.Track{Name: "Close Your Eyes", Artists: []desired.NamedURI{{Name: "Run The Jewels"}, {Name: "Zack de la Rocha"}}, DurationMS: 224_000},
			candidates: []youtubeCandidate{{ID: "artistorder", Title: "Zack de la Rocha x Run The Jewels - Close Your Eyes", Uploader: "Mass Appeal Records", Duration: 224}},
			wantID:     "artistorder",
		},
		{
			name:       "featured artist in title with label uploader",
			track:      desired.Track{Name: "Feel Good Inc.", Artists: []desired.NamedURI{{Name: "Gorillaz"}, {Name: "De La Soul"}}, DurationMS: 222_000},
			candidates: []youtubeCandidate{{ID: "featuredone", Title: "Gorillaz - Feel Good Inc. ft. De La Soul", Uploader: "Parlophone Records", Duration: 222}},
			wantID:     "featuredone",
		},
		{
			name:       "featured artists before title",
			track:      desired.Track{Name: "Feel Good Inc.", Artists: []desired.NamedURI{{Name: "Gorillaz"}, {Name: "De La Soul"}}, DurationMS: 222_000},
			candidates: []youtubeCandidate{{ID: "featurepre1", Title: "Gorillaz feat. De La Soul - Feel Good Inc.", Uploader: "Parlophone Records", Duration: 222}},
			wantID:     "featurepre1",
		},
		{
			name:       "topic uploader cannot independently prove artist",
			track:      youtubeTrack("Teardrop", 330_000),
			candidates: []youtubeCandidate{{ID: "topicupload", Title: "Teardrop", Uploader: "Massive Attack - Topic", Duration: 330}},
			wantReason: ResolutionArtist,
		},
		{
			name:       "artist channel remains strong beside label uploader",
			track:      youtubeTrack("Teardrop", 330_000),
			candidates: []youtubeCandidate{{ID: "labelbeside", Title: "Teardrop", Channel: "Massive Attack", Uploader: "UMG Recordings", Duration: 330}},
			wantID:     "labelbeside",
		},
		{
			name:       "artist substring is not identity",
			track:      desired.Track{Name: "All I Need", Artists: []desired.NamedURI{{Name: "Air"}}, DurationMS: 250_000},
			candidates: []youtubeCandidate{{ID: "substring01", Title: "Chair - All I Need", Uploader: "Chair", Duration: 250}},
			wantReason: ResolutionArtist,
		},
		{
			name:       "title token boundaries remain meaningful",
			track:      desired.Track{Name: "Therapist", Artists: []desired.NamedURI{{Name: "Artist"}}, DurationMS: 250_000},
			candidates: []youtubeCandidate{{ID: "wordbounds1", Title: "Artist - The Rapist", Uploader: "Artist", Duration: 250}},
			wantReason: ResolutionTitle,
		},
		{
			name:       "self titled exact match",
			track:      desired.Track{Name: "Talk Talk", Artists: []desired.NamedURI{{Name: "Talk Talk"}}, DurationMS: 200_000},
			candidates: []youtubeCandidate{{ID: "selftitle01", Title: "Talk Talk - Talk Talk", Uploader: "Talk Talk", Duration: 200}},
			wantID:     "selftitle01",
		},
		{
			name:       "artist words remain in authoritative title",
			track:      desired.Track{Name: "The Day", Artists: []desired.NamedURI{{Name: "The The"}}, DurationMS: 200_000},
			candidates: []youtubeCandidate{{ID: "thetheday01", Title: "The The - The Day", Uploader: "The The", Duration: 200}},
			wantID:     "thetheday01",
		},
		{
			name:       "artist word cannot disappear from track title",
			track:      desired.Track{Name: "Air Supply", Artists: []desired.NamedURI{{Name: "Air"}}, DurationMS: 200_000},
			candidates: []youtubeCandidate{{ID: "airsupply01", Title: "Air - Supply", Uploader: "Air", Duration: 200}},
			wantReason: ResolutionWeakWinner,
		},
		{
			name:       "mix in artist name is not a version marker",
			track:      desired.Track{Name: "Shout Out to My Ex", Artists: []desired.NamedURI{{Name: "Little Mix"}}, DurationMS: 246_000},
			candidates: []youtubeCandidate{{ID: "littlemix01", Title: "Little Mix - Shout Out to My Ex", Uploader: "Little Mix", Duration: 246}},
			wantID:     "littlemix01",
		},
		{
			name:       "artist name does not hide version marker",
			track:      desired.Track{Name: "Lightning Crashes", Artists: []desired.NamedURI{{Name: "Live"}}, DurationMS: 325_000},
			candidates: []youtubeCandidate{{ID: "liveartist1", Title: "Lightning Crashes (Live)", Uploader: "Live", Duration: 325}},
			wantReason: ResolutionVersion,
		},
		{
			name:       "artist prefix does not hide version marker",
			track:      youtubeTrack("Teardrop", 330_000),
			candidates: []youtubeCandidate{{ID: "prefixlive1", Title: "Massive Attack (Live) - Teardrop", Uploader: "Massive Attack", Duration: 330}},
			wantReason: ResolutionVersion,
		},
		{
			name:       "version conflict",
			track:      youtubeTrack("Teardrop", 330_000),
			candidates: []youtubeCandidate{{ID: "liveversion", Title: "Massive Attack - Teardrop (Live)", Uploader: "Massive Attack", Duration: 330}},
			wantReason: ResolutionVersion,
		},
		{
			name:       "duration conflict",
			track:      youtubeTrack("Teardrop", 330_000),
			candidates: []youtubeCandidate{{ID: "durationbad", Title: "Massive Attack - Teardrop", Uploader: "Massive Attack", Duration: 350}},
			wantReason: ResolutionDuration,
		},
		{
			name:       "missing candidate metadata",
			track:      youtubeTrack("Teardrop", 330_000),
			candidates: []youtubeCandidate{{ID: "missingmeta", Channel: "Massive Attack", Duration: 330}},
			wantReason: ResolutionMetadata,
		},
		{
			name:       "short track duration tolerance",
			track:      desired.Track{Name: "Intro", Artists: []desired.NamedURI{{Name: "The xx"}}, DurationMS: 60_000},
			candidates: []youtubeCandidate{{ID: "shorttrack1", Title: "The xx - Intro", Uploader: "The xx", Duration: 70}},
			wantID:     "shorttrack1",
		},
		{
			name:       "long track bounded duration tolerance",
			track:      desired.Track{Name: "Long Song", Artists: []desired.NamedURI{{Name: "Artist"}}, DurationMS: 600_000},
			candidates: []youtubeCandidate{{ID: "longtrack01", Title: "Artist - Long Song", Uploader: "Artist", Duration: 619}},
			wantID:     "longtrack01",
		},
		{
			name:       "weak winner",
			track:      youtubeTrack("Angel", 360_000),
			candidates: []youtubeCandidate{{ID: "weakwinner1", Title: "Massive Attack - Angel Eyes", Uploader: "Massive Attack", Duration: 360}},
			wantReason: ResolutionWeakWinner,
		},
		{
			name:  "close runners up",
			track: youtubeTrack("Teardrop", 330_000),
			candidates: []youtubeCandidate{
				{ID: "ambiguous01", Title: "Massive Attack - Teardrop", Uploader: "Massive Attack", Duration: 330},
				{ID: "ambiguous02", Title: "Massive Attack - Teardrop (Official Audio)", Uploader: "Massive Attack", Duration: 329},
			},
			wantReason: ResolutionAmbiguous,
		},
		{
			name:  "duplicate IDs do not create ambiguity",
			track: youtubeTrack("Teardrop", 330_000),
			candidates: []youtubeCandidate{
				{ID: "duplicate01", Title: "Massive Attack - Teardrop", Uploader: "Massive Attack", Duration: 330},
				{ID: "duplicate01", Title: "Massive Attack - Teardrop", Uploader: "Massive Attack", Duration: 330},
			},
			wantID: "duplicate01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := inspectYouTubeCandidates(tt.track, BuildYouTubeQuery(tt.track), tt.candidates, time.Now())
			url, err := selectYouTubeCandidate(tt.track, tt.candidates)
			if tt.wantID != "" {
				want := "https://www.youtube.com/watch?v=" + tt.wantID
				if err != nil || url != want || report.Decision.SelectedURL != want || report.Decision.UnresolvedReason != "" {
					t.Fatalf("selection = %q, %v; inspection = %#v; want %q", url, err, report.Decision, want)
				}
				return
			}
			var diagnostic *ResolutionDiagnostic
			if !errors.As(err, &diagnostic) || diagnostic.Reason != tt.wantReason || report.Decision.SelectedURL != "" || report.Decision.UnresolvedReason != tt.wantReason {
				t.Fatalf("diagnostic = %#v, error = %v, inspection = %#v, want reason %q", diagnostic, err, report.Decision, tt.wantReason)
			}
		})
	}
}

func TestSelectYouTubeCandidateRejectsConflictingVersionMarkers(t *testing.T) {
	for i, marker := range []string{"Live", "Remix", "Remastered", "Acoustic", "Instrumental", "Cover", "Slowed", "Sped Up", "Reverb", "Bass Boosted", "8D Audio", "Acapella"} {
		t.Run(marker, func(t *testing.T) {
			candidate := youtubeCandidate{ID: fmt.Sprintf("marker%05d", i), Title: "Massive Attack - Teardrop (" + marker + ")", Uploader: "Massive Attack", Duration: 330}
			_, err := selectYouTubeCandidate(youtubeTrack("Teardrop", 330_000), []youtubeCandidate{candidate})
			var diagnostic *ResolutionDiagnostic
			if !errors.As(err, &diagnostic) || diagnostic.Reason != ResolutionVersion || diagnostic.VersionRejected != 1 {
				t.Fatalf("diagnostic = %#v, error = %v", diagnostic, err)
			}
		})
	}

	track := youtubeTrack("Teardrop (Acoustic)", 330_000)
	url, err := selectYouTubeCandidate(track, []youtubeCandidate{{ID: "requested01", Title: "Massive Attack - Teardrop Acoustic", Uploader: "Massive Attack", Duration: 330}})
	if err != nil || url != "https://www.youtube.com/watch?v=requested01" {
		t.Fatalf("requested version = %q, %v", url, err)
	}
}

func TestSelectYouTubeCandidateRejectsAlternateContent(t *testing.T) {
	for i, alternate := range []string{"tribute", "reaction", "tutorial"} {
		t.Run(alternate, func(t *testing.T) {
			candidate := youtubeCandidate{ID: fmt.Sprintf("other%06d", i), Title: "Massive Attack - Teardrop " + alternate, Uploader: "Massive Attack", Duration: 330}
			_, err := selectYouTubeCandidate(youtubeTrack("Teardrop", 330_000), []youtubeCandidate{candidate})
			var diagnostic *ResolutionDiagnostic
			if !errors.As(err, &diagnostic) || diagnostic.MetadataRejected != 1 {
				t.Fatalf("diagnostic = %#v, error = %v", diagnostic, err)
			}
		})
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
