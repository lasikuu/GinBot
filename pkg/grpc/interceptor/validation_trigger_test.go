package interceptor

import (
	"math"
	"strings"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// validTriggerUUID is a well-formed UUIDv7.
const validTriggerUUID = "018f0000-0000-7000-8000-000000000001"

const undefinedTriggerMode = pb.TriggerMode(99)

// discordInstance is a well-formed TriggerInstance, so a rejection of a list of
// copies can only be about length.
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

func triggerInstances(n int) []*pb.TriggerInstance {
	out := make([]*pb.TriggerInstance, 0, n)
	for range n {
		out = append(out, discordInstance())
	}
	return out
}

// validCreateTrigger passes validation, so a mutation of it is the only fault.
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

// validUpdateTrigger is the UpdateTriggerReq equivalent of validCreateTrigger.
func validUpdateTrigger() *pb.UpdateTriggerReq {
	id := validTriggerUUID
	reply := "boundary-reply"

	return pb.UpdateTriggerReq_builder{
		Id:    &id,
		Reply: &reply,
	}.Build()
}

// urlOfLength builds a plausible CDN URL of exactly n bytes, so a max_len case
// cannot fail a shape rule.
func urlOfLength(n int) string {
	const prefix = "https://cdn.discordapp.com/attachments/"
	if n <= len(prefix) {
		return prefix[:n]
	}
	return prefix + strings.Repeat("a", n-len(prefix))
}

// An unbounded instances list is a query multiplier: one DB round trip per element.
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

			requireValid(t, tt.build(triggerInstances(bound)))

			requireOnlyViolation(t, tt.build(triggerInstances(bound+1)), "instances", "repeated.max_items")
		})
	}
}

// A trigger id is a uuid or a decimal ref (ADR-0039); file_id is not a
// user-facing handle and stays uuid-only, so the two report different rules for
// the same input.
func TestTriggerIdsAcceptOnlyAUUIDOrARef(t *testing.T) {
	const refShaped = "12345"

	// string.uuid is two predefined rules: the empty string reports
	// "string.uuid_empty", everything else "string.uuid".
	const (
		rulePattern   = "string.pattern"
		ruleUUID      = "string.uuid"
		ruleUUIDEmpty = "string.uuid_empty"
	)

	tests := []struct {
		name string
		// field is the path requireOnlyViolation matches the violation against.
		field string
		// rule and emptyRule differ because string.uuid splits the empty string
		// into its own rule id and string.pattern does not.
		rule      string
		emptyRule string
		acceptRef bool
		build     func(id string) proto.Message
	}{
		{"GetTriggerReq.id", "id", rulePattern, rulePattern, true, func(id string) proto.Message {
			return pb.GetTriggerReq_builder{Id: &id}.Build()
		}},
		{"DeleteTriggerReq.id", "id", rulePattern, rulePattern, true, func(id string) proto.Message {
			return pb.DeleteTriggerReq_builder{Id: &id}.Build()
		}},
		{"ExecTriggerReq.id", "id", rulePattern, rulePattern, true, func(id string) proto.Message {
			return pb.ExecTriggerReq_builder{Id: &id, Instance: discordInstance()}.Build()
		}},
		{"UpdateTriggerReq.id", "id", rulePattern, rulePattern, true, func(id string) proto.Message {
			req := validUpdateTrigger()
			req.SetId(id)
			return req
		}},
		{"GetFileReq.file_id", "file_id", ruleUUID, ruleUUIDEmpty, false, func(id string) proto.Message {
			return pb.GetFileReq_builder{FileId: &id}.Build()
		}},
	}

	malformed := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"one character short", "018f0000-0000-7000-8000-00000000000"},
		{"braced form", "{018f0000-0000-7000-8000-000000000001}"},
		{"non-hex digit", "018f0000-0000-7000-8000-00000000000g"},
		{"sql injection attempt", "' OR 1=1 --"},
		{"zero ref", "0"},
		{"negative ref", "-1"},
		{"leading zero ref", "0123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireValid(t, tt.build(validTriggerUUID))
			// string.uuid accepts either case, so the ref pattern must too, or
			// relaxing it would quietly narrow what a caller may send.
			requireValid(t, tt.build(strings.ToUpper(validTriggerUUID)))

			if tt.acceptRef {
				requireValid(t, tt.build(refShaped))
			} else {
				requireOnlyViolation(t, tt.build(refShaped), tt.field, tt.rule)
			}

			for _, bad := range malformed {
				t.Run(bad.name, func(t *testing.T) {
					rule := tt.rule
					if bad.id == "" {
						rule = tt.emptyRule
					}
					requireOnlyViolation(t, tt.build(bad.id), tt.field, rule)
				})
			}
		})
	}
}

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

// In regex mode the phrase runs against every message, so an unbounded one is a DoS surface.
func TestTriggerPhraseLengthIsBounded(t *testing.T) {
	limit := declaredMaxLen(t, validCreateTrigger(), "phrase")

	atLimit := validCreateTrigger()
	atLimit.SetPhrase(strings.Repeat("a", limit))
	requireValid(t, atLimit)

	over := validCreateTrigger()
	over.SetPhrase(strings.Repeat("a", limit+1))
	requireOnlyViolation(t, over, "phrase", "string.max_len")
}

// chance is a percentage; 0 is the "use the default" sentinel, not out of
// range. See ADR-0021.
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

	// protovalidate fuses gte and lte on one numeric field into a single rule id.
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

// TriggerMode carries enum.defined_only but deliberately not enum.not_in = 0:
// TRIGGER_MODE_UNSPECIFIED means "default to TRIGGER_MODE_ANY".
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
			requireValid(t, tt.build(nil))
			requireValid(t, tt.build(&unspecified))
			requireValid(t, tt.build(&anyMode))

			requireOnlyViolation(t, tt.build(&undefined), "mode", "enum.defined_only")
		})
	}
}

// pkg/db clamps limit and offset; an lte rule here would turn that forgiving
// clamp into an InvalidArgument.
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

// GetTriggerStatsReq.limit is clamped by pkg/db in the same way.
func TestTriggerStatsLimitIsNotRejected(t *testing.T) {
	limit := int64(math.MaxInt64)
	req := pb.GetTriggerStatsReq_builder{
		Instance:   discordInstance(),
		ActionType: pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED.Enum(),
		Limit:      &limit,
	}.Build()

	requireValid(t, req)
}
