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

	// Embeds the IANA zone database so SetTimezone works on hosts that ship none.
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
)

const (
	maxConcurrentStreams = 250

	// ReadTimeout is left unset: a reverse stream is one long-lived request.
	readHeaderTimeout = 5 * time.Second

	// Counts only while no stream is open, so a parked stream does not age out.
	idleTimeout = 2 * time.Minute

	// Caps every message on every service; Connect applies no cap of its own.
	baselineMessageBytes = 4 << 20

	shutdownTimeout = 10 * time.Second

	// Lets a prober observe the 503 first; with shutdownTimeout it must stay
	// inside the compose stop_grace_period.
	shutdownDrainDelay = 5 * time.Second

	// Under mTLS the listener demands a client certificate no shell HTTP
	// client can present, so the probe is this binary.
	healthProbeArg = "-healthcheck"

	// Loopback, not GRPC.Host, which is a bind address and the production
	// wildcard. The name matches a DNS SAN in cert/server-ext.conf.
	healthProbeHost = "localhost"

	// Under the compose healthcheck timeout, so a slow probe logs its reason.
	healthProbeTimeout = 3 * time.Second
)

func healthProbeURL(tlsEnabled bool, port string) string {
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}

	return scheme + "://" + net.JoinHostPort(healthProbeHost, port) + "/healthz"
}

// probeHealth returns nil only on a 200. tlsConf is nil for a plaintext server.
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
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: %s", url, resp.Status)
	}

	return nil
}

// runHealthProbe returns the process exit code: 0 is healthy.
func runHealthProbe() int {
	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	defer log.Sync()
	config.SetEnv()

	var tlsConf *tls.Config
	if config.Options.GRPC.TLS {
		conf, err := auth.ClientTLSConfig(config.Options.GRPC.CertsPath)
		if err != nil {
			// Not log.Z.Fatal: its os.Exit would skip the log.Sync above.
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

// healthChecker reports NOT_SERVING once shutdown begins, whatever the probe.
type healthChecker struct {
	probe        func(ctx context.Context) error
	shuttingDown atomic.Bool
}

func newHealthChecker(probe func(ctx context.Context) error) *healthChecker {
	return &healthChecker{probe: probe}
}

func (h *healthChecker) shutdown() {
	h.shuttingDown.Store(true)
}

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

func (h *healthChecker) healthzHandler(w http.ResponseWriter, r *http.Request) {
	resp, _ := h.Check(r.Context(), &grpchealth.CheckRequest{})
	if resp.Status != grpchealth.StatusServing {
		http.Error(w, "not serving", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func main() {
	// Before any wiring: the probe must not open the database or bind a listener.
	if len(os.Args) > 1 && os.Args[1] == healthProbeArg {
		os.Exit(runHealthProbe())
	}

	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	defer log.Sync()
	config.SetEnv()

	db.InitDB()
	defer db.CloseDB()
	db.EnsureLatestVersion()

	log.Z.Info("starting GinBot with Connect.", zap.String("host", config.Options.GRPC.Host), zap.String("port", config.Options.GRPC.Port))

	lis, err := net.Listen("tcp", net.JoinHostPort(config.Options.GRPC.Host, config.Options.GRPC.Port))
	if err != nil {
		log.Z.Fatal("failed to listen.", zap.Error(err))
	}

	// Order is load-bearing, outermost first.
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

	// Must precede InitServices: NewTriggerServer reads storage.Default().
	if err := storage.Init(config.Options.Storage.Path); err != nil {
		log.Z.Fatal("failed to initialize storage.", zap.Error(err))
	}

	service.InitServices(db.Ping)

	// The single list: serving, reflection and the coverage test derive from it.
	mux := http.NewServeMux()
	mounts := service.Handlers(handlerOpts)
	serviceNames := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		mux.Handle(mount.Path, mount.Handler)
		serviceNames = append(serviceNames, mount.Name)
	}

	checker := newHealthChecker(db.Ping)
	mux.HandleFunc("GET /healthz", checker.healthzHandler)
	mux.Handle(grpchealth.NewHandler(checker))

	if config.AppEnvironment == enum.DEVELOPMENT {
		// Both v1 and v1alpha: grpcurl asks for v1alpha.
		reflector := grpcreflect.NewStaticReflector(serviceNames...)
		mux.Handle(grpcreflect.NewHandlerV1(reflector))
		mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	}

	// Without UnencryptedHTTP2 a plaintext deployment falls back to HTTP/1.1,
	// where bidi streaming does not work; Go auto-negotiates h2 only over TLS.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		Protocols:         &protocols,
		// The HTTP/2 idle timeout is not set here: net/http derives it from IdleTimeout.
		HTTP2: &http.HTTP2Config{MaxConcurrentStreams: maxConcurrentStreams},
	}

	if config.Options.GRPC.TLS {
		// NextProtos is left unset: ServeTLS appends "h2" itself from srv.Protocols.
		srv.TLSConfig = auth.LoadServerTLSConfig(config.Options.GRPC.CertsPath)
	}

	shutdownCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// LIFO: stopSignals must run before cronWait.Wait, or nothing cancels
	// shutdownCtx and the wait blocks forever.
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
			// Empty filenames: srv.TLSConfig.Certificates is already populated.
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

	// Must precede srv.Shutdown, which waits for handlers without cancelling
	// their contexts, so a parked stream would hold it until timeout.
	service.ReverseServer.Shutdown()
	checker.shutdown()

	// SIGINT and SIGTERM stay trapped until stopSignals, so a plain sleep here
	// would swallow a second one.
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
