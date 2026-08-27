package main

import (
	"context"
	"errors"
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

	// baselineMessageBytes bounds a single message on every service that has no
	// reason to carry a file. Connect applies no cap of its own, so this is the
	// only thing standing between a public procedure and an
	// attacker-chosen allocation; see the comment where it is applied.
	//
	// 4 MiB is grpc-go's own default receive cap, which is what this server ran
	// under before the port for every service including TriggerService, so
	// nothing that worked then is refused now.
	baselineMessageBytes = 4 << 20

	// shutdownTimeout bounds how long graceful shutdown waits for in-flight
	// requests — unary calls draining, reverse streams released by
	// ReverseServer.Shutdown() below — to finish before the listener is forced
	// closed.
	shutdownTimeout = 10 * time.Second
)

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
// code".
//
// Nothing consumes it yet: docker-compose.prod.yml declares a healthcheck for
// Postgres only, so wiring one for this service — and having both clients wait
// on it rather than merely on the container starting — is still outstanding.
//
// Note it stops answering SERVING only microseconds before the listener closes,
// because shutdown flips the checker and then immediately drains. A prober
// therefore sees a refused connection rather than a 503. Draining for a beat
// after the flip is what would make the NOT_SERVING window observable, and is
// worth doing at the same time as the compose healthcheck.
func (h *healthChecker) healthzHandler(w http.ResponseWriter, r *http.Request) {
	resp, _ := h.Check(r.Context(), &grpchealth.CheckRequest{})
	if resp.Status != grpchealth.StatusServing {
		http.Error(w, "not serving", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func main() {
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
	// OriginInterceptor is the exception and deliberately so: its
	// WrapStreamingHandler is a no-op, because a reverse stream is not scoped to
	// one guild or channel to bootstrap.
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

	// TriggerService alone is raised above that baseline: GetFile returns a
	// file's bytes inline in one unary response, up to storage.MaxFileBytes.
	// Later options win, so appending here overrides the baseline for this
	// service and leaves every other one at it.
	triggerHandlerOpts := append(
		append([]connect.HandlerOption{}, handlerOpts...),
		connect.WithReadMaxBytes(config.MaxGRPCMessageBytes),
		connect.WithSendMaxBytes(config.MaxGRPCMessageBytes),
	)

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
	mounts := service.Handlers(handlerOpts, triggerHandlerOpts)
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
	// wait for that actually finishes.
	service.ReverseServer.Shutdown()
	checker.shutdown()

	shutdownTimeoutCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownTimeoutCtx); err != nil {
		log.Z.Warn("graceful shutdown did not complete in time, closing forcibly.", zap.Error(err))
		_ = srv.Close()
	}

	log.Z.Info("gracefully shutting down.")
}
