package server

import (
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"google.golang.org/grpc/codes"
)

// Handler-level authorization tests that need no database, driven through the
// harness so the production interceptor chain runs in front of them.
//
// The decisions covered here are the ones a handler reaches BEFORE it touches
// pkg/db, which is exactly why they can be tested this way — and, in the
// GetUser case below, the fact that no database was touched is itself the
// property under test. The harness leaves pkg/db's pool nil on purpose, so a
// handler that did query would panic and the recovery interceptor would turn
// that into codes.Internal.
//
// Everything requiring real rows lives in authorization_integration_test.go.

// ── GetUser: an unset id means "me" ──────────────────────────────────────────

// TestGetUserWithNoIdReturnsTheCallersOwnRow.
//
// A platform client never learns its own user_account UUID — identity travels
// as a platform id in metadata — so an unset id is the ONLY way for a caller
// to read its own row, and /me on both platform clients depends on it.
//
// The harness has no database, so the call succeeding at all is the assertion
// that carries the weight: the caller's row is served from what the clearance
// interceptor already resolved, not re-fetched. A handler that fell through to
// db.GetUser would dereference a nil pool and come back Internal.
//
// CLEARANCE_REGISTERED throughout, the floor: reading your own row must not
// require the moderator clearance that reading someone ELSE'S does.
func TestGetUserWithNoIdReturnsTheCallersOwnRow(t *testing.T) {
	h, dir := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	resp, err := h.User.GetUser(
		callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
		pb.GetUserReq_builder{}.Build(),
	)
	if err != nil {
		t.Fatalf("GetUser with an unset id: %v", err)
	}

	if got := resp.GetUser().GetId(); got != callerUserID {
		t.Errorf("GetUser().User.Id = %q, want the caller's own id %q", got, callerUserID)
	}

	// Exactly one lookup, by the interceptor. A second would mean the handler
	// resolved the caller again rather than using the resolved one.
	if n := dir.resolveCount(); n != 1 {
		t.Errorf("caller resolved %d times, want exactly 1 (the interceptor's)", n)
	}
}

// TestGetUserNamingTheCallersOwnIdIsShortCircuited: the same path, reached the
// other way. The integration suite already proves a caller can read its own
// row by id, but with a live database that cannot distinguish the short
// circuit from a successful SELECT — and the difference is a database round
// trip on every /me.
func TestGetUserNamingTheCallersOwnIdIsShortCircuited(t *testing.T) {
	h, _ := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	id := callerUserID
	resp, err := h.User.GetUser(
		callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
		pb.GetUserReq_builder{Id: &id}.Build(),
	)
	if err != nil {
		t.Fatalf("GetUser naming the caller's own id: %v", err)
	}

	if got := resp.GetUser().GetId(); got != callerUserID {
		t.Errorf("GetUser().User.Id = %q, want %q", got, callerUserID)
	}
}

// TestGetUserForAnotherUserIsRefusedBeforeTheDatabase: the clearance check
// runs before db.GetUser, so a caller below CLEARANCE_MODERATOR is refused
// without the subject's existence ever being looked up. PermissionDenied
// rather than the Internal a nil-pool panic would produce is what proves the
// ordering.
func TestGetUserForAnotherUserIsRefusedBeforeTheDatabase(t *testing.T) {
	for _, clearance := range []pb.Clearance{
		pb.Clearance_CLEARANCE_REGISTERED,
		pb.Clearance_CLEARANCE_MEMBER,
	} {
		t.Run(clearance.String(), func(t *testing.T) {
			h, _ := registeredHarness(t, clearance)

			someoneElse := "018f0000-0000-7000-8000-0000000000b1"
			_, err := h.User.GetUser(
				callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
				pb.GetUserReq_builder{Id: &someoneElse}.Build(),
			)

			requireCode(t, err, codes.PermissionDenied)
		})
	}
}

// ── callerScopedInstance: a call with no origin at all ───────────────────────

// TestInstanceScopedRPCsWithNoOriginAreFailedPrecondition.
//
// callerScopedInstance has two refusals and they mean different things.
// Naming an instance that is not the call's own origin is NotFound, which the
// integration suite covers four times over. Having NO origin at all — a direct
// message, where there is no guild or room to scope to — is FailedPrecondition,
// and that arm had no coverage: it is reached before any database work, so a
// regression turning it into NotFound or, worse, into a fallthrough that
// scoped to instance 0, would not be caught anywhere.
//
// The distinction is what the platform clients render: FailedPrecondition
// becomes "use this in a server", NotFound becomes silence.
//
// Every request below is otherwise complete, so a rejection cannot be an
// argument-validation failure in disguise, and the harness has no database, so
// reaching one would surface as Internal rather than FailedPrecondition.
func TestInstanceScopedRPCsWithNoOriginAreFailedPrecondition(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	// Caller identity only. No callermeta.NewOutgoingOrigin: this is exactly
	// what a direct message looks like on the wire.
	ctx := callerCtx(pb.Platform_PLATFORM_DISCORD, uid)

	// Built through callermeta rather than hand-spelled, so the jsonb shape is
	// the one production writes and a rejection cannot be about the metadata
	// keys being wrong.
	platform := pb.Platform_PLATFORM_DISCORD
	instanceMeta := callermeta.Origin{InstanceUID: "some-guild"}.InstanceMeta()
	instance := pb.TriggerInstance_builder{PlatformEnum: &platform, InstanceMeta: instanceMeta}.Build()

	phrase := "a-phrase"
	triggerID := "018f0000-0000-7000-8000-0000000000e1"
	reply := "a reply"
	chance := int32(10)

	calls := map[string]func() error{
		"TryTrigger": func() error {
			_, err := h.Trigger.TryTrigger(ctx, pb.TryTriggerReq_builder{
				Instance: instance, Phrase: &phrase,
			}.Build())
			return err
		},
		"ExecTrigger": func() error {
			_, err := h.Trigger.ExecTrigger(ctx, pb.ExecTriggerReq_builder{
				Id: &triggerID, Instance: instance,
			}.Build())
			return err
		},
		"GetTriggerStats": func() error {
			_, err := h.Trigger.GetTriggerStats(ctx, pb.GetTriggerStatsReq_builder{
				Instance: instance,
			}.Build())
			return err
		},
		// CreateTrigger reaches the same check through resolveScopeInstances,
		// both when it names instances explicitly and when it falls back to
		// the origin — so both spellings are driven.
		"CreateTrigger naming an instance": func() error {
			_, err := h.Trigger.CreateTrigger(ctx, pb.CreateTriggerReq_builder{
				Phrase: &phrase, Reply: &reply, Chance: &chance,
				Instances: []*pb.TriggerInstance{instance},
			}.Build())
			return err
		},
		"CreateTrigger falling back to the origin": func() error {
			_, err := h.Trigger.CreateTrigger(ctx, pb.CreateTriggerReq_builder{
				Phrase: &phrase, Reply: &reply, Chance: &chance,
			}.Build())
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			requireCode(t, call(), codes.FailedPrecondition)
		})
	}
}
