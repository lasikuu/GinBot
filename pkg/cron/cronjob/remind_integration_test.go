//go:build integration

// Integration coverage for Remind's producer seam. Needs a live Postgres, and
// reuses TestMain from orphan_test.go, hence the lazy setup below.

package cronjob

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/grpc/server"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const fixtureReminderCap = 1000

var (
	databaseOnce sync.Once
	databasePool *pgxpool.Pool
)

// requireDatabase adds a second pool: pkg/db exposes no hard delete.
func requireDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseOnce.Do(func() {
		config.LoadEnv()
		log.InitializeLogger(config.AppEnvironment, config.LogLevel)
		config.SetEnv()

		db.InitDB()
		db.EnsureLatestVersion()

		// Built as pkg/db builds it, so a reserved character in the password is
		// escaped rather than corrupting the URI.
		uri := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(config.Options.DB.Username, config.Options.DB.Password),
			Host:   net.JoinHostPort(config.Options.DB.Host, strconv.Itoa(int(config.Options.DB.Port))),
			Path:   config.Options.DB.Name,
		}

		pool, err := pgxpool.New(context.Background(), uri.String())
		if err != nil {
			t.Errorf("open test database pool: %v", err)
			return
		}
		databasePool = pool
	})

	if databasePool == nil {
		t.Fatal("no test database pool; is Postgres up?")
	}

	return databasePool
}

// pushedActions is a real client stream, since ReverseServer's registry and
// sender goroutine are unexported.
type pushedActions struct {
	notifications chan *pb.OpenClientActionStreamResp
	// registered closes once a probe round-trips; an earlier push is dropped.
	registered chan struct{}
}

const remindProducerCallerUID = "remind-producer-caller"

func remindProducerResolver(_ context.Context, _ pb.Platform, platformUID string) (*model.User, error) {
	if platformUID != remindProducerCallerUID {
		return nil, db.ErrNotFound
	}
	return &model.User{
		ID:        "018f0000-0000-7000-8000-0000000000f1",
		Username:  "remind-producer-caller",
		Clearance: int32(pb.Clearance_CLEARANCE_REGISTERED),
	}, nil
}

// startPlatformClient returns only once a client has demonstrably registered.
func startPlatformClient(t *testing.T, platform pb.Platform) *pushedActions {
	t.Helper()

	reverse := server.NewReverseServer()

	previous := service.ReverseServer
	service.ReverseServer = reverse
	t.Cleanup(func() { service.ReverseServer = previous })

	// Required: the reverse handler reads the platform from the metadata this
	// interceptor stashes, not from the message body.
	handlerOpts := []connect.HandlerOption{
		connect.WithInterceptors(interceptor.NewClearanceInterceptor(interceptor.DefaultRequirements(), remindProducerResolver)),
	}

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewReverseServiceHandler(reverse, handlerOpts...))

	// A bidi stream needs real HTTP/2; HTTP/1.1 cannot carry one at all.
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()

	httpClient := srv.Client()
	ctx, cancel := context.WithCancel(context.Background())
	ctx = callermeta.NewOutgoingContext(ctx, platform, remindProducerCallerUID)

	t.Cleanup(func() {
		cancel()
		// Before srv.Close: a parked stream handler never returns on its own.
		reverse.Shutdown()
		// Cancelling the stream context does not close the pooled connection,
		// and srv.Close waits for every connection to go inactive.
		httpClient.CloseIdleConnections()
		srv.Close()
	})

	client := ginbotv1connect.NewReverseServiceClient(httpClient, srv.URL, connect.WithInterceptors(callermeta.NewClientInterceptor()))
	stream := client.OpenClientActionStream(ctx)
	// Connect issues the HTTP request lazily, so this hello opens the stream.
	if err := stream.Send(pb.OpenClientActionStreamReq_builder{}.Build()); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	pushed := &pushedActions{
		// The claim is global, and a full channel parks the receive loop.
		notifications: make(chan *pb.OpenClientActionStreamResp, 256),
		registered:    make(chan struct{}),
	}

	go func() {
		closeOnce := sync.Once{}
		for {
			in, err := stream.Receive()
			if err != nil {
				return
			}
			// The heartbeat is the registration probe, never a reminder.
			if in.GetClientAction() == pb.ClientAction_CLIENT_ACTION_SEND_TEST {
				closeOnce.Do(func() { close(pushed.registered) })
				continue
			}
			pushed.notifications <- in
		}
	}()

	waitForRegistration(t, reverse, platform, pushed)

	return pushed
}

// waitForRegistration probes until one round-trips: registration has no wire
// signal, and SendAction silently drops with no clients.
func waitForRegistration(t *testing.T, reverse *server.ReverseServer, platform pb.Platform, pushed *pushedActions) {
	t.Helper()

	action := pb.ClientAction_CLIENT_ACTION_SEND_TEST
	probe := pb.OpenClientActionStreamResp_builder{
		PlatformEnum: platform.Enum(),
		ClientAction: &action,
		Test:         pb.TestAction_builder{EmittedAt: timestamppb.Now()}.Build(),
	}.Build()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		reverse.SendAction(probe)

		select {
		case <-pushed.registered:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}

	t.Fatal("the client action stream never registered; nothing would receive a reminder push")
}

// awaitDelivery filters by id rather than taking the first arrival, because
// ClaimDueReminders claims every due row in the database.
func (p *pushedActions) awaitDelivery(t *testing.T, reminderID string) *pb.OpenClientActionStreamResp {
	t.Helper()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case in := <-p.notifications:
			if in.GetReminderDelivery().GetReminderId() == reminderID {
				return in
			}
		case <-deadline:
			t.Fatalf("no notification for reminder %s arrived within the deadline", reminderID)
			return nil
		}
	}
}

func uniqueSuffix(label string) string {
	return label + "-" + time.Now().Format("150405.000000000")
}

// dueReminder takes ownerPlatform apart from destinationPlatform to produce a
// NULL OwnerPlatformUID; destinationMeta may omit destination_uid entirely.
func dueReminder(
	t *testing.T,
	pool *pgxpool.Pool,
	label string,
	ownerPlatform, destinationPlatform pb.Platform,
	destinationMeta *structpb.Struct,
	message string,
) (reminderID, ownerUID string) {
	t.Helper()
	ctx := context.Background()

	suffix := uniqueSuffix(label)
	ownerUID = "uid-" + suffix

	userID, err := db.CreateUser(ctx, "cron-"+label, ownerPlatform, ownerUID, nil, "en")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() {
		// No ON DELETE CASCADE, so user_account must go second.
		if _, err := pool.Exec(ctx, `DELETE FROM platform_user WHERE user_id = $1`, userID); err != nil {
			t.Errorf("cleanup platform_user for %s: %v", userID, err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM user_account WHERE id = $1`, userID); err != nil {
			t.Errorf("cleanup user_account %s: %v", userID, err)
		}
	})

	instanceMeta := callermeta.Origin{InstanceUID: "instance-" + suffix}.InstanceMeta()
	destination := pb.ReminderDestination_builder{
		PlatformEnum:    destinationPlatform.Enum(),
		InstanceMeta:    instanceMeta,
		DestinationMeta: destinationMeta,
	}.Build()

	destinationID, err := db.GetOrCreateDestinationByMeta(ctx, destination)
	if err != nil {
		t.Fatalf("GetOrCreateDestinationByMeta: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM destination WHERE id = $1`, destinationID); err != nil {
			t.Errorf("cleanup destination %d: %v", destinationID, err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM instance WHERE platform_enum = $1 AND instance_meta = $2`,
			destinationPlatform.Number(), instanceMeta,
		); err != nil {
			t.Errorf("cleanup instance for %s: %v", suffix, err)
		}
	})

	// Already due: datetime's gt_now rule lives in the RPC interceptor, above
	// db.CreateReminder.
	timezone := "UTC"
	req := pb.CreateReminderReq_builder{
		Datetime: timestamppb.New(time.Now().Add(-time.Minute)),
		Timezone: &timezone,
		Message:  &message,
	}.Build()

	reminderID, err = db.CreateReminder(ctx, req, userID, destinationID, fixtureReminderCap)
	if err != nil {
		t.Fatalf("CreateReminder: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM reminder WHERE id = $1`, reminderID); err != nil {
			t.Errorf("cleanup reminder %s: %v", reminderID, err)
		}
	})

	return reminderID, ownerUID
}

// "full" has all three nullable values, "sparse" has none of them.
func TestRemindPushesEveryClaimedValueAsATypedDelivery(t *testing.T) {
	pool := requireDatabase(t)
	pushed := startPlatformClient(t, pb.Platform_PLATFORM_DISCORD)

	fullOrigin := callermeta.Origin{DestinationUID: "channel-" + uniqueSuffix("full")}
	const fullMessage = "stand up and stretch"
	fullID, fullOwnerUID := dueReminder(t, pool, "full",
		pb.Platform_PLATFORM_DISCORD, pb.Platform_PLATFORM_DISCORD,
		fullOrigin.DestinationMeta(), fullMessage)

	// No destination_uid; an unrelated key keeps {} from colliding with every
	// other keyless destination on the unique index.
	sparseMeta := &structpb.Struct{Fields: map[string]*structpb.Value{
		"unrelated": structpb.NewStringValue(uniqueSuffix("sparse")),
	}}
	sparseID, _ := dueReminder(t, pool, "sparse",
		pb.Platform_PLATFORM_MATRIX_PROTOCOL, pb.Platform_PLATFORM_DISCORD,
		sparseMeta, "")

	Remind(context.Background())

	t.Run("every claimed value reaches the client", func(t *testing.T) {
		in := pushed.awaitDelivery(t, fullID)

		if in.GetPlatformEnum() != pb.Platform_PLATFORM_DISCORD {
			t.Errorf("platform = %v, want PLATFORM_DISCORD: the push is routed by this field",
				in.GetPlatformEnum())
		}
		if in.GetClientAction() != pb.ClientAction_CLIENT_ACTION_SEND_NOTIFICATION {
			t.Errorf("client action = %v, want SEND_NOTIFICATION", in.GetClientAction())
		}
		// Checked first: a wrong arm zeroes every accessor below.
		if in.WhichPayload() != pb.OpenClientActionStreamResp_ReminderDelivery_case {
			t.Fatalf("payload arm = %v, want reminder_delivery", in.WhichPayload())
		}

		delivery := in.GetReminderDelivery()
		fields := []struct {
			name string
			got  string
			want string
		}{
			{name: "reminder_id", got: delivery.GetReminderId(), want: fullID},
			{name: "message", got: delivery.GetMessage(), want: fullMessage},
			{name: "destination_uid", got: delivery.GetDestinationUid(), want: fullOrigin.DestinationUID},
			{name: "owner_uid", got: delivery.GetOwnerUid(), want: fullOwnerUID},
		}
		for _, field := range fields {
			if field.got != field.want {
				t.Errorf("delivery %s = %q, want %q", field.name, field.got, field.want)
			}
		}
	})

	t.Run("nullable columns arrive as set empty strings", func(t *testing.T) {
		in := pushed.awaitDelivery(t, sparseID)

		if in.WhichPayload() != pb.OpenClientActionStreamResp_ReminderDelivery_case {
			t.Fatalf("payload arm = %v, want reminder_delivery", in.WhichPayload())
		}

		delivery := in.GetReminderDelivery()
		if delivery.GetReminderId() != sparseID {
			t.Fatalf("reminder_id = %q, want %q", delivery.GetReminderId(), sparseID)
		}

		nullable := []struct {
			name string
			got  string
			has  bool
		}{
			{name: "message", got: delivery.GetMessage(), has: delivery.HasMessage()},
			{name: "destination_uid", got: delivery.GetDestinationUid(), has: delivery.HasDestinationUid()},
			{name: "owner_uid", got: delivery.GetOwnerUid(), has: delivery.HasOwnerUid()},
		}
		for _, field := range nullable {
			if field.got != "" {
				t.Errorf("%s = %q, want empty: the column is NULL for this reminder", field.name, field.got)
			}
			if !field.has {
				t.Errorf("%s is unset; the producer flattens a NULL column to an empty string rather than omitting the field",
					field.name)
			}
		}

		if !delivery.HasReminderId() || delivery.GetReminderId() == "" {
			t.Error("reminder_id is empty; the client would drop this delivery rather than post it")
		}
	})
}
