package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
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
	Mappings []ipc.ManualMappingResult
	Mapping  ipc.ManualMappingResult
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
	Choice                                               string
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
		if len(r.URL.Path) < len(base) || subtle.ConstantTimeCompare([]byte(r.URL.Path[:len(base)]), []byte(base)) != 1 {
			http.NotFound(w, r)
			return
		}
		route := strings.TrimPrefix(r.URL.Path, base)
		if r.Method == http.MethodPost {
			if route != "select" && route != "mapping/set" && route != "mapping/remove" {
				w.Header().Set("Allow", "GET")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("Origin") != origin || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Offbeat-Token")), []byte(token)) != 1 || r.Header.Get("Content-Type") != "application/json" || r.URL.RawQuery != "" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			handleReviewMutation(w, r, route, control)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if route != "" && route != "track" && route != "inspect" && route != "mappings" && route != "mapping" {
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
		case "mappings":
			after := r.URL.Query().Get("after")
			if after != "" && ipc.ValidateManualMappingTrackURI(after) != nil {
				http.Error(w, "invalid cursor", http.StatusBadRequest)
				return
			}
			req := ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.mapping.list"}
			if after != "" {
				req.ManualMappingList = &ipc.ManualMappingListRequest{AfterURI: after}
			}
			result, err := reviewRequest(r.Context(), control, req, controlReadTimeout)
			if err == nil {
				var page ipc.ManualMappingPage
				err = decodeReviewResult(result, &page)
				if err == nil && (len(page.Mappings) > 128 || (page.NextAfterURI != "" && (len(page.Mappings) == 0 || page.NextAfterURI != page.Mappings[len(page.Mappings)-1].TrackURI || page.NextAfterURI <= after))) {
					err = errors.New("invalid mapping page")
				}
				if err == nil {
					previous := after
					for _, item := range page.Mappings {
						if !validMapping(item) || item.TrackURI <= previous {
							err = errors.New("invalid mapping page")
							break
						}
						previous = item.TrackURI
					}
				}
				if err == nil {
					view.Mappings = page.Mappings
					if page.NextAfterURI != "" {
						view.Next = reviewURL(base, "mappings", url.Values{"after": {page.NextAfterURI}})
					}
				}
			}
			if err != nil {
				view.Error = err.Error()
				w.WriteHeader(http.StatusBadGateway)
			}
			tmpl = reviewMappingsTemplate
		case "mapping":
			uri := r.URL.Query().Get("uri")
			if ipc.ValidateManualMappingTrackURI(uri) != nil {
				http.Error(w, "invalid track URI", http.StatusBadRequest)
				return
			}
			view.URI = uri
			result, err := reviewRequest(r.Context(), control, ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.mapping.show", ManualMapping: &ipc.ManualMappingRequest{TrackURI: uri}}, controlReadTimeout)
			if err == nil {
				err = decodeReviewResult(result, &view.Mapping)
			}
			if err == nil && (!validMapping(view.Mapping) || view.Mapping.TrackURI != uri) {
				err = errors.New("invalid mapping")
			}
			if err != nil {
				view.Error = err.Error()
				w.WriteHeader(http.StatusBadGateway)
			}
			tmpl = reviewMappingTemplate
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
								if candidate.VideoID == raw.ID && ipc.ValidateYouTubeVideoID(raw.ID) == nil {
									choice := ipc.AcquisitionChoice{TrackURI: uri, VideoID: raw.ID, ExpectedTitle: report.Track.Title, ExpectedDurationMS: report.Track.DurationMS, ExpectedAlbum: report.Track.Album.Name, ExpectedAlbumURI: report.Track.Album.URI, RejectionReason: string(candidate.RejectionReason)}
									for _, artist := range report.Track.Artists {
										choice.ExpectedArtists = append(choice.ExpectedArtists, artist.Name)
										choice.ExpectedArtistURIs = append(choice.ExpectedArtistURIs, artist.URI)
									}
									encoded, _ := json.Marshal(choice)
									video.Choice = string(encoded)
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

func handleReviewMutation(w http.ResponseWriter, r *http.Request, route string, control reviewControl) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req ipc.Request
	switch route {
	case "select":
		var choice ipc.AcquisitionChoice
		if dec.Decode(&choice) != nil || dec.Decode(new(any)) != io.EOF || ipc.ValidateManualMappingTrackURI(choice.TrackURI) != nil || ipc.ValidateYouTubeVideoID(choice.VideoID) != nil || (choice.RejectionReason != "" && choice.AcknowledgeRejection != choice.RejectionReason) || (choice.RejectionReason == "" && choice.AcknowledgeRejection != "") {
			http.Error(w, `{"error":"invalid selection or rejection acknowledgement"}`, http.StatusBadRequest)
			return
		}
		req = ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.select", AcquisitionChoice: &choice}
	case "mapping/set", "mapping/remove":
		var mapping ipc.ManualMappingRequest
		if dec.Decode(&mapping) != nil || dec.Decode(new(any)) != io.EOF || ipc.ValidateManualMappingTrackURI(mapping.TrackURI) != nil || ipc.ValidateYouTubeVideoID(mapping.ExpectedVideoID) != nil || (route == "mapping/set" && ipc.ValidateYouTubeVideoID(mapping.VideoID) != nil) || (route == "mapping/remove" && mapping.VideoID != "") {
			http.Error(w, `{"error":"invalid mapping"}`, http.StatusBadRequest)
			return
		}
		command := "acquire.mapping.remove"
		if route == "mapping/set" {
			command = "acquire.mapping.set"
		}
		req = ipc.Request{Version: ipc.ProtocolVersion, Command: command, ManualMapping: &mapping}
	}
	result, err := reviewRequest(r.Context(), control, req, youtubeInspectReadTimeout)
	if err != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	var response any
	switch route {
	case "select":
		var work ipc.AcquisitionResult
		if decodeReviewResult(result, &work) != nil || work.TrackURI != req.AcquisitionChoice.TrackURI || work.SourceKind != "youtube" {
			http.Error(w, `{"error":"invalid daemon response"}`, http.StatusBadGateway)
			return
		}
		response = work
	case "mapping/set":
		var mapping ipc.ManualMappingResult
		if decodeReviewResult(result, &mapping) != nil || !validMapping(mapping) || mapping.TrackURI != req.ManualMapping.TrackURI {
			http.Error(w, `{"error":"invalid daemon response"}`, http.StatusBadGateway)
			return
		}
		response = mapping
	case "mapping/remove":
		var mapping ipc.ManualMappingRequest
		if decodeReviewResult(result, &mapping) != nil || mapping.TrackURI != req.ManualMapping.TrackURI {
			http.Error(w, `{"error":"invalid daemon response"}`, http.StatusBadGateway)
			return
		}
		response = mapping
	}
	_ = json.NewEncoder(w).Encode(response)
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
}}).Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Offbeat review</title><style>` + reviewCSS + `</style><main><h1>Review unresolved tracks</h1><p>Currently desired, missing YouTube work. Reviewing does not start acquisition.</p><p><a href="{{.Base}}mappings">Saved mappings</a></p>{{if .Error}}<p role="alert">Could not load review list: {{.Error}}</p><a href="{{.Base}}">Try again</a>{{else}}{{if not .Tracks}}<p>No unresolved tracks on this page.</p>{{end}}<ul>{{range .Tracks}}<li><a href="{{trackURL $.Base .TrackURI}}">{{.Title}}</a><span>{{join .Artists}} · {{duration .DurationMS}} · {{.WorkState}}</span>{{if .WorkError}}<small>Earlier work: {{.WorkError}}</small>{{end}}</li>{{end}}</ul>{{if .Next}}<a href="{{.Next}}">Next page →</a>{{end}}{{end}}</main></html>`))

var reviewTrackTemplate = template.Must(template.New("track").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Inspect track · Offbeat</title><style>` + reviewCSS + `</style><main><a href="{{.Base}}">← Review list</a><h1>Fresh YouTube inspection</h1><p>These are new search observations, not the results of the earlier unresolved attempt. Opening and refreshing do not change work or mappings.</p><p><a href="{{.Base}}track?uri={{urlquery .URI}}">Refresh candidates</a> · <a href="{{.Base}}">Skip for now</a></p><section id="results" role="status">Loading fresh candidates…</section><p id="selection-status" role="status"></p><script nonce="{{.Nonce}}">const base={{.Base}},token={{.Nonce}};fetch({{.Inspect}}, {credentials:'same-origin'}).then(r=>r.text().then(text=>({ok:r.ok,text}))).then(({ok,text})=>{document.getElementById('results').innerHTML=text;if(!ok)document.getElementById('results').setAttribute('role','alert')}).catch(()=>{document.getElementById('results').textContent='Search canceled or connection lost. Refresh to try again.'});document.getElementById('results').addEventListener('click',async e=>{if(!e.target.matches('button[data-choice]'))return;const button=e.target,choice=JSON.parse(button.dataset.choice),status=document.getElementById('selection-status');if(!confirm('Choose exactly video '+choice.video_id+' for this Spotify track?'))return;if(choice.rejection_reason){if(!confirm('Rejected: '+choice.rejection_reason+'. Choose video '+choice.video_id+' anyway?'))return;choice.acknowledge_rejection=choice.rejection_reason}button.disabled=true;status.textContent='Confirming selection…';try{const response=await fetch(base+'select',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Offbeat-Token':token},body:JSON.stringify(choice)}),data=await response.json();if(!response.ok)throw Error(data.error||'Selection failed');status.textContent='Acquisition '+data.state+' (work '+data.acquisition_id+'). Review list will reflect the updated status.';const link=document.createElement('a');link.href=base;link.textContent='Refresh review list';status.append(' ',link)}catch(err){status.textContent='Selection failed or stale: '+err.message+'. Refresh candidates and try again.'}finally{button.disabled=false}})</script></main></html>`))

var reviewInspectionTemplate = template.Must(template.New("inspection").Funcs(template.FuncMap{"duration": formatReviewDuration, "joinArtists": func(a []acquisition.InspectionNamedURI) string {
	names := make([]string, 0, len(a))
	for _, item := range a {
		names = append(names, item.Name)
	}
	return strings.Join(names, ", ")
}}).Parse(`<section>{{if .Error}}<p role="alert">Inspection failed or was canceled: {{.Error}}</p>{{else}}<h2>{{.Track.Title}}</h2><p>{{joinArtists .Track.Artists}} · {{duration .Track.DurationMS}}</p><p>Fresh search at {{.Captured}} · {{.Status}}. Metadata scores do not verify audio identity.</p>{{if not .Results}}<p>No YouTube results found. Refresh to search again.</p>{{end}}<ol>{{range .Results}}<li><strong>{{.Title}}</strong><span>{{.Channel}} · {{.Duration}} · {{.Eligibility}} · score {{.Score}} · {{.Reason}}</span>{{if .Link}}<a href="{{.Link}}" target="_blank" rel="noopener noreferrer">Open on YouTube ↗</a>{{else}}<small>No valid external video link</small>{{end}}{{if .Choice}} <button type="button" data-choice="{{.Choice}}">Choose this video</button>{{end}}</li>{{end}}</ol>{{end}}</section>`))

var reviewMappingsTemplate = template.Must(template.New("mappings").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Saved mappings · Offbeat</title><style>` + reviewCSS + `</style><main><a href="{{.Base}}">← Review list</a><h1>Saved mappings</h1>{{if .Error}}<p role="alert">{{.Error}}</p>{{else}}{{if not .Mappings}}<p>No mappings on this page.</p>{{end}}<ul>{{range .Mappings}}<li><a href="{{$.Base}}mapping?uri={{urlquery .TrackURI}}">{{.TrackURI}}</a><span>{{.VideoID}} · {{.WorkState}} {{.WorkError}}</span></li>{{end}}</ul>{{if .Next}}<a href="{{.Next}}">Next page →</a>{{end}}{{end}}</main></html>`))

var reviewMappingTemplate = template.Must(template.New("mapping").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Mapping · Offbeat</title><style>` + reviewCSS + `</style><main><a href="{{.Base}}mappings">← Saved mappings</a>{{if .Error}}<p role="alert">{{.Error}}</p>{{else}}<h1>{{.Mapping.TrackURI}}</h1><p>Chosen video: <a href="https://www.youtube.com/watch?v={{.Mapping.VideoID}}" target="_blank" rel="noopener noreferrer">{{.Mapping.VideoID}}</a></p><p>Manual · created {{.Mapping.CreatedAt}} · updated {{.Mapping.UpdatedAt}}</p><p>Work: {{.Mapping.WorkState}} {{.Mapping.WorkError}}</p><label>Replacement YouTube video ID <input id="video" maxlength="11" pattern="[A-Za-z0-9_-]{11}"></label> <button id="change">Change mapping</button> <button id="remove">Remove mapping</button><p>Changing or removing a mapping does not replace or delete available files. For unresolved tracks, use the review list to select and queue work.</p><p id="status" role="status"></p><script nonce="{{.Nonce}}">const base={{.Base}},token={{.Nonce}},uri={{.URI}},oldID={{.Mapping.VideoID}},status=document.getElementById('status');async function update(route,video){if(!confirm((route==='mapping/remove'?'Remove':'Change')+' mapping for '+uri+'?'))return;status.textContent='Updating mapping…';try{const response=await fetch(base+route,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Offbeat-Token':token},body:JSON.stringify({track_uri:uri,expected_video_id:oldID,...(video?{video_id:video}:{})})}),data=await response.json();if(!response.ok)throw Error(data.error||'Update failed');status.textContent='Mapping updated. Reload to inspect current state.';document.getElementById('change').disabled=true;document.getElementById('remove').disabled=true}catch(e){status.textContent='Mapping update failed: '+e.message}}document.getElementById('change').onclick=()=>{const video=document.getElementById('video').value;if(!/^[A-Za-z0-9_-]{11}$/.test(video)){status.textContent='Invalid video ID';return}update('mapping/set',video)};document.getElementById('remove').onclick=()=>update('mapping/remove')</script>{{end}}</main></html>`))

const reviewCSS = `body{font:1rem/1.5 system-ui,sans-serif;background:#101820;color:#f2f4f5;margin:0}main{max-width:52rem;margin:3rem auto;padding:0 1.5rem}a{color:#9bd9ff}li{padding:1rem 0;border-bottom:1px solid #46535d}li span,li small{display:block;color:#b8c6cf}ul,ol{padding-left:1.5rem}h1{line-height:1.2}section{margin-top:2rem}`
