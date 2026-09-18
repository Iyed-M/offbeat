package acquisition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/Iyed-M/offbeat/internal/config"
	"github.com/Iyed-M/offbeat/internal/desired"
)

const youtubeCandidateLimit = 5

// ErrUnresolved means the bounded YouTube result set did not contain one
// unique conservative match. It is an expected terminal outcome, not a tool
// or download failure.
var ErrUnresolved = errors.New("no unique eligible YouTube result")

// Resolver selects one canonical YouTube URL for a desired Spotify track.
type Resolver interface {
	Resolve(context.Context, desired.Track) (string, error)
}

// YTDLPResolver uses yt-dlp only as a bounded YouTube search/metadata seam.
// Media retrieval remains the separate Retriever boundary.
type YTDLPResolver struct {
	ytdlp   string
	command func(context.Context, string, ...string) *exec.Cmd
}

func NewYouTubeResolver(cfg config.Downloader) Resolver {
	return &YTDLPResolver{ytdlp: cfg.YTDLPPath, command: exec.CommandContext}
}

type youtubeCandidate struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Channel      string  `json:"channel"`
	Uploader     string  `json:"uploader"`
	Duration     float64 `json:"duration"`
	Availability string  `json:"availability"`
	LiveStatus   string  `json:"live_status"`
}

func (r *YTDLPResolver) Resolve(ctx context.Context, track desired.Track) (string, error) {
	if r == nil || r.ytdlp == "" {
		return "", errors.New("YouTube resolver is not configured")
	}
	query := BuildYouTubeQuery(track)
	if query.Text == "" || query.DurationMS <= 0 {
		return "", ErrUnresolved
	}
	args := []string{
		"--no-config", "--no-plugin-dirs", "--dump-single-json",
		"--playlist-end", fmt.Sprint(youtubeCandidateLimit), "--no-warnings", "--",
		fmt.Sprintf("ytsearch%d:%s", youtubeCandidateLimit, query.Text),
	}
	output, err := runProcessOutput(ctx, r.command(ctx, r.ytdlp, args...), 1<<20)
	if err != nil {
		return "", fmt.Errorf("search YouTube: %w", err)
	}
	var result struct {
		Entries []youtubeCandidate `json:"entries"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", errors.New("decode YouTube search results")
	}
	return selectYouTubeCandidate(track, result.Entries)
}

// YouTubeQuery is the deterministic resolver-local projection of Spotify
// metadata. Duration and markers guide eligibility without polluting search
// text with terms that make YouTube search less predictable.
type YouTubeQuery struct {
	Text           string
	DurationMS     int
	VersionMarkers []string
}

func BuildYouTubeQuery(track desired.Track) YouTubeQuery {
	parts := make([]string, 0, len(track.Artists)+1)
	for _, artist := range track.Artists {
		if value := strings.Join(strings.Fields(artist.Name), " "); value != "" {
			parts = append(parts, value)
		}
	}
	if title := strings.Join(strings.Fields(track.Name), " "); title != "" {
		parts = append(parts, title)
	}
	markers := markerSet(track.Name)
	orderedMarkers := make([]string, 0, len(markers))
	for _, marker := range versionMarkers {
		if markers[marker.name] {
			orderedMarkers = append(orderedMarkers, marker.name)
		}
	}
	return YouTubeQuery{Text: strings.Join(parts, " - "), DurationMS: track.DurationMS, VersionMarkers: orderedMarkers}
}

var youtubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

type candidateRank struct {
	candidate youtubeCandidate
	quality   int
	deltaMS   int
}

func selectYouTubeCandidate(track desired.Track, candidates []youtubeCandidate) (string, error) {
	seen := make(map[string]struct{})
	ranked := make([]candidateRank, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate.ID]; ok {
			continue
		}
		seen[candidate.ID] = struct{}{}
		rank, ok := rankYouTubeCandidate(track, candidate)
		if ok {
			ranked = append(ranked, rank)
		}
	}
	if len(ranked) == 0 {
		return "", ErrUnresolved
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].quality != ranked[j].quality {
			return ranked[i].quality > ranked[j].quality
		}
		return ranked[i].deltaMS < ranked[j].deltaMS
	})
	if len(ranked) > 1 && ranked[0].quality == ranked[1].quality && ranked[0].deltaMS == ranked[1].deltaMS {
		return "", ErrUnresolved
	}
	return "https://www.youtube.com/watch?v=" + ranked[0].candidate.ID, nil
}

var versionMarkers = []struct {
	name  string
	terms []string
}{
	{"live", []string{"live"}},
	{"acoustic", []string{"acoustic"}},
	{"instrumental", []string{"instrumental"}},
	{"karaoke", []string{"karaoke"}},
	{"remix", []string{"remix", "mix"}},
	{"remaster", []string{"remaster", "remastered"}},
	{"edit", []string{"radio edit", "single edit", "edit"}},
	{"extended", []string{"extended"}},
	{"mono", []string{"mono"}},
	{"stereo", []string{"stereo"}},
	{"demo", []string{"demo"}},
}

var ineligibleCandidateTerms = []string{
	"cover", "tribute", "reaction", "nightcore", "sped up", "slowed", "8d audio",
}

func rankYouTubeCandidate(track desired.Track, candidate youtubeCandidate) (candidateRank, bool) {
	if !youtubeIDPattern.MatchString(candidate.ID) || candidate.Title == "" || candidate.Duration <= 0 {
		return candidateRank{}, false
	}
	if candidate.LiveStatus == "is_live" || candidate.LiveStatus == "is_upcoming" || candidate.Availability == "private" || candidate.Availability == "premium_only" || candidate.Availability == "subscriber_only" {
		return candidateRank{}, false
	}
	candidateText := normalizedText(candidate.Title + " " + candidate.Channel + " " + candidate.Uploader)
	for _, term := range ineligibleCandidateTerms {
		if containsPhrase(candidateText, term) {
			return candidateRank{}, false
		}
	}
	if !sameMarkers(markerSet(track.Name), markerSet(candidate.Title)) {
		return candidateRank{}, false
	}
	titleTokens := meaningfulTokens(removeMarkerPhrases(track.Name))
	if len(titleTokens) == 0 || !containsAllTokens(tokenSet(candidate.Title), titleTokens) {
		return candidateRank{}, false
	}
	if len(track.Artists) == 0 {
		return candidateRank{}, false
	}
	primary := normalizedText(track.Artists[0].Name)
	artistMetadata := normalizedText(candidate.Channel + " " + candidate.Uploader)
	artistInTitle := containsPhrase(normalizedText(candidate.Title), primary)
	artistInMetadata := containsPhrase(artistMetadata, primary)
	if primary == "" || (!artistInTitle && !artistInMetadata) {
		return candidateRank{}, false
	}
	if before, _, found := strings.Cut(candidate.Title, " - "); found && !containsPhrase(normalizedText(before), primary) && !artistInMetadata {
		return candidateRank{}, false
	}
	delta := absInt(int(candidate.Duration*1000+0.5) - track.DurationMS)
	allowed := track.DurationMS * 3 / 100
	if allowed < 5000 {
		allowed = 5000
	}
	if delta > allowed {
		return candidateRank{}, false
	}
	quality := 0
	if artistInTitle {
		quality += 2
	} else {
		quality++
	}
	for _, artist := range track.Artists[1:] {
		if containsPhrase(candidateText, normalizedText(artist.Name)) {
			quality++
		}
	}
	if sameTokens(meaningfulTokens(removeMarkerPhrases(candidate.Title)), titleTokens) {
		quality += 2
	}
	return candidateRank{candidate: candidate, quality: quality, deltaMS: delta}, true
}

func markerSet(value string) map[string]bool {
	normalized := normalizedText(value)
	set := make(map[string]bool)
	for _, marker := range versionMarkers {
		for _, term := range marker.terms {
			if containsPhrase(normalized, term) {
				set[marker.name] = true
				break
			}
		}
	}
	return set
}

func removeMarkerPhrases(value string) string {
	result := normalizedText(value)
	for _, marker := range versionMarkers {
		for _, term := range marker.terms {
			result = strings.ReplaceAll(" "+result+" ", " "+term+" ", " ")
			result = strings.TrimSpace(result)
		}
	}
	return result
}

func normalizedText(value string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

var genericTokens = map[string]bool{"official": true, "audio": true, "video": true, "lyrics": true, "lyric": true, "visualizer": true, "hd": true, "hq": true, "topic": true}

func meaningfulTokens(value string) []string {
	fields := strings.Fields(normalizedText(value))
	result := fields[:0]
	for _, field := range fields {
		if !genericTokens[field] {
			result = append(result, field)
		}
	}
	return result
}

func tokenSet(value string) map[string]bool {
	set := make(map[string]bool)
	for _, token := range meaningfulTokens(value) {
		set[token] = true
	}
	return set
}

func containsAllTokens(have map[string]bool, want []string) bool {
	for _, token := range want {
		if !have[token] {
			return false
		}
	}
	return true
}

func sameTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := make(map[string]int), make(map[string]int)
	for _, value := range a {
		left[value]++
	}
	for _, value := range b {
		right[value]++
	}
	if len(left) != len(right) {
		return false
	}
	for key, count := range left {
		if right[key] != count {
			return false
		}
	}
	return true
}

func sameMarkers(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for marker := range a {
		if !b[marker] {
			return false
		}
	}
	return true
}

func containsPhrase(text, phrase string) bool {
	if phrase == "" {
		return false
	}
	return strings.Contains(" "+text+" ", " "+phrase+" ")
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
