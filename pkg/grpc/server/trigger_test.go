package server

import (
	"strings"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/trigger"
	"google.golang.org/grpc/codes"
)

// triggerHarness registers one Discord identity at the given clearance,
// mirroring registeredHarness in user_test.go but named locally so this file
// has no dependency on that one's specific constants.
func triggerHarness(t *testing.T, clearance pb.Clearance) (*harness, string) {
	t.Helper()

	const platformUID = "trigger-caller"
	const userID = "018f0000-0000-7000-8000-0000000000f0"

	dir := newDirectory().add(pb.Platform_PLATFORM_DISCORD, platformUID, testUser(userID, clearance))
	h := newHarness(t, withDirectory(dir))
	return h, platformUID
}

// TestCreateTriggerRequiresPhrase.
func TestCreateTriggerRequiresPhrase(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	reply := "a reply"
	_, err := h.Trigger.CreateTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.CreateTriggerReq_builder{Reply: &reply}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// TestCreateTriggerRequiresReplyOrFileURL.
func TestCreateTriggerRequiresReplyOrFileURL(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	phrase := "hello"
	_, err := h.Trigger.CreateTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.CreateTriggerReq_builder{Phrase: &phrase}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// createRegexReq builds an otherwise-valid CreateTriggerReq in REGEX mode, so
// only the clearance gate under test can be the reason for a PermissionDenied.
func createRegexReq() *pb.CreateTriggerReq {
	phrase := "^abc.*$"
	reply := "a reply"
	mode := pb.TriggerMode_TRIGGER_MODE_REGEX
	return pb.CreateTriggerReq_builder{Phrase: &phrase, Reply: &reply, Mode: &mode}.Build()
}

// TestCreateTriggerRegexModeIsGatedBelowModerator is AC5: a REGISTERED caller
// is refused with PermissionDenied.
func TestCreateTriggerRegexModeIsGatedBelowModerator(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.CreateTrigger(callerCtx(pb.Platform_PLATFORM_DISCORD, uid), createRegexReq())
	requireCode(t, err, codes.PermissionDenied)
}

// TestCreateTriggerRegexModeIsAdmittedAtModerator is the other half of AC5: a
// MODERATOR caller must not be refused for CLEARANCE reasons. The call may
// still fail further in (this harness has no database and no bootstrapped
// call origin, so instance scoping cannot resolve), but that failure must not
// be PermissionDenied.
func TestCreateTriggerRegexModeIsAdmittedAtModerator(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_MODERATOR)

	_, err := h.Trigger.CreateTrigger(callerCtx(pb.Platform_PLATFORM_DISCORD, uid), createRegexReq())
	requireNotCode(t, err, codes.PermissionDenied)
}

// TestCreateTriggerRejectsUncompilableRegex is AC6: an uncompilable regex is
// rejected at creation with InvalidArgument, even for a caller with enough
// clearance to use regex mode at all.
func TestCreateTriggerRejectsUncompilableRegex(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_MODERATOR)

	phrase := "["
	reply := "a reply"
	mode := pb.TriggerMode_TRIGGER_MODE_REGEX
	_, err := h.Trigger.CreateTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.CreateTriggerReq_builder{Phrase: &phrase, Reply: &reply, Mode: &mode}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// TestCreateTriggerRejectsAPhraseOverMaxPatternLength is AC6's other half.
func TestCreateTriggerRejectsAPhraseOverMaxPatternLength(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	phrase := strings.Repeat("a", trigger.MaxPatternLength+1)
	reply := "a reply"
	_, err := h.Trigger.CreateTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.CreateTriggerReq_builder{Phrase: &phrase, Reply: &reply}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// TestCreateTriggerRejectsChanceOutsideRange.
func TestCreateTriggerRejectsChanceOutsideRange(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	for _, chance := range []int32{-1, 101} {
		t.Run("", func(t *testing.T) {
			phrase := "valid-phrase"
			reply := "a reply"
			c := chance
			_, err := h.Trigger.CreateTrigger(
				callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
				pb.CreateTriggerReq_builder{Phrase: &phrase, Reply: &reply, Chance: &c}.Build(),
			)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

// TestTryTriggerRequiresFields: missing instance/phrase is InvalidArgument.
func TestTryTriggerRequiresFields(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.TryTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.TryTriggerReq_builder{}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// TestExecTriggerRequiresFields.
func TestExecTriggerRequiresFields(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.ExecTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.ExecTriggerReq_builder{}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetTriggerRequiresID.
func TestGetTriggerRequiresID(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.GetTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.GetTriggerReq_builder{}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// TestDeleteTriggerRequiresID.
func TestDeleteTriggerRequiresID(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.DeleteTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.DeleteTriggerReq_builder{}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetTriggerStatsRequiresInstance.
func TestGetTriggerStatsRequiresInstance(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.GetTriggerStats(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.GetTriggerStatsReq_builder{}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetFileRequiresFileID.
func TestGetFileRequiresFileID(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.GetFile(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.GetFileReq_builder{}.Build(),
	)
	requireCode(t, err, codes.InvalidArgument)
}

// TestListTriggersRefusesAnotherUsersID: listing by an arbitrary user id is an
// enumeration surface.
func TestListTriggersRefusesAnotherUsersID(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	someoneElse := "018f0000-0000-7000-8000-0000000000f1"
	_, err := h.Trigger.ListTriggers(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.ListTriggersReq_builder{UserId: &someoneElse}.Build(),
	)
	requireCode(t, err, codes.PermissionDenied)
}

// TestAllTriggerRPCsRefuseAnAnonymousCaller: every trigger RPC must reject a
// call carrying no caller identity at all. The exact code produced by the
// clearance interceptor for a guarded method with no metadata is
// InvalidArgument (see TestGuardedMethodWithoutMetadataIsRejected in
// user_test.go for the same behaviour on UserService); what matters here is
// that it is never codes.OK, i.e. never treated as a legitimate, answered
// request.
func TestAllTriggerRPCsRefuseAnAnonymousCaller(t *testing.T) {
	h, _ := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)
	ctx := anonymousCtx()

	calls := map[string]func() error{
		"TryTrigger": func() error {
			_, err := h.Trigger.TryTrigger(ctx, pb.TryTriggerReq_builder{}.Build())
			return err
		},
		"ExecTrigger": func() error {
			_, err := h.Trigger.ExecTrigger(ctx, pb.ExecTriggerReq_builder{}.Build())
			return err
		},
		"GetTrigger": func() error {
			_, err := h.Trigger.GetTrigger(ctx, pb.GetTriggerReq_builder{}.Build())
			return err
		},
		"ListTriggers": func() error {
			_, err := h.Trigger.ListTriggers(ctx, pb.ListTriggersReq_builder{}.Build())
			return err
		},
		"CreateTrigger": func() error {
			_, err := h.Trigger.CreateTrigger(ctx, pb.CreateTriggerReq_builder{}.Build())
			return err
		},
		"UpdateTrigger": func() error {
			_, err := h.Trigger.UpdateTrigger(ctx, pb.UpdateTriggerReq_builder{}.Build())
			return err
		},
		"DeleteTrigger": func() error {
			_, err := h.Trigger.DeleteTrigger(ctx, pb.DeleteTriggerReq_builder{}.Build())
			return err
		},
		"GetTriggerStats": func() error {
			_, err := h.Trigger.GetTriggerStats(ctx, pb.GetTriggerStatsReq_builder{}.Build())
			return err
		},
		"GetFile": func() error {
			_, err := h.Trigger.GetFile(ctx, pb.GetFileReq_builder{}.Build())
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatalf("%s accepted an anonymous caller (returned OK)", name)
			}
		})
	}
}
