package server

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/storage"
	"github.com/lasikuu/GinBot/pkg/trigger"
)

func triggerHarness(t *testing.T, clearance pb.Clearance) (*harness, string) {
	t.Helper()

	const platformUID = "trigger-caller"
	const userID = "018f0000-0000-7000-8000-0000000000f0"

	dir := newDirectory().add(pb.Platform_PLATFORM_DISCORD, platformUID, testUser(userID, clearance))
	h := newHarness(t, withDirectory(dir))
	return h, platformUID
}

func TestCreateTriggerRequiresPhrase(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	reply := "a reply"
	_, err := h.Trigger.CreateTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.CreateTriggerReq_builder{Reply: &reply}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestCreateTriggerRequiresReplyOrFileURL(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	phrase := "hello"
	_, err := h.Trigger.CreateTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.CreateTriggerReq_builder{Phrase: &phrase}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

// createRegexReq is otherwise valid, so only the clearance gate can deny it.
func createRegexReq() *pb.CreateTriggerReq {
	phrase := "^abc.*$"
	reply := "a reply"
	mode := pb.TriggerMode_TRIGGER_MODE_REGEX
	return pb.CreateTriggerReq_builder{Phrase: &phrase, Reply: &reply, Mode: &mode}.Build()
}

func TestCreateTriggerRegexModeIsGatedBelowModerator(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.CreateTrigger(callerCtx(pb.Platform_PLATFORM_DISCORD, uid), createRegexReq())
	requireCode(t, err, connect.CodePermissionDenied)
}

// The call may still fail further in — no database, no origin — but not PermissionDenied.
func TestCreateTriggerRegexModeIsAdmittedAtModerator(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_MODERATOR)

	_, err := h.Trigger.CreateTrigger(callerCtx(pb.Platform_PLATFORM_DISCORD, uid), createRegexReq())
	requireNotCode(t, err, connect.CodePermissionDenied)
}

func TestCreateTriggerRejectsUncompilableRegex(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_MODERATOR)

	phrase := "["
	reply := "a reply"
	mode := pb.TriggerMode_TRIGGER_MODE_REGEX
	_, err := h.Trigger.CreateTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.CreateTriggerReq_builder{Phrase: &phrase, Reply: &reply, Mode: &mode}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestCreateTriggerRejectsAPhraseOverMaxPatternLength(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	phrase := strings.Repeat("a", trigger.MaxPatternLength+1)
	reply := "a reply"
	_, err := h.Trigger.CreateTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.CreateTriggerReq_builder{Phrase: &phrase, Reply: &reply}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

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
			requireCode(t, err, connect.CodeInvalidArgument)
		})
	}
}

func TestTryTriggerRequiresFields(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.TryTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.TryTriggerReq_builder{}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestExecTriggerRequiresFields(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.ExecTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.ExecTriggerReq_builder{}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestGetTriggerRequiresID(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.GetTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.GetTriggerReq_builder{}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestDeleteTriggerRequiresID(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.DeleteTrigger(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.DeleteTriggerReq_builder{}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestGetTriggerStatsRequiresInstance(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, err := h.Trigger.GetTriggerStats(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.GetTriggerStatsReq_builder{}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestGetFileRequiresFileID(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	_, _, err := h.Trigger.GetFile(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		pb.GetFileReq_builder{}.Build(),
	)
	requireCode(t, err, connect.CodeInvalidArgument)
}

// drainGetFileChunks, not the adapter, so the chunk count is directly observable.
func TestGetFileRefusesAnUnauthorisedCallerWithNoChunksSent(t *testing.T) {
	h, _ := triggerHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	fileID := "018f0000-0000-7000-8000-000000000abc"
	chunks, err := drainGetFileChunks(anonymousCtx(), h.Trigger.c, pb.GetFileReq_builder{FileId: &fileID}.Build())
	if err == nil {
		t.Fatal("an anonymous caller was admitted to GetFile")
	}
	requireCode(t, err, connect.CodeInvalidArgument)
	if len(chunks) != 0 {
		t.Errorf("%d chunks arrived for a caller with no identity at all, want 0", len(chunks))
	}
}

func TestGetFileRefusesInsufficientClearanceWithNoChunksSent(t *testing.T) {
	h, uid := triggerHarness(t, pb.Clearance_CLEARANCE_UNSPECIFIED)

	fileID := "018f0000-0000-7000-8000-000000000abd"
	chunks, err := drainGetFileChunks(
		callerCtx(pb.Platform_PLATFORM_DISCORD, uid),
		h.Trigger.c,
		pb.GetFileReq_builder{FileId: &fileID}.Build(),
	)
	if err == nil {
		t.Fatal("a caller below CLEARANCE_REGISTERED was admitted to GetFile")
	}
	requireCode(t, err, connect.CodePermissionDenied)
	if len(chunks) != 0 {
		t.Errorf("%d chunks arrived for a caller below the clearance floor, want 0", len(chunks))
	}
}

// TestDisplayFilenamePrefersTheStoredOriginalName is the single source of the
// name in buildTriggerFile, ListTriggers and the GetFile meta chunk.
func TestDisplayFilenamePrefersTheStoredOriginalName(t *testing.T) {
	tests := []struct {
		name string
		file *model.File
		want string
	}{
		{
			name: "a stored original filename is used as-is",
			file: &model.File{ID: "018f0000-0000-7000-8000-000000000001", MimeType: "image/png", OriginalFilename: "cat.png"},
			want: "cat.png",
		},
		{
			name: "an empty original filename falls back to the id plus a mapped extension",
			file: &model.File{ID: "018f0000-0000-7000-8000-000000000002", MimeType: "image/gif", OriginalFilename: ""},
			want: "018f0000-0000-7000-8000-000000000002.gif",
		},
		{
			name: "an unmapped mime type contributes no extension, so the fallback is a bare id",
			file: &model.File{ID: "018f0000-0000-7000-8000-000000000003", MimeType: "application/octet-stream", OriginalFilename: ""},
			want: "018f0000-0000-7000-8000-000000000003",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayFilename(tt.file); got != tt.want {
				t.Errorf("displayFilename(%+v) = %q, want %q", tt.file, got, tt.want)
			}
		})
	}
}

// mimeExtensions is the fallback's only source of an extension, so a type
// storage accepts but the map omits renders every nameless attachment of that
// type without one.
func TestDisplayFilenameCoversEveryAllowedMIMEType(t *testing.T) {
	for _, mimeType := range storage.AllowedMIMETypes() {
		t.Run(mimeType, func(t *testing.T) {
			file := &model.File{ID: "018f0000-0000-7000-8000-0000000000ab", MimeType: mimeType}
			got := displayFilename(file)
			if got == file.ID {
				t.Fatalf("displayFilename(mime=%q) = %q, want an extension appended", mimeType, got)
			}
			if want := file.ID + mimeExtensions[mimeType]; got != want {
				t.Errorf("displayFilename(mime=%q) = %q, want %q", mimeType, got, want)
			}
		})
	}
}

// ListTriggersReq.user_id is deleted and reserved; `mine` replaced it and needs rows.

// The chain answers InvalidArgument here; what matters is that it is never a success.
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
			_, _, err := h.Trigger.GetFile(ctx, pb.GetFileReq_builder{}.Build())
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
