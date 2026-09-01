package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/grpc/server"
	"golang.org/x/net/http2"
)

// This file drives TestARefusedClientBacksOffRatherThanRetryingEverySecond
// against the real pkg/grpc/server.ReverseServer over plaintext h2c, proving
// runOnce classifies a wire-transmitted ResourceExhausted from the actual
// registry cap as streamRejected and backs off from it.

// realStreamCap mirrors pkg/grpc/server's unexported maxStreamClients. It is a
// fill target only: overshooting it is what fills the registry, and the exact
// value is asserted by that package's own tests, not from here.
const realStreamCap = 64

// reverseH2CCallerUserID must be a UUID for schema validation consistency.
const reverseH2CCallerUserID = "018f0000-0000-7000-8000-0000000000e0"

// fixedCallerResolver resolves every caller to one CLEARANCE_REGISTERED
// identity, the floor OpenClientActionStream enforces.
func fixedCallerResolver() interceptor.CallerResolver {
	user := &model.User{
		ID:        reverseH2CCallerUserID,
		Username:  "reverse-h2c-caller",
		Clearance: int32(pb.Clearance_CLEARANCE_REGISTERED),
	}
	return func(context.Context, pb.Platform, string) (*model.User, error) {
		return user, nil
	}
}

// newRealReverseH2CServer mounts the real ReverseServer behind production's
// recovery, validation and clearance interceptors over plaintext h2c.
// OriginInterceptor is omitted: a reverse stream carries no origin headers.
func newRealReverseH2CServer(t *testing.T) *httptest.Server {
	t.Helper()

	reverseServer := server.NewReverseServer()

	handlerOpts := []connect.HandlerOption{
		connect.WithInterceptors(
			interceptor.RecoverInterceptor{},
			connectvalidate.NewInterceptor(),
			interceptor.NewClearanceInterceptor(interceptor.DefaultRequirements(), fixedCallerResolver()),
		),
	}

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewReverseServiceHandler(reverseServer, handlerOpts...))

	// UnencryptedHTTP2 is load-bearing: without it the server answers HTTP/1.1
	// and the bidi stream cannot open. Set before Start so httptest reads it.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)

	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = &protocols
	srv.Start()
	t.Cleanup(srv.Close)
	// Registered after srv.Close so LIFO runs it first, the ordering
	// cmd/ginbot-server also depends on: httptest's Close waits for in-flight
	// handlers without cancelling their contexts, and nothing else unparks a
	// stream handler blocked in Receive.
	t.Cleanup(reverseServer.Shutdown)

	return srv
}

// newH2CClient builds an http.Client that speaks HTTP/2 over plaintext.
func newH2CClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
}

// parkStream opens one stream, sends the hello that makes connect issue the
// request, and leaves it parked. The cleanup is registered before anything can
// fail, and from every attempt including refused ones: a stream left open by an
// abandoned attempt keeps its handler in-flight, and httptest's Close then
// blocks until the test binary's own timeout kills the package, discarding the
// failure that caused it. Safe to call concurrently; testing.T.Cleanup is.
func parkStream(t *testing.T, ctx context.Context, reverseClient ginbotv1connect.ReverseServiceClient) {
	stream := reverseClient.OpenClientActionStream(ctx)
	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})

	// A refusal surfaces on Receive, not here, and this helper does not care
	// which streams were admitted.
	_ = stream.Send(pb.OpenClientActionStreamReq_builder{}.Build())
}

// fillRealRegistry opens more streams than the server's cap, so the registry
// ends up full whichever ones it admitted. Admission is silent on the wire, so
// a client cannot tell its own streams apart; only a refusal is observable.
func fillRealRegistry(t *testing.T, ctx context.Context, reverseClient ginbotv1connect.ReverseServiceClient) {
	t.Helper()

	const fillAttempts = realStreamCap + 16

	var wg sync.WaitGroup
	wg.Add(fillAttempts)
	for range fillAttempts {
		go func() {
			defer wg.Done()
			parkStream(t, ctx, reverseClient)
		}()
	}
	wg.Wait()
}

const (
	// probeTimeout bounds one probe attempt. A probe admitted because the fill
	// has not landed yet parks in Receive, so this is how long that costs.
	probeTimeout = 2 * time.Second
	// fullRegistryDeadline bounds the retries; only a machine that never gets
	// the registry full needs it.
	fullRegistryDeadline = 30 * time.Second
)

// probeUntilRefused returns the first runOnce attempt the server actually
// refused. An attempt that only ran out of time is retried rather than read as
// a refusal or an admission: inferring either from a timeout is what makes this
// shape of test flaky on a loaded machine, and a probe wrongly counted as
// admitted used to fail an assertion while its stream stayed parked.
func probeUntilRefused(t *testing.T, ctx context.Context, open actionStreamOpener) (streamOutcome, error) {
	t.Helper()

	deadline := time.Now().Add(fullRegistryDeadline)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		outcome, err := runOnce(probeCtx, open, alwaysEnsure, testIdentity, nil)
		cancel()

		// The probe's own deadline, not an answer from the server.
		if errors.Is(err, context.DeadlineExceeded) {
			continue
		}

		return outcome, err
	}

	t.Fatalf("no probe was refused within %v: the registry never reached its %d-stream cap, so this test never reached its subject",
		fullRegistryDeadline, realStreamCap)

	return streamUnreachable, nil
}

func TestARefusedClientBacksOffRatherThanRetryingEverySecond(t *testing.T) {
	// t.Context is cancelled before any cleanup runs, so every parked handler is
	// already unwinding by the time httptest waits for them.
	// The caller metadata is inherited by every stream: without it
	// ClearanceInterceptor has no headers to resolve a caller from and refuses
	// every stream with InvalidArgument.
	ctx := callermeta.NewOutgoingContext(t.Context(), testIdentity.Platform, testIdentity.PlatformUID)

	srv := newRealReverseH2CServer(t)

	httpClient := newH2CClient()
	t.Cleanup(httpClient.CloseIdleConnections)

	// callermeta.NewClientInterceptor turns the ctx value into the ginbot-*
	// request headers ClearanceInterceptor reads; Dial installs it in production.
	reverseClient := ginbotv1connect.NewReverseServiceClient(httpClient, srv.URL, connect.WithInterceptors(callermeta.NewClientInterceptor()))

	fillRealRegistry(t, ctx, reverseClient)

	opener := func(ctx context.Context) actionStream { return reverseClient.OpenClientActionStream(ctx) }

	outcome, err := probeUntilRefused(t, ctx, opener)
	if outcome != streamRejected {
		t.Fatalf("a client refused by a full registry was classified %s (err %v), want streamRejected",
			outcome, err)
	}
	if got := connect.CodeOf(err); got != connect.CodeResourceExhausted {
		t.Errorf("refusal code = %v, want %v (runOnce must classify the code actually on the wire)", got, connect.CodeResourceExhausted)
	}

	if got := nextBackoff(reconnectMinBackoff, outcome); got <= reconnectMinBackoff {
		t.Errorf("a refused client's next delay = %v, want it longer than %v rather than pinned there",
			got, reconnectMinBackoff)
	}
}
