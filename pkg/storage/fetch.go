package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// MaxFileBytes matches Discord's baseline per-guild upload limit of 8 MiB.
const MaxFileBytes int64 = 8 << 20

const maxRedirects = 5

const fetchTimeout = 30 * time.Second

const sniffSampleSize = 512

// DefaultAllowedHosts lists the CDN hosts media may be fetched from; fetching
// an arbitrary user-supplied URL server-side would be SSRF.
func DefaultAllowedHosts() []string {
	return []string{"cdn.discordapp.com", "media.discordapp.net"}
}

// Fetch failure modes, all of which a caller maps to InvalidArgument: they are
// the user's input being refused, not the server breaking.
var (
	ErrHostNotAllowed  = errors.New("host is not allow-listed")
	ErrTooLarge        = errors.New("content exceeds the size cap")
	ErrUnsupportedType = errors.New("unsupported content type")
)

// Fetched is a downloaded blob.
type Fetched struct {
	Content []byte
	// MIMEType is sniffed from the content, not the header or URL extension.
	MIMEType string
	// Filename is a display name only, never used as a storage path.
	Filename string
	// Hash is the lowercase hex SHA-256 of Content.
	Hash string
}

// Fetcher downloads media subject to a host allow-list and a size cap.
type Fetcher struct {
	client   *http.Client
	hosts    map[string]struct{}
	maxBytes int64
}

// NewFetcher returns a Fetcher. transport may be nil (http.DefaultTransport).
// The Fetcher owns its http.Client so it controls the redirect host re-check.
// An empty hosts slice refuses every URL; maxBytes <= 0 means MaxFileBytes.
func NewFetcher(transport http.RoundTripper, hosts []string, maxBytes int64) *Fetcher {
	if maxBytes <= 0 {
		maxBytes = MaxFileBytes
	}
	if transport == nil {
		transport = http.DefaultTransport
	}

	allowed := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		allowed[strings.ToLower(host)] = struct{}{}
	}

	f := &Fetcher{
		hosts:    allowed,
		maxBytes: maxBytes,
	}

	f.client = &http.Client{
		Transport: transport,
		// Bounds a slow-drip response, which the byte cap alone would not.
		Timeout: fetchTimeout,
		// Every redirect hop is re-checked against the allow-list.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if !f.hostAllowed(req.URL) {
				return ErrHostNotAllowed
			}
			return nil
		},
	}

	return f
}

// hostAllowed requires https, a host, no userinfo, and an exact
// case-insensitive Hostname() match — never substring/suffix, so
// "cdn.discordapp.com.evil.test" is refused.
func (f *Fetcher) hostAllowed(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	if u.User != nil {
		return false
	}

	_, ok := f.hosts[strings.ToLower(u.Hostname())]
	return ok
}

// Fetch downloads rawURL subject to the allow-list and the size cap.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (*Fetched, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if !f.hostAllowed(parsed) {
		return nil, ErrHostNotAllowed
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		// CheckRedirect's ErrHostNotAllowed arrives wrapped in a *url.Error.
		if errors.Is(err, ErrHostNotAllowed) {
			return nil, ErrHostNotAllowed
		}
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected response status %d", resp.StatusCode)
	}

	// Refuse a truthful oversized Content-Length before reading the body.
	if resp.ContentLength >= 0 && resp.ContentLength > f.maxBytes {
		return nil, ErrTooLarge
	}

	// Cap the read at cap+1; reading more than cap bytes means it is too large,
	// covering a lying or absent Content-Length.
	limited := io.LimitReader(resp.Body, f.maxBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(content)) > f.maxBytes {
		return nil, ErrTooLarge
	}

	sniffLen := min(len(content), sniffSampleSize)
	mimeType := stripMIMEParams(http.DetectContentType(content[:sniffLen]))
	if !allowedMIMEType(mimeType) {
		return nil, ErrUnsupportedType
	}

	sum := sha256.Sum256(content)

	return &Fetched{
		Content:  content,
		MIMEType: mimeType,
		Filename: filenameFromURL(parsed),
		Hash:     hex.EncodeToString(sum[:]),
	}, nil
}

// AllowedMIMETypes lists the content types a trigger file may have.
func AllowedMIMETypes() []string {
	return []string{
		"image/png", "image/jpeg", "image/gif", "image/webp",
		"video/mp4", "video/webm",
		"audio/mpeg", "audio/ogg", "audio/wave",
	}
}

func allowedMIMEType(mimeType string) bool {
	return slices.Contains(AllowedMIMETypes(), mimeType)
}

// stripMIMEParams drops a trailing "; charset=..." so the sniffed type
// compares equal to the bare entries in AllowedMIMETypes.
func stripMIMEParams(mimeType string) string {
	if before, _, ok := strings.Cut(mimeType, ";"); ok {
		return strings.TrimSpace(before)
	}
	return mimeType
}

// filenameFromURL returns the last path segment of u; url.URL.Path excludes the query.
func filenameFromURL(u *url.URL) string {
	path := u.Path
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		path = path[idx+1:]
	}
	return path
}
