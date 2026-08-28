package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	// Embeds the IANA zone database in the binary so that SetTimezone can
	// resolve a zone on a host that ships none, such as a scratch container.
	// Without it time.LoadLocation fails for every name and no timezone is
	// settable at all.
	_ "time/tzdata"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	connectvalidate "connectrpc.com/validate"
	"github.com/lasikuu/GinBot/internal/auth"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/cron"
	"github.com/lasikuu/GinBot/pkg/db"
	"github.com/lasikuu/GinBot/pkg/enum"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/storage"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

const (
	// maxConcurrentStreams bounds HTTP/2 streams per CONNECTION. It is not what
	// caps concurrent reverse action stream clients — see maxStreamClients in
	// pkg/grpc/server/reverse.go for why that is a separate, application-level
	// limit on the registry rather than a transport setting. This one is the
	// transport's own per-connection ceiling, generous enough that ordinary
	// unary traffic multiplexed onto the same connection as a reverse stream
	// never queues behind it.
	maxConcurrentStreams = 250

	// readHeaderTimeout bounds how long the server waits for a client to finish
	// sending request headers, closing a connection that trickles them in (or
	// never finishes) rather than tying up a goroutine indefinitely.
	//
	// It is deliberately the ONLY read-side timeout set on http.Server. ReadTimeout
	// is NOT set: it bounds the whole request, and a reverse action stream is one
	// HTTP request that legitimately stays open for the platform client's entire
	// connected lifetime. Setting it would cut every open stream at a fixed age.
	readHeaderTimeout = 5 * time.Second

	// idleTimeout bounds an HTTP/2 connection with no open streams.
	//
	// ReadHeaderTimeout does not cover this: after the h2c preface the
	// connection is hijacked out from under net/http, so an idle-but-open
	// connection is otherwise bounded by nothing at all. It is the
	// streaming-safe knob — unlike ReadTimeout, it counts only while no stream
	// is open, so a reverse action stream parked waiting for an action does not
	// age out.
	idleTimeout = 2 * time.Minute

	// baselineMessageBytes bounds a single message on every service, including
	// TriggerService. Connect applies no cap of its own, so this is the only
	// thing standing between a public procedure and an attacker-chosen
	// allocation; see the comment where it is applied.
	//
	// 4 MiB is grpc-go's own default receive cap, which is what this server ran
	// under before the port for every service including TriggerService, so
	// nothing that worked then is refused now. TriggerService no longer needs a
	// raised cap of its own: GetFile streams 1 MiB chunks instead of returning
	// a file inline, so no message on this boundary carries a whole file any
	// more.
	baselineMessageBytes = 4 << 20

	// shutdownTimeout bounds how long graceful shutdown waits for in-flight
	// requests — unary calls draining, reverse streams released by
	// ReverseServer.Shutdown() below — to finish before the listener is forced
	// closed.
	shutdownTimeout = 10 * time.Second

	// shutdownDrainDelay is a pause between flipping the health checker to
	// NOT_SERVING and calling srv.Shutdown. Without it the two happen back to
	// back, so a prober polling GET /healthz never observes the 503: by the
	// time it would reconnect, the listener is already gone and it sees a
	// refused connection instead. This delay buys an orchestrator or load
	// balancer time to see the 503 and stop routing new work here before the
	// listener actually closes — an orderly shutdown now costs at least this
	// long in exchange for that. A second SIGINT or SIGTERM cuts it short, so
	// a developer hitting Ctrl-C twice is not made to wait it out.
	//
	// shutdownDrainDelay + shutdownTimeout is 15s, comfortably inside the 30s
	// stop_grace_period set on ginbot-server in docker-compose.prod.yml — that
	// margin is deliberate, not incidental: a shutdown that ran past the grace
	// period would get SIGKILLed before db.CloseDB and log.Sync ever ran.
	//
	// It bounds the TRANSPORT's half of teardown and nothing else. What runs
	// after srv.Shutdown returns is not covered: cronWait.Wait blocks until an
	// inline cron job finishes, and a job that ignores its context (see
	// pkg/cron) can take as long as its own query does, then db.CloseDB and
	// log.Sync follow. The remaining 15s of grace is the budget for those. If a
	// cron job ever grows a genuinely long-running query, bound the job rather
	// than bounding cronWait.Wait — waiting is what stops the pool being closed
	// under an in-flight query.
	shutdownDrainDelay = 5 * time.Second

	// healthProbeArg turns this binary into a one-shot health probe that exits
	// 0 when the server answers GET /healthz with a 200, instead of starting a
	// server. docker-compose.prod.yml's healthcheck is exactly this.
	//
	// The probe is this binary rather than the runtime image's busybox wget
	// because wget cannot reach the server at all once GINBOT_GRPC_TLS is on:
	// auth.ServerTLSConfig sets RequireAndVerifyClientCert, so the listener
	// accepts nothing that fails to present a client certificate, and busybox
	// wget cannot present one. A plaintext probe against a TLS listener never
	// succeeds either. Since both platform clients gate on
	// `condition: service_healthy`, a probe that cannot pass under TLS does not
	// merely misreport — it stops the whole stack from starting. Reusing this
	// binary means the probe loads the same certificate material through the
	// same internal/auth path the platform clients use, so a green healthcheck
	// under TLS is also evidence that mutual TLS itself works.
	healthProbeArg = "-healthcheck"

	// healthProbeHost is the address the probe dials, and it is deliberately
	// NOT config.Options.GRPC.Host. That variable is a BIND address — the
	// server passes it straight to net.Listen — and in production it is the
	// wildcard 0.0.0.0, which names no peer to connect to. The probe runs
	// inside the server's own container, so loopback is the correct target, and
	// it is what makes the wildcard bind load-bearing rather than incidental:
	// cmd/ginbot-server/compose_test.go pins that relationship.
	//
	// "localhost" rather than "127.0.0.1" so the name matches a DNS SAN in
	// cert/server-ext.conf under TLS, where the probe verifies the server
	// certificate like any other client.
	healthProbeHost = "localhost"

	// healthProbeTimeout bounds the probe's own request. It is well under the
	// 5s `timeout` on the compose healthcheck so that a slow probe reports a
	// failure itself, with a logged reason, rather than being killed by the
	// container runtime with none.
	healthProbeTimeout = 3 * time.Second
)

// healthProbeURL is the URL the in-container probe dials. The scheme has to
// track GINBOT_GRPC_TLS: the server serves either plaintext h2c or TLS on the
// one listener, never both, so probing the wrong scheme cannot connect.
func healthProbeURL(tlsEnabled bool, port string) string {
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}

	return scheme + "://" + net.JoinHostPort(healthProbeHost, port) + "/healthz"
}

// probeHealth performs one GET against the health endpoint, returning nil only
// on a 200.
//
// tlsConf is nil for a plaintext server. When it is non-nil it carries the
// client key pair the server's RequireAndVerifyClientCert demands, and leaves
// ServerName unset so the certificate is verified against the host in url —
// the same posture auth.ClientTLSConfig gives the platform clients.
func probeHealth(ctx context.Context, url string, tlsConf *tls.Config) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConf}}
	defer client.CloseIdleConnections()

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", url, err)
	}
	defer resp.Body.Close()
	// Drained so the connection is reusable and CloseIdleConnections has
	// something to close, rather than leaving a half-read response behind.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: %s", url, resp.Status)
	}

	return nil
}

// runHealthProbe is the whole of the container healthcheck. It returns the
// process exit code: 0 is healthy, anything else is not.
//
// It shares nothing with the running server but the configuration, which is
// the point — it is a separate process, started by the container runtime, that
// has to reach the listener the same way any other client would.
func runHealthProbe() int {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	defer log.Sync()
	config.SetEnv()

	var tlsConf *tls.Config
	if config.Options.GRPC.TLS {
		conf, err := auth.ClientTLSConfig(config.Options.GRPC.CertsPath)
		if err != nil {
			// Not fatal via log.Z.Fatal: os.Exit(1) there would skip the
			// log.Sync above and the reason for an unhealthy container would
			// be the one thing not written out.
			log.Z.Error("health probe could not load client TLS material.", zap.Error(err))
			return 1
		}
		tlsConf = conf
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()

	url := healthProbeURL(config.Options.GRPC.TLS, config.Options.GRPC.Port)
	if err := probeHealth(ctx, url, tlsConf); err != nil {
		log.Z.Error("health probe failed.", zap.Error(err))
		return 1
	}

	return 0
}

// healthChecker backs UtilityService/HealthCheck, the gRPC health protocol and
// GET /healthz from one probe, so the three surfaces this process exposes
// cannot disagree about whether it is healthy.
//
// It additionally reports NOT_SERVING once shutdown begins, independent of what
// the probe itself would say: a database that is still reachable is not a
// reason for a load balancer to keep sending new work to a process on its way
// out.
type healthChecker struct {
	probe        func(ctx context.Context) error
	shuttingDown atomic.Bool
}

func newHealthChecker(probe func(ctx context.Context) error) *healthChecker {
	return &healthChecker{probe: probe}
}

// shutdown flips every health surface to NOT_SERVING. Safe to call once; there
// is no un-shutdown, since nothing in this process's lifecycle needs one.
func (h *healthChecker) shutdown() {
	h.shuttingDown.Store(true)
}

// Check implements grpchealth.Checker.
func (h *healthChecker) Check(ctx context.Context, _ *grpchealth.CheckRequest) (*grpchealth.CheckResponse, error) {
	if h.shuttingDown.Load() {
		return &grpchealth.CheckResponse{Status: grpchealth.StatusNotServing}, nil
	}
	if h.probe != nil {
		if err := h.probe(ctx); err != nil {
			return &grpchealth.CheckResponse{Status: grpchealth.StatusNotServing}, nil
		}
	}
	return &grpchealth.CheckResponse{Status: grpchealth.StatusServing}, nil
}

// healthzHandler is the plain-HTTP counterpart of Check, for any prober that
// speaks neither Connect nor gRPC, only "GET a URL and look at the status
// code". docker-compose.prod.yml wires a healthcheck against it, and both
// platform clients wait on that healthcheck (`condition: service_healthy`)
// rather than merely on the container having started.
//
// It stops answering SERVING shutdownDrainDelay before the listener actually
// closes, not the instant shutdown begins: main flips the checker to
// NOT_SERVING, then pauses for that delay before calling srv.Shutdown, so a
// prober polling this endpoint has a real chance to observe the 503 instead
// of only ever seeing a refused connection once the listener is gone.
func (h *healthChecker) healthzHandler(w http.ResponseWriter, r *http.Request) {
	resp, _ := h.Check(r.Context(), &grpchealth.CheckRequest{})
	if resp.Status != grpchealth.StatusServing {
		http.Error(w, "not serving", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func main() {
	// Checked before anything else is wired: the probe is a short-lived
	// process that must not open the database, run migrations or bind a
	// listener. os.Exit here skips no deferred call, because none is
	// registered yet — runHealthProbe owns its own.
	if len(os.Args) > 1 && os.Args[1] == healthProbeArg {
		os.Exit(runHealthProbe())
	}

	// Environment variables and logger
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	defer log.Sync()
	config.SetEnv()

	// Database
	db.InitDB()
	defer db.CloseDB()
	db.EnsureLatestVersion()

	log.Z.Info("starting GinBot with Connect.", zap.String("host", config.Options.GRPC.Host), zap.String("port", config.Options.GRPC.Port))

	lis, err := net.Listen("tcp", net.JoinHostPort(config.Options.GRPC.Host, config.Options.GRPC.Port))
	if err != nil {
		log.Z.Fatal("failed to listen.", zap.Error(err))
	}

	// Order matters, outermost first:
	//
	//   - recovery, so a panic anywhere below it fails one call instead of the
	//     process;
	//   - validation, so a malformed request is rejected before it costs the
	//     database round trip that resolving a caller needs;
	//   - clearance, which resolves the caller and enforces the per-RPC minimum;
	//   - origin, innermost, because it deliberately does nothing unless
	//     clearance put a caller in the context. Public methods resolve nobody,
	//     and their position in the chain is not what stops them writing rows —
	//     the caller check inside the origin interceptor is.
	//
	// Unlike the grpc-go server this replaced, there is no separate stream
	// chain: a connect.Interceptor implements WrapUnary, WrapStreamingClient and
	// WrapStreamingHandler together, and connect.WithInterceptors installs the
	// same chain, in the same order, on every RPC shape a handler mounts —
	// which is what puts ClearanceInterceptor on
	// ReverseService/OpenClientActionStream for the first time.
	//
	// OriginInterceptor runs on WrapStreamingHandler too, now that
	// TriggerService/GetFile is a streaming RPC whose visibility check needs a
	// call's own origin. It is a no-op in practice for
	// ReverseService/OpenClientActionStream, since that stream carries no
	// origin headers at all — see the comment on OriginInterceptor for why that
	// is safe rather than something that would start writing destination rows
	// for control channels.
	// The baseline message cap is set explicitly and is NOT optional.
	//
	// Connect has no default: connect.WithReadMaxBytes is stored as an int that
	// the envelope reader only consults when it is above zero, so a handler
	// mounted without one accepts a message of any size and buffers it before
	// any interceptor runs. The grpc-go server this replaces set
	// MaxRecvMsgSize/MaxSendMsgSize connection-wide, so dropping the option here
	// would have silently removed a bound that used to cover every service —
	// including the three deliberately public procedures, where no clearance
	// interceptor runs and an unauthenticated peer would be the one choosing how
	// much memory to allocate.
	handlerOpts := []connect.HandlerOption{
		connect.WithReadMaxBytes(baselineMessageBytes),
		connect.WithSendMaxBytes(baselineMessageBytes),
		connect.WithInterceptors(
			interceptor.RecoverInterceptor{},
			connectvalidate.NewInterceptor(),
			interceptor.NewClearanceInterceptor(interceptor.DefaultRequirements(), db.GetUserByPlatformUID),
			interceptor.NewOriginInterceptor(db.GetOrCreateDestinationByMeta),
		),
	}

	// Trigger media needs somewhere to write blobs before TriggerServer is
	// constructed: NewTriggerServer reads the package-level store via
	// storage.Default(), which is nil until this call.
	if err := storage.Init(config.Options.Storage.Path); err != nil {
		log.Z.Fatal("failed to initialize storage.", zap.Error(err))
	}

	service.InitServices(db.Ping)

	// Mounted from service.Handlers rather than nine mux.Handle calls here, so
	// the set that is served and the set reflection, health and the
	// authorization coverage test are told about is literally one list. See the
	// comment on service.Handlers for why that is a security property and not
	// housekeeping. DiscordService is deliberately absent from it.
	mux := http.NewServeMux()
	mounts := service.Handlers(handlerOpts)
	serviceNames := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		mux.Handle(mount.Path, mount.Handler)
		serviceNames = append(serviceNames, mount.Name)
	}

	checker := newHealthChecker(db.Ping)
	// "GET /healthz", not "/healthz": a liveness probe is a read, and Go 1.22+
	// method patterns make anything else a 405 for free.
	mux.HandleFunc("GET /healthz", checker.healthzHandler)
	mux.Handle(grpchealth.NewHandler(checker))

	if config.AppEnvironment == enum.DEVELOPMENT {
		// Reflection lists both v1 and v1alpha: grpcurl asks for v1alpha, so
		// registering only v1 would silently break it.
		reflector := grpcreflect.NewStaticReflector(serviceNames...)
		mux.Handle(grpcreflect.NewHandlerV1(reflector))
		mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	}

	// h2s is registered with both transports below: h2c.NewHandler for
	// plaintext, and http2.ConfigureServer for TLS. One instance means
	// maxConcurrentStreams is enforced identically either way, rather than the
	// TLS path silently falling back to Go's own internal HTTP/2 defaults.
	h2s := &http2.Server{
		MaxConcurrentStreams: maxConcurrentStreams,
		IdleTimeout:          idleTimeout,
	}

	srv := &http.Server{
		Handler:           h2c.NewHandler(mux, h2s),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}

	if config.Options.GRPC.TLS {
		srv.TLSConfig = auth.LoadServerTLSConfig(config.Options.GRPC.CertsPath)
		// ConfigureServer is what adds "h2" to TLSConfig.NextProtos and wires
		// h2s in as the HTTP/2 server used for ALPN-negotiated connections.
		// Setting NextProtos by hand instead would duplicate what this already
		// does and risks disagreeing with it.
		if err := http2.ConfigureServer(srv, h2s); err != nil {
			log.Z.Fatal("failed to configure HTTP/2.", zap.Error(err))
		}
	}

	// Cancelled on SIGINT or SIGTERM. Serve is run in a goroutine so the signal
	// can be acted on: without this, main blocked in Serve until log.Z.Fatal
	// called os.Exit, which skips every deferred call — so log.Sync, db.CloseDB
	// and the cron cancellation below could never actually run.
	shutdownCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// Parallel cron jobs. Cron jobs run INLINE on this goroutine and issue
	// database queries, so the loop has to be finished before db.CloseDB and
	// log.Sync run — otherwise a job mid-query hits a closed pool.
	//
	// The two defers below are registered in this order on purpose, because
	// defers run LIFO:
	//
	//   - stopSignals runs FIRST. It cancels shutdownCtx, which is what makes
	//     RunCronJobs return at all. Without it the serveErr branch, where no
	//     signal ever arrived, would wait below forever.
	//   - cronWait.Wait runs SECOND, and both run before the db.CloseDB and
	//     log.Sync defers registered at the top of main.
	var cronWait sync.WaitGroup
	cronWait.Add(1)
	defer cronWait.Wait()
	defer stopSignals()

	go func() {
		defer cronWait.Done()
		cron.RunCronJobs(shutdownCtx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		if config.Options.GRPC.TLS {
			// Passing "" for both files is correct: srv.TLSConfig.Certificates is
			// already populated by auth.LoadServerTLSConfig above.
			serveErr <- srv.ServeTLS(lis, "", "")
			return
		}
		serveErr <- srv.Serve(lis)
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Z.Error("HTTP server stopped.", zap.Error(err))
		}
	case <-shutdownCtx.Done():
		log.Z.Info("shutdown signal received, draining connections.")
	}

	// Both branches, not just the signal one. Serve returning an error does not
	// unwind the handlers already dispatched, so skipping the drain there let an
	// in-flight unary handler reach the pool that the db.CloseDB defer is about
	// to close — the same failure the cron WaitGroup exists to prevent, one
	// branch over. Serve has returned either way by this point, so draining
	// costs nothing.
	//
	// ReverseServer.Shutdown runs strictly before srv.Shutdown. http.Server.Shutdown
	// waits for in-flight handlers to return but does NOT cancel their request
	// contexts on its own — a reverse-stream handler is parked in a select that
	// only ends on ctx.Done(), s.done closing, or a client message that may never
	// come, so without this it would never return on its own and Shutdown would
	// wait out its whole timeout before forcing the listener closed anyway.
	// ReverseServer.Shutdown closes s.done first, so Shutdown has something to
	// wait for that actually finishes. That ordering is unaffected by the
	// shutdownDrainDelay pause below: the streams are already released by the
	// time it starts, so the delay only holds the listener open a little
	// longer, it does not change what has to be released before srv.Shutdown
	// can succeed. It is also why a stream opened DURING the drain window
	// returns immediately rather than parking — s.done is already closed.
	//
	// One consequence worth knowing rather than discovering: a platform client
	// reads that immediate clean return as a stream that was established and
	// dropped, which resets its reconnect backoff to the minimum, so each
	// client reattaches and is released again a handful of times across the
	// drain window. It is bounded by the window and each attempt is refused
	// straight away, so it costs a few registry round trips and nothing else —
	// but it does mean the drain window quiets probers, not reverse-stream
	// clients.
	service.ReverseServer.Shutdown()
	checker.shutdown()

	// See shutdownDrainDelay's doc comment: this is what turns the NOT_SERVING
	// window from something only this process's memory knows about into
	// something a prober outside it can actually observe before the listener
	// closes.
	//
	// Guarded on a signal having arrived rather than on which select arm won.
	// Where Serve returned on its own with no signal, the listener is already
	// gone, there is nothing left for a prober to connect to, and the delay
	// would be pure dead time before teardown. Where a signal did arrive the
	// delay is taken, including in the case where one landed while Serve was
	// failing — that is deliberate: an operator who asked for a shutdown gets
	// the observable NOT_SERVING window either way.
	//
	// Interruptible, and that is not a nicety. signal.NotifyContext keeps
	// SIGINT and SIGTERM trapped until stopSignals runs, which is after this,
	// so an uninterruptible sleep here would swallow a second Ctrl-C entirely
	// and leave SIGKILL as the operator's only way out of a five-second wait
	// they have already asked twice to skip. secondSignal is registered before
	// the wait for the same reason the outer NotifyContext is: a signal that
	// arrives between the two would otherwise be lost.
	if shutdownCtx.Err() != nil {
		secondSignal := make(chan os.Signal, 1)
		signal.Notify(secondSignal, os.Interrupt, syscall.SIGTERM)

		select {
		case <-time.After(shutdownDrainDelay):
		case <-secondSignal:
			log.Z.Info("second shutdown signal received, skipping the health drain.")
		}

		signal.Stop(secondSignal)
	}

	shutdownTimeoutCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownTimeoutCtx); err != nil {
		log.Z.Warn("graceful shutdown did not complete in time, closing forcibly.", zap.Error(err))
		_ = srv.Close()
	}

	log.Z.Info("gracefully shutting down.")
}
