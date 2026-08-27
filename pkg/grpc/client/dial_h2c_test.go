package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// This file drives client.Dial ITSELF, over a real connection — nothing in
// the repository called Dial at all until now. reverse_h2c_test.go's own
// newH2CClient hand-copies Dial's AllowHTTP + DialTLSContext construction, so
// it proves that copy behaves correctly and nothing about Dial's own wiring.
// An overlay that deletes Dial's whole plaintext branch (see
// TestDialsPlaintextBranchIsNotVacuous below) leaves the rest of this
// package's suite green, because nothing else ever calls Dial either.
//
// Go negotiates HTTP/2 automatically only over TLS, via ALPN. Plaintext needs
// h2c.NewHandler server-side and an http2.Transport with AllowHTTP plus an
// explicit plaintext DialTLSContext client-side — see dial.go's comment on
// why Dial uses http2.Transport directly rather than http.Transport with
// ForceAttemptHTTP2, which is what makes a broken h2c wiring here fail loudly
// on the first call rather than silently falling back to HTTP/1.1. Unary
// Connect calls can still often complete over an HTTP/1.1 fallback; a bidi
// stream cannot carry a server-initiated push over one at all, which is why
// this file asserts both: a transport that silently downgraded (the more
// common mistake elsewhere in the ecosystem, guarded against by the loud
// failure noted above) would still pass a unary-only test while failing the
// bidi one.

// dialTestUtilityHandler answers UtilityService.Ping, enough to prove a
// unary call completes through Dial's transport.
type dialTestUtilityHandler struct {
	ginbotv1connect.UnimplementedUtilityServiceHandler
}

func (dialTestUtilityHandler) Ping(context.Context, *connect.Request[pb.PingReq]) (*connect.Response[pb.PingResp], error) {
	return connect.NewResponse(pb.PingResp_builder{}.Build()), nil
}

// dialTestReverseHandler admits exactly one stream, pushes one action to it
// right after the registration message arrives, and then holds the stream
// open until the client goes away. It is deliberately simpler than
// pkg/grpc/server.ReverseServer or reverse_h2c_test.go's testCapReverseHandler
// (which this file does not need): the ONLY thing under test here is whether
// Dial's transport can carry a server-initiated push at all.
type dialTestReverseHandler struct {
	ginbotv1connect.UnimplementedReverseServiceHandler
}

func (dialTestReverseHandler) OpenClientActionStream(ctx context.Context, stream *connect.BidiStream[pb.OpenClientActionStreamReq, pb.OpenClientActionStreamResp]) error {
	if _, err := stream.Receive(); err != nil {
		return err
	}

	action := pb.ClientAction_CLIENT_ACTION_SEND_TEST
	if err := stream.Send(pb.OpenClientActionStreamResp_builder{ClientAction: &action}.Build()); err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}

// protoRecorder records the negotiated protocol of the last request the
// server observed, so a test can assert the connection ACTUALLY carried
// HTTP/2 rather than merely that a call happened to succeed — Connect's
// unary protocol tolerates an HTTP/1.1 downgrade for a single request/
// response exchange, so "the call succeeded" alone does not prove h2c
// negotiated at all.
type protoRecorder struct {
	mu    sync.Mutex
	proto string
	major int
}

func (r *protoRecorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.proto = req.Proto
		r.major = req.ProtoMajor
		r.mu.Unlock()

		next.ServeHTTP(w, req)
	})
}

func (r *protoRecorder) last() (proto string, major int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proto, r.major
}

// newDialH2CServer mirrors cmd/ginbot-server's own construction for the
// GINBOT_GRPC_TLS=false path: h2c.NewHandler wrapping the mux, served over a
// plain (non-TLS) listener.
func newDialH2CServer(t *testing.T) (*httptest.Server, *protoRecorder) {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewUtilityServiceHandler(dialTestUtilityHandler{}))
	mux.Handle(ginbotv1connect.NewReverseServiceHandler(dialTestReverseHandler{}))

	rec := &protoRecorder{}
	h2s := &http2.Server{}
	srv := httptest.NewUnstartedServer(rec.wrap(h2c.NewHandler(mux, h2s)))
	// Deliberately NOT srv.StartTLS(): plaintext h2c is the point, and it is
	// cmd/ginbot-server's default (GINBOT_GRPC_TLS=false).
	srv.Start()
	t.Cleanup(srv.Close)

	return srv, rec
}

// TestDialCarriesUnaryAndBidiTrafficOverPlaintextH2C is the test the trap
// exists for, driven through Dial itself rather than a hand-copied transport:
// a unary call completes AND the server's negotiated protocol was actually
// HTTP/2, AND a bidi stream opened through the same *Clients carries a
// server-pushed message. Bidi is what actually breaks under a silent
// HTTP/1.1 downgrade while unary keeps passing — see the file comment.
func TestDialCarriesUnaryAndBidiTrafficOverPlaintextH2C(t *testing.T) {
	srv, rec := newDialH2CServer(t)

	clients, err := Dial(Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(clients.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := clients.Utility.Ping(ctx, connect.NewRequest(pb.PingReq_builder{}.Build())); err != nil {
		t.Fatalf("Ping through Dial's transport: %v", err)
	}

	if proto, major := rec.last(); major != 2 {
		t.Fatalf("server observed request proto %q (HTTP major %d), want HTTP/2: Dial's plaintext branch did not negotiate h2c",
			proto, major)
	}

	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()

	stream := clients.Reverse.OpenClientActionStream(streamCtx)
	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})

	if err := stream.Send(pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()); err != nil {
		t.Fatalf("send registration: %v", err)
	}

	resp, err := stream.Receive()
	if err != nil {
		t.Fatalf("Receive: %v — the bidi stream carried no server push at all, exactly what an HTTP/1.1 fallback produces", err)
	}
	if got := resp.GetClientAction(); got != pb.ClientAction_CLIENT_ACTION_SEND_TEST {
		t.Errorf("client_action = %v, want %v", got, pb.ClientAction_CLIENT_ACTION_SEND_TEST)
	}
}

// TestDialCarriesUnaryTrafficOverTLS is the TLS counterpart: Dial with a
// non-nil Options.TLS against a server negotiating HTTP/2 the ordinary way,
// over ALPN. Cheap to add because httptest does the certificate handling;
// unary-only is enough here since the h2c trap this file otherwise exists for
// does not apply on this path — Go negotiates HTTP/2 automatically over TLS.
func TestDialCarriesUnaryTrafficOverTLS(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewUtilityServiceHandler(dialTestUtilityHandler{}))

	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	certPool := x509.NewCertPool()
	certPool.AddCert(srv.Certificate())

	clients, err := Dial(Options{
		BaseURL: srv.URL,
		TLS:     &tls.Config{RootCAs: certPool},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(clients.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := clients.Utility.Ping(ctx, connect.NewRequest(pb.PingReq_builder{}.Build())); err != nil {
		t.Fatalf("Ping through Dial's TLS transport: %v", err)
	}
}
