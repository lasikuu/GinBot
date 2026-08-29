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
)

// Plaintext HTTP/2 needs UnencryptedHTTP2 in http.Server.Protocols on the server and
// AllowHTTP plus an explicit plaintext DialTLSContext on the client. Get either wrong
// and the connection falls back to HTTP/1.1, where only bidi streaming fails.

// newPlaintextH2CServer mirrors cmd/ginbot-server's own GINBOT_GRPC_TLS=false construction.
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

	// Set on srv.Config before Start: httptest reads it only when it calls http.Server.Serve.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)

	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = &protocols
	// Deliberately NOT srv.StartTLS(): plaintext is the point, and the default.
	srv.Start()
	t.Cleanup(srv.Close)

	return srv
}

// newH2CClient sets AllowHTTP and a plaintext DialTLSContext by hand; the http2 package
// has moved that field around, and a stale spelling compiles but does nothing.
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

// A server push with no client request driving it needs real HTTP/2 multiplexing.
func TestReverseStreamCarriesAMessageOverPlaintextH2C(t *testing.T) {
	reverseServer := NewReverseServer()
	dir := newDirectory().add(pb.Platform_PLATFORM_DISCORD, reverseCallerUID, testUser(reverseCallerUserID, pb.Clearance_CLEARANCE_REGISTERED))

	srv := newPlaintextH2CServer(t, reverseServer, dir)
	httpClient := newH2CClient()
	t.Cleanup(httpClient.CloseIdleConnections)

	// A plain GET still gets a 404 over HTTP/1.1, so "the request succeeded" proves nothing.
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

	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		t.Fatalf("send hello: %v", err)
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
