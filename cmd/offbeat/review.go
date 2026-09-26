package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

type reviewControl func(context.Context, ipc.Request, time.Duration) (ipc.Response, error)

func runReview(configPath, homeDir string) int {
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat review: config: %v\n", err)
		return 1
	}
	socket := app.SocketPath(bootstrap.SocketDir)
	control := func(ctx context.Context, req ipc.Request, timeout time.Duration) (ipc.Response, error) {
		return requestControlMessageContext(ctx, socket, req, timeout)
	}
	// Refuse to advertise a session when the daemon is not available.
	if _, err := reviewRequest(context.Background(), control, ipc.Request{Version: ipc.ProtocolVersion, Command: "review.list"}, controlReadTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat review: %v\n", err)
		return 1
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat review: session token: %v\n", err)
		return 1
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat review: listen: %v\n", err)
		return 1
	}
	defer listener.Close()
	token := hex.EncodeToString(random[:])
	server := &http.Server{Handler: newReviewHandler(listener.Addr().String(), token, control), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(os.Stdout, "Review: http://%s/s/%s/\nPress Ctrl-C to close.\n", listener.Addr(), token)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "offbeat review: serve: %v\n", err)
		return 1
	}
	return 0
}

func reviewRequest(ctx context.Context, control reviewControl, req ipc.Request, timeout time.Duration) (any, error) {
	resp, err := control(ctx, req, timeout)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("search canceled")
		}
		return nil, fmt.Errorf("daemon unavailable: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Version != ipc.ProtocolVersion {
		return nil, errors.New("unexpected daemon protocol version")
	}
	return resp.Result, nil
}

func decodeReviewResult(result any, out any) error {
	raw, err := ipc.Encode(result)
	if err != nil {
		return err
	}
	return ipc.Decode(raw, out)
}

type reviewView struct {
	Base     template.URL
	Inspect  template.URL
	Nonce    string
	URI      string
	Next     template.URL
	Tracks   []ipc.ReviewTrack
	Track    acquisition.InspectionTrack
	Results  []reviewVideo
	Captured string
	Status   string
	Error    string
}

type reviewVideo struct {
	Title, Channel, Duration, Score, Reason, Eligibility string
	Link                                                 template.URL
}

func reviewURL(base, route string, values url.Values) template.URL {
	if len(values) == 0 {
		return template.URL(base + route)
	}
	return template.URL(base + route + "?" + values.Encode())
}

func newReviewHandler(host, token string, control reviewControl) http.Handler {
	base := "/s/" + token + "/"
	origin := "http://" + host
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+token+"'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(r.URL.Path) < len(base) || subtle.ConstantTimeCompare([]byte(r.URL.Path[:len(base)]), []byte(base)) != 1 {
			http.NotFound(w, r)
			return
		}
		route := strings.TrimPrefix(r.URL.Path, base)
		if route != "" && route != "track" && route != "inspect" {
			http.NotFound(w, r)
			return
		}
		if len(r.URL.RawQuery) > 512 {
			http.Error(w, "invalid query", http.StatusBadRequest)
			return
		}
		view := reviewView{Base: template.URL(base), Nonce: token}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var tmpl *template.Template
		switch route {
		case "":
			after := r.URL.Query().Get("after")
			if after != "" && ipc.ValidateManualMappingTrackURI(after) != nil {
				http.Error(w, "invalid cursor", http.StatusBadRequest)
				return
			}
			req := ipc.Request{Version: ipc.ProtocolVersion, Command: "review.list"}
			if after != "" {
				req.ReviewList = &ipc.ReviewListRequest{AfterURI: after}
			}
			result, err := reviewRequest(r.Context(), control, req, controlReadTimeout)
			if err == nil {
				var page ipc.ReviewPage
				err = decodeReviewResult(result, &page)
				if err == nil && (len(page.Tracks) > reviewPageSizeHTTP || (page.NextAfterURI != "" && (len(page.Tracks) == 0 || page.NextAfterURI != page.Tracks[len(page.Tracks)-1].TrackURI || page.NextAfterURI <= after))) {
					err = errors.New("invalid review page")
				}
				if err == nil {
					view.Tracks = page.Tracks
					if page.NextAfterURI != "" {
						view.Next = reviewURL(base, "", url.Values{"after": {page.NextAfterURI}})
					}
				}
			}
			if err != nil {
				view.Error = err.Error()
				w.WriteHeader(http.StatusBadGateway)
			}
			tmpl = reviewListTemplate
		case "track", "inspect":
			uri := r.URL.Query().Get("uri")
			if ipc.ValidateManualMappingTrackURI(uri) != nil {
				http.Error(w, "invalid track URI", http.StatusBadRequest)
				return
			}
			view.URI = uri
			if route == "track" {
				view.Inspect = reviewURL(base, "inspect", url.Values{"uri": {uri}})
				tmpl = reviewTrackTemplate
				break
			}
			result, err := reviewRequest(r.Context(), control, ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.inspect", AcquisitionInspect: &ipc.AcquisitionTrackRequest{TrackURI: uri}}, youtubeInspectReadTimeout)
			if err == nil {
				var report acquisition.ResolutionInspection
				err = decodeReviewResult(result, &report)
				if err == nil && (report.ReportVersion != acquisition.ResolutionInspectionVersion || !report.FreshSearch || report.Track.URI != uri || len(report.Search.RawResults) > acquisition.MaxYouTubeSearchCandidates || len(report.Candidates) > acquisition.MaxYouTubeSearchCandidates) {
					err = errors.New("invalid inspection report")
				}
				if err == nil {
					view.Track = report.Track
					view.Captured = report.CapturedAt.Local().Format(time.RFC1123)
					view.Status = string(report.Decision.UnresolvedReason)
					if view.Status == "" {
						view.Status = "Search completed"
					}
					for position, raw := range report.Search.RawResults {
						video := reviewVideo{Title: raw.Title, Channel: raw.Channel, Duration: formatReviewDuration(int(raw.Duration * 1000)), Score: "—", Eligibility: "Rejected", Reason: "metadata unavailable"}
						if video.Channel == "" {
							video.Channel = raw.Uploader
						}
						for _, candidate := range report.Candidates {
							if candidate.FirstSearchPosition == position+1 || containsReviewPosition(candidate.DuplicateSearchPositions, position+1) {
								video.Reason = string(candidate.RejectionReason)
								if candidate.Eligible {
									video.Eligibility = "Eligible"
									video.Reason = "—"
								}
								if candidate.Score != nil {
									video.Score = fmt.Sprint(*candidate.Score)
								}
								break
							}
						}
						if ipc.ValidateYouTubeVideoID(raw.ID) == nil {
							video.Link = template.URL("https://www.youtube.com/watch?v=" + raw.ID)
						}
						view.Results = append(view.Results, video)
					}
				}
			}
			if err != nil {
				view.Error = err.Error()
				w.WriteHeader(http.StatusBadGateway)
			}
			tmpl = reviewInspectionTemplate
		}
		_ = tmpl.Execute(w, view)
	})
}

const reviewPageSizeHTTP = 32

func containsReviewPosition(positions []int, wanted int) bool {
	for _, p := range positions {
		if p == wanted {
			return true
		}
	}
	return false
}

func formatReviewDuration(ms int) string {
	if ms <= 0 {
		return "unknown duration"
	}
	seconds := ms / 1000
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

var reviewListTemplate = template.Must(template.New("list").Funcs(template.FuncMap{"duration": formatReviewDuration, "join": func(s []string) string { return strings.Join(s, ", ") }, "trackURL": func(base template.URL, uri string) template.URL {
	return reviewURL(string(base), "track", url.Values{"uri": {uri}})
}}).Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Offbeat review</title><style>` + reviewCSS + `</style><main><h1>Review unresolved tracks</h1><p>Currently desired, missing YouTube work. Reviewing does not start acquisition.</p>{{if .Error}}<p role="alert">Could not load review list: {{.Error}}</p><a href="{{.Base}}">Try again</a>{{else}}{{if not .Tracks}}<p>No unresolved tracks on this page.</p>{{end}}<ul>{{range .Tracks}}<li><a href="{{trackURL $.Base .TrackURI}}">{{.Title}}</a><span>{{join .Artists}} · {{duration .DurationMS}} · {{.WorkState}}</span>{{if .WorkError}}<small>Earlier work: {{.WorkError}}</small>{{end}}</li>{{end}}</ul>{{if .Next}}<a href="{{.Next}}">Next page →</a>{{end}}{{end}}</main></html>`))

var reviewTrackTemplate = template.Must(template.New("track").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Inspect track · Offbeat</title><style>` + reviewCSS + `</style><main><a href="{{.Base}}">← Review list</a><h1>Fresh YouTube inspection</h1><p>These are new search observations, not the results of the earlier unresolved attempt. Opening and refreshing do not change work or mappings.</p><p><a href="{{.Base}}track?uri={{urlquery .URI}}">Refresh candidates</a> · <a href="{{.Base}}">Skip for now</a></p><section id="results" role="status">Loading fresh candidates…</section><script nonce="{{.Nonce}}">fetch({{.Inspect}}, {credentials:'same-origin'}).then(r=>r.text().then(text=>({ok:r.ok,text}))).then(({ok,text})=>{document.getElementById('results').innerHTML=text;if(!ok)document.getElementById('results').setAttribute('role','alert')}).catch(()=>{document.getElementById('results').textContent='Search canceled or connection lost. Refresh to try again.'})</script></main></html>`))

var reviewInspectionTemplate = template.Must(template.New("inspection").Funcs(template.FuncMap{"duration": formatReviewDuration, "joinArtists": func(a []acquisition.InspectionNamedURI) string {
	names := make([]string, 0, len(a))
	for _, item := range a {
		names = append(names, item.Name)
	}
	return strings.Join(names, ", ")
}}).Parse(`<section>{{if .Error}}<p role="alert">Inspection failed or was canceled: {{.Error}}</p>{{else}}<h2>{{.Track.Title}}</h2><p>{{joinArtists .Track.Artists}} · {{duration .Track.DurationMS}}</p><p>Fresh search at {{.Captured}} · {{.Status}}. Metadata scores do not verify audio identity.</p>{{if not .Results}}<p>No YouTube results found. Refresh to search again.</p>{{end}}<ol>{{range .Results}}<li><strong>{{.Title}}</strong><span>{{.Channel}} · {{.Duration}} · {{.Eligibility}} · score {{.Score}} · {{.Reason}}</span>{{if .Link}}<a href="{{.Link}}" target="_blank" rel="noopener noreferrer">Open on YouTube ↗</a>{{else}}<small>No valid external video link</small>{{end}}</li>{{end}}</ol>{{end}}</section>`))

const reviewCSS = `body{font:1rem/1.5 system-ui,sans-serif;background:#101820;color:#f2f4f5;margin:0}main{max-width:52rem;margin:3rem auto;padding:0 1.5rem}a{color:#9bd9ff}li{padding:1rem 0;border-bottom:1px solid #46535d}li span,li small{display:block;color:#b8c6cf}ul,ol{padding-left:1.5rem}h1{line-height:1.2}section{margin-top:2rem}`
