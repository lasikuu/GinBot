package client

import (
	"context"
	"crypto/tls"
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

// realStreamCap mirrors pkg/grpc/server's unexported maxStreamClients; it must
// match production exactly since this package cannot read it.
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

// admissionWindow bounds how long to wait for a refusal before concluding a
// stream was admitted. Admission is only the absence of a refusal, so a too-short
// window can only under-count admissions, never mistake a refusal for one; 300ms
// is generous for a loopback round trip.
const admissionWindow = 300 * time.Millisecond

// openAndCheckAdmission opens one real stream, sends its hello, and reports
// whether the server refused it within admissionWindow. An admitted stream is
// returned open and left parked in Receive until cleanup.
func openAndCheckAdmission(ctx context.Context, reverseClient ginbotv1connect.ReverseServiceClient) (entry actionStream, refusalErr error) {
	stream := reverseClient.OpenClientActionStream(ctx)

	// Empty hello; the server takes the platform from the ginbot-platform-enum
	// header ctx carries.
	hello := pb.OpenClientActionStreamReq_builder{}.Build()
	if err := stream.Send(hello); err != nil {
		return nil, err
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := stream.Receive()
		errCh <- err
	}()

	select {
	case err := <-errCh:
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		return nil, err
	case <-time.After(admissionWindow):
		return stream, nil
	}
}

// fillRealRegistryToCap opens fillAttempts streams concurrently, more than
// realStreamCap so some are refused, and discovers the admitted count
// empirically. It asserts that count equals realStreamCap.
func fillRealRegistryToCap(t *testing.T, ctx context.Context, reverseClient ginbotv1connect.ReverseServiceClient) []actionStream {
	t.Helper()

	const fillAttempts = realStreamCap + 16

	type attemptResult struct {
		stream actionStream
		err    error
	}

	results := make([]attemptResult, fillAttempts)
	var wg sync.WaitGroup
	wg.Add(fillAttempts)
	for i := range fillAttempts {
		go func(i int) {
			defer wg.Done()
			stream, err := openAndCheckAdmission(ctx, reverseClient)
			results[i] = attemptResult{stream: stream, err: err}
		}(i)
	}
	wg.Wait()

	admitted := make([]actionStream, 0, realStreamCap)
	refused := 0
	for i, r := range results {
		if r.err != nil {
			if connect.CodeOf(r.err) != connect.CodeResourceExhausted {
				t.Fatalf("attempt %d was refused with code %v, want %v", i, connect.CodeOf(r.err), connect.CodeResourceExhausted)
			}
			refused++
			continue
		}
		admitted = append(admitted, r.stream)
	}

	if len(admitted) != realStreamCap {
		t.Fatalf("registry admitted %d of %d concurrent streams, want exactly %d (pkg/grpc/server's maxStreamClients)",
			len(admitted), fillAttempts, realStreamCap)
	}
	if refused != fillAttempts-realStreamCap {
		t.Fatalf("registry refused %d of %d concurrent streams, want exactly %d", refused, fillAttempts, fillAttempts-realStreamCap)
	}

	return admitted
}

func TestARefusedClientBacksOffRatherThanRetryingEverySecond(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Inherited by every stream: without it ClearanceInterceptor has no headers
	// to resolve a caller from and refuses every stream with InvalidArgument.
	ctx = callermeta.NewOutgoingContext(ctx, testIdentity.Platform, testIdentity.PlatformUID)

	srv := newRealReverseH2CServer(t)

	httpClient := newH2CClient()
	t.Cleanup(httpClient.CloseIdleConnections)

	// callermeta.NewClientInterceptor turns the ctx value into the ginbot-*
	// request headers ClearanceInterceptor reads; Dial installs it in production.
	reverseClient := ginbotv1connect.NewReverseServiceClient(httpClient, srv.URL, connect.WithInterceptors(callermeta.NewClientInterceptor()))

	admitted := fillRealRegistryToCap(t, ctx, reverseClient)
	t.Cleanup(func() {
		for _, stream := range admitted {
			_ = stream.CloseRequest()
			_ = stream.CloseResponse()
		}
	})

	opener := func(ctx context.Context) actionStream { return reverseClient.OpenClientActionStream(ctx) }

	// Bounded so a wrongly admitted probe fails rather than blocking in Receive.
	probeCtx, cancelProbe := context.WithTimeout(ctx, 10*time.Second)
	defer cancelProbe()

	outcome, err := runOnce(probeCtx, opener, alwaysEnsure, testIdentity, nil)
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
