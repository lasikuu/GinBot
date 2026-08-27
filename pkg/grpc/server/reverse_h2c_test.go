package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// This file is the single highest-value test in the whole Connect port.
//
// Go negotiates HTTP/2 automatically only over TLS, via ALPN. Plaintext
// HTTP/2 needs h2c.NewHandler on the server and an http2.Transport with
// AllowHTTP plus an explicit plaintext DialTLSContext on the client, both
// spelled out by hand. Get either wrong and the connection silently falls
// back to HTTP/1.1 — where every unary RPC still passes, because Connect's
// unary protocol works fine over HTTP/1.1, and only OpenClientActionStream's
// bidirectional stream fails. That lands exactly in the GINBOT_GRPC_TLS=false
// configuration cmd/ginbot-server ships by default in
// docker-compose.prod.yml. The general harness in harness_test.go cannot
// catch this class of bug at all: it deliberately runs over TLS (StartTLS),
// where Go's http2 support is automatic and the h2c wiring this file is
// about is never exercised.
//
// newPlaintextH2CServer below reproduces cmd/ginbot-server's OWN
// construction — h2c.NewHandler wrapping the mux, handed to a plain (non-TLS)
// http.Server — rather than reusing the general harness, because reusing it
// would test httptest's h2c support instead of production's.

// newPlaintextH2CServer mirrors cmd/ginbot-server/main.go's construction for
// the GINBOT_GRPC_TLS=false path: an http2.Server wrapped by h2c.NewHandler,
// served over a plain (non-TLS) listener. It mounts ReverseService alone —
// enough to prove the transport carries a bidi stream, without pulling in
// every other service's dependencies main.go wires (storage, cron, database).
func newPlaintextH2CServer(t *testing.T, reverse *ReverseServer, dir *directory) *httptest.Server {
	t.Helper()

	handlerOpts := []connect.HandlerOption{
		connect.WithInterceptors(
			interceptor.RecoverInterceptor{},
			connectvalidate.NewInterceptor(),
			interceptor.NewClearanceInterceptor(interceptor.DefaultRequirements(), dir.resolve),
			interceptor.NewOriginInterceptor(newOriginLog().resolve),
		),
	}

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewReverseServiceHandler(reverse, handlerOpts...))

	// The same h2s := &http2.Server{...}; srv.Handler = h2c.NewHandler(mux, h2s)
	// pairing cmd/ginbot-server/main.go uses. MaxConcurrentStreams is left at
	// its zero value here — that setting is unrelated to what this test is
	// checking — but the h2c wiring itself is byte-for-byte the same shape.
	h2s := &http2.Server{}
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, h2s))
	// Deliberately NOT srv.StartTLS(): plaintext is the point. This is the
	// GINBOT_GRPC_TLS=false configuration, which is also cmd/ginbot-server's
	// default.
	srv.Start()
	t.Cleanup(srv.Close)

	return srv
}

// newH2CClient builds an http.Client that speaks HTTP/2 over plaintext,
// mirroring what a Connect client dialing GINBOT_GRPC_TLS=false has to do:
// http2.Transport does not offer h2c automatically, unlike its TLS/ALPN path,
// so AllowHTTP and an explicit plaintext DialTLSContext are both required by
// hand. DialTLSContext is the field to get right — the http2 package's API
// has moved this around across versions, and the wrong name compiles against
// a stale vendored copy but silently does nothing here.
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

// TestReverseStreamCarriesAMessageOverPlaintextH2C is the test the trap
// exists for: it opens OpenClientActionStream over plaintext h2c against a
// server built exactly the way cmd/ginbot-server builds it, pushes an action
// through SendAction, and asserts the message actually arrives — server to
// client, the direction SendAction's fan-out uses and the one that matters in
// production. A test that only opened the stream without carrying a message
// would not catch an HTTP/1.1 fallback: Connect can often still complete a
// SINGLE request/response-shaped exchange degraded, but a message pushed from
// the server with no client request driving it cannot arrive at all without
// real HTTP/2 multiplexing.
func TestReverseStreamCarriesAMessageOverPlaintextH2C(t *testing.T) {
	reverseServer := NewReverseServer()
	dir := newDirectory().add(pb.Platform_PLATFORM_DISCORD, reverseCallerUID, testUser(reverseCallerUserID, pb.Clearance_CLEARANCE_REGISTERED))

	srv := newPlaintextH2CServer(t, reverseServer, dir)
	httpClient := newH2CClient()
	t.Cleanup(httpClient.CloseIdleConnections)

	// Assert the negotiated protocol directly, not just that SOME request
	// succeeded: a plain GET against the mux still gets a response (404, since
	// nothing is mounted at "/") over HTTP/1.1 if the h2c wiring is broken,
	// which would make "the request succeeded" alone a false-positive signal.
	resp, err := httpClient.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET %s: %v", srv.URL, err)
	}
	_ = resp.Body.Close()
	if resp.Proto != "HTTP/2.0" {
		t.Fatalf("negotiated protocol = %q, want %q; the connection fell back instead of speaking h2c",
			resp.Proto, "HTTP/2.0")
	}

	client := ginbotv1connect.NewReverseServiceClient(httpClient, srv.URL, connect.WithInterceptors(identityInterceptor{}))

	ctx, cancel := context.WithCancel(callerCtx(pb.Platform_PLATFORM_DISCORD, reverseCallerUID))
	t.Cleanup(cancel)
	stream := client.OpenClientActionStream(ctx)
	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})

	if err := stream.Send(pb.OpenClientActionStreamReq_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
	}.Build()); err != nil {
		t.Fatalf("send registration: %v", err)
	}

	waitFor(t, func() bool { return reverseServer.clientCount() == 1 })

	reverseServer.SendAction(testAction(pb.Platform_PLATFORM_DISCORD))

	received := make(chan *pb.OpenClientActionStreamResp, 1)
	receiveErr := make(chan error, 1)
	go func() {
		resp, err := stream.Receive()
		if err != nil {
			receiveErr <- err
			return
		}
		received <- resp
	}()

	select {
	case resp := <-received:
		if resp.GetClientAction() != pb.ClientAction_CLIENT_ACTION_SEND_TEST {
			t.Errorf("client_action = %v, want %v", resp.GetClientAction(), pb.ClientAction_CLIENT_ACTION_SEND_TEST)
		}
		if resp.GetPlatformEnum() != pb.Platform_PLATFORM_DISCORD {
			t.Errorf("platform_enum = %v, want %v", resp.GetPlatformEnum(), pb.Platform_PLATFORM_DISCORD)
		}
	case err := <-receiveErr:
		t.Fatalf("Receive: %v — the bidi stream did not carry the server's push at all, "+
			"which is exactly what an HTTP/1.1 fallback produces", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no message arrived within the deadline: the server never delivered the push, " +
			"which is exactly what an HTTP/1.1 fallback produces")
	}
}
