package interceptor

import (
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The three-rule enum contract, applied to every enum field that carries it.
//
// The rules are `required`, `enum.defined_only` and `enum.not_in = 0`, and the
// reason all three are needed is not the intuitive one. Established empirically
// against the real validator, and documented on
// OpenClientActionStreamReq.platform_enum in reverse.proto:
//
//   - `required` is a PRESENCE check. Every field here has explicit presence,
//     so a client that explicitly sets the enum's zero value SATISFIES it.
//     Only an absent field fails it.
//   - `enum.defined_only` rejects a number the enum does not declare, e.g. 99.
//     It does NOT reject 0, because 0 is declared — it is the _UNSPECIFIED
//     member.
//   - `enum.not_in = 0` is therefore the only rule that actually refuses
//     _UNSPECIFIED.
//
// So each rule catches something neither of the others does, and a test that
// exercised only one input could not tell you which rule was doing the work: a
// suite that only sent 99 would still pass with `required` and `not_in`
// deleted. Every field below is therefore driven with THREE distinct inputs
// plus a valid one, and each is asserted to produce exactly one violation,
// naming exactly one rule.
//
// ReminderDestination.platform_enum and RepostCandidate.kind previously carried
// a bare `required`, which meant an explicit PLATFORM_UNSPECIFIED or
// REPOST_KIND_UNSPECIFIED sailed through into a handler that had to defend
// itself. They are brought up to the same standard here.

// undefinedPlatform is a number Platform does not declare. Far outside the
// declared range (0..6) rather than one past the end, so adding a platform
// cannot quietly make these tests stop testing anything.
const undefinedPlatform = pb.Platform(99)

// undefinedRepostKind is the same idea for RepostKind (declared range 0..4).
const undefinedRepostKind = pb.RepostKind(99)

// enumContract is one enum field driven through all four inputs. Each builder
// differs from validInput in exactly one field: the one under test.
type enumContract struct {
	// name identifies the schema field, not the containing request.
	name string
	// path is how protovalidate spells the field in a violation.
	path string

	validInput       func() proto.Message
	absentInput      func() proto.Message
	undefinedInput   func() proto.Message
	unspecifiedInput func() proto.Message
}

// futureTimestamp satisfies CreateReminderReq.datetime's timestamp.gt_now.
// Rebuilt per call: a shared value goes stale as a test binary runs.
func futureTimestamp() *timestamppb.Timestamp {
	return timestamppb.New(time.Now().Add(time.Hour))
}

// reminderWith wraps a destination in an otherwise-valid CreateReminderReq, so
// the only thing a violation can be about is the destination's platform.
func reminderWith(destination *pb.ReminderDestination) *pb.CreateReminderReq {
	timezone := "UTC"
	message := "enum contract"

	return pb.CreateReminderReq_builder{
		Datetime:    futureTimestamp(),
		Timezone:    &timezone,
		Message:     &message,
		Destination: destination,
	}.Build()
}

// destinationBuilder is ReminderDestination minus its platform, which each case
// supplies (or does not).
func destinationBuilder() pb.ReminderDestination_builder {
	return pb.ReminderDestination_builder{
		InstanceMeta:    mustStruct(map[string]any{"instance_uid": "guild-1"}),
		DestinationMeta: mustStruct(map[string]any{"destination_uid": "channel-1"}),
	}
}

// repostWith wraps a candidate in an otherwise-valid CheckRepostReq.
func repostWith(candidate *pb.RepostCandidate) *pb.CheckRepostReq {
	uid := "message-uid"

	return pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{candidate},
		MessageUid: &uid,
		AuthorUid:  &uid,
	}.Build()
}

// candidateBuilder is RepostCandidate minus its kind.
func candidateBuilder() pb.RepostCandidate_builder {
	url := "https://cdn.discordapp.com/attachments/1/2/a.png"
	return pb.RepostCandidate_builder{Url: &url}
}

// tryTriggerWith wraps a TriggerInstance in an otherwise-valid TryTriggerReq.
func tryTriggerWith(instance *pb.TriggerInstance) *pb.TryTriggerReq {
	phrase := "enum contract"
	return pb.TryTriggerReq_builder{Instance: instance, Phrase: &phrase}.Build()
}

// instanceBuilder is TriggerInstance minus its platform.
func instanceBuilder() pb.TriggerInstance_builder {
	return pb.TriggerInstance_builder{
		InstanceMeta: mustStruct(map[string]any{"instance_uid": "guild-1"}),
	}
}

func enumContracts() []enumContract {
	undefined := undefinedPlatform
	unspecified := pb.Platform_PLATFORM_UNSPECIFIED
	undefinedKind := undefinedRepostKind
	unspecifiedKind := pb.RepostKind_REPOST_KIND_UNSPECIFIED

	return []enumContract{
		{
			name: "TriggerInstance.platform_enum",
			// Exercised through TryTriggerReq. The rules live on
			// TriggerInstance itself, so every RPC that carries one — TryTrigger,
			// ExecTrigger, GetTriggerStats, and the instances lists on
			// Create/Update/ListTriggers — inherits them from here.
			path: "instance.platform_enum",
			validInput: func() proto.Message {
				b := instanceBuilder()
				b.PlatformEnum = pb.Platform_PLATFORM_DISCORD.Enum()
				return tryTriggerWith(b.Build())
			},
			absentInput: func() proto.Message {
				return tryTriggerWith(instanceBuilder().Build())
			},
			undefinedInput: func() proto.Message {
				b := instanceBuilder()
				b.PlatformEnum = &undefined
				return tryTriggerWith(b.Build())
			},
			unspecifiedInput: func() proto.Message {
				b := instanceBuilder()
				b.PlatformEnum = &unspecified
				return tryTriggerWith(b.Build())
			},
		},
		{
			name: "CreateInstanceReq.platform_enum",
			path: "platform_enum",
			validInput: func() proto.Message {
				return validCreateInstance()
			},
			absentInput: func() proto.Message {
				req := validCreateInstance()
				req.ClearPlatformEnum()
				return req
			},
			undefinedInput: func() proto.Message {
				req := validCreateInstance()
				req.SetPlatformEnum(undefined)
				return req
			},
			unspecifiedInput: func() proto.Message {
				req := validCreateInstance()
				req.SetPlatformEnum(unspecified)
				return req
			},
		},
		{
			name: "ReminderDestination.platform_enum",
			path: "destination.platform_enum",
			validInput: func() proto.Message {
				b := destinationBuilder()
				b.PlatformEnum = pb.Platform_PLATFORM_DISCORD.Enum()
				return reminderWith(b.Build())
			},
			absentInput: func() proto.Message {
				return reminderWith(destinationBuilder().Build())
			},
			undefinedInput: func() proto.Message {
				b := destinationBuilder()
				b.PlatformEnum = &undefined
				return reminderWith(b.Build())
			},
			unspecifiedInput: func() proto.Message {
				b := destinationBuilder()
				b.PlatformEnum = &unspecified
				return reminderWith(b.Build())
			},
		},
		{
			name: "RepostCandidate.kind",
			path: "candidates[0].kind",
			validInput: func() proto.Message {
				b := candidateBuilder()
				b.Kind = pb.RepostKind_REPOST_KIND_IMAGE.Enum()
				return repostWith(b.Build())
			},
			absentInput: func() proto.Message {
				return repostWith(candidateBuilder().Build())
			},
			undefinedInput: func() proto.Message {
				b := candidateBuilder()
				b.Kind = &undefinedKind
				return repostWith(b.Build())
			},
			unspecifiedInput: func() proto.Message {
				b := candidateBuilder()
				b.Kind = &unspecifiedKind
				return repostWith(b.Build())
			},
		},
	}
}

// A well-formed enum must still be accepted. Without this, rules that refused
// everything would satisfy all three rejection tests below.
func TestEnumContractAcceptsADefinedValue(t *testing.T) {
	for _, contract := range enumContracts() {
		t.Run(contract.name, func(t *testing.T) {
			requireValid(t, contract.validInput())
		})
	}
}

// Isolates `required`, and nothing else. An absent field is trivially
// expressible in Go and is the default in several other languages' builders,
// so this is a real input, not a synthetic one.
func TestEnumContractRejectsAnAbsentValue(t *testing.T) {
	for _, contract := range enumContracts() {
		t.Run(contract.name, func(t *testing.T) {
			requireOnlyViolation(t, contract.absentInput(), contract.path, "required")
		})
	}
}

// Isolates `enum.defined_only`, and nothing else.
//
// Protobuf enums are open: a number the enum does not declare round-trips
// through the generated code without complaint and arrives at the handler as
// pb.Platform(99). No handler check catches it either — they test for
// _UNSPECIFIED, and 99 is not that.
func TestEnumContractRejectsAnUndefinedNumber(t *testing.T) {
	for _, contract := range enumContracts() {
		t.Run(contract.name, func(t *testing.T) {
			requireOnlyViolation(t, contract.undefinedInput(), contract.path, "enum.defined_only")
		})
	}
}

// Isolates `enum.not_in = 0`, and nothing else.
//
// This is the case that `required` and `enum.defined_only` between them do NOT
// catch, which is the entire reason the third rule exists. A field carrying
// only the first two accepts an explicit _UNSPECIFIED cleanly — verified
// against the real validator, not assumed.
func TestEnumContractRejectsAnUnspecifiedValue(t *testing.T) {
	for _, contract := range enumContracts() {
		t.Run(contract.name, func(t *testing.T) {
			requireOnlyViolation(t, contract.unspecifiedInput(), contract.path, "enum.not_in")
		})
	}
}
