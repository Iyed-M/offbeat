package lansync

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the fake phone/reference client. Certificate pinning completes
// during the TLS handshake, before any HTTP credential can be sent.
type Client struct {
	address, credential string
	http                *http.Client
}

func NewClient(address, fingerprint, credential string) (*Client, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, fmt.Errorf("invalid desktop HTTPS address")
	}
	expected, err := hex.DecodeString(fingerprint)
	if err != nil || len(expected) != 32 {
		return nil, fmt.Errorf("invalid desktop fingerprint")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13,
		// The provisioned certificate pin replaces public CA/hostname trust; the
		// callback is mandatory and rejects every unpinned or expired identity.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 {
				return fmt.Errorf("desktop identity mismatch")
			}
			cert := state.PeerCertificates[0]
			actual, _ := hex.DecodeString(ContentVersion(cert.Raw))
			now := time.Now()
			if subtle.ConstantTimeCompare(actual, expected) != 1 || now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return fmt.Errorf("desktop identity mismatch")
			}
			return nil
		}}, ResponseHeaderTimeout: 15 * time.Second, MaxConnsPerHost: 2, DisableCompression: true}
	return &Client{address: address, credential: credential, http: &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Get(ctx context.Context, reference string) (*http.Response, error) {
	u, err := url.Parse(reference)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || !(u.Path == ManifestRoute || strings.HasPrefix(u.Path, FilesRoute)) {
		return nil, fmt.Errorf("invalid download reference")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.address+reference, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	return c.http.Do(request)
}

func (c *Client) Manifest(ctx context.Context) (Manifest, error) {
	response, err := c.Get(ctx, ManifestRoute)
	if err != nil {
		return Manifest{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("sync manifest unavailable (HTTP %d)", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxManifestBytes+1))
	if err != nil || len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("manifest exceeds limit")
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || manifest.Version != Version || len(manifest.Tracks)+len(manifest.Playlists) > MaxEntries {
		return Manifest{}, fmt.Errorf("invalid current-state manifest")
	}
	return manifest, nil
}
