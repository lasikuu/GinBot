// Package client holds the platform clients' half of the Connect boundary to
// ginbot-server: the generated service clients, the transport they share, and
// the reverse action stream's reconnect loop.
//
// It is deliberately isolated from internal/config, internal/auth, pkg/db and
// pkg/grpc/interceptor. internal/config -> pkg/grpc/interceptor -> pkg/db ->
// internal/config is already a cycle on the server side; this package sits on
// the client side of the same boundary and importing any of those four would
// either recreate the cycle or drag the database into every platform binary.
// The platform packages (pkg/discord, pkg/matrix) build an Options value from
// config and auth themselves and pass it in.
package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"golang.org/x/net/http2"
)

// Options configures the Connect transport to ginbot-server.
type Options struct {
	// BaseURL is "http://host:port" or "https://host:port". TLS is decided by
	// whether TLS below is set, not by this scheme, but the two have to agree —
	// see Dial.
	BaseURL string
	// TLS is the client's mutual TLS configuration. nil dials plaintext h2c,
	// matching the server: cmd/ginbot-server only ever terminates one of the
	// two, controlled by the same GINBOT_GRPC_TLS switch on both sides.
	TLS *tls.Config
	// MaxRecvBytes and MaxSendBytes bound a single Connect message on every
	// client this package builds.
	MaxRecvBytes int
	MaxSendBytes int
	// DefaultTimeout is applied to a call that arrives with no deadline of its
	// own. Zero means "use the package default" — see defaultCallTimeout.
	DefaultTimeout time.Duration
}

// Clients holds every generated Connect client a platform process needs,
// sharing one underlying transport.
//
// Every field is exported and every unexported field is safe at its zero
// value, so a test can build a Clients{} literal with only the service
// fields it needs set and skip Dial entirely.
type Clients struct {
	User          ginbotv1connect.UserServiceClient
	Utility       ginbotv1connect.UtilityServiceClient
	Reminder      ginbotv1connect.ReminderServiceClient
	Entertainment ginbotv1connect.EntertainmentServiceClient
	Reverse       ginbotv1connect.ReverseServiceClient
	Trigger       ginbotv1connect.TriggerServiceClient
	Repost        ginbotv1connect.RepostServiceClient

	// transport is closed by Close. nil on a struct literal built by a test,
	// where Close is still safe to call — see Close.
	transport *http2.Transport
}

// defaultCallTimeout is applied to a call that supplied no deadline of its
// own.
//
// 30s, chosen so every explicit per-command budget in pkg/discord still wins:
// triggerCallTimeout (20s), triggerAttemptTimeout and repostAttemptTimeout
// (15s each), triggerFileTimeout and confirmDeliveryTimeout (10s each) are all
// strictly tighter. This is a backstop for a call site that forgets one, not a
// replacement for choosing a tighter budget deliberately.
const defaultCallTimeout = 30 * time.Second

// keepaliveReadIdleTimeout and keepaliveTimeout configure HTTP/2 ping-based
// keepalive on the transport.
//
// cmd/ginbot-server sets http2.Server.IdleTimeout to 2 minutes (see the
// comment there), which closes a connection with no open stream for that
// long. A reverse action stream is exactly such a connection whenever it has
// no action to deliver — it is parked in Receive with nothing else in
// flight — so without a ping keeping the connection active from this side, it
// would be silently dropped by the server well before any real idleness
// problem, and the platform client would not notice until the next Send or
// Receive failed. 30s pings comfortably beat the server's 2-minute window.
const (
	keepaliveReadIdleTimeout = 30 * time.Second
	keepaliveTimeout         = 15 * time.Second
)

// Dial builds the shared HTTP/2 transport and every generated service client.
// It owns keepalive, the default-deadline interceptor and the retry
// interceptor; callers only supply Options.
func Dial(opts Options) (*Clients, error) {
	if err := validateBaseURL(opts.BaseURL); err != nil {
		return nil, err
	}

	transport := &http2.Transport{
		ReadIdleTimeout: keepaliveReadIdleTimeout,
		PingTimeout:     keepaliveTimeout,
	}

	if opts.TLS == nil {
		// Plaintext h2c. AllowHTTP tells the transport an "http://" URL is not a
		// mistake, and DialTLSContext — despite the name, which is x/net's, not
		// a claim about this dial — is what actually makes it skip the
		// handshake: http2.Transport.dialTLS calls this hook when it is set and
		// performs a real TLS handshake when it is not.
		//
		// Note this is http2.Transport DIRECTLY, not http.Transport with
		// ForceAttemptHTTP2. That is deliberate and it removes the trap this
		// port's plan warns about at length: there is no HTTP/1.1 path to fall
		// back to, so getting h2c wrong fails loudly with
		// "http2: unencrypted HTTP/2 not enabled" on the first call, rather
		// than silently downgrading and passing every unary RPC while only
		// bidi streaming breaks.
		transport.AllowHTTP = true
		transport.DialTLSContext = func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, addr)
		}
	} else {
		transport.TLSClientConfig = opts.TLS
	}

	httpClient := &http.Client{Transport: transport}

	timeout := opts.DefaultTimeout
	if timeout <= 0 {
		timeout = defaultCallTimeout
	}

	// Order matters, outermost first: callermeta writes the identity and
	// origin headers before anything else runs, so a retried call carries them
	// on every attempt; the deadline interceptor then establishes the budget a
	// retry has to fit inside; the retry interceptor itself is innermost, so it
	// sees — and can act on — the deadline the layer above it just set.
	clientOpts := []connect.ClientOption{
		connect.WithInterceptors(
			callermeta.NewClientInterceptor(),
			newDeadlineInterceptor(timeout),
			newRetryInterceptor(),
		),
	}
	if opts.MaxRecvBytes > 0 {
		clientOpts = append(clientOpts, connect.WithReadMaxBytes(opts.MaxRecvBytes))
	}
	if opts.MaxSendBytes > 0 {
		clientOpts = append(clientOpts, connect.WithSendMaxBytes(opts.MaxSendBytes))
	}

	return &Clients{
		User:          ginbotv1connect.NewUserServiceClient(httpClient, opts.BaseURL, clientOpts...),
		Utility:       ginbotv1connect.NewUtilityServiceClient(httpClient, opts.BaseURL, clientOpts...),
		Reminder:      ginbotv1connect.NewReminderServiceClient(httpClient, opts.BaseURL, clientOpts...),
		Entertainment: ginbotv1connect.NewEntertainmentServiceClient(httpClient, opts.BaseURL, clientOpts...),
		Reverse:       ginbotv1connect.NewReverseServiceClient(httpClient, opts.BaseURL, clientOpts...),
		Trigger:       ginbotv1connect.NewTriggerServiceClient(httpClient, opts.BaseURL, clientOpts...),
		Repost:        ginbotv1connect.NewRepostServiceClient(httpClient, opts.BaseURL, clientOpts...),
		transport:     transport,
	}, nil
}

// validateBaseURL rejects a base URL the generated clients would accept and
// then fail on, one call at a time.
//
// connect.NewClient parses the URL and stores any failure on the client
// itself, surfacing it per call rather than at construction
// (connect@v1.20.0/client.go). Without this check Dial reports success,
// cmd/ginbot-discord logs a connected client, and every RPC afterwards fails
// with no indication of why. grpc.NewClient, which this replaced, reported the
// same class of mistake at dial time and main went Fatal on it; this keeps
// that.
//
// The realistic mistake is a scheme in GINBOT_GRPC_HOST: config's
// GRPCServerOptions.ClientBaseURL builds the URL with net.JoinHostPort, which
// brackets a host containing a colon, so GINBOT_GRPC_HOST="http://x" yields
// "http://[http://x]:50051" and fails the parse below. Note the checks here
// are a sanity floor, not a parser — a hand-concatenated "http://http://x"
// parses with Host "http:" and would pass. Nothing builds one that way today.
func validateBaseURL(baseURL string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("parse base url %q: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("base url %q: scheme must be http or https, got %q", baseURL, parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("base url %q: no host", baseURL)
	}

	return nil
}

// Close releases the shared transport's idle connections.
//
// Safe on a zero-value or struct-literal Clients, where transport is nil: a
// test injecting fakes for only some service fields still gets a Clients it
// can defer Close() on unconditionally.
func (c *Clients) Close() {
	if c == nil || c.transport == nil {
		return
	}
	c.transport.CloseIdleConnections()
}
