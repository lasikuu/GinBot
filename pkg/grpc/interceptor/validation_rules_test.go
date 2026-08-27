package interceptor

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Shared machinery for the schema-rule tests in this package
// (validation_trigger_test.go, validation_instance_test.go,
// validation_enum_test.go).
//
// These tests talk to protovalidate directly rather than going through
// NewValidationUnaryInterceptor, and that is a deliberate choice of layer. The
// interceptor flattens every violation into one InvalidArgument string, so a
// test driven through it can only ever assert "something was rejected" — which
// is exactly the assertion that cannot tell required from enum.defined_only
// from enum.not_in, and cannot tell a rejection caused by the field under test
// from one caused by an unrelated field on the same request. Validating here
// gives the structured violation list, so each case pins the ONE rule it is
// about, by rule id, on the ONE field it is about.
//
// The end-to-end half is not skipped: pkg/grpc/server/trigger_validation_test.go
// drives the instances bound and a malformed id through the server harness and
// the production interceptor chain, so the rules are also proven to be reachable
// by a real client.

// violation is one protovalidate finding, reduced to the two things a test
// cares about: which field, and which declared rule.
type violation struct {
	Field string
	Rule  string
}

func (v violation) String() string { return v.Field + " [" + v.Rule + "]" }

// validateMessage runs the same validator the production interceptors build and
// returns the violations as an ordered list.
func validateMessage(t *testing.T, msg proto.Message) []violation {
	t.Helper()

	validator, err := protovalidate.New()
	if err != nil {
		t.Fatalf("protovalidate.New: %v", err)
	}

	err = validator.Validate(msg)
	if err == nil {
		return nil
	}

	var validationErr *protovalidate.ValidationError
	if !errors.As(err, &validationErr) {
		// A compilation failure in the schema itself, not a rejected message.
		t.Fatalf("validator returned a non-validation error: %v", err)
	}

	out := make([]violation, 0, len(validationErr.Violations))
	for _, v := range validationErr.Violations {
		out = append(out, violation{
			Field: fieldPath(v.Proto.GetField()),
			Rule:  v.Proto.GetRuleId(),
		})
	}

	return out
}

// fieldPath renders a violation's field path the way the schema spells it,
// e.g. "instances[0].platform_enum".
func fieldPath(path *validate.FieldPath) string {
	var parts []string
	for _, element := range path.GetElements() {
		part := element.GetFieldName()
		if element.HasIndex() {
			part += "[" + strconv.FormatUint(element.GetIndex(), 10) + "]"
		}
		parts = append(parts, part)
	}

	return strings.Join(parts, ".")
}

// requireValid asserts a message is accepted outright.
//
// Every rejection case below is paired with one of these on an otherwise
// identical message. Without that pairing a rejection test proves nothing: a
// request that was already invalid for an unrelated reason would produce the
// same "rejected" verdict no matter what the rule under test does.
func requireValid(t *testing.T, msg proto.Message) {
	t.Helper()

	if got := validateMessage(t, msg); len(got) != 0 {
		t.Fatalf("message was rejected, want accepted: %v", got)
	}
}

// requireOnlyViolation asserts that a message is rejected for exactly one
// reason: the named rule on the named field.
//
// "Exactly one" is the point. A case that merely contains the expected
// violation would still pass if the fixture were malformed elsewhere, and a
// case that only asserts rejection cannot distinguish the three enum rules from
// each other at all.
func requireOnlyViolation(t *testing.T, msg proto.Message, field, rule string) {
	t.Helper()

	want := violation{Field: field, Rule: rule}
	got := validateMessage(t, msg)

	if len(got) == 0 {
		t.Fatalf("message was accepted, want it rejected by %v", want)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("violations = %v, want exactly [%v]", got, want)
	}
}

// declaredRules reads the buf.validate rules a field carries in the schema.
//
// Reading the bound out of the descriptor is what keeps a boundary test honest:
// the number lives in the .proto and nowhere else, so the test cannot drift
// from it and cannot silently keep passing if the rule is dropped.
func declaredRules(t *testing.T, msg proto.Message, field protoreflect.Name) *validate.FieldRules {
	t.Helper()

	descriptor := msg.ProtoReflect().Descriptor()
	fd := descriptor.Fields().ByName(field)
	if fd == nil {
		t.Fatalf("%s has no field %q", descriptor.FullName(), field)
	}

	options, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || options == nil {
		t.Fatalf("%s.%s carries no field options at all, so it carries no buf.validate rules",
			descriptor.FullName(), field)
	}

	rules, ok := proto.GetExtension(options, validate.E_Field).(*validate.FieldRules)
	if !ok || rules == nil {
		t.Fatalf("%s.%s carries no (buf.validate.field) rules", descriptor.FullName(), field)
	}

	return rules
}

// declaredMaxItems reads a repeated.max_items bound, failing loudly when the
// rule is absent rather than falling back to a number invented here.
func declaredMaxItems(t *testing.T, msg proto.Message, field protoreflect.Name) int {
	t.Helper()

	repeated := declaredRules(t, msg, field).GetRepeated()
	if !repeated.HasMaxItems() {
		t.Fatalf("%s.%s declares no repeated.max_items; an unbounded list is a query multiplier "+
			"— resolveScopeInstances makes one database round trip per element and any "+
			"CLEARANCE_REGISTERED caller can send it",
			msg.ProtoReflect().Descriptor().FullName(), field)
	}

	return int(repeated.GetMaxItems())
}

// declaredMaxLen reads a string.max_len bound, failing loudly when absent.
func declaredMaxLen(t *testing.T, msg proto.Message, field protoreflect.Name) int {
	t.Helper()

	str := declaredRules(t, msg, field).GetString_()
	if !str.HasMaxLen() {
		t.Fatalf("%s.%s declares no string.max_len",
			msg.ProtoReflect().Descriptor().FullName(), field)
	}

	return int(str.GetMaxLen())
}
