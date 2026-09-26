package main

import (
	"context"
	"encoding/json"
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
			return ipc.Response{Version: 1, Result: acquisition.ResolutionInspection{ReportVersion: 1, FreshSearch: true, CapturedAt: time.Now(), Track: acquisition.InspectionTrack{URI: uri, Title: `<svg onload=alert(3)>`, Artists: []acquisition.InspectionNamedURI{{Name: `<b>evil</b>`, URI: "spotify:artist:abc"}}, DurationMS: 123000}, Search: acquisition.InspectionSearch{RawResults: []acquisition.YouTubeSearchResult{{ID: "abcdefghijk", Title: `<script>alert(4)</script>`, Uploader: `" onmouseover="evil`, Channel: `other <channel>`, Duration: 123}, {ID: "https://bad", Title: "Rejected"}}}, Candidates: []acquisition.CandidateEvidence{{FirstSearchPosition: 1, VideoID: "abcdefghijk", Eligible: true, Score: intReview(92)}, {FirstSearchPosition: 2, RejectionReason: acquisition.ResolutionMetadata}}}}, nil
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
	if track.Code != 200 || !strings.Contains(track.Body.String(), "Loading fresh candidates") || !strings.Contains(track.Body.String(), "base+'status?'") || !strings.Contains(track.Body.String(), "Refresh acquisition status") {
		t.Fatalf("track: %s", track.Body.String())
	}
	inspect := get(base + "inspect?uri=" + url.QueryEscape(uri))
	if inspect.Code != 200 || strings.Contains(inspect.Body.String(), `<script>alert`) || strings.Contains(inspect.Body.String(), `<svg`) || !strings.Contains(inspect.Body.String(), "score 92") || !strings.Contains(inspect.Body.String(), "metadata_mismatch") || !strings.Contains(inspect.Body.String(), "https://www.youtube.com/watch?v=abcdefghijk") || strings.Contains(inspect.Body.String(), "https://bad") || !strings.Contains(inspect.Body.String(), `data-choice=`) || !strings.Contains(inspect.Body.String(), `expected_artist_uris`) || strings.Contains(inspect.Body.String(), `onmouseover="evil`) || !strings.Contains(inspect.Body.String(), "Uploader: &#34; onmouseover=&#34;evil · Channel: other &lt;channel&gt;") {
		t.Fatalf("inspection: %d %s", inspect.Code, inspect.Body.String())
	}
	get(base + "track?uri=" + url.QueryEscape(uri)) // refresh
	get(base)                                       // skip
	if strings.Join(commands, ",") != "review.list,acquire.inspect,review.list" {
		t.Fatalf("commands: %v", commands)
	}
}

func TestReviewSelectionAndMappingMutations(t *testing.T) {
	const host = "127.0.0.1:9876"
	token := strings.Repeat("d", 64)
	base := "/s/" + token + "/"
	const uri = "spotify:track:abc"
	var calls []ipc.Request
	fake := func(_ context.Context, req ipc.Request, _ time.Duration) (ipc.Response, error) {
		calls = append(calls, req)
		switch req.Command {
		case "acquire.select":
			if req.AcquisitionChoice.VideoID != "abcdefghijk" {
				t.Fatal("wrong selected ID")
			}
			if req.AcquisitionChoice.ExpectedTitle == "old" {
				return ipc.Response{Version: 1, Error: &ipc.Error{Code: ipc.CodeFailedPrecondition, Message: "Spotify metadata changed; refresh inspection"}}, nil
			}
			return ipc.Response{Version: 1, Result: ipc.AcquisitionResult{ID: 3, TrackURI: uri, SourceKind: "youtube", State: "pending"}}, nil
		case "acquire.mapping.show":
			return ipc.Response{Version: 1, Result: ipc.ManualMappingResult{TrackURI: uri, VideoID: "abcdefghijk", Provenance: "manual", CreatedAt: "now", UpdatedAt: "now"}}, nil
		case "acquire.mapping.list":
			return ipc.Response{Version: 1, Result: ipc.ManualMappingPage{Mappings: []ipc.ManualMappingResult{{TrackURI: uri, VideoID: "abcdefghijk", Provenance: "manual", CreatedAt: "now", UpdatedAt: "now"}}}}, nil
		case "acquire.mapping.remove":
			return ipc.Response{Version: 1, Result: ipc.ManualMappingRequest{TrackURI: uri}}, nil
		case "acquire.mapping.set":
			return ipc.Response{Version: 1, Result: ipc.ManualMappingResult{TrackURI: uri, VideoID: req.ManualMapping.VideoID, Provenance: "manual", CreatedAt: "now", UpdatedAt: "now"}}, nil
		}
		t.Fatalf("unexpected command %s", req.Command)
		return ipc.Response{}, nil
	}
	h := newReviewHandler(host, token, fake)
	post := func(route, body, hostHeader, origin, header string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "http://"+host+base+route, strings.NewReader(body))
		r.Host = hostHeader
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Offbeat-Token", header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	choice := ipc.AcquisitionChoice{TrackURI: uri, VideoID: "abcdefghijk", ExpectedTitle: "Song", ExpectedArtists: []string{"Singer"}, ExpectedArtistURIs: []string{"spotify:artist:abc"}, ExpectedDurationMS: 120000, RejectionReason: "metadata_mismatch", AcknowledgeRejection: "metadata_mismatch"}
	data, _ := json.Marshal(choice)
	for _, tc := range []struct {
		route, body, host, origin, token string
		code                             int
	}{
		{"select", string(data), host, "", token, 403},
		{"select", string(data), host, "http://evil.test", token, 403},
		{"select", string(data), "evil.test", "http://" + host, token, 403},
		{"select", string(data), host, "http://" + host, "bad", 403},
		{"select", `{"track_uri":"spotify:track:abc","video_id":"short"}`, host, "http://" + host, token, 400},
		{"select", strings.Repeat("a", 5000), host, "http://" + host, token, 400},
		{"mapping/set", `{"track_uri":"spotify:track:abc","video_id":"ZYXWvu_987-","expected_video_id":"abcdefghijk"}`, host, "http://evil.test", token, 403},
		{"mapping/remove", `{"track_uri":"spotify:track:abc","expected_video_id":"abcdefghijk"}`, host, "http://" + host, "bad", 403},
	} {
		w := post(tc.route, tc.body, tc.host, tc.origin, tc.token)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.route, w.Code, w.Body.String())
		}
	}
	if len(calls) != 0 {
		t.Fatalf("unauthorized mutation reached Control: %+v", calls)
	}
	for _, route := range []string{"select", "mapping/set", "mapping/remove"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+base+route, nil))
		if w.Code != 404 {
			t.Fatalf("GET %s: %d", route, w.Code)
		}
	}
	choice.AcknowledgeRejection = ""
	data, _ = json.Marshal(choice)
	if w := post("select", string(data), host, "http://"+host, token); w.Code != 400 {
		t.Fatalf("rejected without acknowledgement: %d", w.Code)
	}
	choice.AcknowledgeRejection = choice.RejectionReason
	choice.ExpectedTitle = "old"
	data, _ = json.Marshal(choice)
	if w := post("select", string(data), host, "http://"+host, token); w.Code != 409 || !strings.Contains(w.Body.String(), "refresh inspection") {
		t.Fatalf("stale: %d %s", w.Code, w.Body.String())
	}
	choice.ExpectedTitle = "Song"
	data, _ = json.Marshal(choice)
	if w := post("select", string(data), host, "http://"+host, token); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"pending"`) {
		t.Fatalf("selected: %d %s", w.Code, w.Body.String())
	}
	for _, route := range []string{"mappings", "mapping?uri=" + url.QueryEscape(uri)} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+base+route, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body.String())
		}
	}
	for _, route := range []string{"mapping/set", "mapping/remove"} {
		body := `{"track_uri":"spotify:track:abc","expected_video_id":"abcdefghijk"`
		if route == "mapping/set" {
			body += `,"video_id":"ZYXWvu_987-"`
		}
		body += `}`
		if w := post(route, body, host, "http://"+host, token); w.Code != 200 {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body.String())
		}
	}
	if len(calls) != 6 {
		t.Fatalf("Control calls: %+v", calls)
	}
}

func intReview(n int) *int { return &n }

func TestReviewStatusIsScopedReadOnlyAndReportsOutcome(t *testing.T) {
	const host = "127.0.0.1:9876"
	const uri = "spotify:track:abc"
	token := strings.Repeat("e", 64)
	base := "http://" + host + "/s/" + token + "/"
	states := []ipc.AcquisitionResult{
		{ID: 42, TrackURI: uri, SourceKind: "youtube", State: "running"},
		{ID: 42, TrackURI: uri, SourceKind: "youtube", State: "failed", Error: "video unavailable"},
		{ID: 42, TrackURI: uri, SourceKind: "youtube", State: "complete"},
	}
	var calls int
	fake := func(_ context.Context, req ipc.Request, _ time.Duration) (ipc.Response, error) {
		if req.Command != "acquire.status" || req.AcquisitionStatus == nil || req.AcquisitionStatus.ID != 42 {
			t.Fatalf("unexpected Control request: %+v", req)
		}
		calls++
		return ipc.Response{Version: ipc.ProtocolVersion, Result: states[calls-1]}, nil
	}
	h := newReviewHandler(host, token, fake)
	request := func(path, method, requestHost, origin, fetchSite string) *httptest.ResponseRecorder {
		t.Helper()
		address := base + path
		if strings.HasPrefix(path, "/") {
			address = "http://" + host + path
		}
		r := httptest.NewRequest(method, address, nil)
		r.Host = requestHost
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if fetchSite != "" {
			r.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	path := "status?id=42&uri=" + url.QueryEscape(uri)
	for _, tc := range []struct {
		path, method, host, origin, site string
		code                             int
	}{
		{path, "GET", "evil.test", "", "", 403},
		{path, "GET", host, "http://evil.test", "", 403},
		{path, "GET", host, "", "cross-site", 403},
		{"/s/" + strings.Repeat("f", 64) + "/" + path, "GET", host, "", "", 404},
		{"status?id=0&uri=" + url.QueryEscape(uri), "GET", host, "", "", 400},
		{"status?id=42&uri=bad", "GET", host, "", "", 400},
		{"status?id=42&uri=" + url.QueryEscape(uri) + "&extra=1", "GET", host, "", "", 400},
		{path, "POST", host, "", "", 405},
	} {
		w := request(tc.path, tc.method, tc.host, tc.origin, tc.site)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("unauthorized status reached daemon: %d", calls)
	}
	for _, state := range states {
		w := request(path, "GET", host, "", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"`+state.State+`"`) || (state.Error != "" && !strings.Contains(w.Body.String(), state.Error)) {
			t.Fatalf("status: %d %s", w.Code, w.Body.String())
		}
	}
	if calls != 3 {
		t.Fatalf("status calls: %d", calls)
	}
}

func TestReviewStatusRejectsMismatchedDaemonWorkAndErrors(t *testing.T) {
	const host = "127.0.0.1:9876"
	token := strings.Repeat("f", 64)
	base := "http://" + host + "/s/" + token + "/status?id=42&uri=spotify:track:abc"
	var result ipc.Response
	h := newReviewHandler(host, token, func(_ context.Context, req ipc.Request, _ time.Duration) (ipc.Response, error) { return result, nil })
	for _, work := range []ipc.AcquisitionResult{
		{ID: 43, TrackURI: "spotify:track:abc", SourceKind: "youtube", State: "complete"},
		{ID: 42, TrackURI: "spotify:track:other", SourceKind: "youtube", State: "failed"},
		{ID: 42, TrackURI: "spotify:track:abc", SourceKind: "http", State: "complete"},
		{ID: 42, TrackURI: "spotify:track:abc", SourceKind: "youtube", State: "unknown"},
	} {
		result = ipc.Response{Version: ipc.ProtocolVersion, Result: work}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", base, nil))
		if w.Code != 502 || !strings.Contains(w.Body.String(), "invalid daemon response") {
			t.Fatalf("mismatched work %+v: %d %s", work, w.Code, w.Body.String())
		}
	}
	result = ipc.Response{Version: ipc.ProtocolVersion, Error: &ipc.Error{Code: ipc.CodeInternal, Message: "worker unavailable"}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", base, nil))
	if w.Code != 502 || !strings.Contains(w.Body.String(), "worker unavailable") {
		t.Fatalf("daemon error: %d %s", w.Code, w.Body.String())
	}
}

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
