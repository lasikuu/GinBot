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
)

// This file drives client.Dial itself over a real connection. Plaintext HTTP/2
// needs UnencryptedHTTP2 server-side and an http2.Transport with AllowHTTP plus
// an explicit plaintext DialTLSContext client-side; get it wrong and traffic
// silently downgrades to HTTP/1.1, where unary calls still pass but a bidi
// stream cannot carry a server push. Both are asserted below.

// dialTestUtilityHandler answers Ping through Dial's transport.
type dialTestUtilityHandler struct {
	ginbotv1connect.UnimplementedUtilityServiceHandler
}

func (dialTestUtilityHandler) Ping(context.Context, *connect.Request[pb.PingReq]) (*connect.Response[pb.PingResp], error) {
	return connect.NewResponse(pb.PingResp_builder{}.Build()), nil
}

// dialTestReverseHandler pushes one action after the hello arrives, then holds
// the stream open. All that is under test is whether Dial's transport can carry
// a server push.
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

// protoRecorder records the negotiated protocol of the last request, so a test
// can assert HTTP/2 was actually used: a succeeding unary call alone does not
// prove h2c negotiated.
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

// newDialH2CServer mirrors cmd/ginbot-server's GINBOT_GRPC_TLS=false path: a
// plain listener whose http.Server has UnencryptedHTTP2 in its Protocols.
func newDialH2CServer(t *testing.T) (*httptest.Server, *protoRecorder) {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewUtilityServiceHandler(dialTestUtilityHandler{}))
	mux.Handle(ginbotv1connect.NewReverseServiceHandler(dialTestReverseHandler{}))

	// UnencryptedHTTP2 is what protoRecorder watches for: drop it and every
	// request arrives as HTTP/1.1. Set before Start so httptest reads it.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)

	rec := &protoRecorder{}
	srv := httptest.NewUnstartedServer(rec.wrap(mux))
	srv.Config.Protocols = &protocols
	srv.Start()
	t.Cleanup(srv.Close)

	return srv, rec
}

// A unary call completes over HTTP/2 and a bidi stream through the same *Clients
// carries a server push. Bidi is what breaks under a silent HTTP/1.1 downgrade.
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

	streamCtx := t.Context()

	stream := clients.Reverse.OpenClientActionStream(streamCtx)
	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})

	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	resp, err := stream.Receive()
	if err != nil {
		t.Fatalf("Receive: %v — the bidi stream carried no server push at all, exactly what an HTTP/1.1 fallback produces", err)
	}
	if got := resp.GetClientAction(); got != pb.ClientAction_CLIENT_ACTION_SEND_TEST {
		t.Errorf("client_action = %v, want %v", got, pb.ClientAction_CLIENT_ACTION_SEND_TEST)
	}
}

// The TLS counterpart: Dial with a non-nil Options.TLS. Unary-only, since Go
// negotiates HTTP/2 automatically over TLS.
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
