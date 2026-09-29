// Package artwork fetches bounded public HTTPS images supplied by Spotify.
package artwork

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxBytes = 5 << 20

// Image contains verified encoded artwork; MIME is either image/jpeg or image/png.
type Image struct {
	Data []byte
	MIME string
}

type entry struct {
	key   [32]byte
	image Image
}

// Fetcher caches at most 32 verified album images (160 MiB maximum).
// Network requests run without holding the cache mutex, so callers can cancel
// independently; concurrent misses may fetch the same image twice.
type Fetcher struct {
	mu    sync.Mutex
	cache []entry
	// test-only: local TLS servers use a private address, never permitted by New.
	allowPrivate bool
	tlsConfig    *tls.Config
	testDial     string
}

func New() *Fetcher { return &Fetcher{} }

// NewWithTestRoute routes HTTPS requests to a local TLS fixture while leaving
// URL/redirect validation intact. Only tests should use this constructor.
func NewWithTestRoute(address string, tlsConfig *tls.Config) *Fetcher {
	return &Fetcher{allowPrivate: true, testDial: address, tlsConfig: tlsConfig}
}

var errUnsafe = errors.New("unsafe artwork destination")

func validURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 2048 || strings.ContainsAny(raw, " \\\r\n\t") {
		return nil, errUnsafe
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Hostname() == "" || u.Port() != "" || strings.HasSuffix(u.Hostname(), ".") {
		return nil, errUnsafe
	}
	if net.ParseIP(u.Hostname()) != nil || strings.Contains(u.Hostname(), "%") || !strings.Contains(u.Hostname(), ".") {
		return nil, errUnsafe
	}
	return u, nil
}

func publicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!a.IsLinkLocalMulticast() && !a.IsMulticast() && !a.Is4In6() &&
		!a.IsUnspecified() && !a.IsInterfaceLocalMulticast() &&
		!reserved(a)
}

func reserved(ip netip.Addr) bool {
	// Global-unicast includes documentation, shared, benchmarking and reserved ranges.
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "64:ff9b::/96", "2001:db8::/32", "2001::/32", "2002::/16", "fc00::/7", "fe80::/10"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return true
		}
	}
	return false
}

// Get returns a sanitized error; callers must never log the source URL.
func (f *Fetcher) Get(ctx context.Context, albumURI, raw string) (Image, error) {
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	u, err := validURL(raw)
	if err != nil {
		return Image{}, err
	}
	key := sha256.Sum256([]byte(albumURI + "\x00" + raw))
	f.mu.Lock()
	for _, cached := range f.cache {
		if cached.key == key {
			f.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return Image{}, err
			}
			return cached.image, nil
		}
	}
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	image, err := f.fetch(ctx, u)
	if err != nil {
		return Image{}, err
	}
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cached := range f.cache {
		if cached.key == key {
			return cached.image, nil
		}
	}
	if len(f.cache) == 32 {
		f.cache = f.cache[1:]
	}
	f.cache = append(f.cache, entry{key, image})
	return image, nil
}

func (f *Fetcher) fetch(ctx context.Context, u *url.URL) (Image, error) {
	transport := &http.Transport{Proxy: nil, TLSClientConfig: f.tlsConfig, DisableKeepAlives: true,
		TLSHandshakeTimeout: 4 * time.Second, ResponseHeaderTimeout: 4 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil || port != "443" {
				return nil, errUnsafe
			}
			if f.allowPrivate && f.testDial != "" {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "tcp", f.testDial)
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil || len(ips) == 0 {
				return nil, errUnsafe
			}
			for _, ip := range ips {
				if !f.allowPrivate && !publicIP(ip) {
					return nil, errUnsafe
				}
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", net.JoinHostPort(ips[0].String(), port))
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errUnsafe
		}
		_, err := validURL(req.URL.String())
		return err
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Image{}, errUnsafe
	}
	resp, err := client.Do(req)
	if err != nil {
		return Image{}, errors.New("artwork fetch failed or timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Image{}, errors.New("artwork response unavailable")
	}
	if resp.ContentLength > maxBytes {
		return Image{}, errors.New("artwork exceeds byte limit")
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if contentType != "image/png" && contentType != "image/jpeg" {
		return Image{}, errors.New("artwork type unsupported")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return Image{}, errors.New("artwork exceeds byte limit")
	}
	if http.DetectContentType(data) != contentType {
		return Image{}, errors.New("artwork type mismatch")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != strings.TrimPrefix(contentType, "image/") || config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 16<<20 {
		return Image{}, errors.New("artwork dimensions or encoding invalid")
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return Image{}, errors.New("artwork encoding invalid")
	}
	return Image{Data: data, MIME: contentType}, nil
}
