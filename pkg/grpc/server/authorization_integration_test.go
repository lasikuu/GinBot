//go:build integration

// Authorization tests that need real rows, driven through the bufconn harness
// with the real interceptor chain and a real database.
//
//	docker compose -f docker-compose.psql.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/grpc/server/...
//
// This file exists to close the gaps left by the per-surface integration
// files: it covers the authorization decisions those files' happy paths reach
// past without asserting. Everything it uses is declared elsewhere and is not
// redeclared here — requireDatabase, liveHarness, registerUser, setClearance,
// uniqueUID, cleanupUser (user_integration_test.go); liveReminderHarness,
// withOriginResolver, originFor, destinationFor, cleanupInstanceRows,
// createReminderVia, registeredCaller (reminder_integration_test.go);
// liveTriggerHarness, triggerCtx, triggerInstanceFor, createTriggerVia,
// cleanupTriggerRow, bootstrapInstance (trigger_integration_test.go);
// mediaServer, pngContent, newMediaFetcherAndBlobs, liveTriggerMediaHarness
// (trigger_media_integration_test.go).
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/storage"
	"google.golang.org/grpc/codes"
)

// ── SetLocale / SetTimezone act on the caller and on nobody else ─────────────

// TestSetLocaleAndSetTimezoneTouchOnlyTheCallersRow.
//
// Neither request carries a subject, so the caller IS the subject. The
// existing round-trip test proves the caller's own row changes; what it cannot
// prove is that no OTHER row changed, because it only ever registers one user.
// That is the half that matters here: these are the only two RPCs that write
// to user_account on behalf of an ordinary CLEARANCE_REGISTERED caller, so a
// handler that took a subject from anywhere but metadata would be an
// unauthenticated write to any account.
func TestSetLocaleAndSetTimezoneTouchOnlyTheCallersRow(t *testing.T) {
	h, pool := liveHarness(t)

	callerPlatformUID := uniqueUID("prefs-caller")
	callerID := registerUser(t, h, pool, callerPlatformUID)
	setClearance(t, pool, callerID, pb.Clearance_CLEARANCE_REGISTERED)

	bystanderID := registerUser(t, h, pool, uniqueUID("prefs-bystander"))

	// Both accounts start at "en", registered with an explicit locale, so a
	// write landing on the wrong row is visible rather than being masked by a
	// NULL that could equally mean "never set".
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

// equalStringPtr compares two nullable columns, treating NULL as equal to
// NULL. Written out rather than reached for from a helper package because the
// nil-vs-empty distinction is exactly what is being asserted.
func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ── GetReminder is private to its creator ────────────────────────────────────

// TestGetReminderRefusesAnotherUsersRowWithNotFound.
//
// Update, Delete and ConfirmDelivery all have this test; the plain read did
// not, which is the one an attacker reaches for first. NotFound rather than
// PermissionDenied is the assertion that carries the weight: a reminder body
// is private, and so is the fact that a given id exists at all — a
// PermissionDenied here turns the id space into an oracle for enumerating
// other people's reminders.
func TestGetReminderRefusesAnotherUsersRowWithNotFound(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "get-rem-owner")
	strangerUID, _ := registeredCaller(t, h, pool, "get-rem-stranger")
	suffix := uniqueUID("getrem")

	// Registered BEFORE the reminder exists: CreateReminder writes an
	// action_record attributed to the caller, and action_record.actor_id is
	// ON DELETE SET NULL, so once cleanupUser removes the owner the row can
	// never be found by actor again. Cleaning up by actor has to happen while
	// the actor still exists.
	cleanupActionRecords(t, pool, ownerID)

	id := createReminderVia(t, h, pool, ownerUID, suffix, "")

	// The owner can read it, so the NotFound below is provably about the
	// caller and not about a reminder that was never created.
	ownerCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, ownerUID)
	if _, err := h.Reminder.GetReminder(ownerCtx, pb.GetReminderReq_builder{Id: &id}.Build()); err != nil {
		t.Fatalf("owner GetReminder: %v", err)
	}

	strangerCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, strangerUID)
	_, err := h.Reminder.GetReminder(strangerCtx, pb.GetReminderReq_builder{Id: &id}.Build())
	requireCode(t, err, codes.NotFound)
}

// TestGetReminderForAnUnknownIdIsIndistinguishableFromAnotherUsersRow is the
// other half of the privacy claim, and the only way to state it as a test: the
// two cases must produce the SAME code, or the difference between them is
// itself the leak.
func TestGetReminderForAnUnknownIdIsIndistinguishableFromAnotherUsersRow(t *testing.T) {
	h, pool := liveReminderHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "get-rem-oracle-owner")
	strangerUID, _ := registeredCaller(t, h, pool, "get-rem-oracle-stranger")
	suffix := uniqueUID("getremoracle")

	// See TestGetReminderRefusesAnotherUsersRowWithNotFound: action_record
	// rows must be reclaimed while their actor still exists.
	cleanupActionRecords(t, pool, ownerID)

	existingID := createReminderVia(t, h, pool, ownerUID, suffix, "")
	missingID := "018f0000-0000-7000-8000-0000000000ff"

	strangerCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, strangerUID)

	_, existingErr := h.Reminder.GetReminder(strangerCtx, pb.GetReminderReq_builder{Id: &existingID}.Build())
	_, missingErr := h.Reminder.GetReminder(strangerCtx, pb.GetReminderReq_builder{Id: &missingID}.Build())

	requireCode(t, existingErr, codes.NotFound)
	requireCode(t, missingErr, codes.NotFound)
}

// ── The forced-fire rate limit is keyed on the CALLER ────────────────────────

// TestForcedTriggerRateLimitIsPerCallerNotPerInstance.
//
// pkg/trigger already pins ForcedLimiter's own semantics against a fake clock.
// What is untested — and is an authorization decision rather than a limiter
// one — is the KEY the server passes it: caller.ID, taken from metadata.
//
// Both mistakes it prevents are real. Keying on something per-instance would
// let one user's forced fire silence every other member of the guild for a
// minute. Keying on a request field would let a client spam by varying it, and
// the whole point of the limit is that mentioning the bot cannot be used to
// flood a channel.
//
// Deterministic without touching the clock: ForcedInterval is 60s and these
// calls are milliseconds apart, so the second fire by the same caller is
// certainly inside the window and the two different callers certainly each
// get their first.
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
	// Chance 0: a forced fire bypasses the roll entirely, so any fire observed
	// below can only be the forced path. Without this a chance-100 trigger
	// would fire regardless and the test would prove nothing.
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

	// Premise: a forced fire works at all, and at chance 0.
	if got := tryForced(t, firstCtx); got != id {
		t.Fatalf("first forced TryTrigger returned %q, want %q; the forced path must bypass the chance roll", got, id)
	}

	// The same caller again, immediately: refused, and refusal is silence
	// rather than an error.
	if got := tryForced(t, firstCtx); got != "" {
		t.Errorf("the same caller's second forced fire returned %q, want silence; the rate limit did not apply", got)
	}

	// A DIFFERENT caller on the SAME instance still gets their own first fire.
	if got := tryForced(t, secondCtx); got != id {
		t.Errorf("a second caller's first forced fire returned %q, want %q; the limit is keyed on something wider than the caller",
			got, id)
	}

	// And that second caller is now limited too, independently.
	if got := tryForced(t, secondCtx); got != "" {
		t.Errorf("the second caller's second forced fire returned %q, want silence", got)
	}
}

// ── GetTrigger visibility ────────────────────────────────────────────────────

// TestGetTriggerIsVisibleToItsOwnerAndToTheInstanceItIsScopedTo.
//
// Three callers, one assertion each, because the rule has three outcomes and
// only the first had coverage:
//
//   - the creator sees it from anywhere, including a direct message with no
//     origin at all;
//   - anyone calling from an instance the trigger is scoped to sees it, even
//     though they did not create it — a trigger that can fire in your guild is
//     not a secret from you;
//   - everybody else gets NotFound.
//
// The middle case is what makes /trigger info usable by ordinary members; the
// last is what stops the id space being enumerable across guilds.
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

	// Bootstrapped for real so the outsider's NotFound is provably about
	// scoping rather than about an instance row that never existed.
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
		// Caller identity only, no origin: ownership alone must be enough, or
		// /trigger info in a DM stops working for the person who made it.
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
		requireCode(t, err, codes.NotFound)
	})

	t.Run("a non-owner with no origin at all gets NotFound", func(t *testing.T) {
		// The member above can only see it BECAUSE of where they called from.
		// Strip the origin and the same person must lose access, or visibility
		// is leaking from something other than the instance scope.
		dmCtx := callerCtx(pb.Platform_PLATFORM_DISCORD, memberUID)

		_, err := h.Trigger.GetTrigger(dmCtx, pb.GetTriggerReq_builder{Id: &id}.Build())
		requireCode(t, err, codes.NotFound)
	})
}

// ── ListTriggers scopes to the origin instance ───────────────────────────────

// TestListTriggersScopesToTheOriginInstanceRatherThanTheCaller.
//
// The no-origin fallback already has a regression test, which pins that a
// caller cannot see EVERYTHING. This is the other direction and it is a real
// behaviour, not an implementation detail: with an origin and `mine` unset, the
// listing is the INSTANCE's triggers, including ones the caller did not create
// — that is what /trigger list is for — while triggers belonging to the same
// caller on a different instance must not appear.
//
// Getting this wrong in the obvious way (filter by caller.ID whenever it is
// known) would silently turn /trigger list into "your triggers", and nobody
// would notice until a moderator asked why they could not see the guild's.
// That is now precisely what `mine` asks for, opt-in, which makes the mistake
// easier to make by accident: leaking the owner predicate out of the `mine`
// branch is invisible to every other test.
//
// The instance predicate under test is deliberately the only scoping here, so
// the request leaves `mine` unset.
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

// ── UpdateTrigger: the regex clearance gate applies on the way in too ────────

// TestUpdateTriggerIntoRegexModeIsGatedBelowModerator.
//
// CreateTrigger's regex gate has a test on both sides. UpdateTrigger's did
// not, and it is the same gate reached by a different door: without it, any
// CLEARANCE_REGISTERED caller creates a harmless ANY-mode trigger and then
// edits it into a regex, defeating the create-time check entirely.
//
// The trigger's phrase is left alone and only the MODE is changed, which is
// the cheapest form of the bypass and the one the handler's "effective mode"
// logic exists to catch.
func TestUpdateTriggerIntoRegexModeIsGatedBelowModerator(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, ownerID := registeredCaller(t, h, pool, "regex-update")
	suffix := uniqueUID("regexupdate")

	origin := callermeta.Origin{InstanceUID: "regex-update-" + suffix, DestinationUID: "regex-update-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	// A phrase that is a valid regex as well as a valid literal, so the
	// refusal below can only be about clearance and never about the pattern
	// failing to compile.
	phrase := "regex-update-phrase-" + suffix
	id := createTriggerVia(t, h, pool, ctx, phrase, "a reply", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	regex := pb.TriggerMode_TRIGGER_MODE_REGEX

	t.Run("refused below moderator", func(t *testing.T) {
		_, err := h.Trigger.UpdateTrigger(ctx, pb.UpdateTriggerReq_builder{Id: &id, Mode: &regex}.Build())
		requireCode(t, err, codes.PermissionDenied)

		// The refusal must not have applied anyway.
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
		// The other side of the boundary, so the test cannot pass because
		// UpdateTrigger refuses this request for some unrelated reason.
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

// ── UpdateTrigger checks ownership before it fetches anything ────────────────

// TestUpdateTriggerRefusesAnotherUsersRowBeforeFetchingAFile is the assertion
// UpdateTrigger's own doc comment makes and that nothing checked.
//
// That the update is refused with NotFound is already covered. What is not is
// WHERE the refusal happens. Deferring the ownership check until after the
// file fetch — which is where it naturally ends up if you write the handler in
// request-field order — leaves a caller who owns nothing able to name any
// trigger id plus any file_url and still cause the server to download that
// URL, insert a file row and write a blob to disk, on its way to being told
// NotFound. That is an unauthenticated outbound request and an unauthenticated
// disk write, from an RPC that returns an error.
//
// The media server counts its own requests, so "no fetch happened" is
// observed rather than assumed.
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

	// Registered BEFORE the trigger exists, so t.Cleanup's LIFO order runs it
	// AFTER the trigger row has been deleted — fk_trigger_file is NO ACTION
	// and would otherwise reject the delete. The id is read through the
	// pointer at cleanup time, since the owner's update below is what creates
	// the row.
	var fileID string
	deferFileCleanup(t, pool, &fileID)

	id := createTriggerVia(t, h, pool, ownerCtx,
		"fetch-order-phrase-"+suffix, "original reply", 10, pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED)

	// Anything the setup above happened to fetch is not what this test is
	// measuring; only what the refused update does counts.
	fetches.Store(0)

	var filesBefore int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM file`).Scan(&filesBefore); err != nil {
		t.Fatalf("count file rows before: %v", err)
	}

	fileURL := server.URL + mediaPath
	_, err = h.Trigger.UpdateTrigger(attackerCtx, pb.UpdateTriggerReq_builder{
		Id: &id, FileUrl: &fileURL,
	}.Build())
	requireCode(t, err, codes.NotFound)

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

	// The owner's own update through the same URL must still work. Without
	// this, a fetcher that was simply unreachable — a mis-parsed host, a
	// transport the allow-list rejects — would satisfy the "0 fetches"
	// assertion above while proving nothing at all.
	if _, err := h.Trigger.UpdateTrigger(ownerCtx, pb.UpdateTriggerReq_builder{
		Id: &id, FileUrl: &fileURL,
	}.Build()); err != nil {
		t.Fatalf("the owner's own UpdateTrigger with the same file_url: %v", err)
	}
	if got := fetches.Load(); got == 0 {
		t.Fatal("the owner's own update fetched nothing either; the media server is unreachable, so the 0-fetch assertion above proved nothing")
	}

	// Capture the file the owner's update created so the cleanup registered
	// above can remove it.
	getResp, err := h.Trigger.GetTrigger(ownerCtx, pb.GetTriggerReq_builder{Id: &id}.Build())
	if err != nil {
		t.Fatalf("GetTrigger after the owner's update: %v", err)
	}
	fileID = getResp.GetTrigger().GetFile().GetFileId()
	if fileID == "" {
		t.Error("the owner's update stored no file, so the file row cannot be cleaned up")
	}
}
