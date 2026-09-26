package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func TestReviewReadOnlyAndEscaped(t *testing.T) {
	const uri = "spotify:track:abc"
	var commands []string
	fake := func(_ context.Context, req ipc.Request, _ time.Duration) (ipc.Response, error) {
		commands = append(commands, req.Command)
		switch req.Command {
		case "review.list":
			return ipc.Response{Version: 1, Result: ipc.ReviewPage{Tracks: []ipc.ReviewTrack{{TrackURI: uri, Title: `<img src=x onerror=alert(1)>`, Artists: []string{`<script>alert(2)</script>`}, DurationMS: 123000, WorkState: "unresolved"}}}}, nil
		case "acquire.inspect":
			return ipc.Response{Version: 1, Result: acquisition.ResolutionInspection{ReportVersion: 1, FreshSearch: true, CapturedAt: time.Now(), Track: acquisition.InspectionTrack{URI: uri, Title: `<svg onload=alert(3)>`, Artists: []acquisition.InspectionNamedURI{{Name: `<b>evil</b>`}}, DurationMS: 123000}, Search: acquisition.InspectionSearch{RawResults: []acquisition.YouTubeSearchResult{{ID: "abcdefghijk", Title: `<script>alert(4)</script>`, Uploader: `" onmouseover="evil`, Duration: 123}, {ID: "https://bad", Title: "Rejected"}}}, Candidates: []acquisition.CandidateEvidence{{FirstSearchPosition: 1, VideoID: "abcdefghijk", Eligible: true, Score: intReview(92)}, {FirstSearchPosition: 2, RejectionReason: acquisition.ResolutionMetadata}}}}, nil
		}
		t.Fatalf("mutating command: %s", req.Command)
		return ipc.Response{}, nil
	}
	h := newReviewHandler("127.0.0.1:12345", strings.Repeat("a", 64), fake)
	base := "/s/" + strings.Repeat("a", 64) + "/"
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "http://127.0.0.1:12345"+path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	list := get(base)
	if list.Code != 200 || strings.Contains(list.Body.String(), `<img`) || !strings.Contains(list.Body.String(), "&lt;img") {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	track := get(base + "track?uri=" + url.QueryEscape(uri))
	if track.Code != 200 || !strings.Contains(track.Body.String(), "Loading fresh candidates") || !strings.Contains(track.Body.String(), "fetch(") {
		t.Fatalf("track: %s", track.Body.String())
	}
	inspect := get(base + "inspect?uri=" + url.QueryEscape(uri))
	if inspect.Code != 200 || strings.Contains(inspect.Body.String(), `<script>alert`) || strings.Contains(inspect.Body.String(), `<svg`) || !strings.Contains(inspect.Body.String(), "score 92") || !strings.Contains(inspect.Body.String(), "metadata_mismatch") || !strings.Contains(inspect.Body.String(), "https://www.youtube.com/watch?v=abcdefghijk") || strings.Contains(inspect.Body.String(), "https://bad") {
		t.Fatalf("inspection: %d %s", inspect.Code, inspect.Body.String())
	}
	get(base + "track?uri=" + url.QueryEscape(uri)) // refresh
	get(base)                                       // skip
	if strings.Join(commands, ",") != "review.list,acquire.inspect,review.list" {
		t.Fatalf("commands: %v", commands)
	}
}

func intReview(n int) *int { return &n }

func TestReviewSecurityBoundsAndErrors(t *testing.T) {
	const host = "127.0.0.1:9876"
	base := "/s/" + strings.Repeat("b", 64) + "/"
	var calls int
	fake := func(_ context.Context, req ipc.Request, _ time.Duration) (ipc.Response, error) {
		calls++
		if req.Command == "review.list" {
			return ipc.Response{Version: 1, Result: ipc.ReviewPage{Tracks: make([]ipc.ReviewTrack, 33)}}, nil
		}
		return ipc.Response{Version: 1, Error: &ipc.Error{Code: ipc.CodeInternal, Message: `<tool failed>`}}, nil
	}
	h := newReviewHandler(host, strings.Repeat("b", 64), fake)
	for _, tc := range []struct {
		path, host, origin, method string
		want                       int
	}{
		{base, "evil.test", "", "GET", 403}, {base, host, "http://evil.test", "GET", 403}, {"/", host, "", "GET", 404}, {base, host, "", "POST", 405}, {base + "track?uri=bad", host, "", "GET", 400}, {base, host, "", "GET", 502}, {base + "inspect?uri=spotify:track:abc", host, "", "GET", 502},
		{"/s/" + strings.Repeat("a", 64) + "/", host, "", "GET", 404},
	} {
		r := httptest.NewRequest(tc.method, "http://"+host+tc.path, nil)
		r.Host = tc.host
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.want)
		}
		if tc.want == 502 && strings.Contains(w.Body.String(), `<tool failed>`) {
			t.Fatal("unescaped error")
		}
		if w.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatal("missing security headers")
		}
	}
	if calls != 2 {
		t.Fatalf("unexpected Control calls: %d", calls)
	}
}

func TestReviewPaginationAndCanceledInspection(t *testing.T) {
	const host = "127.0.0.1:9876"
	base := "/s/" + strings.Repeat("c", 64) + "/"
	var calls []ipc.Request
	fake := func(ctx context.Context, req ipc.Request, _ time.Duration) (ipc.Response, error) {
		calls = append(calls, req)
		if req.Command == "acquire.inspect" {
			return ipc.Response{Version: ipc.ProtocolVersion, Error: &ipc.Error{Code: ipc.CodeInternal, Message: "YouTube inspection canceled"}}, nil
		}
		start := 0
		if req.ReviewList != nil {
			for i := 0; i < 105; i++ {
				if req.ReviewList.AfterURI == fmt.Sprintf("spotify:track:%03d", i) {
					start = i + 1
					break
				}
			}
		}
		page := ipc.ReviewPage{}
		for i := start; i < 105 && len(page.Tracks) < reviewPageSizeHTTP; i++ {
			page.Tracks = append(page.Tracks, ipc.ReviewTrack{TrackURI: fmt.Sprintf("spotify:track:%03d", i), Title: fmt.Sprintf("Song %03d", i), WorkState: "unresolved"})
		}
		if start+len(page.Tracks) < 105 {
			page.NextAfterURI = page.Tracks[len(page.Tracks)-1].TrackURI
		}
		return ipc.Response{Version: ipc.ProtocolVersion, Result: page}, nil
	}
	h := newReviewHandler(host, strings.Repeat("c", 64), fake)
	path := base
	for page := 0; page < 4; page++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+path, nil))
		if w.Code != 200 {
			t.Fatalf("page %d: %d %s", page, w.Code, w.Body.String())
		}
		if page == 3 && (!strings.Contains(w.Body.String(), "Song 104") || strings.Contains(w.Body.String(), "Next page")) {
			t.Fatalf("last page: %s", w.Body.String())
		}
		if page < 3 {
			path = base + "?after=" + url.QueryEscape(fmt.Sprintf("spotify:track:%03d", page*32+31))
		}
	}
	if len(calls) != 4 || calls[0].ReviewList != nil || calls[3].ReviewList.AfterURI != "spotify:track:095" {
		t.Fatalf("pagination requests: %#v", calls)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+base+"inspect?uri=spotify:track:abc", nil))
	if w.Code != 502 || !strings.Contains(w.Body.String(), "canceled") {
		t.Fatalf("canceled inspection: %d %s", w.Code, w.Body.String())
	}
	for _, req := range calls {
		if req.Command != "review.list" && req.Command != "acquire.inspect" {
			t.Fatalf("unexpected command: %s", req.Command)
		}
	}
}
