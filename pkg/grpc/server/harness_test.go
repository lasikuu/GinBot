package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/repost"
	"github.com/lasikuu/GinBot/pkg/repost/fingerprint"
	"github.com/lasikuu/GinBot/pkg/repost/urlnorm"
	"github.com/lasikuu/GinBot/pkg/storage"
	"google.golang.org/grpc/codes"
)

// This file is the shared harness for the pkg/grpc/server tests. It boots a
// real Connect server over httptest.NewUnstartedServer with HTTP/2 enabled,
// with the same interceptor chain cmd/ginbot-server installs, in the same
// order — recovery, validation, clearance, origin — applied identically to
// every RPC shape a handler mounts, unary or streaming. Handler tests
// therefore exercise caller resolution, clearance, validation and origin
// bootstrap the way a deployed server does, including the paths where the
// call never reaches the handler at all, and including OpenClientActionStream
// for the first time: a connect.Interceptor implements WrapStreamingHandler
// alongside WrapUnary, so there is no separate stream-only chain to fall out
// of sync with the unary one the way grpc-go's did.
//
// The one difference from production is what the interceptors are given
// rather than which are installed: both the caller resolver and the origin
// resolver are injected, and default to in-memory fakes. That keeps the
// harness free of any database dependency, so reminder, trigger and repost
// tests can be built on it without Postgres. Integration tests pass
// db.GetUserByPlatformUID instead, which satisfies interceptor.CallerResolver
// as it stands.
//
// Identity travels as REQUEST HEADERS on this transport, not gRPC metadata.
// A Connect client sets headers per *connect.Request (or per
// StreamingClientConn), not on the context, so callerCtx/originCtx stash
// identity in ordinary context values of their own and identityInterceptor —
// a CLIENT-side connect.Interceptor installed on every client this harness
// builds — translates them into the ginbot-* headers a real platform client
// would set, at the last possible moment before the request leaves the
// process. This mirrors production exactly: the wire contract is still
// pkg/grpc/callermeta's header names, just assembled by test code instead of
// by pkg/discord or pkg/matrix.

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

// harnessBaselineMessageBytes mirrors cmd/ginbot-server's baselineMessageBytes.
// It is restated rather than imported because that constant lives in package
// main; the value's justification lives there, and TestBaselineMessageCapIsSet
// in cmd/ginbot-server is what pins the production side.
const harnessBaselineMessageBytes = 4 << 20

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

// harnessIdentityKey and harnessOriginKey type the context values callerCtx,
// anonymousCtx and originCtx attach. identityInterceptor reads them back and
// turns them into request headers immediately before a call leaves the
// process — see the file-level comment for why headers, not context, is what
// actually crosses the wire.
type harnessIdentityKey struct{}
type harnessOriginKey struct{}

// harnessIdentity is what callerCtx stashes.
type harnessIdentity struct {
	platform    pb.Platform
	platformUID string
}

// callerCtx attaches caller identity exactly as a platform client does.
func callerCtx(platform pb.Platform, platformUID string) context.Context {
	return context.WithValue(context.Background(), harnessIdentityKey{}, harnessIdentity{platform, platformUID})
}

// anonymousCtx carries no caller identity at all.
func anonymousCtx() context.Context {
	return context.Background()
}

// originCtx attaches a call origin to ctx, the harness's equivalent of
// callermeta.NewOutgoingOrigin — which manipulates gRPC metadata and has no
// effect on a Connect client's headers. It must be called on a context
// callerCtx already produced, mirroring NewOutgoingOrigin's own ordering
// requirement (see callermeta_test.go's
// TestOriginIsLostWhenAttachedBeforeIdentity for why that requirement exists
// at all): identityInterceptor reads both keys independently here, so in
// practice the harness does not share NewOutgoingContext's wholesale-replace
// hazard, but every call site still attaches identity first for parity with
// production call sites and with the two client packages this test suite
// otherwise mirrors.
func originCtx(ctx context.Context, origin callermeta.Origin) context.Context {
	return context.WithValue(ctx, harnessOriginKey{}, origin)
}

// stampIdentityHeaders turns whatever callerCtx/originCtx attached to ctx into
// the ginbot-* request headers a real platform client sends.
func stampIdentityHeaders(ctx context.Context, header http.Header) {
	if id, ok := ctx.Value(harnessIdentityKey{}).(harnessIdentity); ok {
		header.Set(callermeta.HeaderPlatformEnum, id.platform.String())
		if id.platformUID != "" {
			header.Set(callermeta.HeaderUserID, id.platformUID)
		}
	}
	if origin, ok := ctx.Value(harnessOriginKey{}).(callermeta.Origin); ok {
		if origin.InstanceUID != "" {
			header.Set(callermeta.HeaderInstanceUID, origin.InstanceUID)
		}
		if origin.DestinationUID != "" {
			header.Set(callermeta.HeaderDestinationUID, origin.DestinationUID)
		}
	}
}

// identityInterceptor is a CLIENT-side connect.Interceptor that stamps
// headers from context values immediately before a request or stream leaves
// the process — the harness's stand-in for pkg/discord and pkg/matrix, which
// do the equivalent through callermeta directly. It is installed on every
// client this harness builds, unary and streaming alike, which is what makes
// h.Reverse.OpenClientActionStream(callerCtx(...)) carry the same identity a
// unary call under the same context would.
type identityInterceptor struct{}

func (identityInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		stampIdentityHeaders(ctx, req.Header())
		return next(ctx, req)
	}
}

func (identityInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		stampIdentityHeaders(ctx, conn.RequestHeader())
		return conn
	}
}

// WrapStreamingHandler is a no-op: identityInterceptor is only ever installed
// as a client option in this package, never as a server one, so a handler
// never sees it.
func (identityInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// unary adapts a generated Connect client method — func(context.Context,
// *connect.Request[Req]) (*connect.Response[Resp], error) — to the plain
// pb-in/pb-out shape every test file in this package already calls
// (h.User.GetUser(ctx, req) etc.), so porting the transport did not require
// rewriting every call site in every _test.go in this directory to wrap and
// unwrap connect.Request/Response by hand.
func unary[Req, Resp any](ctx context.Context, call func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error), req *Req) (*Resp, error) {
	resp, err := call(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// The eight adapters below exist purely to give every test file in this
// package the pb-in/pb-out calling convention it already used against the
// grpc-go client, over the real ginbotv1connect client underneath.

type userClient struct {
	c ginbotv1connect.UserServiceClient
}

func (a userClient) GetUser(ctx context.Context, req *pb.GetUserReq) (*pb.GetUserResp, error) {
	return unary(ctx, a.c.GetUser, req)
}
func (a userClient) Register(ctx context.Context, req *pb.RegisterReq) (*pb.RegisterResp, error) {
	return unary(ctx, a.c.Register, req)
}
func (a userClient) GetCongratulableBirthdays(ctx context.Context, req *pb.GetCongratulableBirthdaysReq) (*pb.GetCongratulableBirthdaysResp, error) {
	return unary(ctx, a.c.GetCongratulableBirthdays, req)
}
func (a userClient) SetLocale(ctx context.Context, req *pb.SetLocaleReq) (*pb.SetLocaleResp, error) {
	return unary(ctx, a.c.SetLocale, req)
}
func (a userClient) SetTimezone(ctx context.Context, req *pb.SetTimezoneReq) (*pb.SetTimezoneResp, error) {
	return unary(ctx, a.c.SetTimezone, req)
}

type utilityClient struct {
	c ginbotv1connect.UtilityServiceClient
}

func (a utilityClient) HealthCheck(ctx context.Context, req *pb.HealthCheckReq) (*pb.HealthCheckResp, error) {
	return unary(ctx, a.c.HealthCheck, req)
}
func (a utilityClient) Ping(ctx context.Context, req *pb.PingReq) (*pb.PingResp, error) {
	return unary(ctx, a.c.Ping, req)
}

type instanceClient struct {
	c ginbotv1connect.InstanceServiceClient
}

func (a instanceClient) GetInstance(ctx context.Context, req *pb.GetInstanceReq) (*pb.GetInstanceResp, error) {
	return unary(ctx, a.c.GetInstance, req)
}
func (a instanceClient) ListInstances(ctx context.Context, req *pb.ListInstancesReq) (*pb.ListInstancesResp, error) {
	return unary(ctx, a.c.ListInstances, req)
}
func (a instanceClient) CreateInstance(ctx context.Context, req *pb.CreateInstanceReq) (*pb.CreateInstanceResp, error) {
	return unary(ctx, a.c.CreateInstance, req)
}
func (a instanceClient) UpdateInstance(ctx context.Context, req *pb.UpdateInstanceReq) (*pb.UpdateInstanceResp, error) {
	return unary(ctx, a.c.UpdateInstance, req)
}
func (a instanceClient) DeleteInstance(ctx context.Context, req *pb.DeleteInstanceReq) (*pb.DeleteInstanceResp, error) {
	return unary(ctx, a.c.DeleteInstance, req)
}

type reminderClient struct {
	c ginbotv1connect.ReminderServiceClient
}

func (a reminderClient) GetReminder(ctx context.Context, req *pb.GetReminderReq) (*pb.GetReminderResp, error) {
	return unary(ctx, a.c.GetReminder, req)
}
func (a reminderClient) ListReminders(ctx context.Context, req *pb.ListRemindersReq) (*pb.ListRemindersResp, error) {
	return unary(ctx, a.c.ListReminders, req)
}
func (a reminderClient) CreateReminder(ctx context.Context, req *pb.CreateReminderReq) (*pb.CreateReminderResp, error) {
	return unary(ctx, a.c.CreateReminder, req)
}
func (a reminderClient) UpdateReminder(ctx context.Context, req *pb.UpdateReminderReq) (*pb.UpdateReminderResp, error) {
	return unary(ctx, a.c.UpdateReminder, req)
}
func (a reminderClient) DeleteReminder(ctx context.Context, req *pb.DeleteReminderReq) (*pb.DeleteReminderResp, error) {
	return unary(ctx, a.c.DeleteReminder, req)
}
func (a reminderClient) ConfirmDelivery(ctx context.Context, req *pb.ConfirmDeliveryReq) (*pb.ConfirmDeliveryResp, error) {
	return unary(ctx, a.c.ConfirmDelivery, req)
}

type analyticsClient struct {
	c ginbotv1connect.AnalyticsServiceClient
}

func (a analyticsClient) CreateActionRecord(ctx context.Context, req *pb.CreateActionRecordReq) (*pb.CreateActionRecordResp, error) {
	return unary(ctx, a.c.CreateActionRecord, req)
}
func (a analyticsClient) ListActionRecords(ctx context.Context, req *pb.ListActionRecordsReq) (*pb.ListActionRecordsResp, error) {
	return unary(ctx, a.c.ListActionRecords, req)
}

type entertainmentClient struct {
	c ginbotv1connect.EntertainmentServiceClient
}

func (a entertainmentClient) GetRandomNumber(ctx context.Context, req *pb.GetRandomNumberReq) (*pb.GetRandomNumberResp, error) {
	return unary(ctx, a.c.GetRandomNumber, req)
}
func (a entertainmentClient) SetBirthday(ctx context.Context, req *pb.SetBirthdayReq) (*pb.SetBirthdayResp, error) {
	return unary(ctx, a.c.SetBirthday, req)
}

type triggerClient struct {
	c ginbotv1connect.TriggerServiceClient
}

func (a triggerClient) TryTrigger(ctx context.Context, req *pb.TryTriggerReq) (*pb.TryTriggerResp, error) {
	return unary(ctx, a.c.TryTrigger, req)
}
func (a triggerClient) ExecTrigger(ctx context.Context, req *pb.ExecTriggerReq) (*pb.TryTriggerResp, error) {
	return unary(ctx, a.c.ExecTrigger, req)
}
func (a triggerClient) GetTrigger(ctx context.Context, req *pb.GetTriggerReq) (*pb.GetTriggerResp, error) {
	return unary(ctx, a.c.GetTrigger, req)
}
func (a triggerClient) ListTriggers(ctx context.Context, req *pb.ListTriggersReq) (*pb.ListTriggersResp, error) {
	return unary(ctx, a.c.ListTriggers, req)
}
func (a triggerClient) CreateTrigger(ctx context.Context, req *pb.CreateTriggerReq) (*pb.CreateTriggerResp, error) {
	return unary(ctx, a.c.CreateTrigger, req)
}
func (a triggerClient) UpdateTrigger(ctx context.Context, req *pb.UpdateTriggerReq) (*pb.UpdateTriggerResp, error) {
	return unary(ctx, a.c.UpdateTrigger, req)
}
func (a triggerClient) DeleteTrigger(ctx context.Context, req *pb.DeleteTriggerReq) (*pb.DeleteTriggerResp, error) {
	return unary(ctx, a.c.DeleteTrigger, req)
}
func (a triggerClient) GetTriggerStats(ctx context.Context, req *pb.GetTriggerStatsReq) (*pb.GetTriggerStatsResp, error) {
	return unary(ctx, a.c.GetTriggerStats, req)
}
func (a triggerClient) GetFile(ctx context.Context, req *pb.GetFileReq) (*pb.GetFileResp, error) {
	return unary(ctx, a.c.GetFile, req)
}

type repostClient struct {
	c ginbotv1connect.RepostServiceClient
}

func (a repostClient) CheckRepost(ctx context.Context, req *pb.CheckRepostReq) (*pb.CheckRepostResp, error) {
	return unary(ctx, a.c.CheckRepost, req)
}

// harness is a running in-process server plus a client for every service.
type harness struct {
	Server *httptest.Server

	// reverseServer is the concrete *ReverseServer the harness mounted,
	// exposed so a test can drive its white-box surface (SendAction,
	// Shutdown, clientCount) alongside the real client below.
	reverseServer *ReverseServer

	User          userClient
	Utility       utilityClient
	Instance      instanceClient
	Reminder      reminderClient
	Analytics     analyticsClient
	Entertainment entertainmentClient
	Reverse       ginbotv1connect.ReverseServiceClient
	Trigger       triggerClient
	Repost        repostClient
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

	// Order matches cmd/ginbot-server exactly: recovery outermost so it
	// covers the interceptors themselves, then validation so a malformed
	// request is rejected before the caller lookup clearance needs, then
	// clearance, then origin innermost — which does nothing unless clearance
	// resolved a caller. connect.WithInterceptors applies this SAME chain to
	// every RPC shape a handler mounts, unary or streaming, which is what
	// puts ClearanceInterceptor and OriginInterceptor on
	// ReverseService/OpenClientActionStream for the first time.
	//
	// Recovery earns its place here twice over: tests that inject a resolver
	// leave pkg/db's pool nil on purpose, so a handler that reaches the
	// database dereferences it, and without recovery that panic takes the
	// whole test binary down instead of failing one call.
	// The message caps are mirrored, not just the interceptors. Connect applies
	// no cap of its own — WithReadMaxBytes is only consulted when it is above
	// zero — so a harness that omitted them would be the one part of the chain
	// where production's bound is absent, and deleting that bound from
	// cmd/ginbot-server would break nothing in this suite.
	handlerOpts := []connect.HandlerOption{
		connect.WithReadMaxBytes(harnessBaselineMessageBytes),
		connect.WithSendMaxBytes(harnessBaselineMessageBytes),
		connect.WithInterceptors(
			interceptor.RecoverInterceptor{},
			connectvalidate.NewInterceptor(),
			interceptor.NewClearanceInterceptor(cfg.requirements, cfg.resolve),
			interceptor.NewOriginInterceptor(cfg.resolveOrigin),
		),
	}

	// TriggerService alone is raised above the baseline, exactly as
	// cmd/ginbot-server does it, because GetFile returns file bytes inline.
	triggerHandlerOpts := append(
		append([]connect.HandlerOption{}, handlerOpts...),
		connect.WithReadMaxBytes(config.MaxGRPCMessageBytes),
		connect.WithSendMaxBytes(config.MaxGRPCMessageBytes),
	)

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewUserServiceHandler(NewUserServer(), handlerOpts...))
	mux.Handle(ginbotv1connect.NewUtilityServiceHandler(NewUtilityServer(nil), handlerOpts...))
	mux.Handle(ginbotv1connect.NewInstanceServiceHandler(NewInstanceServer(), handlerOpts...))
	mux.Handle(ginbotv1connect.NewReminderServiceHandler(NewReminderServer(), handlerOpts...))
	mux.Handle(ginbotv1connect.NewAnalyticsServiceHandler(NewAnalyticsServer(), handlerOpts...))
	mux.Handle(ginbotv1connect.NewEntertainmentServiceHandler(NewEntertainmentServer(), handlerOpts...))

	reverseServer := NewReverseServer()
	mux.Handle(ginbotv1connect.NewReverseServiceHandler(reverseServer, handlerOpts...))

	triggerServer := cfg.triggerServer
	if triggerServer == nil {
		triggerServer = NewTriggerServer()
	}
	mux.Handle(ginbotv1connect.NewTriggerServiceHandler(triggerServer, triggerHandlerOpts...))

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
	mux.Handle(ginbotv1connect.NewRepostServiceHandler(repostServer, handlerOpts...))

	// httptest.NewUnstartedServer + EnableHTTP2 + StartTLS gives a real
	// HTTP/2 connection end to end (ALPN negotiates "h2" over the server's
	// generated test certificate), which is what OpenClientActionStream's
	// bidirectional stream needs — HTTP/1.1 cannot carry it at all. This is
	// the general-purpose harness for every handler test in this package;
	// reverse_h2c_test.go is the SEPARATE, dedicated test for the plaintext
	// h2c configuration cmd/ginbot-server actually ships with
	// GINBOT_GRPC_TLS=false, which this TLS-based harness cannot exercise and
	// is not meant to.
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	httpClient := srv.Client()
	// A test that opened OpenClientActionStream and then only cancelled its
	// OWN request context leaves the underlying HTTP/2 connection itself
	// pooled for reuse — cancelling one stream is not the same as closing the
	// TCP connection it rode on, and http.Transport keeps idle connections
	// alive on purpose. srv.Close() waits for every connection to go
	// inactive, so without this it hangs (eventually failing the whole
	// package on -timeout) rather than the one test that leaked a stream.
	// Registered after srv.Close so LIFO runs it FIRST, closing the
	// now-otherwise-idle connection before srv.Close waits on it.
	t.Cleanup(httpClient.CloseIdleConnections)
	clientOpts := connect.WithInterceptors(identityInterceptor{})

	return &harness{
		Server:        srv,
		reverseServer: reverseServer,
		User:          userClient{ginbotv1connect.NewUserServiceClient(httpClient, srv.URL, clientOpts)},
		Utility:       utilityClient{ginbotv1connect.NewUtilityServiceClient(httpClient, srv.URL, clientOpts)},
		Instance:      instanceClient{ginbotv1connect.NewInstanceServiceClient(httpClient, srv.URL, clientOpts)},
		Reminder:      reminderClient{ginbotv1connect.NewReminderServiceClient(httpClient, srv.URL, clientOpts)},
		Analytics:     analyticsClient{ginbotv1connect.NewAnalyticsServiceClient(httpClient, srv.URL, clientOpts)},
		Entertainment: entertainmentClient{ginbotv1connect.NewEntertainmentServiceClient(httpClient, srv.URL, clientOpts)},
		Reverse:       ginbotv1connect.NewReverseServiceClient(httpClient, srv.URL, clientOpts),
		Trigger:       triggerClient{ginbotv1connect.NewTriggerServiceClient(httpClient, srv.URL, clientOpts)},
		Repost:        repostClient{ginbotv1connect.NewRepostServiceClient(httpClient, srv.URL, clientOpts)},
	}
}

// requireCode asserts the exact Connect code carried by err.
func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error with code %v, got nil", want)
	}

	got := connect.CodeOf(err)
	wantConnect := connect.Code(uint32(want))
	if got != wantConnect {
		t.Fatalf("code = %v, want %v (error: %v)", got, wantConnect, err)
	}
}

// requireNotCode asserts that err — which may be nil — is not a particular
// rejection. It exists for the calls that get past the interceptor chain and
// then fail further in, at the database, which most of these tests do not have.
func requireNotCode(t *testing.T, err error, unwanted ...codes.Code) {
	t.Helper()

	if err == nil {
		return
	}

	got := connect.CodeOf(err)
	for _, code := range unwanted {
		if got == connect.Code(uint32(code)) {
			t.Fatalf("code = %v, want anything else (error: %v)", got, err)
		}
	}
}
