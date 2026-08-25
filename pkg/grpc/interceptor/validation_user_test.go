package interceptor

import (
	"strings"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// These exercise the buf.validate constraints on SetLocaleReq and
// SetTimezoneReq through the validation interceptor. They reuse the invoke
// helper from validate_test.go.

func setLocale(locale string) *pb.SetLocaleReq {
	return pb.SetLocaleReq_builder{Locale: &locale}.Build()
}

func setTimezone(timezone string) *pb.SetTimezoneReq {
	return pb.SetTimezoneReq_builder{Timezone: &timezone}.Build()
}

func TestSetLocaleAcceptsSupportedLocales(t *testing.T) {
	// The set the bot actually ships translations for.
	for _, locale := range []string{"en", "fi", "ja"} {
		t.Run(locale, func(t *testing.T) {
			reached, err := invoke(t, setLocale(locale))
			if err != nil {
				t.Fatalf("locale %q rejected: %v", locale, err)
			}
			if !reached {
				t.Error("handler was not reached")
			}
		})
	}
}

func TestSetLocaleRejectsEverythingElse(t *testing.T) {
	tests := []struct {
		name   string
		locale string
	}{
		{"uppercase", "EN"},
		{"region qualified", "en-US"},
		{"underscore region", "en_US"},
		{"unsupported language", "de"},
		{"empty", ""},
		{"leading space", " en"},
		{"trailing space", "en "},
		// The pattern is anchored; RE2 anchors bind to the end of the text and
		// not to the end of a line, so a trailing newline must not slip through.
		{"trailing newline", "en\n"},
		{"two locales concatenated", "enfi"},
		{"prefix of a supported locale", "e"},
		{"long garbage", strings.Repeat("a", 200)},
		{"path traversal", "../../etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached, err := invoke(t, setLocale(tt.locale))
			if err == nil {
				t.Fatalf("locale %q was accepted", tt.locale)
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
			}
			if reached {
				t.Error("handler was reached despite an invalid locale")
			}
		})
	}
}

// The field carries no `required` rule, so an unset locale passes validation
// and the handler is the one that has to decide what an absent locale means.
func TestSetLocaleUnsetPassesValidation(t *testing.T) {
	req := pb.SetLocaleReq_builder{}.Build()

	reached, err := invoke(t, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reached {
		t.Error("handler was not reached")
	}
	if req.HasLocale() {
		t.Error("locale reported as set on an empty builder")
	}
}

// SetTimezoneReq declares only min_len = 1. This test pins that gap so it is a
// recorded decision rather than an assumption: every timezone string that is
// not empty reaches the handler, and resolving it against the IANA database is
// the handler's job.
func TestSetTimezoneValidationOnlyRejectsEmpty(t *testing.T) {
	tests := []struct {
		name         string
		timezone     string
		wantRejected bool
	}{
		{"empty", "", true},
		{"valid iana name", "Europe/Helsinki", false},
		{"utc", "UTC", false},
		// Not a zone, but min_len cannot tell.
		{"unknown region", "Mars/Olympus", false},
		{"fixed offset", "+02:00", false},
		{"single space", " ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached, err := invoke(t, setTimezone(tt.timezone))

			if tt.wantRejected {
				if err == nil {
					t.Fatalf("timezone %q was accepted by validation", tt.timezone)
				}
				if got := status.Code(err); got != codes.InvalidArgument {
					t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
				}
				return
			}

			if err != nil {
				t.Fatalf("timezone %q rejected by validation: %v", tt.timezone, err)
			}
			if !reached {
				t.Error("handler was not reached")
			}
		})
	}
}
