package artwork

import (
	"bytes"
	"context"
	"crypto/tls"
	imagepkg "image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func pngFixture(t *testing.T) []byte {
	t.Helper()
	im := imagepkg.NewRGBA(imagepkg.Rect(0, 0, 2, 2))
	im.Set(0, 0, color.RGBA{R: 200, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func localFetcher(server *httptest.Server) *Fetcher {
	return &Fetcher{allowPrivate: true, testDial: server.Listener.Addr().String(), tlsConfig: &tls.Config{InsecureSkipVerify: true}} // test-only TLS server
}

func TestFetchValidationCacheAndRedirects(t *testing.T) {
	image := pngFixture(t)
	wide := imagepkg.NewRGBA(imagepkg.Rect(0, 0, 4097, 1))
	var widePNG bytes.Buffer
	if err := png.Encode(&widePNG, wide); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(image)
		case "/redirect":
			http.Redirect(w, r, "https://art.example/ok", http.StatusFound)
		case "/private":
			http.Redirect(w, r, "https://127.0.0.1/ok", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "https://art.example/loop", http.StatusFound)
		case "/large":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(bytes.Repeat([]byte("x"), maxBytes+1))
		case "/fake":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("fake"))
		case "/wrong":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(image)
		case "/broken":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(image[:len(image)-4])
		case "/dimensions":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(widePNG.Bytes())
		}
	}))
	defer server.Close()
	f := localFetcher(server)
	for _, path := range []string{"/ok", "/redirect"} {
		got, err := f.Get(context.Background(), "album", "https://art.example"+path)
		if err != nil || !bytes.Equal(got.Data, image) || got.MIME != "image/png" {
			t.Fatalf("%s: %v", path, err)
		}
	}
	before := requests.Load()
	_, err := f.Get(context.Background(), "album", "https://art.example/ok")
	if err != nil || requests.Load() != before {
		t.Fatal("album image not reused")
	}
	_, err = f.Get(context.Background(), "another album", "https://art.example/ok")
	if err != nil || requests.Load() != before+1 {
		t.Fatal("cache crossed album identity")
	}
	for _, path := range []string{"/private", "/loop", "/large", "/fake", "/wrong", "/broken", "/dimensions"} {
		if _, err := f.Get(context.Background(), "album", "https://art.example"+path); err == nil || strings.Contains(err.Error(), "art.example") {
			t.Fatalf("%s: unsafe error %v", path, err)
		}
	}
}

func TestRejectUnsafeDestinationsAndCancellation(t *testing.T) {
	for _, raw := range []string{"http://art.example/a", "https://user@art.example/a", "https://art.example:443/a", "https://127.0.0.1/a", "https://art.example/#fragment", "https://art.example/\nnext"} {
		if _, err := validURL(raw); err == nil && !strings.Contains(raw, "127.0.0.1") {
			t.Fatalf("accepted %q", raw)
		}
	}
	started := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	f := localFetcher(server)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := f.Get(ctx, "album", "https://art.example/wait"); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fetch ignored cancellation")
	}
	if _, err := f.Get(canceledContext(), "album", "https://art.example/wait"); err == nil {
		t.Fatal("cancel accepted")
	}
	for _, ip := range []string{"127.0.0.1", "10.1.1.1", "169.254.169.254", "100.64.1.1", "192.0.2.1", "::1", "fc00::1", "2001:db8::1"} {
		if publicIP(parseIP(t, ip)) {
			t.Fatalf("allowed %s", ip)
		}
	}
}

func TestConcurrentFetchWaiterCancelsWhileFirstRequestIsInFlight(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	image := pngFixture(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/held" {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(image)
	}))
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		server.Close()
	}()
	f := localFetcher(server)
	first := make(chan error, 1)
	go func() { _, err := f.Get(context.Background(), "album", "https://art.example/held"); first <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first fetch did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { _, err := f.Get(ctx, "album", "https://art.example/held"); second <- err }()
	cancel()
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("canceled waiter returned artwork")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled waiter blocked on first fetch")
	}
	if got, err := f.Get(context.Background(), "other album", "https://art.example/free"); err != nil || !bytes.Equal(got.Data, image) {
		t.Fatalf("unrelated album blocked: %v", err)
	}
	close(release)
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first fetch did not finish")
	}
	before := calls.Load()
	if _, err := f.Get(context.Background(), "album", "https://art.example/held"); err != nil || calls.Load() != before {
		t.Fatal("successful fetch not cached by album and URL")
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func parseIP(t *testing.T, value string) []byte {
	t.Helper()
	ip := net.ParseIP(value)
	if ip == nil {
		t.Fatal(value)
	}
	return ip
}
