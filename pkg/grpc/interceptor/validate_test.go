package interceptor

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// invoke runs req through connectvalidate's interceptor, reporting whether the
// handler was reached and what error surfaced.
func invoke(t *testing.T, req any) (reached bool, err error) {
	t.Helper()

	intercept := connectvalidate.NewInterceptor()

	handler := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		reached = true
		return newFakeResponse(), nil
	})

	fake := newFakeRequest(registeredMethod)
	fake.msg = req

	_, err = intercept.WrapUnary(handler)(context.Background(), fake)
	return reached, err
}

func futureReminder(t *testing.T, repeatCron string) *pb.CreateReminderReq {
	t.Helper()
	timezone := "UTC"
	message := "test"
	builder := pb.CreateReminderReq_builder{
		Datetime: timestamppb.New(time.Now().Add(time.Hour)),
		Timezone: &timezone,
		Message:  &message,
	}
	if repeatCron != "" {
		builder.RepeatCron = &repeatCron
	}
	return builder.Build()
}

func TestValidDatetimePassesValidation(t *testing.T) {
	reached, err := invoke(t, futureReminder(t, ""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reached {
		t.Error("handler was not reached")
	}
}

func TestPastDatetimeIsRejected(t *testing.T) {
	req := pb.CreateReminderReq_builder{
		Datetime: timestamppb.New(time.Now().Add(-time.Hour)),
	}.Build()

	reached, err := invoke(t, req)
	if err == nil {
		t.Fatal("expected past datetime to be rejected")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
	}
	if reached {
		t.Error("handler was reached despite invalid request")
	}
}

func TestMissingDatetimeIsRejected(t *testing.T) {
	req := pb.CreateReminderReq_builder{}.Build()

	if _, err := invoke(t, req); err == nil {
		t.Fatal("expected missing datetime to be rejected")
	}
}

// RE2 has no /.../ delimiters: a pattern written with them rejects everything.
func TestRepeatCronAcceptsValidExpressions(t *testing.T) {
	valid := []string{
		"0 9 * * *",
		"*/15 * * * *",
		"0 0 1 * *",
		"30 8 * * 1-5",
		"0 0 1 1 *",
		"0 0 1 1 * 2026",  // optional sixth field
		"1,15,30 * * * *", // lists
		"0 */2 * * *",     // step on a wildcard
		"0 0 1-5/2 * *",   // step on a range
		"0 9 * * MON",     // day-of-week name
		"0 9 * * MON-FRI", // day-of-week range by name
		"5 4 * * sun",     // lowercase name
		"0 0 * JAN *",     // month name
		"0\t9\t*\t*\t*",   // tab separated
		"@daily",
		"@weekly",
		"@annually",
		"@midnight",
		"@reboot",
		"@every 1h",
		"@every 30m",
		"@every 1h30m",
	}

	for _, cron := range valid {
		t.Run(cron, func(t *testing.T) {
			reached, err := invoke(t, futureReminder(t, cron))
			if err != nil {
				t.Fatalf("valid cron %q rejected: %v", cron, err)
			}
			if !reached {
				t.Error("handler was not reached")
			}
		})
	}
}

func TestRepeatCronRejectsGarbage(t *testing.T) {
	invalid := []string{
		"not a cron",
		"@hourlyish",
		"* * *",            // too few fields
		"0 9 * *",          // one field short
		"0 9 * * * * *",    // too many fields
		"@every",           // no duration
		"@every abc",       // unparseable duration
		"/0 9 * * */",      // the old /.../ delimiter form
		"*-5 * * * *",      // a range cannot start at a wildcard
		"0 9 * * ; rm -rf", // shell metacharacters
	}

	for _, cron := range invalid {
		t.Run(cron, func(t *testing.T) {
			if _, err := invoke(t, futureReminder(t, cron)); err == nil {
				t.Errorf("invalid cron %q was accepted", cron)
			}
		})
	}
}

// The pattern is a shape check; the cron parser range-checks at scheduling time.
func TestRepeatCronDoesNotRangeCheckValues(t *testing.T) {
	for _, cron := range []string{"99 99 99 99 99", "0 0 0 0 0"} {
		t.Run(cron, func(t *testing.T) {
			if _, err := invoke(t, futureReminder(t, cron)); err != nil {
				t.Errorf("expected %q to pass the shape check, got: %v", cron, err)
			}
		})
	}
}

func TestParentIDMustBeUUID(t *testing.T) {
	req := futureReminder(t, "")

	notAUUID := "12345"
	withBadParent := pb.CreateReminderReq_builder{
		Datetime: req.GetDatetime(),
		ParentId: &notAUUID,
	}.Build()

	if _, err := invoke(t, withBadParent); err == nil {
		t.Error("expected a non-UUID parent_id to be rejected")
	}

	validUUID := "018f0000-0000-7000-8000-000000000001"
	withGoodParent := pb.CreateReminderReq_builder{
		Datetime: req.GetDatetime(),
		ParentId: &validUUID,
	}.Build()

	if _, err := invoke(t, withGoodParent); err != nil {
		t.Errorf("valid UUID parent_id rejected: %v", err)
	}
}

func TestUnconstrainedMessagePassesThrough(t *testing.T) {
	digits := int32(2)
	reqType := pb.GetRandomNumberReq_DOUBLES
	req := pb.GetRandomNumberReq_builder{
		Type:   &reqType,
		Digits: &digits,
	}.Build()

	reached, err := invoke(t, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reached {
		t.Error("handler was not reached")
	}
}
