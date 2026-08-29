package server

import (
	"testing"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// The harness leaves pkg/db's pool nil: a handler that reached it would come back Internal.

// An unset id is the only way to read your own row, and succeeding at all proves the
// row came from what the interceptor resolved rather than being re-fetched.
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

	if n := dir.resolveCount(); n != 1 {
		t.Errorf("caller resolved %d times, want exactly 1 (the interceptor's)", n)
	}
}

// A live database cannot distinguish the short circuit from a successful SELECT.
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

// PermissionDenied, not the Internal a nil pool would produce, proves the ordering.
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

			requireCode(t, err, connect.CodePermissionDenied)
		})
	}
}

// No origin at all — a direct message — is FailedPrecondition, not the NotFound naming
// a foreign instance gets; the platform clients render the two differently.
func TestInstanceScopedRPCsWithNoOriginAreFailedPrecondition(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	// Caller identity only: this is exactly what a direct message looks like on the wire.
	ctx := callerCtx(pb.Platform_PLATFORM_DISCORD, uid)

	// Built through callermeta, so a rejection cannot be about the metadata keys.
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
		// CreateTrigger reaches the same check whether it names instances or falls back.
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
			requireCode(t, call(), connect.CodeFailedPrecondition)
		})
	}
}
