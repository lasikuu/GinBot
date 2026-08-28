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

// The trigger schema rules, driven end to end through the harness and
// the production interceptor chain.
//
// pkg/grpc/interceptor/validation_trigger_test.go covers the full table of
// boundary values, because validating directly is the only layer that can name
// which rule fired. This file covers the other half of the question: that a
// real client actually hits those rules, in the right order, before anything
// expensive happens. Neither substitutes for the other — a rule can be
// perfectly declared and still be unreachable if the interceptor is not
// installed, and the harness alone cannot tell one rejection from another.
//
// Every rejection here is asserted as connect.CodeInvalidArgument AND paired with an
// otherwise identical request that must NOT be rejected that way. Without the
// pair, a request that failed for an unrelated reason would look identical.

// unregisteredCaller is a caller whose identity is well formed but who has no
// account, so the clearance interceptor answers FailedPrecondition.
//
// That is the useful "got past validation" marker: it is produced strictly
// after validation and by a different interceptor, so it cannot be confused
// with a schema rejection. anonymousCtx() would not do — callermeta reports
// missing metadata as InvalidArgument, which is exactly the code under test.
const unregisteredCaller = "harness-unregistered"

func unregisteredCtx() context.Context {
	return callerCtx(pb.Platform_PLATFORM_DISCORD, unregisteredCaller)
}

// schemaMaxItems reads a repeated.max_items bound out of the descriptor.
//
// Read rather than restated: the bound lives in trigger.proto and nowhere else,
// so this test cannot drift from it, and it fails loudly instead of silently
// passing if the rule is dropped.
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

// harnessInstances builds n individually valid TriggerInstances, so a rejection
// can only be about how many there are.
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

// createTriggerWith builds an otherwise-valid CreateTriggerReq carrying an
// instance list of the given length.
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

// The bound that matters most, proven reachable by a real client.
//
// TriggerServer.resolveScopeInstances calls callerScopedInstance once per
// element, and each of those is a database round trip. Unbounded, that is a
// query multiplier any CLEARANCE_REGISTERED caller can fire at will — a single
// RPC turning into as many queries as the request has elements.
//
// The resolver call count is asserted too: the rejection must happen before the
// caller is even looked up, which is what makes the bound worth having. A bound
// enforced inside the handler would already have paid for the lookup.
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

	// Exactly at the bound must still work. A rule that refused this would stop
	// a caller scoping a trigger to every instance they are legitimately in.
	t.Run("exactly at the bound", func(t *testing.T) {
		dir := newDirectory()
		h := newHarness(t, withDirectory(dir))

		_, err := h.Trigger.CreateTrigger(unregisteredCtx(), createTriggerWith(harnessInstances(t, bound)))
		// FailedPrecondition, not InvalidArgument: the request passed the
		// schema and was stopped by the clearance interceptor instead, which is
		// as far as an unregistered caller gets.
		requireCode(t, err, connect.CodeFailedPrecondition)

		if n := dir.resolveCount(); n != 1 {
			t.Errorf("the caller was resolved %d times, want 1: the request did not reach clearance", n)
		}
	})
}

// A malformed trigger id must be refused at the edge.
//
// This pins a behaviour change. Before string.uuid, "12345" reached
// db.GetTrigger and came back as NotFound — or as an Internal, when Postgres
// failed to cast it to uuid — so a caller could not tell a typo from a deleted
// trigger, and the query was paid for either way.
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

// limit and offset carry no cap, and this is the end-to-end guard for that.
//
// pkg/db clamps both rather than refusing them, and turning a forgiving clamp
// into an InvalidArgument would be a regression for anyone who typed a big
// number. A future `lte` rule on either field fails here.
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
// A rule that rejected it here would refuse every trigger created without an
// explicit chance, which is the common case from both platform clients.
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
