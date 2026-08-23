package server

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/repost"
	"github.com/lasikuu/GinBot/pkg/repost/fingerprint"
	"github.com/lasikuu/GinBot/pkg/repost/urlnorm"
	"github.com/lasikuu/GinBot/pkg/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// This file is the shared harness for the pkg/grpc/server tests. It boots a
// real gRPC server over an in-memory listener with the same interceptor chain
// cmd/ginbot-server installs, in the same order — recovery, validation,
// clearance, origin on the unary path, and recovery plus validation on the
// stream path. Handler tests therefore exercise caller resolution, clearance
// and origin bootstrap the way a deployed server does, including the paths
// where the call never reaches the handler at all.
//
// The one difference from production is what the interceptors are given rather
// than which are installed: both the caller resolver and the origin resolver
// are injected, and default to in-memory fakes. That keeps the harness free of
// any database dependency, so reminder, trigger and repost tests can be built
// on it without Postgres. Integration tests pass db.GetUserByPlatformUID
// instead, which satisfies interceptor.CallerResolver as it stands.

// bufSize is the in-memory listener's buffer. Requests in this suite are tiny;
// this is just grpc's own example value.
const bufSize = 1024 * 1024

// directory is an in-memory interceptor.CallerResolver.
type directory struct {
	mu    sync.Mutex
	users map[string]*model.User
	calls int
}

func newDirectory() *directory {
	return &directory{users: make(map[string]*model.User)}
}

// directoryKey scopes a platform uid to its platform, because the same uid may
// exist on two platforms and mean two different people.
func directoryKey(platform pb.Platform, platformUID string) string {
	return platform.String() + "\x00" + platformUID
}

// add registers a platform identity. It returns the directory so several
// identities can be declared in one expression.
func (d *directory) add(platform pb.Platform, platformUID string, user *model.User) *directory {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.users[directoryKey(platform, platformUID)] = user
	return d
}

// resolve matches interceptor.CallerResolver. An unknown identity reports
// db.ErrNotFound, which is what the production resolver returns.
func (d *directory) resolve(_ context.Context, platform pb.Platform, platformUID string) (*model.User, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls++

	user, ok := d.users[directoryKey(platform, platformUID)]
	if !ok {
		return nil, db.ErrNotFound
	}

	return user, nil
}

// resolveCount reports how often the interceptor asked who the caller was.
func (d *directory) resolveCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// testUser builds a user_account row for the directory. The id must be a UUID
// because GetUserReq validates it as one.
func testUser(id string, clearance pb.Clearance) *model.User {
	return &model.User{
		ID:        id,
		Username:  "harness-user",
		Clearance: int32(clearance),
	}
}

// originLog is an in-memory interceptor.OriginResolver, and the harness
// default: it records the bootstrap writes the chain would have made instead
// of writing rows, so a test that is not about origins needs no database.
type originLog struct {
	mu           sync.Mutex
	destinations []*pb.ReminderDestination
}

func newOriginLog() *originLog {
	return &originLog{}
}

// resolve matches interceptor.OriginResolver. The id is the row count, which is
// enough for a caller that only checks the error.
func (o *originLog) resolve(_ context.Context, destination *pb.ReminderDestination) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.destinations = append(o.destinations, destination)
	return int64(len(o.destinations)), nil
}

// harnessConfig is what the options mutate.
type harnessConfig struct {
	requirements  interceptor.Requirements
	resolve       interceptor.CallerResolver
	resolveOrigin interceptor.OriginResolver
	triggerServer *TriggerServer
	repostServer  *RepostServer
}

type harnessOption func(*harnessConfig)

// withResolver replaces the caller resolver. Pass db.GetUserByPlatformUID to
// run against a real database.
func withResolver(resolve interceptor.CallerResolver) harnessOption {
	return func(cfg *harnessConfig) { cfg.resolve = resolve }
}

// withDirectory is the common case: resolve callers from an in-memory map.
func withDirectory(dir *directory) harnessOption {
	return withResolver(dir.resolve)
}

// withTriggerServer replaces the default TriggerServer, e.g. with one built
// over newTriggerServer(fetcher, blobs) so the media fetch/store/dedupe path
// can be exercised against an httptest server instead of the real,
// allow-listed CDN hosts.
func withTriggerServer(server *TriggerServer) harnessOption {
	return func(cfg *harnessConfig) { cfg.triggerServer = server }
}

// withRepostServer replaces the default RepostServer, e.g. with one built over
// newRepostServer(fetcher, hasher, norm, tiers) so the attachment fetch path
// can be exercised against an httptest server instead of the real,
// allow-listed CDN hosts, exactly as withTriggerServer does for TriggerServer.
func withRepostServer(server *RepostServer) harnessOption {
	return func(cfg *harnessConfig) { cfg.repostServer = server }
}

// defaultTestRepostServer builds a RepostServer with the same defaults
// NewRepostServer would apply given an unconfigured environment
// (repost.DefaultTiers, fingerprint.DefaultGuards, the platform CDN
// allow-list, no ffmpeg path), but without touching config.Options. It exists
// purely so newHarness has something safe to fall back to; tests that care
// about the fetch/hash path inject their own via withRepostServer instead.
func defaultTestRepostServer() *RepostServer {
	return newRepostServer(
		storage.NewFetcher(nil, storage.DefaultAllowedHosts(), storage.MaxFileBytes),
		fingerprint.NewHasher(fingerprint.DefaultGuards(), ""),
		urlnorm.New(nil),
		repost.DefaultTiers(),
	)
}

// harness is a running in-process server plus a client for every service.
type harness struct {
	Conn *grpc.ClientConn

	User          pb.UserServiceClient
	Utility       pb.UtilityServiceClient
	Instance      pb.InstanceServiceClient
	Reminder      pb.ReminderServiceClient
	Analytics     pb.AnalyticsServiceClient
	Entertainment pb.EntertainmentServiceClient
	Reverse       pb.ReverseServiceClient
	Trigger       pb.TriggerServiceClient
	Repost        pb.RepostServiceClient
}

// newHarness starts a server and connects a client to it. Everything is torn
// down through t.Cleanup.
func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()

	cfg := harnessConfig{
		requirements:  interceptor.DefaultRequirements(),
		resolve:       newDirectory().resolve,
		resolveOrigin: newOriginLog().resolve,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	validateUnary, err := interceptor.NewValidationUnaryInterceptor()
	if err != nil {
		t.Fatalf("NewValidationUnaryInterceptor: %v", err)
	}

	validateStream, err := interceptor.NewValidationStreamInterceptor()
	if err != nil {
		t.Fatalf("NewValidationStreamInterceptor: %v", err)
	}

	// Order matches cmd/ginbot-server exactly: recovery outermost so it covers
	// the interceptors themselves, then validation so a malformed request is
	// rejected before the caller lookup clearance needs, then clearance, then
	// origin innermost — which does nothing unless clearance resolved a caller.
	//
	// Recovery earns its place here twice over: tests that inject a resolver
	// leave pkg/db's pool nil on purpose, so a handler that reaches the database
	// dereferences it, and without recovery that panic takes the whole test
	// binary down instead of failing one call.
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.RecoverUnaryInterceptor,
			validateUnary,
			interceptor.NewClearanceUnaryInterceptor(cfg.requirements, cfg.resolve),
			interceptor.NewOriginUnaryInterceptor(cfg.resolveOrigin),
		),
		grpc.ChainStreamInterceptor(
			interceptor.RecoverStreamInterceptor,
			validateStream,
		),
	)

	pb.RegisterUserServiceServer(grpcServer, NewUserServer())
	pb.RegisterUtilityServiceServer(grpcServer, NewUtilityServer())
	pb.RegisterInstanceServiceServer(grpcServer, NewInstanceServer())
	pb.RegisterReminderServiceServer(grpcServer, NewReminderServer())
	pb.RegisterAnalyticsServiceServer(grpcServer, NewAnalyticsServer())
	pb.RegisterEntertainmentServiceServer(grpcServer, NewEntertainmentServer())
	pb.RegisterReverseServiceServer(grpcServer, NewReverseServer())

	triggerServer := cfg.triggerServer
	if triggerServer == nil {
		triggerServer = NewTriggerServer()
	}
	pb.RegisterTriggerServiceServer(grpcServer, triggerServer)

	repostServer := cfg.repostServer
	if repostServer == nil {
		// NOT NewRepostServer(): it reads config.Options directly, which is
		// nil in every test in this package (this harness deliberately stays
		// config-free, matching reverse_test.go's TestMain and the same
		// reasoning documented on triggerServer above), so calling it here
		// would nil-deref before a single test in the whole package could
		// run. newRepostServer with hardcoded, config-independent defaults is
		// the same seam trigger_media_integration_test.go uses to bypass
		// NewTriggerServer's package-level storage dependency.
		repostServer = defaultTestRepostServer()
	}
	pb.RegisterRepostServiceServer(grpcServer, repostServer)

	listener := bufconn.Listen(bufSize)

	served := make(chan struct{})
	go func() {
		defer close(served)
		// Serve returns ErrServerStopped on Stop, which is the normal path here.
		_ = grpcServer.Serve(listener)
	}()

	// passthrough is required: grpc.NewClient defaults to the DNS resolver, and
	// "bufnet" is not a hostname.
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close client connection: %v", err)
		}
		// Stop rather than GracefulStop: a test that leaves a stream open must
		// not hang the suite.
		grpcServer.Stop()
		<-served
	})

	return &harness{
		Conn:          conn,
		User:          pb.NewUserServiceClient(conn),
		Utility:       pb.NewUtilityServiceClient(conn),
		Instance:      pb.NewInstanceServiceClient(conn),
		Reminder:      pb.NewReminderServiceClient(conn),
		Analytics:     pb.NewAnalyticsServiceClient(conn),
		Entertainment: pb.NewEntertainmentServiceClient(conn),
		Reverse:       pb.NewReverseServiceClient(conn),
		Trigger:       pb.NewTriggerServiceClient(conn),
		Repost:        pb.NewRepostServiceClient(conn),
	}
}

// callerCtx attaches caller identity exactly as a platform client does.
func callerCtx(platform pb.Platform, platformUID string) context.Context {
	return callermeta.NewOutgoingContext(context.Background(), platform, platformUID)
}

// anonymousCtx carries no caller identity at all.
func anonymousCtx() context.Context {
	return context.Background()
}

// requireCode asserts the exact gRPC code carried by err.
func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error with code %v, got nil", want)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	if st.Code() != want {
		t.Fatalf("code = %v, want %v (message: %q)", st.Code(), want, st.Message())
	}
}

// requireNotCode asserts that err — which may be nil — is not a particular
// rejection. It exists for the calls that get past the interceptor chain and
// then fail further in, at the database, which most of these tests do not have.
func requireNotCode(t *testing.T, err error, unwanted ...codes.Code) {
	t.Helper()

	got := status.Code(err)
	for _, code := range unwanted {
		if got == code {
			t.Fatalf("code = %v, want anything else (message: %q)", got, status.Convert(err).Message())
		}
	}
}
