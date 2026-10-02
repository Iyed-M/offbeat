package app

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/Iyed-M/offbeat/internal/lansync"
	"github.com/Iyed-M/offbeat/internal/managed"
)

type syncFileCache struct {
	info              os.FileInfo
	version, duration string
}
type syncAudio struct {
	entry lansync.File
	file  *os.File
	track playlistTrack
}
type syncObservation struct {
	manifest  lansync.Manifest
	audio     []syncAudio
	playlists map[string][]byte
}

func (o *syncObservation) close() {
	for _, audio := range o.audio {
		_ = audio.file.Close()
	}
}

func (d *Daemon) startLANSync(parent context.Context) error {
	if d.Cfg.Sync.HTTPSPort == 0 {
		return nil
	}
	identity, err := lansync.LoadIdentity(d.Cfg.Paths.CertsDir)
	if err != nil {
		return fmt.Errorf("load LAN sync identity: %w", err)
	}
	certificate, err := identity.TLSCertificate()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(d.Cfg.Sync.LANBindAddress, fmt.Sprint(d.Cfg.Sync.HTTPSPort)))
	if err != nil {
		return fmt.Errorf("cannot bind LAN sync listener")
	}
	ctx, cancel := context.WithCancel(parent)
	server := &http.Server{Handler: http.HandlerFunc(d.serveSync), BaseContext: func(net.Listener) context.Context { return ctx }, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	d.syncMu.Lock()
	d.syncIdentity = identity
	d.syncServer = server
	d.syncCancel = cancel
	d.syncServeDone = make(chan struct{})
	d.syncSlots = make(chan struct{}, 4)
	d.syncStopping = false
	done := d.syncServeDone
	d.syncMu.Unlock()
	go func() {
		defer close(done)
		if err := server.ServeTLS(listener, "", ""); err != nil && err != http.ErrServerClosed {
			d.Logger.Error("LAN sync listener stopped")
		}
	}()
	return nil
}

func (d *Daemon) stopLANSync() {
	d.syncMu.Lock()
	d.syncStopping = true
	server, cancel, done := d.syncServer, d.syncCancel, d.syncServeDone
	d.syncMu.Unlock()
	if server == nil {
		return
	}
	cancel()
	_ = server.Close()
	<-done
	d.syncHandlers.Wait()
	d.syncMu.Lock()
	d.syncServer = nil
	d.syncCancel = nil
	d.syncServeDone = nil
	d.syncMu.Unlock()
}

func (d *Daemon) handleSyncControl(command string) (any, error) {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	enabled := d.syncServer != nil && !d.syncStopping
	address := "https://" + net.JoinHostPort(d.Cfg.Sync.LANBindAddress, fmt.Sprint(d.Cfg.Sync.HTTPSPort))
	if command == "sync.status" {
		result := ipc.SyncStatusResult{Enabled: enabled}
		if enabled {
			result.Address = address
			result.CertificateSHA256 = d.syncIdentity.Fingerprint()
		}
		return result, nil
	}
	if !enabled {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "LAN sync is disabled; configure sync.https_port and restart the daemon")
	}
	if command == "sync.reset" {
		replacement := *d.syncIdentity
		replacement.Credential = lansync.NewCredential()
		if err := replacement.Save(d.Cfg.Paths.CertsDir); err != nil {
			return nil, ipc.NewError(ipc.CodeInternal, "could not persist phone credential reset")
		}
		d.syncIdentity = &replacement
	}
	return ipc.SyncSetupResult{Address: address, CertificateSHA256: d.syncIdentity.Fingerprint(), Credential: d.syncIdentity.Credential}, nil
}

func (d *Daemon) serveSync(w http.ResponseWriter, r *http.Request) {
	d.syncMu.Lock()
	if d.syncStopping {
		d.syncMu.Unlock()
		http.Error(w, "sync unavailable", http.StatusServiceUnavailable)
		return
	}
	d.syncHandlers.Add(1)
	authenticated := d.syncIdentity != nil && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+d.syncIdentity.Credential)) == 1
	d.syncMu.Unlock()
	defer d.syncHandlers.Done()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !authenticated {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "read-only sync", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != lansync.ManifestRoute && !strings.HasPrefix(r.URL.Path, lansync.FilesRoute) {
		http.Error(w, "unknown sync resource", http.StatusNotFound)
		return
	}
	if len(r.URL.RequestURI()) > 1024 || r.Header.Get("Range") != "" {
		http.Error(w, "invalid sync request", http.StatusBadRequest)
		return
	}
	select {
	case d.syncSlots <- struct{}{}:
		defer func() { <-d.syncSlots }()
	default:
		http.Error(w, "sync busy; retry later", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	observation, err := d.observeSync(ctx)
	if err != nil {
		http.Error(w, "current playable library unavailable", http.StatusServiceUnavailable)
		return
	}
	defer observation.close()
	if r.URL.Path == lansync.ManifestRoute {
		if r.URL.RawQuery != "" {
			http.Error(w, "invalid manifest request", http.StatusBadRequest)
			return
		}
		data, err := json.Marshal(observation.manifest)
		if err != nil || len(data) > lansync.MaxManifestBytes {
			http.Error(w, "manifest exceeds limit", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
		return
	}
	query, err := netQueryVersion(r)
	if err != nil {
		http.Error(w, "invalid content version", http.StatusBadRequest)
		return
	}
	for _, audio := range observation.audio {
		if strings.Split(audio.entry.Download, "?")[0] != r.URL.Path {
			continue
		}
		if query != audio.entry.ContentVersion {
			http.Error(w, "stale content version; fetch manifest", http.StatusConflict)
			return
		}
		// The descriptor was confined and versioned before the lock was released.
		// Atomic managed replacement cannot change its bytes during streaming.
		for _, other := range observation.audio {
			if other.file != audio.file {
				_ = other.file.Close()
			}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(audio.entry.Size))
		_, _ = io.CopyN(w, io.NewSectionReader(audio.file, 0, audio.entry.Size), audio.entry.Size)
		return
	}
	for _, playlist := range observation.manifest.Playlists {
		if strings.Split(playlist.Download, "?")[0] != r.URL.Path {
			continue
		}
		if query != playlist.ContentVersion {
			http.Error(w, "stale content version; fetch manifest", http.StatusConflict)
			return
		}
		data := observation.playlists[playlist.ID]
		w.Header().Set("Content-Type", "audio/x-mpegurl")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
		return
	}
	http.Error(w, "unknown sync file", http.StatusNotFound)
}

func netQueryVersion(r *http.Request) (string, error) {
	// ParseQuery explicitly rejects malformed escaping and semicolon separators.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["version"]) != 1 || !validSyncVersion(query.Get("version")) {
		return "", fmt.Errorf("invalid version")
	}
	return query.Get("version"), nil
}

func validSyncVersion(version string) bool {
	if len(version) != 64 {
		return false
	}
	for _, r := range version {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Capture state, mappings and confined descriptors under the same mutation
// lock. Hashing, media probing and all network writes happen after release.
// Descriptors preserve the observation across atomic managed replacement.
func (d *Daemon) observeSync(ctx context.Context) (*syncObservation, error) {
	d.managedMu.Lock()
	state, metadata, err := d.DB.ReadDesiredSpotifyState(ctx)
	if err != nil {
		d.managedMu.Unlock()
		return nil, err
	}
	tracks, err := d.DB.DesiredManagedTracks(ctx)
	if err != nil {
		d.managedMu.Unlock()
		return nil, err
	}
	if len(tracks)+len(state.Playlists)+1 > lansync.MaxEntries {
		d.managedMu.Unlock()
		return nil, fmt.Errorf("sync entry limit exceeded")
	}
	observation := &syncObservation{manifest: lansync.Manifest{Version: lansync.Version, StateRevision: metadata.Revision, Tracks: []lansync.File{}, Playlists: []lansync.File{}}, playlists: map[string][]byte{}}
	for _, track := range tracks {
		if err := ctx.Err(); err != nil {
			d.managedMu.Unlock()
			observation.close()
			return nil, err
		}
		if track.RelativePath == "" {
			continue
		}
		file, err := d.managedFiles.OpenManaged(track.Track.URI, track.RelativePath)
		if err != nil {
			if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
				d.managedMu.Unlock()
				observation.close()
				return nil, fmt.Errorf("sync descriptor limit exceeded")
			}
			continue
		}
		observation.audio = append(observation.audio, syncAudio{file: file, entry: lansync.File{ID: track.Track.URI, Path: track.RelativePath}, track: playlistTrack{path: track.RelativePath, track: track.Track}})
	}
	d.managedMu.Unlock()
	success := false
	defer func() {
		if !success {
			observation.close()
		}
	}()
	available := make(map[string]playlistTrack, len(observation.audio))
	active := make(map[string]struct{}, len(observation.audio))
	for index := range observation.audio {
		audio := &observation.audio[index]
		before, err := audio.file.Stat()
		if err != nil {
			return nil, err
		}
		d.syncCacheMu.Lock()
		cached, ok := d.syncCache[audio.entry.ID]
		d.syncCacheMu.Unlock()
		if !ok || !sameSyncFile(before, cached.info) {
			version, err := managed.Digest(ctx, audio.file)
			if err != nil {
				return nil, err
			}
			duration := ""
			if completePlaylistDisplay(audio.track.track) {
				duration = probePlaylistDuration(ctx, audio.file, d.Cfg.Downloader.FFprobePath)
			}
			after, err := audio.file.Stat()
			if err != nil || !sameSyncFile(before, after) {
				return nil, fmt.Errorf("managed bytes changed during observation")
			}
			cached = syncFileCache{info: after, version: version, duration: duration}
			d.syncCacheMu.Lock()
			if d.syncCache == nil {
				d.syncCache = map[string]syncFileCache{}
			}
			d.syncCache[audio.entry.ID] = cached
			d.syncCacheMu.Unlock()
		}
		audio.entry.Size = before.Size()
		audio.entry.ContentVersion = cached.version
		audio.entry.Download = lansync.DownloadReference("tracks", audio.entry.ID, cached.version)
		audio.track.duration = cached.duration
		observation.manifest.Tracks = append(observation.manifest.Tracks, audio.entry)
		available[audio.entry.ID] = audio.track
		active[audio.entry.ID] = struct{}{}
	}
	d.syncCacheMu.Lock()
	for id := range d.syncCache {
		if _, ok := active[id]; !ok {
			delete(d.syncCache, id)
		}
	}
	d.syncCacheMu.Unlock()
	appendPlaylist := func(id, name string, data []byte) {
		version := lansync.ContentVersion(data)
		observation.playlists[id] = data
		observation.manifest.Playlists = append(observation.manifest.Playlists, lansync.File{ID: id, Path: "playlists/" + name, Size: int64(len(data)), ContentVersion: version, Download: lansync.DownloadReference("playlists", id, version)})
	}
	appendPlaylist(lansync.LikedSongsID, likedSongsPlaylistFilename, renderM3U8(state.LikedSongs, available))
	filenames := playlistFilenames(state.Playlists)
	for i, playlist := range state.Playlists {
		appendPlaylist(playlist.URI, filenames[i], renderM3U8(playlist.Entries, available))
	}
	// State revision is informational: only playable identities/paths/bytes
	// determine the content version, including acquisitions and tag refreshes.
	contents, err := json.Marshal([]any{observation.manifest.Tracks, observation.manifest.Playlists})
	if err != nil || len(contents) > lansync.MaxManifestBytes-1024 {
		return nil, fmt.Errorf("sync manifest limit exceeded")
	}
	observation.manifest.ContentVersion = lansync.ContentVersion(contents)
	success = true
	return observation, nil
}

func sameSyncFile(a, b os.FileInfo) bool {
	if a == nil || b == nil || !os.SameFile(a, b) || a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	left, lok := a.Sys().(*syscall.Stat_t)
	right, rok := b.Sys().(*syscall.Stat_t)
	return lok && rok && left.Ctim == right.Ctim
}
