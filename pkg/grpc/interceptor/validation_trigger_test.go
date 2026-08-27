package interceptor

import (
	"math"
	"strings"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// Boundary tests for the buf.validate rules on trigger.proto.
//
// See validation_rules_test.go for why these validate directly rather than
// through the interceptor, and for the end-to-end counterparts that do go
// through the whole chain.

// validTriggerUUID is a well-formed UUIDv7, the shape every trigger and file id
// in this schema actually has.
const validTriggerUUID = "018f0000-0000-7000-8000-000000000001"

// undefinedTriggerMode is a number TriggerMode does not declare. Far outside
// the declared range (0..3) rather than one past the end, so adding a mode
// cannot quietly make these tests stop testing anything.
const undefinedTriggerMode = pb.TriggerMode(99)

// discordInstance is a well-formed TriggerInstance: a defined, non-unspecified
// platform and a present instance_meta. Every list boundary below is built from
// copies of this, so a rejection can only be about the list's length.
func discordInstance() *pb.TriggerInstance {
	return pb.TriggerInstance_builder{
		PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
		InstanceMeta: mustStruct(map[string]any{"instance_uid": "guild-1"}),
	}.Build()
}

func mustStruct(fields map[string]any) *structpb.Struct {
	s, err := structpb.NewStruct(fields)
	if err != nil {
		panic(err)
	}
	return s
}

// triggerInstances builds n distinct, individually valid instances.
func triggerInstances(n int) []*pb.TriggerInstance {
	out := make([]*pb.TriggerInstance, 0, n)
	for range n {
		out = append(out, discordInstance())
	}
	return out
}

// validCreateTrigger is a CreateTriggerReq that must pass validation, so that
// every mutation below is the only thing wrong with the request.
func validCreateTrigger() *pb.CreateTriggerReq {
	phrase := "boundary-phrase"
	reply := "boundary-reply"
	chance := int32(50)
	mode := pb.TriggerMode_TRIGGER_MODE_ANY

	return pb.CreateTriggerReq_builder{
		Phrase: &phrase,
		Reply:  &reply,
		Chance: &chance,
		Mode:   &mode,
	}.Build()
}

// validUpdateTrigger is the UpdateTriggerReq equivalent.
func validUpdateTrigger() *pb.UpdateTriggerReq {
	id := validTriggerUUID
	reply := "boundary-reply"

	return pb.UpdateTriggerReq_builder{
		Id:    &id,
		Reply: &reply,
	}.Build()
}

// urlOfLength builds a plausible CDN URL of exactly n bytes. Padding a real
// prefix rather than repeating a filler character keeps the fixture valid
// against any shape rule file_url may also carry, so a max_len case cannot pass
// for the wrong reason.
func urlOfLength(n int) string {
	const prefix = "https://cdn.discordapp.com/attachments/"
	if n <= len(prefix) {
		return prefix[:n]
	}
	return prefix + strings.Repeat("a", n-len(prefix))
}

// The bound that matters most. TriggerServer.resolveScopeInstances loops over
// this list making a database round trip per element, so an unbounded list is a
// query multiplier any CLEARANCE_REGISTERED caller can reach. The bound is read
// from the schema, never restated here.
func TestTriggerInstancesListIsBounded(t *testing.T) {
	tests := []struct {
		name  string
		build func(instances []*pb.TriggerInstance) proto.Message
	}{
		{
			name: "CreateTriggerReq",
			build: func(instances []*pb.TriggerInstance) proto.Message {
				req := validCreateTrigger()
				req.SetInstances(instances)
				return req
			},
		},
		{
			name: "UpdateTriggerReq",
			build: func(instances []*pb.TriggerInstance) proto.Message {
				req := validUpdateTrigger()
				req.SetInstances(instances)
				return req
			},
		},
		{
			name: "ListTriggersReq",
			build: func(instances []*pb.TriggerInstance) proto.Message {
				req := pb.ListTriggersReq_builder{}.Build()
				req.SetInstances(instances)
				return req
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bound := declaredMaxItems(t, tt.build(nil), "instances")
			if bound < 1 {
				t.Fatalf("repeated.max_items = %d, which would refuse every list", bound)
			}

			// Exactly at the bound is legal: a rule that refused this would
			// break scoping a trigger to every instance a caller may name.
			requireValid(t, tt.build(triggerInstances(bound)))

			requireOnlyViolation(t, tt.build(triggerInstances(bound+1)), "instances", "repeated.max_items")
		})
	}
}

// A malformed id must be refused before it reaches Postgres. This is a
// behaviour change worth pinning: such an id previously reached the database and
// came back as NotFound, or as an Internal from a failed uuid cast, so the
// caller could not tell a typo from a missing row.
func TestTriggerIdsMustBeUUIDs(t *testing.T) {
	valid := validTriggerUUID

	tests := []struct {
		name  string
		field string
		build func(id string) proto.Message
	}{
		{"GetTriggerReq.id", "id", func(id string) proto.Message {
			return pb.GetTriggerReq_builder{Id: &id}.Build()
		}},
		{"DeleteTriggerReq.id", "id", func(id string) proto.Message {
			return pb.DeleteTriggerReq_builder{Id: &id}.Build()
		}},
		{"ExecTriggerReq.id", "id", func(id string) proto.Message {
			return pb.ExecTriggerReq_builder{Id: &id, Instance: discordInstance()}.Build()
		}},
		{"UpdateTriggerReq.id", "id", func(id string) proto.Message {
			req := validUpdateTrigger()
			req.SetId(id)
			return req
		}},
		{"GetFileReq.file_id", "file_id", func(id string) proto.Message {
			return pb.GetFileReq_builder{FileId: &id}.Build()
		}},
	}

	// string.uuid is declared as TWO predefined rules, not one, so the rule id a
	// rejection reports depends on the input: an empty string reports
	// "string.uuid_empty" ("value is empty, which is not a valid UUID") and
	// everything else reports "string.uuid". Verified against the real validator
	// rather than assumed — see buf/validate/validate.proto's predefined CEL on
	// StringRules.uuid, where the non-empty expression short-circuits on
	// `this == ''` precisely so the empty case can carry its own message.
	//
	// The distinction is asserted rather than papered over with "either id will
	// do": accepting both would stop this test noticing if the schema lost the
	// rule on a field and gained a min_len somewhere instead.
	const (
		ruleUUID      = "string.uuid"
		ruleUUIDEmpty = "string.uuid_empty"
	)

	malformed := []struct {
		name string
		id   string
		rule string
	}{
		{"not a uuid at all", "12345", ruleUUID},
		{"empty", "", ruleUUIDEmpty},
		// The shapes a real client typo produces, and the shapes a SQL cast
		// would have choked on.
		{"one character short", "018f0000-0000-7000-8000-00000000000", ruleUUID},
		{"braced form", "{018f0000-0000-7000-8000-000000000001}", ruleUUID},
		{"non-hex digit", "018f0000-0000-7000-8000-00000000000g", ruleUUID},
		{"sql injection attempt", "' OR 1=1 --", ruleUUID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireValid(t, tt.build(valid))

			for _, bad := range malformed {
				t.Run(bad.name, func(t *testing.T) {
					requireOnlyViolation(t, tt.build(bad.id), tt.field, bad.rule)
				})
			}
		})
	}
}

// file_url is fetched by the server, so its length is bounded. Exactly at the
// limit must still be accepted: platform CDN URLs are long and signed, and a
// limit that refused a real one would make trigger media unusable.
func TestTriggerFileURLLengthIsBounded(t *testing.T) {
	tests := []struct {
		name  string
		build func(fileURL string) proto.Message
	}{
		{"CreateTriggerReq", func(fileURL string) proto.Message {
			req := validCreateTrigger()
			req.SetFileUrl(fileURL)
			return req
		}},
		{"UpdateTriggerReq", func(fileURL string) proto.Message {
			req := validUpdateTrigger()
			req.SetFileUrl(fileURL)
			return req
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := declaredMaxLen(t, tt.build(""), "file_url")

			requireValid(t, tt.build(urlOfLength(limit)))
			requireOnlyViolation(t, tt.build(urlOfLength(limit+1)), "file_url", "string.max_len")
		})
	}
}

// phrase is compiled into a pattern and, in regex mode, run against every
// message on the instance, so an unbounded one is a denial-of-service surface.
func TestTriggerPhraseLengthIsBounded(t *testing.T) {
	limit := declaredMaxLen(t, validCreateTrigger(), "phrase")

	atLimit := validCreateTrigger()
	atLimit.SetPhrase(strings.Repeat("a", limit))
	requireValid(t, atLimit)

	over := validCreateTrigger()
	over.SetPhrase(strings.Repeat("a", limit+1))
	requireOnlyViolation(t, over, "phrase", "string.max_len")
}

// chance is a percentage, and 0 is NOT out of range: it is the "use the
// default" sentinel carried over from the old bot (trigger.DefaultChance, ADR
// 0021). A rule that rejected 0 would refuse every trigger created without an
// explicit chance; worse, a rule that silently rewrote it would overwrite tuned
// chances on update.
func TestTriggerChanceRange(t *testing.T) {
	tests := []struct {
		name  string
		build func(chance int32) proto.Message
	}{
		{"CreateTriggerReq", func(chance int32) proto.Message {
			req := validCreateTrigger()
			req.SetChance(chance)
			return req
		}},
		{"UpdateTriggerReq", func(chance int32) proto.Message {
			req := validUpdateTrigger()
			req.SetChance(chance)
			return req
		}},
	}

	accepted := []struct {
		name   string
		chance int32
	}{
		{"zero is the use-the-default sentinel", 0},
		{"one", 1},
		{"fifty", 50},
		{"one hundred is certainty", 100},
	}

	// protovalidate FUSES gte and lte on the same numeric field into a single
	// rule whose id is "int32.gte_lte" — verified against the real validator,
	// not assumed. Asserting the fused id is deliberately stricter than
	// accepting either half: a schema that declared only lte would report
	// "int32.lte" here and fail, which is the correct answer, because 0 being
	// the legal sentinel is only meaningful if the lower bound exists too.
	const rangeRule = "int32.gte_lte"

	rejected := []struct {
		name   string
		chance int32
	}{
		{"one over the top", 101},
		{"far over the top", math.MaxInt32},
		{"negative", -1},
		{"far negative", math.MinInt32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, ok := range accepted {
				t.Run(ok.name, func(t *testing.T) {
					requireValid(t, tt.build(ok.chance))
				})
			}
			for _, bad := range rejected {
				t.Run(bad.name, func(t *testing.T) {
					requireOnlyViolation(t, tt.build(bad.chance), "chance", rangeRule)
				})
			}
		})
	}
}

// TriggerMode carries enum.defined_only but deliberately NOT enum.not_in = 0:
// TRIGGER_MODE_UNSPECIFIED legitimately means "default to TRIGGER_MODE_ANY",
// which is what CreateTrigger and UpdateTrigger both implement. Getting this
// backwards would break every trigger created without an explicit mode.
func TestTriggerModeAcceptsUnspecifiedAndRefusesUndefined(t *testing.T) {
	tests := []struct {
		name  string
		build func(mode *pb.TriggerMode) proto.Message
	}{
		{"CreateTriggerReq", func(mode *pb.TriggerMode) proto.Message {
			req := validCreateTrigger()
			if mode == nil {
				req.ClearMode()
				return req
			}
			req.SetMode(*mode)
			return req
		}},
		{"UpdateTriggerReq", func(mode *pb.TriggerMode) proto.Message {
			req := validUpdateTrigger()
			if mode == nil {
				req.ClearMode()
				return req
			}
			req.SetMode(*mode)
			return req
		}},
		{"ListTriggersReq", func(mode *pb.TriggerMode) proto.Message {
			req := pb.ListTriggersReq_builder{}.Build()
			if mode == nil {
				return req
			}
			req.SetMode(*mode)
			return req
		}},
	}

	unspecified := pb.TriggerMode_TRIGGER_MODE_UNSPECIFIED
	anyMode := pb.TriggerMode_TRIGGER_MODE_ANY
	undefined := undefinedTriggerMode

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Absent: the client simply did not choose a mode.
			requireValid(t, tt.build(nil))
			// Explicitly the zero value: the client chose "the default".
			requireValid(t, tt.build(&unspecified))
			// An ordinary mode, so the rule is not refusing everything.
			requireValid(t, tt.build(&anyMode))

			requireOnlyViolation(t, tt.build(&undefined), "mode", "enum.defined_only")
		})
	}
}

// No cap is placed on limit or offset, and that is the decision under test.
//
// pkg/db already clamps both (defaultTriggerListLimit / maxTriggerListLimit,
// and a negative offset to 0). Adding an lte rule here would convert a
// forgiving clamp into an InvalidArgument for a caller who typed a big number,
// which is a regression for real users, not a hardening. This test is what a
// future well-meaning `lte` breaks.
func TestTriggerListLimitAndOffsetAreNotRejected(t *testing.T) {
	tests := []struct {
		name   string
		limit  int64
		offset int64
	}{
		{"absurd limit", math.MaxInt64, 0},
		{"absurd offset", 50, math.MaxInt64},
		{"both absurd", math.MaxInt64, math.MaxInt64},
		{"negative limit", -1, 0},
		{"negative offset", 50, -1},
		{"zero means the default", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := pb.ListTriggersReq_builder{Limit: &tt.limit, Offset: &tt.offset}.Build()
			requireValid(t, req)
		})
	}
}

// GetTriggerStatsReq.limit is clamped by pkg/db in the same way, for the same
// reason.
func TestTriggerStatsLimitIsNotRejected(t *testing.T) {
	limit := int64(math.MaxInt64)
	req := pb.GetTriggerStatsReq_builder{
		Instance:   discordInstance(),
		ActionType: pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED.Enum(),
		Limit:      &limit,
	}.Build()

	requireValid(t, req)
}
