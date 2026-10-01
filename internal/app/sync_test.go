package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/Iyed-M/offbeat/internal/lansync"
)

func syncTestDaemon(t *testing.T, home string, enabled bool) (*Daemon, func()) {
	t.Helper()
	port := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return p
	}
	syncPort := 0
	if enabled {
		syncPort = port()
	}
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[spotify_adapter]\nport=%d\n[sync]\nlan_bind_address='127.0.0.1'\nhttps_port=%d\n", port(), syncPort)), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(context.Background(), Options{HomeDir: home, ConfigPath: configPath, AdapterCredential: testAdapterCredential, EnableSyntheticFixtures: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, RunOptions{SignalCh: make(chan os.Signal)}) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("sync daemon shutdown timed out")
			}
			d.Close()
		})
	}
	t.Cleanup(stop)
	waitForSocket(t, SocketPath(d.socketDir))
	return d, stop
}

func syncSetup(t *testing.T, d *Daemon, command string) ipc.SyncSetupResult {
	t.Helper()
	response := sendRaw(t, SocketPath(d.socketDir), []byte(fmt.Sprintf("{\"version\":1,\"command\":%q}\n", command)))
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	raw, _ := json.Marshal(response.Result)
	var result ipc.SyncSetupResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestLANSyncAuthenticationIdentityAndEmptyManifest(t *testing.T) {
	d, stop := syncTestDaemon(t, syncTestHome(t), true)
	defer stop()
	setup := syncSetup(t, d, "sync.setup")
	client, err := lansync.NewClient(setup.Address, setup.CertificateSHA256, setup.Credential)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	manifest, err := client.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || len(manifest.Tracks) != 0 || len(manifest.Playlists) != 1 || manifest.Playlists[0].ID != lansync.LikedSongsID {
		t.Fatalf("empty manifest: %#v", manifest)
	}
	for _, credential := range []string{"", "wrong", testAdapterCredential} {
		bad, _ := lansync.NewClient(setup.Address, setup.CertificateSHA256, credential)
		defer bad.Close()
		if _, err := bad.Manifest(context.Background()); err == nil {
			t.Fatalf("accepted credential %q", credential)
		}
	}
	bad, _ := lansync.NewClient(setup.Address, fmt.Sprintf("%064d", 0), setup.Credential)
	defer bad.Close()
	if _, err := bad.Manifest(context.Background()); err == nil {
		t.Fatal("accepted wrong desktop identity")
	}
}

func TestLANSyncPrivateProvisioningResetRestartAndDisabled(t *testing.T) {
	home := syncTestHome(t)
	d, stop := syncTestDaemon(t, home, true)
	original := syncSetup(t, d, "sync.setup")
	for _, command := range []string{"status", "config", "sync.status"} {
		response := sendRaw(t, SocketPath(d.socketDir), []byte(fmt.Sprintf("{\"version\":1,\"command\":%q}\n", command)))
		raw, _ := json.Marshal(response)
		if bytes.Contains(raw, []byte(original.Credential)) {
			t.Fatalf("credential exposed in %s", command)
		}
	}
	info, err := os.Stat(filepath.Join(d.Cfg.Paths.CertsDir, "lan-sync.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("identity permissions: %v %v", info, err)
	}
	replacement := syncSetup(t, d, "sync.reset")
	if replacement.Credential == original.Credential || replacement.CertificateSHA256 != original.CertificateSHA256 {
		t.Fatal("reset must change only phone credential")
	}
	old, _ := lansync.NewClient(original.Address, original.CertificateSHA256, original.Credential)
	if _, err := old.Manifest(context.Background()); err == nil {
		t.Fatal("old credential still accepted")
	}
	old.Close()
	stop()
	d, stop = syncTestDaemon(t, home, true)
	restarted := syncSetup(t, d, "sync.setup")
	if restarted.Credential != replacement.Credential || restarted.CertificateSHA256 != replacement.CertificateSHA256 {
		t.Fatal("private identity did not survive restart")
	}
	good, _ := lansync.NewClient(restarted.Address, restarted.CertificateSHA256, restarted.Credential)
	if _, err := good.Manifest(context.Background()); err != nil {
		t.Fatal(err)
	}
	good.Close()
	stop()
	logs, err := os.ReadFile(d.Cfg.Paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(logs, []byte(original.Credential)) || bytes.Contains(logs, []byte(replacement.Credential)) {
		t.Fatal("phone credential leaked to logs")
	}
	disabled, stopDisabled := syncTestDaemon(t, syncTestHome(t), false)
	defer stopDisabled()
	response := sendRaw(t, SocketPath(disabled.socketDir), []byte("{\"version\":1,\"command\":\"sync.setup\"}\n"))
	if response.Error == nil || response.Error.Code != ipc.CodeFailedPrecondition {
		t.Fatalf("disabled setup: %#v", response)
	}
	if _, err := os.Stat(filepath.Join(disabled.Cfg.Paths.CertsDir, "lan-sync.json")); !os.IsNotExist(err) {
		t.Fatal("disabled daemon provisioned LAN secrets")
	}
}

func TestLANSyncPlayableManifestFilesAndStaleness(t *testing.T) {
	d, stop := syncTestDaemon(t, syncTestHome(t), true)
	defer stop()
	setup := syncSetup(t, d, "sync.setup")
	client, _ := lansync.NewClient(setup.Address, setup.CertificateSHA256, setup.Credential)
	defer client.Close()
	one, two, retained := lifecycleTrack("one"), lifecycleTrack("two"), lifecycleTrack("retained")
	// The adapter commits complete candidates; fixture publication is the
	// existing opt-in managed-file seam, with no live Spotify/media dependency.
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
	syncCandidate := func(includeRetained bool) {
		playlists := []map[string]any{{"uri": "spotify:playlist:mix", "name": "Mix", "entries": []any{syncSupportedEntry(0, one.URI), syncSupportedEntry(1, two.URI), syncSupportedEntry(2, one.URI), map[string]any{"position": 3, "kind": "unsupported", "source_uri": "spotify:episode:one"}}}}
		liked := []any{syncSupportedEntry(0, one.URI)}
		if includeRetained {
			liked = append(liked, syncSupportedEntry(1, retained.URI))
		}
		done := sendSync(t, d)
		requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
		writeAdapterJSON(t, adapter, map[string]any{"version": 1, "type": "snapshot.response", "request_id": requestID, "snapshot": map[string]any{"kind": "candidate", "playlists": playlists, "liked_songs": map[string]any{"entries": liked}}})
		assertSyncSuccess(t, syncResponse(t, <-done))
	}
	syncCandidate(true)
	path, err := d.RegisterSyntheticTrackFixture(context.Background(), one.URI)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterSyntheticTrackFixture(context.Background(), retained.URI); err != nil {
		t.Fatal(err)
	}
	syncCandidate(false)
	manifest, err := client.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tracks) != 1 || manifest.Tracks[0].ID != one.URI || manifest.Tracks[0].Path != path || len(manifest.Playlists) != 2 {
		t.Fatalf("playable set: %#v", manifest)
	}
	original := manifest.Tracks[0]
	raw, _ := json.Marshal(manifest)
	if bytes.Contains(raw, []byte(d.Cfg.Paths.MusicRoot)) || bytes.Contains(raw, []byte("youtube")) || bytes.Contains(raw, []byte(setup.Credential)) {
		t.Fatalf("private data in manifest: %s", raw)
	}
	want, err := os.ReadFile(filepath.Join(d.Cfg.Paths.MusicRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	got := syncDownload(t, client, original.Download, http.StatusOK)
	if !bytes.Equal(got, want) || original.Size != int64(len(want)) || original.ContentVersion != lansync.ContentVersion(want) {
		t.Fatal("download/version do not identify actual published bytes")
	}
	for _, playlist := range manifest.Playlists {
		content := string(syncDownload(t, client, playlist.Download, http.StatusOK))
		count := 1
		if playlist.ID != "offbeat:liked-songs" {
			count = 2
		}
		if strings.Count(content, "../"+path+"\n") != count || strings.Contains(content, "two") || strings.Contains(content, "episode") {
			t.Fatalf("incoherent playlist: %q", content)
		}
	}
	same, err := client.Manifest(context.Background())
	if err != nil || same.ContentVersion != manifest.ContentVersion {
		t.Fatal("no-op changed manifest")
	}
	for _, reference := range []string{lansync.FilesRoute + "tracks/" + strings.Repeat("0", 64) + "?version=" + original.ContentVersion, lansync.FilesRoute + "../config.toml?version=" + original.ContentVersion, lansync.FilesRoute + "tracks/%2e%2e%2fetc%2fpasswd?version=" + original.ContentVersion} {
		syncDownload(t, client, reference, http.StatusNotFound)
	}
	// A same-size metadata/content replacement must change the published
	// version even if the filename and informational State revision stay fixed.
	replacement := append([]byte(nil), want...)
	replacement[len(replacement)-1] = 1
	source, err := os.CreateTemp(t.TempDir(), "replacement")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	source.Write(replacement)
	d.managedMu.Lock()
	_, err = d.managedFiles.Publish(one.URI, source, "wav")
	d.managedMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := client.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated.StateRevision != manifest.StateRevision || updated.ContentVersion == manifest.ContentVersion || updated.Tracks[0].ContentVersion == original.ContentVersion {
		t.Fatal("same-size replacement not detected")
	}
	syncDownload(t, client, original.Download, http.StatusConflict)
	if got := syncDownload(t, client, updated.Tracks[0].Download, http.StatusOK); !bytes.Equal(got, replacement) {
		t.Fatal("replacement download differs")
	}
	if err := os.Remove(filepath.Join(d.Cfg.Paths.MusicRoot, path)); err != nil {
		t.Fatal(err)
	}
	missing, err := client.Manifest(context.Background())
	if err != nil || len(missing.Tracks) != 0 {
		t.Fatalf("missing file advertised: %#v %v", missing, err)
	}
	syncDownload(t, client, updated.Tracks[0].Download, http.StatusNotFound)
	if _, err := d.RegisterSyntheticTrackFixture(context.Background(), two.URI); err != nil {
		t.Fatal(err)
	}
	acquired, err := client.Manifest(context.Background())
	if err != nil || len(acquired.Tracks) != 1 || acquired.Tracks[0].ID != two.URI || acquired.StateRevision != manifest.StateRevision || acquired.ContentVersion == missing.ContentVersion {
		t.Fatalf("new acquisition without Spotify revision: %#v %v", acquired, err)
	}
}

func syncDownload(t *testing.T, client *lansync.Client, reference string, status int) []byte {
	t.Helper()
	response, err := client.Get(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s: HTTP %d, want %d: %s", reference, response.StatusCode, status, data)
	}
	return data
}

func syncSupportedEntry(position int, uri string) map[string]any {
	return map[string]any{"position": position, "kind": "supported", "track": map[string]any{"uri": uri, "name": strings.TrimPrefix(uri, "spotify:track:"), "artists": []any{map[string]any{"uri": "spotify:artist:one", "name": "Artist"}}, "album": map[string]any{"uri": "spotify:album:one", "name": "Album"}, "duration_ms": 1000}}
}

func syncTestHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "offbeat-sync-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return home
}

func TestLANSyncConcurrentReplacementAndShutdown(t *testing.T) {
	d, stop := syncTestDaemon(t, syncTestHome(t), true)
	defer stop()
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
	done := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
	writeAdapterJSON(t, adapter, map[string]any{"version": 1, "type": "snapshot.response", "request_id": requestID, "snapshot": map[string]any{"kind": "candidate", "playlists": []any{}, "liked_songs": map[string]any{"entries": []any{syncSupportedEntry(0, "spotify:track:one")}}}})
	assertSyncSuccess(t, syncResponse(t, <-done))
	uri := "spotify:track:one"
	path, err := d.RegisterSyntheticTrackFixture(context.Background(), uri)
	if err != nil {
		t.Fatal(err)
	}
	small, err := os.ReadFile(filepath.Join(d.Cfg.Paths.MusicRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	original := make([]byte, 16<<20)
	copy(original, small)
	publish := func(data []byte) error {
		source, err := os.CreateTemp(t.TempDir(), "media")
		if err != nil {
			return err
		}
		defer source.Close()
		if _, err := source.Write(data); err != nil {
			return err
		}
		d.managedMu.Lock()
		defer d.managedMu.Unlock()
		_, err = d.managedFiles.Publish(uri, source, "wav")
		return err
	}
	if err := publish(original); err != nil {
		t.Fatal(err)
	}
	setup := syncSetup(t, d, "sync.setup")
	client, _ := lansync.NewClient(setup.Address, setup.CertificateSHA256, setup.Credential)
	defer client.Close()
	manifest, err := client.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Leave a large response unread so network backpressure cannot finish it.
	response, err := client.Get(context.Background(), manifest.Tracks[0].Download)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("download status %d", response.StatusCode)
	}
	replaced := make(chan error, 1)
	go func() { replaced <- publish(small) }()
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow phone transfer held desktop mutation lock")
	}
	downloaded, err := io.ReadAll(response.Body)
	if err != nil || !bytes.Equal(downloaded, original) {
		t.Fatalf("concurrent replacement changed advertised descriptor: %v", err)
	}
	syncDownload(t, client, manifest.Tracks[0].Download, http.StatusConflict)
	// Symlinks never turn a known identity into arbitrary file access.
	outside := filepath.Join(t.TempDir(), "outside.wav")
	os.WriteFile(outside, small, 0600)
	os.Remove(filepath.Join(d.Cfg.Paths.MusicRoot, path))
	if err := os.Symlink(outside, filepath.Join(d.Cfg.Paths.MusicRoot, path)); err != nil {
		t.Fatal(err)
	}
	confined, err := client.Manifest(context.Background())
	if err != nil || len(confined.Tracks) != 0 {
		t.Fatalf("symlink advertised: %#v %v", confined, err)
	}
	syncDownload(t, client, manifest.Tracks[0].Download, http.StatusNotFound)
	os.Remove(filepath.Join(d.Cfg.Paths.MusicRoot, path))
	tracksDirectory := filepath.Join(d.Cfg.Paths.MusicRoot, "tracks")
	retainedDirectory := filepath.Join(d.Cfg.Paths.MusicRoot, "saved-tracks")
	if err := os.Rename(tracksDirectory, retainedDirectory); err != nil {
		t.Fatal(err)
	}
	outsideDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDirectory, filepath.Base(path)), small, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDirectory, tracksDirectory); err != nil {
		t.Fatal(err)
	}
	confined, err = client.Manifest(context.Background())
	if err != nil || len(confined.Tracks) != 0 {
		t.Fatalf("symlink directory advertised: %#v %v", confined, err)
	}
	if err := os.Remove(tracksDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(retainedDirectory, tracksDirectory); err != nil {
		t.Fatal(err)
	}
	if err := publish(original); err != nil {
		t.Fatal(err)
	}
	manifest, err = client.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := client.Get(context.Background(), manifest.Tracks[0].Download)
	if err != nil {
		t.Fatal(err)
	}
	defer pending.Body.Close()
	stop() // cancellation must drain the blocked handler before closing DB/root
	if data, err := io.ReadAll(pending.Body); err == nil && len(data) == len(original) {
		t.Fatal("shutdown did not interrupt pending transfer")
	}
}

func TestLANSyncRejectsOversizedPlayableObservation(t *testing.T) {
	d, stop := syncTestDaemon(t, syncTestHome(t), true)
	defer stop()
	playlists := make([]desired.CandidatePlaylist, lansync.MaxEntries)
	for i := range playlists {
		playlists[i] = desired.CandidatePlaylist{URI: fmt.Sprintf("spotify:playlist:%d", i), Name: "Empty", Position: i}
	}
	// Like existing daemon fixtures, seed a complete normalized state before
	// exercising the public HTTPS boundary. No partial manifest may escape.
	d.managedMu.Lock()
	_, _, _, err := d.DB.ApplyDesiredSpotifyState(context.Background(), desired.Candidate{Playlists: playlists})
	d.managedMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	setup := syncSetup(t, d, "sync.setup")
	client, _ := lansync.NewClient(setup.Address, setup.CertificateSHA256, setup.Credential)
	defer client.Close()
	syncDownload(t, client, lansync.ManifestRoute, http.StatusServiceUnavailable)
}
