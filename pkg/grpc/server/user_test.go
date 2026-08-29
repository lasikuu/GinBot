package server

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// The ids are UUIDs because GetUserReq validates them as such.
const (
	callerUID    = "discord-caller"
	callerUserID = "018f0000-0000-7000-8000-00000000000a"
	strangerUID  = "discord-stranger"
)

func registeredHarness(t *testing.T, clearance pb.Clearance) (*harness, *directory) {
	t.Helper()

	dir := newDirectory().add(pb.Platform_PLATFORM_DISCORD, callerUID, testUser(callerUserID, clearance))
	return newHarness(t, withDirectory(dir)), dir
}

func TestPublicMethodsNeedNoMetadata(t *testing.T) {
	h, dir := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	t.Run("HealthCheck", func(t *testing.T) {
		// The opaque API gives an empty message no usable literal, so it comes from its builder.
		resp, err := h.Utility.HealthCheck(anonymousCtx(), pb.HealthCheckReq_builder{}.Build())
		if err != nil {
			t.Fatalf("HealthCheck rejected an anonymous caller: %v", err)
		}
		if resp.GetStatus() == pb.HealthStatus_HEALTH_STATUS_UNSPECIFIED {
			t.Error("health status is unspecified")
		}
	})

	t.Run("GetRandomNumber", func(t *testing.T) {
		reqType := pb.GetRandomNumberReq_DOUBLES
		digits := int32(3)
		req := pb.GetRandomNumberReq_builder{Type: &reqType, Digits: &digits}.Build()

		resp, err := h.Entertainment.GetRandomNumber(anonymousCtx(), req)
		if err != nil {
			t.Fatalf("GetRandomNumber rejected an anonymous caller: %v", err)
		}
		if len(resp.GetNumber()) != 3 {
			t.Errorf("number = %q, want 3 digits", resp.GetNumber())
		}
	})

	// Resolving on a public method would fail for exactly the people who call it.
	if n := dir.resolveCount(); n != 0 {
		t.Errorf("caller resolved %d times on public methods, want 0", n)
	}
}

// The client measures latency around Ping, so it has to answer without an account.
func TestPingIsPublicAndAnswers(t *testing.T) {
	h, dir := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	resp, err := h.Utility.Ping(anonymousCtx(), pb.PingReq_builder{}.Build())
	if err != nil {
		t.Fatalf("Ping rejected an anonymous caller: %v", err)
	}
	if resp.GetMessage() == "" {
		t.Error("ping answered with an empty message")
	}
	if n := dir.resolveCount(); n != 0 {
		t.Errorf("caller resolved %d times on Ping, want 0", n)
	}
}

func TestGuardedMethodWithoutMetadataIsRejected(t *testing.T) {
	h, dir := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	id := callerUserID
	_, err := h.User.GetUser(anonymousCtx(), pb.GetUserReq_builder{Id: &id}.Build())

	requireCode(t, err, connect.CodeInvalidArgument)
	if n := dir.resolveCount(); n != 0 {
		t.Errorf("caller resolved %d times without metadata, want 0", n)
	}
}

// The client turns FailedPrecondition into "run /register first".
func TestUnregisteredCallerIsRejectedWithFailedPrecondition(t *testing.T) {
	h, _ := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	id := callerUserID
	_, err := h.User.GetUser(callerCtx(pb.Platform_PLATFORM_DISCORD, strangerUID), pb.GetUserReq_builder{Id: &id}.Build())

	requireCode(t, err, connect.CodeFailedPrecondition)
}

func TestCallerIsResolvedPerPlatform(t *testing.T) {
	h, _ := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	id := callerUserID
	_, err := h.User.GetUser(callerCtx(pb.Platform_PLATFORM_MATRIX_PROTOCOL, callerUID), pb.GetUserReq_builder{Id: &id}.Build())

	requireCode(t, err, connect.CodeFailedPrecondition)
}

func TestInstanceMutationIsRejectedBelowAdministrator(t *testing.T) {
	// MODERATOR is the closest level below ADMINISTRATOR, so this pins the boundary.
	for _, clearance := range []pb.Clearance{
		pb.Clearance_CLEARANCE_REGISTERED,
		pb.Clearance_CLEARANCE_MEMBER,
		pb.Clearance_CLEARANCE_MODERATOR,
	} {
		t.Run(clearance.String(), func(t *testing.T) {
			h, _ := registeredHarness(t, clearance)

			id := int64(1)
			_, err := h.Instance.DeleteInstance(
				callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
				pb.DeleteInstanceReq_builder{Id: &id}.Build(),
			)

			requireCode(t, err, connect.CodePermissionDenied)
		})
	}
}

// This harness has no database, so only "the chain did not reject it" is asserted.
func TestInstanceMutationIsAdmittedAtAdministrator(t *testing.T) {
	for _, clearance := range []pb.Clearance{
		pb.Clearance_CLEARANCE_ADMINISTRATOR,
		pb.Clearance_CLEARANCE_OWNER,
	} {
		t.Run(clearance.String(), func(t *testing.T) {
			h, dir := registeredHarness(t, clearance)

			id := int64(1)
			_, err := h.Instance.DeleteInstance(
				callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
				pb.DeleteInstanceReq_builder{Id: &id}.Build(),
			)

			requireNotCode(t, err, connect.CodePermissionDenied, connect.CodeFailedPrecondition, connect.CodeInvalidArgument)
			if n := dir.resolveCount(); n != 1 {
				t.Errorf("caller resolved %d times, want exactly 1 per call", n)
			}
		})
	}
}

// A reminder scheduled in a zone the Go runtime cannot resolve can never fire.
func TestSetTimezoneRejectsUnresolvableNames(t *testing.T) {
	// Without a zoneinfo database every name fails and the test asserts nothing.
	if _, err := time.LoadLocation("Europe/Helsinki"); err != nil {
		t.Skipf("no zoneinfo database available: %v", err)
	}

	tests := []struct {
		name         string
		timezone     string
		wantRejected bool
	}{
		{"iana europe", "Europe/Helsinki", false},
		{"utc", "UTC", false},
		{"iana america", "America/New_York", false},
		{"unknown region", "Mars/Olympus", true},
		{"misspelled city", "Europe/Helsinky", true},
		{"empty", "", true},
		{"single space", " ", true},
		{"fixed offset", "+02:00", true},
		// Zone lookup is a file lookup, so it is case sensitive on Linux.
		{"wrong case", "europe/helsinki", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// time.LoadLocation("") resolves to UTC, which is why the field carries min_len = 1.
			if tt.timezone != "" {
				if _, err := time.LoadLocation(tt.timezone); (err != nil) != tt.wantRejected {
					t.Fatalf("table disagrees with time.LoadLocation for %q", tt.timezone)
				}
			}

			h, _ := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

			_, err := h.User.SetTimezone(
				callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
				pb.SetTimezoneReq_builder{Timezone: &tt.timezone}.Build(),
			)

			if tt.wantRejected {
				requireCode(t, err, connect.CodeInvalidArgument)
				return
			}

			// The write itself needs a database this harness does not have.
			requireNotCode(t, err, connect.CodeInvalidArgument, connect.CodePermissionDenied, connect.CodeFailedPrecondition)
		})
	}
}

func TestSetLocaleRejectionsSurviveTheChain(t *testing.T) {
	tests := []struct {
		name         string
		locale       string
		wantRejected bool
	}{
		{"english", "en", false},
		{"finnish", "fi", false},
		{"japanese", "ja", false},
		{"uppercase", "EN", true},
		{"region qualified", "en-US", true},
		{"unsupported", "de", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

			_, err := h.User.SetLocale(
				callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
				pb.SetLocaleReq_builder{Locale: &tt.locale}.Build(),
			)

			if tt.wantRejected {
				requireCode(t, err, connect.CodeInvalidArgument)
				return
			}

			requireNotCode(t, err, connect.CodeInvalidArgument, connect.CodePermissionDenied, connect.CodeFailedPrecondition)
		})
	}
}

func TestGuardedCallResolvesTheCallerOnce(t *testing.T) {
	h, dir := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	locale := "en"
	_, _ = h.User.SetLocale(
		callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
		pb.SetLocaleReq_builder{Locale: &locale}.Build(),
	)

	if n := dir.resolveCount(); n != 1 {
		t.Errorf("caller resolved %d times, want exactly 1", n)
	}
}

// Validation first, so an unparseable request costs no caller-resolution round trip.
func TestValidationRunsBeforeCallerResolution(t *testing.T) {
	h, dir := registeredHarness(t, pb.Clearance_CLEARANCE_REGISTERED)

	notAUUID := "12345"
	_, err := h.User.GetUser(
		callerCtx(pb.Platform_PLATFORM_DISCORD, callerUID),
		pb.GetUserReq_builder{Id: &notAUUID}.Build(),
	)

	requireCode(t, err, connect.CodeInvalidArgument)
	if n := dir.resolveCount(); n != 0 {
		t.Errorf("caller resolved %d times for an unparseable request, want 0", n)
	}
}
