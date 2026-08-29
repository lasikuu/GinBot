package interceptor

import (
	"testing"
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// All three of `required`, `enum.defined_only` and `enum.not_in = 0` are needed:
// `required` is a presence check that an explicit zero satisfies,
// `enum.defined_only` accepts 0 because _UNSPECIFIED is declared, and only
// `not_in` refuses it. Each input below isolates one rule.

// undefinedPlatform is far outside the declared range (0..6), so adding a
// platform cannot quietly make these tests stop testing anything.
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

// futureTimestamp is rebuilt per call: a shared value goes stale as a test runs.
func futureTimestamp() *timestamppb.Timestamp {
	return timestamppb.New(time.Now().Add(time.Hour))
}

// reminderWith wraps a destination in an otherwise-valid CreateReminderReq.
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

// destinationBuilder is ReminderDestination minus its platform.
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
			// The rules live on TriggerInstance, so every RPC carrying one
			// inherits them from here.
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

// Without this, rules that refused everything would satisfy all three tests below.
func TestEnumContractAcceptsADefinedValue(t *testing.T) {
	for _, contract := range enumContracts() {
		t.Run(contract.name, func(t *testing.T) {
			requireValid(t, contract.validInput())
		})
	}
}

func TestEnumContractRejectsAnAbsentValue(t *testing.T) {
	for _, contract := range enumContracts() {
		t.Run(contract.name, func(t *testing.T) {
			requireOnlyViolation(t, contract.absentInput(), contract.path, "required")
		})
	}
}

// Protobuf enums are open: an undeclared number reaches the handler intact.
func TestEnumContractRejectsAnUndefinedNumber(t *testing.T) {
	for _, contract := range enumContracts() {
		t.Run(contract.name, func(t *testing.T) {
			requireOnlyViolation(t, contract.undefinedInput(), contract.path, "enum.defined_only")
		})
	}
}

// The case `required` and `enum.defined_only` between them do not catch.
func TestEnumContractRejectsAnUnspecifiedValue(t *testing.T) {
	for _, contract := range enumContracts() {
		t.Run(contract.name, func(t *testing.T) {
			requireOnlyViolation(t, contract.unspecifiedInput(), contract.path, "enum.not_in")
		})
	}
}
