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

// MaxFileBytes is the largest media file the server will fetch and store.
// Discord's baseline per-guild upload limit is 8 MiB, so a larger file could
// not have come from an unboosted guild and cannot be played back to one
// either.
const MaxFileBytes int64 = 8 << 20

// maxRedirects caps the redirect chain Fetch will follow before giving up.
const maxRedirects = 5

// fetchTimeout bounds a whole fetch, redirects and body read included.
const fetchTimeout = 30 * time.Second

// sniffSampleSize is how much of the body http.DetectContentType inspects.
const sniffSampleSize = 512

// DefaultAllowedHosts lists the platform CDN hosts media may be fetched from.
// Fetching an arbitrary user-supplied URL server-side is server-side request
// forgery; only these hosts are reachable.
func DefaultAllowedHosts() []string {
	return []string{"cdn.discordapp.com", "media.discordapp.net"}
}

// Fetch failure modes, all of which a caller maps to InvalidArgument rather
// than Internal: they are the user's input being refused, not the server
// breaking.
var (
	ErrHostNotAllowed  = errors.New("host is not allow-listed")
	ErrTooLarge        = errors.New("content exceeds the size cap")
	ErrUnsupportedType = errors.New("unsupported content type")
)

// Fetched is a downloaded blob.
type Fetched struct {
	// Content is the full body, never longer than the fetcher's cap.
	Content []byte
	// MIMEType is sniffed from the content, not taken from the response header
	// or the URL extension.
	MIMEType string
	// Filename is the last path segment of the URL, with any query string
	// removed. It is a display name only and is never used as a storage path.
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

// NewFetcher returns a Fetcher.
//
// transport may be nil, in which case http.DefaultTransport is used. The
// Fetcher always builds its own http.Client so that it owns the redirect
// policy: a caller-supplied client could not be trusted to re-check the host
// after a redirect.
//
// hosts is the allow-list; an empty slice refuses every URL. maxBytes caps the
// body; a value <= 0 means MaxFileBytes.
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
		// The size cap bounds bytes, not time: an allow-listed host that drips
		// a response slowly would otherwise pin a handler goroutine for as long
		// as the caller's context lives, and a gRPC call carries no deadline
		// unless the client set one.
		Timeout: fetchTimeout,
		// Re-checked on every hop: a redirect from an allow-listed host to one
		// that is not must be refused just as the initial URL would be.
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

// hostAllowed checks the scheme, host and userinfo of u, then compares its
// hostname against the allow-list case-insensitively. It is deliberately an
// exact match against Hostname() alone, never a substring or suffix match
// against the whole URL: "cdn.discordapp.com.evil.test" has a different
// hostname to "cdn.discordapp.com" and must be refused.
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
		// CheckRedirect's ErrHostNotAllowed arrives wrapped in a *url.Error;
		// errors.Is unwraps it.
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

	// A truthful, oversized Content-Length is refused before any of the body
	// is read.
	if resp.ContentLength >= 0 && resp.ContentLength > f.maxBytes {
		return nil, ErrTooLarge
	}

	// A lying or absent Content-Length must not let an oversized body be
	// buffered in full: the limit reader caps the read at cap+1, and reading
	// more than cap bytes is the signal that the body is too large.
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

// stripMIMEParams drops a trailing "; charset=..." or similar parameter, so
// the sniffed type compares equal to the bare entries in AllowedMIMETypes.
func stripMIMEParams(mimeType string) string {
	if before, _, ok := strings.Cut(mimeType, ";"); ok {
		return strings.TrimSpace(before)
	}
	return mimeType
}

// filenameFromURL returns the last path segment of u, with no query string:
// url.URL.Path already excludes it.
func filenameFromURL(u *url.URL) string {
	path := u.Path
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		path = path[idx+1:]
	}
	return path
}
