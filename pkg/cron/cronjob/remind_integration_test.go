//go:build integration

// Integration coverage for the reminder producer. Requires a live Postgres, the
// same one every other integration suite in this repository uses:
//
//	docker compose -f docker-compose.dev.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/cron/cronjob/...
//
// TestMain is declared in orphan_test.go in this same package and is reused, not
// redeclared; requireDatabase below is lazy for that reason.
//
// SCOPE: Remind's PRODUCER seam — the mapping from a claimed reminder row to the
// ReminderDelivery pushed over the reverse stream. Nothing here re-tests
// ClaimDueReminders' SQL (pkg/db) or the routing the Discord client does with the
// payload once it arrives (pkg/discord). What is only testable here is the join
// between them, and it is only testable here because Remind reads a package-level
// pool and writes to a package-level server: there is no seam to inject.

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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/grpc/server"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"github.com/lasikuu/GinBot/pkg/log"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fixtureReminderCap is high enough never to be reached, so a test that is not
// about the per-owner cap cannot trip over it.
const fixtureReminderCap = 1000

var (
	databaseOnce sync.Once
	databasePool *pgxpool.Pool
)

// requireDatabase brings up pkg/db's pool, plus a second pool of this file's
// own for the raw DELETEs cleanup needs — pkg/db keeps its pool unexported and
// exposes no way to hard-delete a reminder.
func requireDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseOnce.Do(func() {
		config.LoadEnv()
		log.InitializeLogger(config.AppEnvironment, config.LogLevel)
		config.SetEnv()

		db.InitDB()
		db.EnsureLatestVersion()

		// Built the same way pkg/db builds it, so a password with reserved
		// characters is escaped rather than corrupting the URI.
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

// ── A real client on the other end of the reverse stream ─────────────────────

// pushedActions is a live platform client: a real gRPC stream against the
// ReverseServer that Remind pushes into.
//
// A fake is not an option here. Remind writes to service.ReverseServer, a
// package-level singleton, and ReverseServer's registry and sender goroutine are
// unexported — so the only way to observe what it pushed is to be a client.
type pushedActions struct {
	// notifications carries every SEND_NOTIFICATION the server pushed.
	notifications chan *pb.OpenClientActionStreamResp
	// registered closes once a probe action has made the full round trip, which
	// is the only positive proof the server has admitted this client. Pushing
	// before that point fans out to an empty registry and is silently dropped.
	registered chan struct{}
}

// startPlatformClient stands up a ReverseServer, installs it as the one Remind
// pushes to, and connects a client that has demonstrably registered by the time
// this returns.
func startPlatformClient(t *testing.T, platform pb.Platform) *pushedActions {
	t.Helper()

	reverse := server.NewReverseServer()

	previous := service.ReverseServer
	service.ReverseServer = reverse
	t.Cleanup(func() { service.ReverseServer = previous })

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewReverseServiceHandler(reverse))

	// EnableHTTP2 + StartTLS, matching pkg/grpc/server's harness: a bidi stream
	// needs a real HTTP/2 connection and HTTP/1.1 cannot carry one at all.
	// No interceptor chain is installed, because this suite is about Remind's
	// producer seam and not about authorization — the clearance interceptor
	// covering this stream is pkg/grpc/server's to prove.
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()

	httpClient := srv.Client()
	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(func() {
		cancel()
		// Shutdown BEFORE the server closes, which is cmd/ginbot-server's
		// ordering: http.Server.Shutdown waits for handlers to return without
		// cancelling their contexts, and a reverse-stream handler waiting for a
		// client message never returns on its own.
		reverse.Shutdown()
		// Cancelling the stream's context does not close the pooled HTTP/2
		// connection it rode on, and srv.Close waits for every connection to go
		// inactive — so without this the cleanup hangs rather than finishing.
		httpClient.CloseIdleConnections()
		srv.Close()
	})

	stream := ginbotv1connect.NewReverseServiceClient(httpClient, srv.URL).OpenClientActionStream(ctx)
	if err := stream.Send(pb.OpenClientActionStreamReq_builder{
		PlatformEnum: platform.Enum(),
	}.Build()); err != nil {
		t.Fatalf("register client action stream: %v", err)
	}

	pushed := &pushedActions{
		// Buffered well past the two reminders this file creates, because the
		// claim is global: leftover due rows from another suite are pushed to
		// this client too, and a full channel would park the receive loop.
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
			// The heartbeat is the registration probe below and never a
			// reminder, so it is consumed here rather than collected.
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

// waitForRegistration pushes heartbeats until one comes back.
//
// Registration has no positive signal on the wire and SendAction is a silent
// drop for a platform with no clients, so a test that simply slept would race:
// too short and Remind's push lands in an empty registry, and the test fails
// claiming the producer sent nothing. A round-tripped probe is proof.
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

// awaitDelivery waits for the notification carrying reminderID.
//
// Filtered by id rather than taking the first arrival, because ClaimDueReminders
// claims every due row in the database: another suite's leftover reminder is
// pushed to this same client and would otherwise be asserted against.
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

// ── Fixtures ─────────────────────────────────────────────────────────────────

// uniqueSuffix keeps identities from separate runs, and from the two fixtures in
// one run, apart.
func uniqueSuffix(label string) string {
	return label + "-" + time.Now().Format("150405.000000000")
}

// dueReminder inserts an owner, a destination and one reminder already past its
// fire time, and registers cleanup for all of it.
//
// ownerPlatform is separable from destinationPlatform because ClaimDueReminders
// resolves the owner's platform id by joining platform_user on the DESTINATION's
// platform. An owner who never linked that platform yields a NULL
// OwnerPlatformUID — the nullable column the producer has to flatten.
//
// destinationMeta is passed in rather than derived so a fixture can supply a
// shape with no destination_uid key at all, which is what makes
// destination_meta->>'destination_uid' NULL.
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
		// platform_user.user_id has no ON DELETE CASCADE, so user_account has to
		// go second or the foreign key rejects the delete.
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

	// Already due, so the very next claim picks it up. The gt_now constraint on
	// CreateReminderReq.datetime is a protovalidate rule enforced by the RPC
	// interceptor; db.CreateReminder is below that layer and inserts what it is
	// given, which is what lets a producer test exist without a scheduler.
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

// ── The producer ─────────────────────────────────────────────────────────────

// TestRemindPushesEveryClaimedValueAsATypedDelivery.
//
// The claimed row has four values the client needs and three of them are
// nullable. Before the typed payload, each was written into a Struct under a
// string key that only a shared constant tied to the reader; a rename or an
// omission on either side produced an empty string on the other and nothing
// failed. The oneof removes that failure mode at compile time — but not the one
// this test covers, which is the producer reading the WRONG COLUMN into the right
// field, or dropping one altogether.
//
// Two reminders, claimed by a single Remind call, cover the two ends of the
// nullability:
//
//   - "full" has a message, a resolvable channel and an owner registered on the
//     destination's platform.
//   - "sparse" has none of the three. Its message column is NULL (db.CreateReminder
//     maps an empty string to NULL), its destination_meta carries no
//     destination_uid key so the extraction yields NULL, and its owner is
//     registered on Matrix while the destination is Discord, so the platform_user
//     LEFT JOIN misses. All three must arrive as empty strings that are SET, not
//     as an omitted or unset field: "" and absent mean the same thing to the
//     client, and flattening them here is what lets it read every field
//     unconditionally.
func TestRemindPushesEveryClaimedValueAsATypedDelivery(t *testing.T) {
	pool := requireDatabase(t)
	pushed := startPlatformClient(t, pb.Platform_PLATFORM_DISCORD)

	fullOrigin := callermeta.Origin{DestinationUID: "channel-" + uniqueSuffix("full")}
	const fullMessage = "stand up and stretch"
	fullID, fullOwnerUID := dueReminder(t, pool, "full",
		pb.Platform_PLATFORM_DISCORD, pb.Platform_PLATFORM_DISCORD,
		fullOrigin.DestinationMeta(), fullMessage)

	// No destination_uid key at all. A destination_meta of {} would collide with
	// every other keyless destination on the unique index, so the shape carries
	// an unrelated key instead — which is exactly what a platform that has not
	// been taught to write destination_uid would produce.
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
		// Checked before the fields, because every accessor below returns the
		// zero value for the wrong arm and would report four confusing
		// mismatches instead of one accurate line.
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

		// The id is never legitimately empty, and it is the whole reason the
		// other three may be: without it the client drops the delivery instead
		// of posting a reminder it could never confirm.
		if !delivery.HasReminderId() || delivery.GetReminderId() == "" {
			t.Error("reminder_id is empty; the client would drop this delivery rather than post it")
		}
	})
}
