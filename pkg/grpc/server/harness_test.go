package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
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
)

// Shared harness: a real Connect server over httptest with HTTP/2 and the same
// interceptor chain cmd/ginbot-server installs. The resolvers are in-memory
// fakes, so no test in this package needs Postgres.

// directory is an in-memory interceptor.CallerResolver.
type directory struct {
	mu    sync.Mutex
	users map[string]*model.User
	calls int
}

func newDirectory() *directory {
	return &directory{users: make(map[string]*model.User)}
}

// directoryKey scopes a uid to its platform: the same uid may mean two people.
func directoryKey(platform pb.Platform, platformUID string) string {
	return platform.String() + "\x00" + platformUID
}

// add returns the directory, for chaining.
func (d *directory) add(platform pb.Platform, platformUID string, user *model.User) *directory {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.users[directoryKey(platform, platformUID)] = user
	return d
}

// resolve matches interceptor.CallerResolver.
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

func (d *directory) resolveCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// testUser needs a UUID id, since GetUserReq validates it as one.
func testUser(id string, clearance pb.Clearance) *model.User {
	return &model.User{
		ID:        id,
		Username:  "harness-user",
		Clearance: int32(clearance),
	}
}

// originLog is an in-memory interceptor.OriginResolver that records rather than writes.
type originLog struct {
	mu           sync.Mutex
	destinations []*pb.ReminderDestination
}

func newOriginLog() *originLog {
	return &originLog{}
}

// resolve matches interceptor.OriginResolver.
func (o *originLog) resolve(_ context.Context, destination *pb.ReminderDestination) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.destinations = append(o.destinations, destination)
	return int64(len(o.destinations)), nil
}

// harnessBaselineMessageBytes mirrors cmd/ginbot-server's, which is in package main.
const harnessBaselineMessageBytes = 4 << 20

type harnessConfig struct {
	requirements  interceptor.Requirements
	resolve       interceptor.CallerResolver
	resolveOrigin interceptor.OriginResolver
	triggerServer *TriggerServer
	repostServer  *RepostServer
}

type harnessOption func(*harnessConfig)

// withResolver replaces the caller resolver, e.g. with db.GetUserByPlatformUID.
func withResolver(resolve interceptor.CallerResolver) harnessOption {
	return func(cfg *harnessConfig) { cfg.resolve = resolve }
}

func withDirectory(dir *directory) harnessOption {
	return withResolver(dir.resolve)
}

// withTriggerServer substitutes a TriggerServer not bound to the real CDN allow-list.
func withTriggerServer(server *TriggerServer) harnessOption {
	return func(cfg *harnessConfig) { cfg.triggerServer = server }
}

// withRepostServer substitutes a RepostServer not bound to the real CDN allow-list.
func withRepostServer(server *RepostServer) harnessOption {
	return func(cfg *harnessConfig) { cfg.repostServer = server }
}

// defaultTestRepostServer avoids config.Options, which is nil throughout this package.
func defaultTestRepostServer() *RepostServer {
	return newRepostServer(
		storage.NewFetcher(nil, storage.DefaultAllowedHosts(), storage.MaxFileBytes),
		fingerprint.NewHasher(fingerprint.DefaultGuards(), ""),
		urlnorm.New(nil),
		repost.DefaultTiers(),
	)
}

// harnessIdentityKey and harnessOriginKey are read back into headers by identityInterceptor.
type harnessIdentityKey struct{}
type harnessOriginKey struct{}

type harnessIdentity struct {
	platform    pb.Platform
	platformUID string
}

func callerCtx(platform pb.Platform, platformUID string) context.Context {
	return context.WithValue(context.Background(), harnessIdentityKey{}, harnessIdentity{platform, platformUID})
}

func anonymousCtx() context.Context {
	return context.Background()
}

// originCtx must be given a context callerCtx already produced.
func originCtx(ctx context.Context, origin callermeta.Origin) context.Context {
	return context.WithValue(ctx, harnessOriginKey{}, origin)
}

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

// identityInterceptor stands in for pkg/discord and pkg/matrix, on every client here.
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

// WrapStreamingHandler is a no-op: this is only ever installed client-side.
func (identityInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// unary adapts a generated Connect client method to a plain pb-in/pb-out call.
func unary[Req, Resp any](ctx context.Context, call func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error), req *Req) (*Resp, error) {
	resp, err := call(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

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

// GetFile drains the stream; a rejection before the first chunk is (nil, nil, err).
func (a triggerClient) GetFile(ctx context.Context, req *pb.GetFileReq) (*pb.GetFileMeta, []byte, error) {
	stream, err := a.c.GetFile(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = stream.Close() }()

	var meta *pb.GetFileMeta
	var content []byte
	for stream.Receive() {
		chunk := stream.Msg()
		switch {
		case chunk.HasMeta():
			meta = chunk.GetMeta()
		case chunk.HasContent():
			content = append(content, chunk.GetContent()...)
		}
	}

	return meta, content, stream.Err()
}

// drainGetFileChunks returns the raw chunk sequence rather than the assembled result.
func drainGetFileChunks(ctx context.Context, c ginbotv1connect.TriggerServiceClient, req *pb.GetFileReq) ([]*pb.GetFileChunk, error) {
	stream, err := c.GetFile(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()

	var chunks []*pb.GetFileChunk
	for stream.Receive() {
		// Receive allocates a fresh message each call, so retaining the pointer is safe.
		chunks = append(chunks, stream.Msg())
	}

	return chunks, stream.Err()
}

type repostClient struct {
	c ginbotv1connect.RepostServiceClient
}

func (a repostClient) CheckRepost(ctx context.Context, req *pb.CheckRepostReq) (*pb.CheckRepostResp, error) {
	return unary(ctx, a.c.CheckRepost, req)
}

type harness struct {
	Server *httptest.Server

	// reverseServer is exposed for its white-box surface.
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

// newHarness tears everything down through t.Cleanup.
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

	// Order matches cmd/ginbot-server. The caps are mirrored too, since Connect
	// applies none of its own without WithReadMaxBytes.
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
	mux.Handle(ginbotv1connect.NewTriggerServiceHandler(triggerServer, handlerOpts...))

	repostServer := cfg.repostServer
	if repostServer == nil {
		// NOT NewRepostServer(): it reads config.Options, which is nil here.
		repostServer = defaultTestRepostServer()
	}
	mux.Handle(ginbotv1connect.NewRepostServiceHandler(repostServer, handlerOpts...))

	// A real h2 connection, which the bidi stream needs; HTTP/1.1 cannot carry it.
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	httpClient := srv.Client()
	// srv.Close waits for connections to go idle, so a cancelled stream's pooled
	// connection would hang it. Registered second so LIFO runs it first.
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

func requireCode(t *testing.T, err error, want connect.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error with code %v, got nil", want)
	}

	got := connect.CodeOf(err)
	if got != want {
		t.Fatalf("code = %v, want %v (error: %v)", got, want, err)
	}
}

// requireNotCode tolerates a nil err.
func requireNotCode(t *testing.T, err error, unwanted ...connect.Code) {
	t.Helper()

	if err == nil {
		return
	}

	got := connect.CodeOf(err)
	for _, code := range unwanted {
		if got == code {
			t.Fatalf("code = %v, want anything else (error: %v)", got, err)
		}
	}
}
