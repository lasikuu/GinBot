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
	"golang.org/x/net/http2/h2c"
)

// This file is TestARefusedClientBacksOffRatherThanRetryingEverySecond,
// driven against the REAL pkg/grpc/server.ReverseServer — cap, clearance
// interceptor and the wire-transmitted connect.CodeResourceExhausted
// included — over plaintext h2c, following pkg/grpc/server/reverse_h2c_test.go's
// own pattern for driving a bidi stream over that transport: the
// GINBOT_GRPC_TLS=false configuration cmd/ginbot-server ships by default, and
// the one Go does not negotiate automatically the way it does over TLS/ALPN.
//
// Its grpc-go predecessor drove a real *server.ReverseServer over bufconn,
// registered with pb.RegisterReverseServiceServer — neither of which exists
// any more. Driving pkg/grpc/server.ReverseServer from here used to be a
// compile error: pkg/grpc/server imported pkg/db, which imported
// internal/config, which imported THIS package (internal/config/client.go
// built client.Options) — so pkg/grpc/client's own internal (white-box) test
// files could not import pkg/grpc/server, pkg/grpc/interceptor, or pkg/db
// without `go vet` reporting "import cycle not allowed in test".
//
// That cycle is gone: the config -> client edge moved out to the leaf
// package internal/clientopts (see its own doc comment for the full
// reasoning), and pkg/grpc/client's test files can import pkg/grpc/server
// with no cycle. So the interim stand-in this file used to carry —
// testCapReverseHandler, a 40-line handler reproducing ONLY "refuse past a
// small known cap with connect.CodeResourceExhausted" — is deleted below in
// favour of the real thing. What this file now proves that a stand-in never
// could: that recvOutcome classifies a REAL wire-transmitted
// ResourceExhausted from the ACTUAL maxStreamClients registry as
// streamRejected, and that nextBackoff escalates rather than resets from it,
// over a genuine HTTP/2 connection driving genuine server-side registry
// bookkeeping — not a hand-built error value standing in for it.
//
// The real ReverseServer's Shutdown()-before-server-stop teardown ordering
// this test's grpc-go predecessor was the only thing in the repository to
// exercise is still NOT reproduced here — that ordering lives entirely
// server-side and is covered there: pkg/grpc/server/reverse_test.go's
// TestShutdownTerminatesEveryOpenStream and friends, plus
// cmd/ginbot-server/main.go's own shutdown sequencing.

// realStreamCap mirrors pkg/grpc/server's own unexported maxStreamClients
// (documented there as 64, "far above the real deployment ... headroom for
// reconnect churn and rolling restarts"). This test drives the REAL
// server's registry all the way to its actual admission limit rather than a
// locally-owned stand-in cap, so the value here has to match production's
// exactly — there is no way to read maxStreamClients from this package, and
// probing for it by filling until refused would make the fill loop's own
// length nondeterministic.
const realStreamCap = 64

// reverseH2CCallerUserID is the user_account id fixedCallerResolver resolves
// every caller to. It must be a UUID: GetUserReq-shaped validation elsewhere
// in the schema expects one, and this keeps the fixture consistent with that
// even though nothing in this file exercises GetUser.
const reverseH2CCallerUserID = "018f0000-0000-7000-8000-0000000000e0"

// fixedCallerResolver is an interceptor.CallerResolver that always resolves
// to the one identity every stream in this file opens as, at
// CLEARANCE_REGISTERED — the floor OpenClientActionStream enforces via
// interceptor.DefaultRequirements.
//
// Unlike pkg/grpc/server's own harness (harness_test.go's directory type),
// this does not need to distinguish between callers or report an unknown one
// as db.ErrNotFound: every stream this file opens, admitted or refused, is
// the SAME caller, so there is only ever one identity to resolve and no
// negative case to prove.
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

// newRealReverseH2CServer mounts the REAL pkg/grpc/server.ReverseServer
// behind the same recovery, validation and clearance interceptors
// cmd/ginbot-server installs in front of it — see
// pkg/grpc/server/reverse_h2c_test.go's newPlaintextH2CServer, which this
// mirrors — over plaintext h2c.
//
// interceptor.NewOriginInterceptor is deliberately NOT included. Its own doc
// comment records that WrapStreamingHandler is a no-op for
// OpenClientActionStream specifically — the reverse stream is not scoped to
// a single origin at all — so adding it here would change nothing this test
// can observe while pulling in an OriginResolver fixture purely for show.
// RecoverInterceptor and connectvalidate.NewInterceptor are kept: the former
// is what stops a handler-side bug from taking the whole test binary down
// with it, and the latter is what actually enforces
// OpenClientActionStreamReq's platform_enum requirement at the edge — see
// pkg/grpc/server/reverse.go's comment on why that check is still duplicated
// defensively inside the handler.
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

	h2s := &http2.Server{}
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, h2s))
	// Deliberately NOT srv.StartTLS(): plaintext h2c is the point, and it is
	// cmd/ginbot-server's default.
	srv.Start()
	t.Cleanup(srv.Close)

	return srv
}

// newH2CClient builds an http.Client that speaks HTTP/2 over plaintext, the
// same construction pkg/grpc/server/reverse_h2c_test.go uses from the other
// side of this same trap.
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

// admissionWindow bounds how long openAndCheckAdmission waits for a refusal
// to arrive on a freshly registered stream before concluding it was admitted
// and moving on to the next one.
//
// It cannot make this test wrongly report a refusal as an admission: a
// refusal is a positive signal (an error actually arrives), while
// "admitted" is only ever the ABSENCE of one within the window. The
// window's only failure mode is therefore treating a still-processing
// registration as admitted prematurely — which costs nothing here, since
// the next registration then finds the registry either genuinely full
// (correctly detected) or genuinely not (also correctly detected). 300ms
// is generous for a loopback HTTP/2 round trip specifically so that never
// happens in practice.
//
// An earlier version of this fill loop confirmed admission by having
// ReverseServer.SendAction (a real fan-out broadcast) push a probe action
// and waiting for it to arrive, retrying the broadcast until it did. That
// broadcasts to EVERY client already registered on the platform, so by the
// time the 64th stream was being confirmed, the 1st had accumulated dozens
// of real, in-flight Send() calls on its own server-side sender goroutine.
// Tearing every stream down while some of those sends were still in flight
// made ReverseServer.OpenClientActionStream's sender goroutine (reverse.go,
// the "failed to send action to client" path) log through the shared
// package-global log.Z at a point this test could not bound — including
// after this test function had already returned, racing the NEXT test in
// this binary that swaps log.Z (observeLogs, reverse_test.go), caught by
// -race. This design pushes nothing to any admitted stream at all, so no
// admitted stream's sender goroutine ever calls Send, and none has anything
// to log when the connection is torn down at cleanup.
const admissionWindow = 300 * time.Millisecond

// openAndCheckAdmission opens one real stream, sends its registration, and
// reports whether the server refused it within admissionWindow. A refused
// stream is closed and reported via refusalErr; an admitted one is returned
// open, with nothing further ever read from or written to it by this file —
// it stays parked in Receive, exactly like a real platform client with
// nothing to deliver, until test cleanup closes it.
func openAndCheckAdmission(ctx context.Context, reverseClient ginbotv1connect.ReverseServiceClient) (entry actionStream, refusalErr error) {
	stream := reverseClient.OpenClientActionStream(ctx)

	registration := pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()
	if err := stream.Send(registration); err != nil {
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

// fillRealRegistryToCap opens fillAttempts real streams CONCURRENTLY against
// the REAL server — more than realStreamCap, so some are refused — and
// discovers the registry's actual cap empirically from how many come back
// admitted rather than counting to a value copied from production (see
// admissionWindow's comment for why that matters). Concurrent rather than
// sequential for the same reason pkg/grpc/server's own
// TestManyConcurrentRegistrationsWinRaceRegardlessOfOrder drives its
// equivalent check concurrently: it is both the faster shape (one
// admissionWindow wait total instead of one per stream) and the more
// realistic one — s.register's own admission check and insert run under a
// single write lock specifically to be race-safe under exactly this kind of
// contention.
//
// realStreamCap documents the expected value this should settle on
// (pkg/grpc/server's own unexported maxStreamClients, currently 64) and is
// asserted against, so a change to that constant is caught here rather than
// silently tolerated.
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
	for i := 0; i < fillAttempts; i++ {
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

	// Attached once here and inherited by every stream this test opens,
	// mirroring how RunClientActionStream attaches it before the reconnect
	// loop ever calls open — see reverse.go. Without it ClearanceInterceptor
	// has no headers to resolve a caller from and refuses every stream with
	// InvalidArgument before the cap is ever reached.
	ctx = callermeta.NewOutgoingContext(ctx, testIdentity.Platform, testIdentity.PlatformUID)

	srv := newRealReverseH2CServer(t)

	httpClient := newH2CClient()
	t.Cleanup(httpClient.CloseIdleConnections)

	// callermeta.NewClientInterceptor is what turns NewOutgoingContext's
	// context value into the ginbot-* request headers ClearanceInterceptor
	// reads — pkg/grpc/client.Dial installs it in production; this test
	// builds the raw ginbotv1connect client directly, so it has to install
	// the same interceptor itself.
	reverseClient := ginbotv1connect.NewReverseServiceClient(httpClient, srv.URL, connect.WithInterceptors(callermeta.NewClientInterceptor()))

	admitted := fillRealRegistryToCap(t, ctx, reverseClient)
	t.Cleanup(func() {
		for _, stream := range admitted {
			_ = stream.CloseRequest()
			_ = stream.CloseResponse()
		}
	})

	opener := func(ctx context.Context) actionStream { return reverseClient.OpenClientActionStream(ctx) }

	// A bounded context so a probe that is wrongly ADMITTED fails this test
	// rather than blocking in Receive until the whole suite times out.
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
