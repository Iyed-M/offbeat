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

const (
	youtubeCandidateLimit    = 10
	youtubeTitleFloor        = 60
	youtubeArtistFloor       = 75
	youtubeAcceptanceFloor   = 82
	youtubeRunnerUpMargin    = 7
	youtubeTitleWeight       = 60
	youtubeArtistWeight      = 25
	youtubeDurationWeight    = 15
	youtubeDurationFloorMS   = 12_000
	youtubeDurationPercent   = 5
	youtubeDurationCeilingMS = 20_000
)

// ErrUnresolved means the bounded YouTube result set did not contain one
// unique conservative match. It is an expected terminal outcome, not a tool
// or download failure.
var ErrUnresolved = errors.New("no unique eligible YouTube result")

type ResolutionReason string

const (
	ResolutionNoCandidates ResolutionReason = "no_candidates"
	ResolutionMetadata     ResolutionReason = "metadata_mismatch"
	ResolutionTitle        ResolutionReason = "title_mismatch"
	ResolutionArtist       ResolutionReason = "artist_mismatch"
	ResolutionVersion      ResolutionReason = "version_conflict"
	ResolutionDuration     ResolutionReason = "duration_mismatch"
	ResolutionWeakWinner   ResolutionReason = "weak_winner"
	ResolutionAmbiguous    ResolutionReason = "ambiguous"
)

// ResolutionDiagnostic is the bounded persisted explanation for an
// unresolved search. It deliberately contains aggregate counts only.
type ResolutionDiagnostic struct {
	Reason           ResolutionReason
	Candidates       int
	MetadataRejected int
	TitleRejected    int
	ArtistRejected   int
	VersionRejected  int
	DurationRejected int
	Eligible         int
}

func (d *ResolutionDiagnostic) Error() string {
	return fmt.Sprintf("YouTube resolution %s: candidates=%d metadata=%d title=%d artist=%d version=%d duration=%d eligible=%d", d.Reason, d.Candidates, d.MetadataRejected, d.TitleRejected, d.ArtistRejected, d.VersionRejected, d.DurationRejected, d.Eligible)
}

func (d *ResolutionDiagnostic) Unwrap() error { return ErrUnresolved }

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
		return "", &ResolutionDiagnostic{Reason: ResolutionMetadata}
	}
	ytdlp, err := configuredExecutable(r.ytdlp, "yt-dlp")
	if err != nil {
		return "", err
	}
	args := []string{
		"--no-config", "--no-plugin-dirs", "--flat-playlist", "--dump-single-json",
		"--playlist-end", fmt.Sprint(youtubeCandidateLimit), "--no-warnings", "--",
		fmt.Sprintf("ytsearch%d:%s", youtubeCandidateLimit, query.Text),
	}
	output, err := runProcessOutput(ctx, r.command(ctx, ytdlp, args...), 1<<20)
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
	score     int
	deltaMS   int
}

type candidateRejection int

const (
	rejectionMetadata candidateRejection = iota
	rejectionTitle
	rejectionArtist
	rejectionVersion
	rejectionDuration
)

func selectYouTubeCandidate(track desired.Track, candidates []youtubeCandidate) (string, error) {
	seen := make(map[string]struct{})
	ranked := make([]candidateRank, 0, len(candidates))
	diagnostic := &ResolutionDiagnostic{}
	for _, candidate := range candidates {
		if _, ok := seen[candidate.ID]; ok {
			continue
		}
		seen[candidate.ID] = struct{}{}
		diagnostic.Candidates++
		rank, rejection, ok := rankYouTubeCandidate(track, candidate)
		if ok {
			ranked = append(ranked, rank)
			continue
		}
		switch rejection {
		case rejectionTitle:
			diagnostic.TitleRejected++
		case rejectionArtist:
			diagnostic.ArtistRejected++
		case rejectionVersion:
			diagnostic.VersionRejected++
		case rejectionDuration:
			diagnostic.DurationRejected++
		default:
			diagnostic.MetadataRejected++
		}
	}
	diagnostic.Eligible = len(ranked)
	if len(ranked) == 0 {
		diagnostic.Reason = ResolutionMetadata
		switch {
		case diagnostic.Candidates == 0:
			diagnostic.Reason = ResolutionNoCandidates
		case diagnostic.TitleRejected > 0 && diagnostic.Candidates == diagnostic.TitleRejected:
			diagnostic.Reason = ResolutionTitle
		case diagnostic.ArtistRejected > 0 && diagnostic.Candidates == diagnostic.ArtistRejected:
			diagnostic.Reason = ResolutionArtist
		case diagnostic.VersionRejected > 0 && diagnostic.Candidates == diagnostic.VersionRejected:
			diagnostic.Reason = ResolutionVersion
		case diagnostic.DurationRejected > 0 && diagnostic.Candidates == diagnostic.DurationRejected:
			diagnostic.Reason = ResolutionDuration
		}
		return "", diagnostic
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].deltaMS != ranked[j].deltaMS {
			return ranked[i].deltaMS < ranked[j].deltaMS
		}
		return ranked[i].candidate.ID < ranked[j].candidate.ID
	})
	if ranked[0].score < youtubeAcceptanceFloor {
		diagnostic.Reason = ResolutionWeakWinner
		return "", diagnostic
	}
	if len(ranked) > 1 && ranked[0].score-ranked[1].score < youtubeRunnerUpMargin {
		diagnostic.Reason = ResolutionAmbiguous
		return "", diagnostic
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
	{"cover", []string{"cover"}},
	{"slowed", []string{"slowed"}},
	{"sped up", []string{"sped up"}},
	{"reverb", []string{"reverb"}},
	{"bass boosted", []string{"bass boosted", "bass boost", "bassboosted"}},
	{"8d audio", []string{"8d audio"}},
	{"acapella", []string{"acapella", "a cappella"}},
	{"nightcore", []string{"nightcore"}},
	{"edit", []string{"radio edit", "single edit", "edit"}},
	{"extended", []string{"extended"}},
	{"mono", []string{"mono"}},
	{"stereo", []string{"stereo"}},
	{"demo", []string{"demo"}},
}

var ineligibleCandidateTerms = []string{"tribute", "reaction", "tutorial"}

func rankYouTubeCandidate(track desired.Track, candidate youtubeCandidate) (candidateRank, candidateRejection, bool) {
	if !youtubeIDPattern.MatchString(candidate.ID) || candidate.Title == "" || candidate.Duration <= 0 {
		return candidateRank{}, rejectionMetadata, false
	}
	if candidate.LiveStatus == "is_live" || candidate.LiveStatus == "is_upcoming" || candidate.Availability == "private" || candidate.Availability == "premium_only" || candidate.Availability == "subscriber_only" {
		return candidateRank{}, rejectionMetadata, false
	}
	candidateText := normalizedText(candidate.Title + " " + candidate.Channel + " " + candidate.Uploader)
	for _, term := range ineligibleCandidateTerms {
		if containsPhrase(candidateText, term) {
			return candidateRank{}, rejectionMetadata, false
		}
	}
	if !sameMarkers(markerSet(track.Name), markerSet(candidateVersionText(candidate.Title, track.Artists))) {
		return candidateRank{}, rejectionVersion, false
	}
	titleTokens := trackTitleIdentityTokens(track.Name)
	candidateTitleTokens := candidateTitleIdentityTokens(candidate.Title, track.Artists)
	titleScore := tokenSimilarity(titleTokens, candidateTitleTokens)
	if len(titleTokens) == 0 || titleScore < youtubeTitleFloor {
		return candidateRank{}, rejectionTitle, false
	}
	if len(track.Artists) == 0 {
		return candidateRank{}, rejectionMetadata, false
	}
	titleArtistTokens := meaningfulTokens(candidate.Title)
	channel, uploader := normalizedText(candidate.Channel), normalizedText(candidate.Uploader)
	channelTokens, uploaderTokens := meaningfulTokens(channel), meaningfulTokens(uploader)
	artistTotal := 0
	for i, artist := range track.Artists {
		wanted := meaningfulTokens(artist.Name)
		inTitle := containmentScore(wanted, titleArtistTokens)
		inChannel := containmentScore(wanted, channelTokens)
		inUploader := containmentScore(wanted, uploaderTokens)
		if i == 0 && inTitle < youtubeArtistFloor && (supportingUploaderOnly(channel) || inChannel < youtubeArtistFloor) && (supportingUploaderOnly(uploader) || inUploader < youtubeArtistFloor) {
			return candidateRank{}, rejectionArtist, false
		}
		artistTotal += max(inTitle, inChannel, inUploader)
	}
	artistScore := artistTotal / len(track.Artists)
	if artistScore < youtubeArtistFloor {
		return candidateRank{}, rejectionArtist, false
	}
	delta := absInt(int(candidate.Duration*1000+0.5) - track.DurationMS)
	allowed := track.DurationMS * youtubeDurationPercent / 100
	if allowed < youtubeDurationFloorMS {
		allowed = youtubeDurationFloorMS
	}
	if allowed > youtubeDurationCeilingMS {
		allowed = youtubeDurationCeilingMS
	}
	if delta > allowed {
		return candidateRank{}, rejectionDuration, false
	}
	durationScore := 100 - delta*30/allowed
	score := (titleScore*youtubeTitleWeight + artistScore*youtubeArtistWeight + durationScore*youtubeDurationWeight) / 100
	return candidateRank{candidate: candidate, score: score, deltaMS: delta}, 0, true
}

func trackTitleIdentityTokens(value string) []string {
	return titleSegmentTokens(removeMarkerPhrases(value))
}

func candidateTitleIdentityTokens(value string, artists []desired.NamedURI) []string {
	for _, separator := range []string{" - ", " – ", " — "} {
		prefix, remainder, found := strings.Cut(value, separator)
		if found && textContainsArtist(prefix, artists) {
			return titleSegmentTokens(removeMarkerPhrases(remainder))
		}
	}
	fields := titleSegmentTokens(removeMarkerPhrases(value))
	original := append([]string(nil), fields...)
	for _, artist := range artists {
		artistTokens := meaningfulTokens(artist.Name)
		if len(fields) >= len(artistTokens) && sameTokenSequence(fields[:len(artistTokens)], artistTokens) {
			fields = fields[len(artistTokens):]
		}
	}
	if len(fields) == 0 {
		return original
	}
	return fields
}

func titleSegmentTokens(value string) []string {
	fields := strings.Fields(normalizedText(value))
	for i, field := range fields {
		if field == "feat" || field == "featuring" || field == "ft" {
			fields = fields[:i]
			break
		}
	}
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if !genericTokens[field] {
			result = append(result, field)
		}
	}
	return result
}

func sameTokenSequence(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func candidateVersionText(title string, artists []desired.NamedURI) string {
	for _, separator := range []string{" - ", " – ", " — "} {
		prefix, remainder, found := strings.Cut(title, separator)
		if found && textContainsArtist(prefix, artists) {
			prefixTokens := removeArtistTokens(strings.Fields(normalizedText(prefix)), artists)
			return strings.Join(prefixTokens, " ") + " " + remainder
		}
	}
	return title
}

func textContainsArtist(value string, artists []desired.NamedURI) bool {
	tokens := meaningfulTokens(value)
	for _, artist := range artists {
		if containmentScore(meaningfulTokens(artist.Name), tokens) >= youtubeArtistFloor {
			return true
		}
	}
	return false
}

func removeArtistTokens(fields []string, artists []desired.NamedURI) []string {
	remove := make(map[string]int)
	for _, artist := range artists {
		for _, token := range meaningfulTokens(artist.Name) {
			remove[token]++
		}
	}
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if remove[field] > 0 {
			remove[field]--
			continue
		}
		result = append(result, field)
	}
	return result
}

func tokenSimilarity(left, right []string) int {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	counts := make(map[string]int, len(left))
	for _, token := range left {
		counts[token]++
	}
	overlap := 0
	for _, token := range right {
		if counts[token] > 0 {
			overlap++
			counts[token]--
		}
	}
	overlapScore := 200 * overlap / (len(left) + len(right))
	if len(left) != len(right) {
		return overlapScore
	}
	leftSorted, rightSorted := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftSorted)
	sort.Strings(rightSorted)
	return max(overlapScore, editSimilarity(strings.Join(leftSorted, " "), strings.Join(rightSorted, " ")))
}

func containmentScore(want, have []string) int {
	if len(want) == 0 {
		return 0
	}
	if bestTokenWindowSimilarity(want, have) >= 85 {
		return 100
	}
	matched := 0
	used := make([]bool, len(have))
	for _, wanted := range want {
		best, bestIndex := 0, -1
		for i, token := range have {
			similarity := editSimilarity(wanted, token)
			if !used[i] && similarity > best {
				best, bestIndex = similarity, i
			}
		}
		if best >= 80 {
			matched++
			used[bestIndex] = true
		}
	}
	return 100 * matched / len(want)
}

func bestTokenWindowSimilarity(want, have []string) int {
	best := 0
	minimumWidth := max(1, len(want)-1)
	maximumWidth := min(len(have), len(want)+1)
	for width := minimumWidth; width <= maximumWidth; width++ {
		if width != len(want) {
			continue
		}
		for start := 0; start+width <= len(have); start++ {
			best = max(best, editSimilarity(strings.Join(want, " "), strings.Join(have[start:start+width], " ")))
		}
	}
	return best
}

func editSimilarity(left, right string) int {
	if left == right {
		return 100
	}
	if left == "" || right == "" {
		return 0
	}
	leftRunes, rightRunes := []rune(left), []rune(right)
	previous := make([]int, len(rightRunes)+1)
	for i := range previous {
		previous[i] = i
	}
	for i, l := range leftRunes {
		current := make([]int, len(rightRunes)+1)
		current[0] = i + 1
		for j, r := range rightRunes {
			cost := 0
			if l != r {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	longest := max(len(leftRunes), len(rightRunes))
	return 100 * (longest - previous[len(rightRunes)]) / longest
}

func supportingUploaderOnly(value string) bool {
	for _, term := range []string{"topic", "records", "recordings", "record label"} {
		if containsPhrase(value, term) {
			return true
		}
	}
	return false
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
		} else if r == '\'' || r == '’' {
			continue
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

var genericTokens = map[string]bool{"official": true, "audio": true, "video": true, "lyrics": true, "lyric": true, "visualizer": true, "hd": true, "hq": true, "topic": true, "feat": true, "featuring": true, "ft": true}

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
