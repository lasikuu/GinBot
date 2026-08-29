package server

import (
	"context"
	"math"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// Every rejection is paired with an otherwise identical request that must not be
// rejected the same way, or an unrelated failure would look identical.

// unregisteredCaller is well formed but has no account, so clearance answers
// FailedPrecondition: a "got past validation" marker anonymousCtx cannot give.
const unregisteredCaller = "harness-unregistered"

func unregisteredCtx() context.Context {
	return callerCtx(pb.Platform_PLATFORM_DISCORD, unregisteredCaller)
}

// schemaMaxItems reads the bound from the descriptor, so this cannot drift from the proto.
func schemaMaxItems(t *testing.T, msg proto.Message, field protoreflect.Name) int {
	t.Helper()

	descriptor := msg.ProtoReflect().Descriptor()
	fd := descriptor.Fields().ByName(field)
	if fd == nil {
		t.Fatalf("%s has no field %q", descriptor.FullName(), field)
	}

	options, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || options == nil {
		t.Fatalf("%s.%s carries no field options, so it carries no buf.validate rules",
			descriptor.FullName(), field)
	}

	rules, ok := proto.GetExtension(options, validate.E_Field).(*validate.FieldRules)
	if !ok || !rules.GetRepeated().HasMaxItems() {
		t.Fatalf("%s.%s declares no repeated.max_items", descriptor.FullName(), field)
	}

	return int(rules.GetRepeated().GetMaxItems())
}

// harnessInstances are individually valid, so a rejection can only be about how many.
func harnessInstances(t *testing.T, n int) []*pb.TriggerInstance {
	t.Helper()

	out := make([]*pb.TriggerInstance, 0, n)
	for range n {
		meta, err := structpb.NewStruct(map[string]any{"instance_uid": "guild-1"})
		if err != nil {
			t.Fatalf("structpb.NewStruct: %v", err)
		}
		out = append(out, pb.TriggerInstance_builder{
			PlatformEnum: pb.Platform_PLATFORM_DISCORD.Enum(),
			InstanceMeta: meta,
		}.Build())
	}
	return out
}

func createTriggerWith(instances []*pb.TriggerInstance) *pb.CreateTriggerReq {
	phrase := "harness-phrase"
	reply := "harness-reply"
	chance := int32(50)

	return pb.CreateTriggerReq_builder{
		Phrase:    &phrase,
		Reply:     &reply,
		Chance:    &chance,
		Instances: instances,
	}.Build()
}

// resolveScopeInstances costs a database round trip per element, so the schema rejects first.
func TestOversizedInstanceListIsRejectedByTheChain(t *testing.T) {
	bound := schemaMaxItems(t, createTriggerWith(nil), "instances")

	t.Run("over the bound", func(t *testing.T) {
		dir := newDirectory()
		h := newHarness(t, withDirectory(dir))

		_, err := h.Trigger.CreateTrigger(unregisteredCtx(), createTriggerWith(harnessInstances(t, bound+1)))
		requireCode(t, err, connect.CodeInvalidArgument)

		if n := dir.resolveCount(); n != 0 {
			t.Errorf("the caller was resolved %d times for a request the schema refuses; "+
				"validation must run before clearance so an oversized list costs nothing", n)
		}
	})

	// A rule refusing this would stop a caller scoping to every instance they are in.
	t.Run("exactly at the bound", func(t *testing.T) {
		dir := newDirectory()
		h := newHarness(t, withDirectory(dir))

		_, err := h.Trigger.CreateTrigger(unregisteredCtx(), createTriggerWith(harnessInstances(t, bound)))
		// FailedPrecondition: the request passed the schema and was stopped by clearance.
		requireCode(t, err, connect.CodeFailedPrecondition)

		if n := dir.resolveCount(); n != 1 {
			t.Errorf("the caller was resolved %d times, want 1: the request did not reach clearance", n)
		}
	})
}

// Without string.uuid a malformed id reaches db.GetTrigger and is paid for anyway.
func TestMalformedTriggerIdIsRejectedByTheChain(t *testing.T) {
	const malformed = "12345"
	const wellFormed = "018f0000-0000-7000-8000-000000000001"

	t.Run("malformed", func(t *testing.T) {
		dir := newDirectory()
		h := newHarness(t, withDirectory(dir))

		id := malformed
		_, err := h.Trigger.GetTrigger(unregisteredCtx(), pb.GetTriggerReq_builder{Id: &id}.Build())
		requireCode(t, err, connect.CodeInvalidArgument)

		if n := dir.resolveCount(); n != 0 {
			t.Errorf("the caller was resolved %d times for a malformed id, want 0", n)
		}
	})

	t.Run("well formed", func(t *testing.T) {
		dir := newDirectory()
		h := newHarness(t, withDirectory(dir))

		id := wellFormed
		_, err := h.Trigger.GetTrigger(unregisteredCtx(), pb.GetTriggerReq_builder{Id: &id}.Build())
		requireCode(t, err, connect.CodeFailedPrecondition)

		if n := dir.resolveCount(); n != 1 {
			t.Errorf("the caller was resolved %d times, want 1: a valid id did not reach clearance", n)
		}
	})
}

// pkg/db clamps limit and offset rather than refusing them; a future lte rule fails here.
func TestAbsurdListTriggersLimitIsNotRejectedByTheChain(t *testing.T) {
	tests := []struct {
		name   string
		limit  int64
		offset int64
	}{
		{"absurd limit", math.MaxInt64, 0},
		{"absurd offset", 50, math.MaxInt64},
		{"negative limit", -1, 0},
		{"negative offset", 50, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)

			_, err := h.Trigger.ListTriggers(unregisteredCtx(),
				pb.ListTriggersReq_builder{Limit: &tt.limit, Offset: &tt.offset}.Build())

			// The clearance interceptor is what stops it, not the schema.
			requireCode(t, err, connect.CodeFailedPrecondition)
		})
	}
}

// chance 0 is the "use the default" sentinel and must survive the whole chain.
func TestChanceBoundariesSurviveTheChain(t *testing.T) {
	tests := []struct {
		name   string
		chance int32
	}{
		{"zero is the use-the-default sentinel", 0},
		{"one hundred is certainty", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)

			phrase := "sentinel-phrase"
			reply := "sentinel-reply"
			chance := tt.chance
			_, err := h.Trigger.CreateTrigger(unregisteredCtx(), pb.CreateTriggerReq_builder{
				Phrase: &phrase,
				Reply:  &reply,
				Chance: &chance,
			}.Build())

			requireCode(t, err, connect.CodeFailedPrecondition)
		})
	}
}
