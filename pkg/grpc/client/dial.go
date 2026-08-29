// Package client holds the platform clients' half of the Connect boundary to
// ginbot-server. It must not import internal/config, internal/auth, pkg/db or
// pkg/grpc/interceptor: that recreates an import cycle and drags the database
// into every platform binary. Callers pass an Options value in instead.
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
	// BaseURL is "http://host:port" or "https://host:port"; its scheme must
	// agree with TLS below but does not decide it.
	BaseURL string
	// TLS is the client's mutual TLS configuration; nil dials plaintext h2c.
	TLS *tls.Config
	// MaxRecvBytes and MaxSendBytes bound a single Connect message.
	MaxRecvBytes int
	MaxSendBytes int
	// DefaultTimeout applies to a call with no deadline; zero means defaultCallTimeout.
	DefaultTimeout time.Duration
}

// Clients holds every generated Connect client a platform process needs, sharing
// one transport. Every unexported field is safe at its zero value.
type Clients struct {
	User          ginbotv1connect.UserServiceClient
	Utility       ginbotv1connect.UtilityServiceClient
	Reminder      ginbotv1connect.ReminderServiceClient
	Entertainment ginbotv1connect.EntertainmentServiceClient
	Reverse       ginbotv1connect.ReverseServiceClient
	Trigger       ginbotv1connect.TriggerServiceClient
	Repost        ginbotv1connect.RepostServiceClient

	// transport is closed by Close; nil on a struct literal built by a test.
	transport *http2.Transport
}

// defaultCallTimeout is a backstop, looser than every per-command budget in
// pkg/discord so those still win.
const defaultCallTimeout = 30 * time.Second

// HTTP/2 ping keepalive: a parked reverse stream keeps no stream open, so these
// must beat the server's 2-minute idle timeout.
const (
	keepaliveReadIdleTimeout = 30 * time.Second
	keepaliveTimeout         = 15 * time.Second
)

// Dial builds the shared HTTP/2 transport and every generated service client.
func Dial(opts Options) (*Clients, error) {
	if err := validateBaseURL(opts.BaseURL); err != nil {
		return nil, err
	}

	transport := &http2.Transport{
		ReadIdleTimeout: keepaliveReadIdleTimeout,
		PingTimeout:     keepaliveTimeout,
	}

	if opts.TLS == nil {
		// Plaintext h2c: AllowHTTP accepts the "http://" URL and DialTLSContext
		// makes http2.Transport skip the handshake. Using http2.Transport
		// directly leaves no HTTP/1.1 path to silently downgrade to.
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

	// Outermost first: callermeta writes the headers, the deadline sets the
	// budget, and retry runs inside it.
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

// validateBaseURL fails at dial time: connect.NewClient otherwise stores a URL
// parse failure and only surfaces it on each RPC.
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

// Close releases the shared transport's idle connections; safe on a nil or
// struct-literal Clients.
func (c *Clients) Close() {
	if c == nil || c.transport == nil {
		return
	}
	c.transport.CloseIdleConnections()
}
