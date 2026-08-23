package storage

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// ── Assumed symbols from pkg/storage (spec §6.2) ─────────────────────────────
//
//	const MaxFileBytes int64 = 8 << 20
//
//	func DefaultAllowedHosts() []string
//	func AllowedMIMETypes() []string
//
//	var (
//		ErrHostNotAllowed  = errors.New("host is not allow-listed")
//		ErrTooLarge        = errors.New("content exceeds the size cap")
//		ErrUnsupportedType = errors.New("unsupported content type")
//	)
//
//	type Fetched struct {
//		Content  []byte
//		MIMEType string
//		Filename string
//		Hash     string
//	}
//
//	type Fetcher struct { /* unexported fields */ }
//
//	func NewFetcher(transport http.RoundTripper, hosts []string, maxBytes int64) *Fetcher
//	func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (*Fetched, error)
//
// Fetch, in order: rejects a non-https scheme / no host / userinfo, rejects a
// hostname not on the allow-list (hostname-only, case-insensitive, no
// substring/suffix match), re-checks every redirect target against the same
// rule (cap 5 hops), rejects non-2xx, rejects Content-Length over the cap
// BEFORE reading the body, reads via a cap+1-byte LimitReader and rejects an
// oversized body found that way even when Content-Length lied or was absent,
// sniffs the type with http.DetectContentType (never trusting the header or
// the URL extension) and rejects one outside AllowedMIMETypes(), then hashes
// with SHA-256. None of these exist yet; pkg/storage does not exist as of
// this writing.

// pngSignature is enough of a PNG file for http.DetectContentType to sniff
// "image/png" — the sniffer only inspects the magic bytes, not a valid image.
var pngSignature = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}

// newTLSServerOn starts an httptest TLS server bound to a specific loopback
// address, so two servers in one test have genuinely different hostnames
// (httptest.NewTLSServer always uses 127.0.0.1, which would make every
// allow-list/redirect test comparing two servers compare a host against
// itself). 127.0.0.0/8 is entirely loopback on Linux, so 127.0.0.2 is valid
// and requires no DNS.
func newTLSServerOn(t *testing.T, addr string, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	listener, err := net.Listen("tcp", addr+":0")
	if err != nil {
		t.Skipf("cannot bind %s (sandboxed loopback aliasing?): %v", addr, err)
	}

	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)

	return server
}

// insecureTransport trusts any TLS certificate, which is required to make one
// Fetcher's client cross-trust two independently self-signed httptest servers.
// This only weakens the TEST's own transport, exercising the Fetcher's
// application-level host allow-list rather than its (irrelevant, in this
// package) TLS trust behaviour.
func insecureTransport() *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // test-only, see doc comment
}

// mustHostname extracts the hostname the way the allow-list is specified to
// compare against: the URL's Hostname(), not the whole authority.
func mustHostname(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Hostname()
}

// TestDefaultAllowedHostsExactSet.
func TestDefaultAllowedHostsExactSet(t *testing.T) {
	want := []string{"cdn.discordapp.com", "media.discordapp.net"}
	got := DefaultAllowedHosts()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultAllowedHosts() = %v, want %v", got, want)
	}
}

// TestAllowedMIMETypesExactSet.
func TestAllowedMIMETypesExactSet(t *testing.T) {
	want := []string{
		"image/png", "image/jpeg", "image/gif", "image/webp",
		"video/mp4", "video/webm",
		"audio/mpeg", "audio/ogg", "audio/wave",
	}
	got := AllowedMIMETypes()

	gotSet := make(map[string]bool, len(got))
	for _, m := range got {
		gotSet[m] = true
	}
	if len(got) != len(want) {
		t.Errorf("AllowedMIMETypes() has %d entries, want %d: got %v", len(got), len(want), got)
	}
	for _, m := range want {
		if !gotSet[m] {
			t.Errorf("AllowedMIMETypes() missing %q", m)
		}
	}
}

// TestFetcherHappyPath: correct content, sniffed MIME type, lowercase hex
// SHA-256 hash, and the filename derived from the URL path with the query
// string dropped.
func TestFetcherHappyPath(t *testing.T) {
	body := append(append([]byte{}, pngSignature...), []byte("more-bytes-after-the-signature")...)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		if _, err := w.Write(body); err != nil {
			t.Errorf("handler write: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	host := mustHostname(t, server.URL)
	fetcher := NewFetcher(server.Client().Transport, []string{host}, 0)

	fetched, err := fetcher.Fetch(context.Background(), server.URL+"/media/pic.png?ignored=1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if string(fetched.Content) != string(body) {
		t.Errorf("Content = %q, want %q", fetched.Content, body)
	}
	if fetched.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", fetched.MIMEType)
	}

	sum := sha256.Sum256(body)
	wantHash := hex.EncodeToString(sum[:])
	if fetched.Hash != wantHash {
		t.Errorf("Hash = %q, want %q", fetched.Hash, wantHash)
	}
	if fetched.Hash != strLower(fetched.Hash) {
		t.Errorf("Hash %q is not lowercase", fetched.Hash)
	}

	if fetched.Filename != "pic.png" {
		t.Errorf("Filename = %q, want %q (query string must be dropped)", fetched.Filename, "pic.png")
	}
}

func strLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// TestFetcherRefusesHostNotAllowed.
func TestFetcherRefusesHostNotAllowed(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler was invoked; the host check must happen before any request is sent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	fetcher := NewFetcher(server.Client().Transport, []string{"not-this-host.example"}, 0)

	_, err := fetcher.Fetch(context.Background(), server.URL+"/x")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("Fetch err = %v, want ErrHostNotAllowed", err)
	}
}

// TestFetcherRefusesRedirectToNonAllowedHost: an allow-listed server redirects
// to a server on a different hostname that is not allow-listed. Two distinct
// loopback addresses (127.0.0.1 / 127.0.0.2) give genuinely different
// hostnames, so the allow-list comparison is real rather than comparing a host
// to itself.
func TestFetcherRefusesRedirectToNonAllowedHost(t *testing.T) {
	var disallowedHits atomic.Int32
	disallowed := newTLSServerOn(t, "127.0.0.2", func(w http.ResponseWriter, r *http.Request) {
		disallowedHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	allowed := newTLSServerOn(t, "127.0.0.1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, disallowed.URL+"/target", http.StatusFound)
	})

	fetcher := NewFetcher(insecureTransport(), []string{mustHostname(t, allowed.URL)}, 0)

	_, err := fetcher.Fetch(context.Background(), allowed.URL+"/start")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("Fetch err = %v, want ErrHostNotAllowed", err)
	}
	if got := disallowedHits.Load(); got != 0 {
		t.Errorf("disallowed redirect target was hit %d times, want 0", got)
	}
}

// TestFetcherRefusesNonHTTPSScheme.
func TestFetcherRefusesNonHTTPSScheme(t *testing.T) {
	fetcher := NewFetcher(nil, []string{"cdn.discordapp.com"}, 0)

	_, err := fetcher.Fetch(context.Background(), "http://cdn.discordapp.com/x.png")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("Fetch(http://...) err = %v, want ErrHostNotAllowed", err)
	}
}

// TestFetcherRefusesURLWithNoHost.
func TestFetcherRefusesURLWithNoHost(t *testing.T) {
	fetcher := NewFetcher(nil, []string{"cdn.discordapp.com"}, 0)

	_, err := fetcher.Fetch(context.Background(), "https:///no-host")
	if err == nil {
		t.Error("Fetch with no host succeeded, want an error")
	}
}

// TestFetcherRefusesUserinfoInURL.
func TestFetcherRefusesUserinfoInURL(t *testing.T) {
	fetcher := NewFetcher(nil, []string{"cdn.discordapp.com"}, 0)

	_, err := fetcher.Fetch(context.Background(), "https://user:pass@cdn.discordapp.com/x.png")
	if err == nil {
		t.Error("Fetch with userinfo in the URL succeeded, want an error")
	}
}

// TestFetcherRefusesHostnameThatMerelyContainsAnAllowedHost: the comparison
// must be exact, not substring/suffix, or an attacker-controlled subdomain of
// an attacker-controlled domain could impersonate an allow-listed CDN.
func TestFetcherRefusesHostnameThatMerelyContainsAnAllowedHost(t *testing.T) {
	fetcher := NewFetcher(nil, []string{"cdn.discordapp.com"}, 0)

	_, err := fetcher.Fetch(context.Background(), "https://cdn.discordapp.com.evil.test/x.png")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("Fetch err = %v, want ErrHostNotAllowed", err)
	}
}

// TestFetcherRefusesOversizedContentLengthBeforeReadingTheBody: the handler
// declares a Content-Length over the cap, then blocks instead of ever sending
// a body. If Fetch tried to read the body before checking Content-Length, this
// test would hang (and eventually fail on the suite's own test timeout)
// instead of passing quickly — that is deliberate: it is what makes the
// ordering assertion real rather than cosmetic.
func TestFetcherRefusesOversizedContentLengthBeforeReadingTheBody(t *testing.T) {
	const cap_ = 1024
	release := make(chan struct{})

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(cap_+1))
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release // never sends a body until the test releases it
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	fetcher := NewFetcher(server.Client().Transport, []string{mustHostname(t, server.URL)}, cap_)

	_, err := fetcher.Fetch(context.Background(), server.URL+"/big")
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("Fetch err = %v, want ErrTooLarge", err)
	}
}

// TestFetcherRefusesOversizedBodyWithLyingOrAbsentContentLength: chunked
// transfer (no Content-Length) that actually exceeds the cap must still be
// refused with ErrTooLarge, and the fetcher must not have buffered the whole
// thing — it must stop reading at roughly the cap. The handler streams far
// more than the cap and counts how many bytes it actually got out onto the
// wire; if the fetcher drained the entire stream, that counter would reach the
// full intended size, so a generous upper bound well below the intended size
// is enough to prove early termination without being sensitive to exact OS
// socket-buffering behaviour.
func TestFetcherRefusesOversizedBodyWithLyingOrAbsentContentLength(t *testing.T) {
	const cap_ = 1024
	const chunkSize = 1024
	const totalChunks = 8192 // 8 MiB intended, far more than the 1 KiB cap
	const intendedTotal = int64(chunkSize) * int64(totalChunks)

	var written int64
	chunk := make([]byte, chunkSize)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		deadline := time.Now().Add(5 * time.Second)
		for i := 0; i < totalChunks; i++ {
			if time.Now().After(deadline) {
				return // safety valve: the client is gone, do not spin forever
			}
			n, err := w.Write(chunk)
			atomic.AddInt64(&written, int64(n))
			if flusher != nil {
				flusher.Flush()
			}
			if err != nil {
				return // client stopped reading (or closed the connection)
			}
		}
	}))
	t.Cleanup(server.Close)

	fetcher := NewFetcher(server.Client().Transport, []string{mustHostname(t, server.URL)}, cap_)

	_, err := fetcher.Fetch(context.Background(), server.URL+"/stream")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Fetch err = %v, want ErrTooLarge", err)
	}

	// Give the handler goroutine a moment to observe the closed connection and
	// stop, so the counter below is not read mid-write.
	time.Sleep(200 * time.Millisecond)

	got := atomic.LoadInt64(&written)
	if got >= intendedTotal/2 {
		t.Errorf("handler wrote %d bytes (intended %d); the fetcher did not stop early, it drained most or all of the stream",
			got, intendedTotal)
	}
}

// TestFetcherRefusesSniffedTypeNotAllowedEvenWithAnAllowedContentTypeHeader:
// serves text/html bytes under a Content-Type: image/png header. The sniffed
// type must decide, proving the header is not trusted.
func TestFetcherRefusesSniffedTypeNotAllowedEvenWithAnAllowedContentTypeHeader(t *testing.T) {
	htmlBody := []byte("<html><head></head><body>not actually a png</body></html>")

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png") // lies
		if _, err := w.Write(htmlBody); err != nil {
			t.Errorf("handler write: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	fetcher := NewFetcher(server.Client().Transport, []string{mustHostname(t, server.URL)}, 0)

	_, err := fetcher.Fetch(context.Background(), server.URL+"/fake.png")
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("Fetch err = %v, want ErrUnsupportedType", err)
	}
}

// TestFetcherRefusesNon2xxResponse.
func TestFetcherRefusesNon2xxResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	fetcher := NewFetcher(server.Client().Transport, []string{mustHostname(t, server.URL)}, 0)

	_, err := fetcher.Fetch(context.Background(), server.URL+"/missing")
	if err == nil {
		t.Error("Fetch against a 404 response succeeded, want an error")
	}
}

// TestFetcherEmptyAllowListRefusesEverything.
func TestFetcherEmptyAllowListRefusesEverything(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler was invoked with an empty allow-list; nothing should ever reach it")
	}))
	t.Cleanup(server.Close)

	fetcher := NewFetcher(server.Client().Transport, []string{}, 0)

	_, err := fetcher.Fetch(context.Background(), server.URL+"/x")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("Fetch err = %v, want ErrHostNotAllowed", err)
	}
}
