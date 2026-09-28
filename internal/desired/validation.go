package desired

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxArtists = 64

var releaseDatePattern = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2})?)?$`)

// ValidateTrack bounds normalized presentation data at the daemon boundary.
// Empty optional strings mean absent; optional numbers use nil for absence.
func ValidateTrack(t Track) error {
	if !validText(t.URI, 512, true) || !validText(t.Name, 1024, true) ||
		!validText(t.Album.URI, 512, true) || !validText(t.Album.Name, 1024, true) ||
		len(t.Artists) == 0 || len(t.Artists) > MaxArtists || t.DurationMS <= 0 || t.DurationMS > 24*60*60*1000 {
		return errors.New("invalid required track metadata")
	}
	for _, artist := range t.Artists {
		if !validText(artist.URI, 512, true) || !validText(artist.Name, 1024, true) {
			return errors.New("invalid artist metadata")
		}
	}
	if !validText(t.AlbumArtist, 1024, false) || !validNumber(t.TrackNumber) || !validNumber(t.DiscNumber) || !validDate(t.ReleaseDate) || !validArtworkURL(t.ArtworkURL) {
		return errors.New("invalid optional track metadata")
	}
	return nil
}

func validText(s string, max int, required bool) bool {
	if required && s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validNumber(n *int) bool { return n == nil || *n > 0 && *n <= 9999 }

func validDate(s string) bool {
	if s == "" {
		return true
	}
	if !releaseDatePattern.MatchString(s) {
		return false
	}
	// Spotify can supply year-only and year-month precision; check ranges without inventing a day.
	if len(s) >= 7 && (s[5:7] < "01" || s[5:7] > "12") {
		return false
	}
	if len(s) == 10 {
		if s[8:10] < "01" || s[8:10] > "31" {
			return false
		}
		// Reject impossible calendar dates, including leap-day errors.
		return validCalendarDay(s)
	}
	return true
}

func validCalendarDay(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func validArtworkURL(s string) bool {
	if s == "" {
		return true
	}
	if !validText(s, 2048, true) || strings.ContainsAny(s, " \\") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == "" && u.Port() == ""
}
