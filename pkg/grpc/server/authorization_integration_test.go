//go:build integration

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/storage"
)

// A handler taking the subject from anywhere but metadata is an unauthenticated write.
func TestSetLocaleAndSetTimezoneTouchOnlyTheCallersRow(t *testing.T) {
	h, pool := liveHarness(t)

	callerPlatformUID := uniqueUID("prefs-caller")
	callerID := registerUser(t, h, pool, callerPlatformUID)
	setClearance(t, pool, callerID, pb.Clearance_CLEARANCE_REGISTERED)

	bystanderID := registerUser(t, h, pool, uniqueUID("prefs-bystander"))

	// Both accounts start at an explicit locale, so a misdirected write is not masked by NULL.
	readPrefs := func(userID string) (locale, timezone *string) {
		t.Helper()
		if err := pool.QueryRow(context.Background(),
			`SELECT locale, timezone FROM user_account WHERE id = $1`, userID,
		).Scan(&locale, &timezone); err != nil {
			t.Fatalf("read preferences for %s: %v", userID, err)
		}
		return locale, timezone
	}

	beforeLocale, beforeTimezone := readPrefs(bystanderID)

	ctx := callerCtx(pb.Platform_PLATFORM_DISCORD, callerPlatformUID)

	locale := "ja"
	if _, err := h.User.SetLocale(ctx, pb.SetLocaleReq_builder{Locale: &locale}.Build()); err != nil {
		t.Fatalf("SetLocale: %v", err)
	}
	timezone := "Asia/Tokyo"
	if _, err := h.User.SetTimezone(ctx, pb.SetTimezoneReq_builder{Timezone: &timezone}.Build()); err != nil {
		t.Fatalf("SetTimezone: %v", err)
	}

	callerLocale, callerTimezone := readPrefs(callerID)
	if callerLocale == nil || *callerLocale != locale {
		t.Errorf("caller locale = %v, want %q", callerLocale, locale)
	}
	if callerTimezone == nil || *callerTimezone != timezone {
		t.Errorf("caller timezone = %v, want %q", callerTimezone, timezone)
	}

	afterLocale, afterTimezone := readPrefs(bystanderID)
	if !equalStringPtr(beforeLocale, afterLocale) {
		t.Errorf("bystander locale changed from %v to %v; the write was not scoped to the caller", beforeLocale, afterLocale)
	}
	if !equalStringPtr(beforeTimezone, afterTimezone) {
		t.Errorf("bystander timezone changed from %v to %v; the write was not scoped to the caller", beforeTimezone, afterTimezone)
	}
}

// equalStringPtr compares two nullable columns, treating NULL as equal to NULL.
func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// NotFound, not PermissionDenied: whether a given id exists at all is itself private.
func TestGetReminderRefusesAnotherUsersRowWithNotFound(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "get-rem-owner")
	strangerUID, _ := registeredCaller(t, h, pool, "get-rem-stranger")
	suffix := uniqueUID("getrem")

	// action_record.actor_id is ON DELETE SET NULL, so rows must be reclaimed by actor first.
	cleanupActionRecords(t, pool, ownerID)

	id := createReminderVia(t, h, pool, ownerUID, suffix, "")

	ownerCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID)
	if _, err := h.Reminder.GetReminder(ownerCtx, pb.GetReminderReq_builder{Id: &id}.Build()); err != nil {
		t.Fatalf("owner GetReminder: %v", err)
	}

	strangerCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, strangerUID)
	_, err := h.Reminder.GetReminder(strangerCtx, pb.GetReminderReq_builder{Id: &id}.Build())
	requireCode(t, err, connect.CodeNotFound)
}

func TestGetReminderForAnUnknownIdIsIndistinguishableFromAnotherUsersRow(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "get-rem-oracle-owner")
	strangerUID, _ := registeredCaller(t, h, pool, "get-rem-oracle-stranger")
	suffix := uniqueUID("getremoracle")

	cleanupActionRecords(t, pool, ownerID)

	existingID := createReminderVia(t, h, pool, ownerUID, suffix, "")
	missingID := "018f0000-0000-7000-8000-0000000000ff"

	strangerCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, strangerUID)

	_, existingErr := h.Reminder.GetReminder(strangerCtx, pb.GetReminderReq_builder{Id: &existingID}.Build())
	_, missingErr := h.Reminder.GetReminder(strangerCtx, pb.GetReminderReq_builder{Id: &missingID}.Build())

	requireCode(t, existingErr, connect.CodeNotFound)
	requireCode(t, missingErr, connect.CodeNotFound)
}

// The limiter key is caller.ID from metadata: keying on the instance would silence a
// guild, on a request field would allow spam. ForcedInterval is 60s, so no fake clock.
func TestForcedTriggerRateLimitIsPerCallerNotPerInstance(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	firstUID, _ := registeredCaller(t, h, pool, "forced-first")
	secondUID, _ := registeredCaller(t, h, pool, "forced-second")
	suffix := uniqueUID("forced")

	origin := callermeta.Origin{InstanceUID: "forced-instance-" + suffix, DestinationUID: "forced-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	firstCtx := triggerCtx(firstUID, origin)
	secondCtx := triggerCtx(secondUID, origin)

	phrase := "forced-phrase-" + suffix
	reply := "forced-reply"
	// Chance 0: only a forced fire bypasses the roll, so any fire below is forced.
	id := createTriggerVia(t, h, pool, firstCtx, phrase, reply, 0, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	forced := true
	tryForced := func(t *testing.T, ctx context.Context) string {
		t.Helper()
		resp, err := h.Trigger.TryTrigger(ctx, pb.TryTriggerReq_builder{
			Instance: triggerInstanceFor(origin),
			Phrase:   &phrase,
			Forced:   &forced,
		}.Build())
		if err != nil {
			t.Fatalf("forced TryTrigger: %v", err)
		}
		return resp.GetId()
	}

	if got := tryForced(t, firstCtx); got != id {
		t.Fatalf("first forced TryTrigger returned %q, want %q; the forced path must bypass the chance roll", got, id)
	}

	if got := tryForced(t, firstCtx); got != "" {
		t.Errorf("the same caller's second forced fire returned %q, want silence; the rate limit did not apply", got)
	}

	if got := tryForced(t, secondCtx); got != id {
		t.Errorf("a second caller's first forced fire returned %q, want %q; the limit is keyed on something wider than the caller",
			got, id)
	}

	if got := tryForced(t, secondCtx); got != "" {
		t.Errorf("the second caller's second forced fire returned %q, want silence", got)
	}
}

func TestGetTriggerIsVisibleToItsOwnerAndToTheInstanceItIsScopedTo(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "vis-owner")
	memberUID, _ := registeredCaller(t, h, pool, "vis-member")
	outsiderUID, _ := registeredCaller(t, h, pool, "vis-outsider")
	suffix := uniqueUID("vis")

	home := callermeta.Origin{InstanceUID: "vis-home-" + suffix, DestinationUID: "vis-home-dest-" + suffix}
	elsewhere := callermeta.Origin{InstanceUID: "vis-elsewhere-" + suffix, DestinationUID: "vis-elsewhere-dest-" + suffix}
	cleanupInstanceRows(t, pool, home.InstanceMeta())
	cleanupInstanceRows(t, pool, elsewhere.InstanceMeta())

	ownerCtx := triggerCtx(ownerUID, home)
	memberCtx := triggerCtx(memberUID, home)
	outsiderCtx := triggerCtx(outsiderUID, elsewhere)

	// Bootstrapped for real, so the outsider's NotFound is provably about scoping.
	bootstrapInstance(t, h, outsiderCtx)

	phrase := "vis-phrase-" + suffix
	reply := "vis-reply"
	id := createTriggerVia(t, h, pool, ownerCtx, phrase, reply, 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	t.Run("the creator sees it", func(t *testing.T) {
		resp, err := h.Trigger.GetTrigger(ownerCtx, pb.GetTriggerReq_builder{Id: &id}.Build())
		if err != nil {
			t.Fatalf("owner GetTrigger: %v", err)
		}
		if resp.GetTrigger().GetId() != id {
			t.Errorf("id = %q, want %q", resp.GetTrigger().GetId(), id)
		}
	})

	t.Run("the creator sees it from a direct message too", func(t *testing.T) {
		dmCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID)

		resp, err := h.Trigger.GetTrigger(dmCtx, pb.GetTriggerReq_builder{Id: &id}.Build())
		if err != nil {
			t.Fatalf("owner GetTrigger with no origin: %v", err)
		}
		if resp.GetTrigger().GetId() != id {
			t.Errorf("id = %q, want %q", resp.GetTrigger().GetId(), id)
		}
	})

	t.Run("another member of the same instance sees it", func(t *testing.T) {
		resp, err := h.Trigger.GetTrigger(memberCtx, pb.GetTriggerReq_builder{Id: &id}.Build())
		if err != nil {
			t.Fatalf("GetTrigger by a non-owner on the trigger's own instance: %v", err)
		}
		if resp.GetTrigger().GetId() != id {
			t.Errorf("id = %q, want %q", resp.GetTrigger().GetId(), id)
		}
	})

	t.Run("a caller from another instance gets NotFound", func(t *testing.T) {
		_, err := h.Trigger.GetTrigger(outsiderCtx, pb.GetTriggerReq_builder{Id: &id}.Build())
		requireCode(t, err, connect.CodeNotFound)
	})

	t.Run("a non-owner with no origin at all gets NotFound", func(t *testing.T) {
		dmCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, memberUID)

		_, err := h.Trigger.GetTrigger(dmCtx, pb.GetTriggerReq_builder{Id: &id}.Build())
		requireCode(t, err, connect.CodeNotFound)
	})
}

func TestListTriggersScopesToTheOriginInstanceRatherThanTheCaller(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	authorUID, _ := registeredCaller(t, h, pool, "list-author")
	readerUID, _ := registeredCaller(t, h, pool, "list-reader")
	suffix := uniqueUID("listscope")

	here := callermeta.Origin{InstanceUID: "list-here-" + suffix, DestinationUID: "list-here-dest-" + suffix}
	there := callermeta.Origin{InstanceUID: "list-there-" + suffix, DestinationUID: "list-there-dest-" + suffix}
	cleanupInstanceRows(t, pool, here.InstanceMeta())
	cleanupInstanceRows(t, pool, there.InstanceMeta())

	authorHereCtx := triggerCtx(authorUID, here)
	authorThereCtx := triggerCtx(authorUID, there)
	readerHereCtx := triggerCtx(readerUID, here)

	hereID := createTriggerVia(t, h, pool, authorHereCtx,
		"list-here-phrase-"+suffix, "here", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)
	thereID := createTriggerVia(t, h, pool, authorThereCtx,
		"list-there-phrase-"+suffix, "there", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	resp, err := h.Trigger.ListTriggers(readerHereCtx, pb.ListTriggersReq_builder{}.Build())
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}

	var sawHere, sawThere bool
	for _, trig := range resp.GetTriggers() {
		switch trig.GetId() {
		case hereID:
			sawHere = true
		case thereID:
			sawThere = true
		}
	}

	if !sawHere {
		t.Errorf("ListTriggers from instance %q did not include trigger %s, which is scoped to it and was created by somebody else; the listing is scoped to the caller rather than the instance",
			here.InstanceUID, hereID)
	}
	if sawThere {
		t.Errorf("ListTriggers from instance %q included trigger %s, which belongs to instance %q",
			here.InstanceUID, thereID, there.InstanceUID)
	}
}

// The same regex gate as CreateTrigger by a different door: create ANY, then edit.
func TestUpdateTriggerIntoRegexModeIsGatedBelowModerator(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "regex-update")
	suffix := uniqueUID("regexupdate")

	origin := callermeta.Origin{InstanceUID: "regex-update-" + suffix, DestinationUID: "regex-update-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	// Valid as a regex as well as a literal, so the refusal can only be about clearance.
	phrase := "regex-update-phrase-" + suffix
	id := createTriggerVia(t, h, pool, ctx, phrase, "a reply", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	regex := pb.TriggerMode_TRIGGER_MODE_REGEX

	t.Run("refused below moderator", func(t *testing.T) {
		_, err := h.Trigger.UpdateTrigger(ctx, pb.UpdateTriggerReq_builder{Id: &id, Mode: &regex}.Build())
		requireCode(t, err, connect.CodePermissionDenied)

		var mode int32
		if err := pool.QueryRow(context.Background(),
			`SELECT mode FROM trigger WHERE id = $1`, id,
		).Scan(&mode); err != nil {
			t.Fatalf("read trigger mode: %v", err)
		}
		if mode == int32(pb.TriggerMode_TRIGGER_MODE_REGEX.Number()) {
			t.Error("the trigger is in REGEX mode after a PermissionDenied update")
		}
	})

	t.Run("admitted at moderator", func(t *testing.T) {
		setClearance(t, pool, ownerID, pb.Clearance_CLEARANCE_MODERATOR)

		if _, err := h.Trigger.UpdateTrigger(ctx, pb.UpdateTriggerReq_builder{Id: &id, Mode: &regex}.Build()); err != nil {
			t.Fatalf("UpdateTrigger into REGEX mode as a moderator: %v", err)
		}

		var mode int32
		if err := pool.QueryRow(context.Background(),
			`SELECT mode FROM trigger WHERE id = $1`, id,
		).Scan(&mode); err != nil {
			t.Fatalf("read trigger mode: %v", err)
		}
		if mode != int32(pb.TriggerMode_TRIGGER_MODE_REGEX.Number()) {
			t.Errorf("mode = %d after a moderator's update, want %d (REGEX)",
				mode, pb.TriggerMode_TRIGGER_MODE_REGEX.Number())
		}
	})
}

// Ownership must be checked BEFORE the file fetch, or any caller can drive an outbound
// request and a disk write with any id on the way to being told NotFound.
func TestUpdateTriggerRefusesAnotherUsersRowBeforeFetchingAFile(t *testing.T) {
	const mediaPath = "/ownership-probe.png"

	var fetches atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if _, err := w.Write(pngContent("ownership-probe-payload")); err != nil {
			t.Errorf("media server write for %s: %v", r.URL.Path, err)
		}
	}))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse media server URL %q: %v", server.URL, err)
	}

	fetcher := storage.NewFetcher(server.Client().Transport, []string{parsed.Hostname()}, 0)
	blobs, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}

	h, pool := liveTriggerMediaHarness(t, fetcher, blobs)

	ownerUID, _ := registeredCaller(t, h, pool, "fetch-owner")
	attackerUID, _ := registeredCaller(t, h, pool, "fetch-attacker")
	suffix := uniqueUID("fetchorder")

	origin := callermeta.Origin{InstanceUID: "fetch-order-" + suffix, DestinationUID: "fetch-order-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	ownerCtx := triggerCtx(ownerUID, origin)
	attackerCtx := triggerCtx(attackerUID, origin)

	// Registered before the trigger so LIFO runs it after: fk_trigger_file is NO ACTION.
	var fileID string
	deferFileCleanup(t, pool, &fileID)

	id := createTriggerVia(t, h, pool, ownerCtx,
		"fetch-order-phrase-"+suffix, "original reply", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	fetches.Store(0)

	var filesBefore int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM file`).Scan(&filesBefore); err != nil {
		t.Fatalf("count file rows before: %v", err)
	}

	fileURL := server.URL + mediaPath
	_, err = h.Trigger.UpdateTrigger(attackerCtx, pb.UpdateTriggerReq_builder{
		Id: &id, FileUrl: &fileURL,
	}.Build())
	requireCode(t, err, connect.CodeNotFound)

	if got := fetches.Load(); got != 0 {
		t.Errorf("the media server was called %d times by an update that was refused with NotFound, want 0; "+
			"ownership is being checked after the file fetch, so any caller can drive an outbound request with any id",
			got)
	}

	var filesAfter int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM file`).Scan(&filesAfter); err != nil {
		t.Fatalf("count file rows after: %v", err)
	}
	if filesAfter != filesBefore {
		t.Errorf("file rows went from %d to %d during a refused update, want no change", filesBefore, filesAfter)
	}

	// Without this, an unreachable fetcher would satisfy the 0-fetch assertion for free.
	if _, err := h.Trigger.UpdateTrigger(ownerCtx, pb.UpdateTriggerReq_builder{
		Id: &id, FileUrl: &fileURL,
	}.Build()); err != nil {
		t.Fatalf("the owner's own UpdateTrigger with the same file_url: %v", err)
	}
	if got := fetches.Load(); got == 0 {
		t.Fatal("the owner's own update fetched nothing either; the media server is unreachable, so the 0-fetch assertion above proved nothing")
	}

	getResp, err := h.Trigger.GetTrigger(ownerCtx, pb.GetTriggerReq_builder{Id: &id}.Build())
	if err != nil {
		t.Fatalf("GetTrigger after the owner's update: %v", err)
	}
	fileID = getResp.GetTrigger().GetFile().GetFileId()
	if fileID == "" {
		t.Error("the owner's update stored no file, so the file row cannot be cleaned up")
	}
}
