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

// pngSignature is enough magic bytes for http.DetectContentType to sniff "image/png".
var pngSignature = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}

// newTLSServerOn binds to a specific loopback address so two servers in one
// test have genuinely different hostnames (httptest.NewTLSServer always uses
// 127.0.0.1). 127.0.0.0/8 is entirely loopback on Linux, needing no DNS.
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

// insecureTransport trusts any TLS cert so one client can reach two
// independently self-signed httptest servers; exercises the app-level
// allow-list, not TLS trust.
func insecureTransport() *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // test-only, see doc comment
}

func mustHostname(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Hostname()
}

func TestDefaultAllowedHostsExactSet(t *testing.T) {
	want := []string{"cdn.discordapp.com", "media.discordapp.net"}
	got := DefaultAllowedHosts()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultAllowedHosts() = %v, want %v", got, want)
	}
}

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

func TestFetcherRefusesNonHTTPSScheme(t *testing.T) {
	fetcher := NewFetcher(nil, []string{"cdn.discordapp.com"}, 0)

	_, err := fetcher.Fetch(context.Background(), "http://cdn.discordapp.com/x.png")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("Fetch(http://...) err = %v, want ErrHostNotAllowed", err)
	}
}

func TestFetcherRefusesURLWithNoHost(t *testing.T) {
	fetcher := NewFetcher(nil, []string{"cdn.discordapp.com"}, 0)

	_, err := fetcher.Fetch(context.Background(), "https:///no-host")
	if err == nil {
		t.Error("Fetch with no host succeeded, want an error")
	}
}

func TestFetcherRefusesUserinfoInURL(t *testing.T) {
	fetcher := NewFetcher(nil, []string{"cdn.discordapp.com"}, 0)

	_, err := fetcher.Fetch(context.Background(), "https://user:pass@cdn.discordapp.com/x.png")
	if err == nil {
		t.Error("Fetch with userinfo in the URL succeeded, want an error")
	}
}

// TestFetcherRefusesHostnameThatMerelyContainsAnAllowedHost: the match must be
// exact, not substring/suffix, so "cdn.discordapp.com.evil.test" is refused.
func TestFetcherRefusesHostnameThatMerelyContainsAnAllowedHost(t *testing.T) {
	fetcher := NewFetcher(nil, []string{"cdn.discordapp.com"}, 0)

	_, err := fetcher.Fetch(context.Background(), "https://cdn.discordapp.com.evil.test/x.png")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("Fetch err = %v, want ErrHostNotAllowed", err)
	}
}

// TestFetcherRefusesOversizedContentLengthBeforeReadingTheBody: the handler
// declares an over-cap Content-Length then blocks forever; if Fetch read the
// body before checking Content-Length the test would hang instead of passing.
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

// TestFetcherRefusesOversizedBodyWithLyingOrAbsentContentLength: a chunked
// over-cap body must be refused with ErrTooLarge and the fetcher must stop
// reading near the cap; the handler counts wire bytes to prove early exit.
func TestFetcherRefusesOversizedBodyWithLyingOrAbsentContentLength(t *testing.T) {
	const cap_ = 1024
	const chunkSize = 1024
	const totalChunks = 8192 // 8 MiB intended, far more than the 1 KiB cap
	const intendedTotal = int64(chunkSize) * int64(totalChunks)

	var written atomic.Int64
	chunk := make([]byte, chunkSize)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		deadline := time.Now().Add(5 * time.Second)
		for range totalChunks {
			if time.Now().After(deadline) {
				return // safety valve: the client is gone, do not spin forever
			}
			n, err := w.Write(chunk)
			written.Add(int64(n))
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

	// Let the handler observe the closed connection so the counter is not read mid-write.
	time.Sleep(200 * time.Millisecond)

	got := written.Load()
	if got >= intendedTotal/2 {
		t.Errorf("handler wrote %d bytes (intended %d); the fetcher did not stop early, it drained most or all of the stream",
			got, intendedTotal)
	}
}

// TestFetcherRefusesSniffedTypeNotAllowedEvenWithAnAllowedContentTypeHeader:
// html bytes under a Content-Type: image/png header; the sniffed type decides.
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
