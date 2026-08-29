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

// Shared machinery for the schema-rule tests in this package. They call
// protovalidate directly: the interceptor flattens every violation into one
// InvalidArgument string, which cannot pin which rule fired on which field.

// violation is one protovalidate finding: which field, and which declared rule.
type violation struct {
	Field string
	Rule  string
}

func (v violation) String() string { return v.Field + " [" + v.Rule + "]" }

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

// fieldPath renders a field path as the schema spells it, e.g.
// "instances[0].platform_enum".
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

// requireValid asserts a message is accepted outright. Every rejection case is
// paired with one of these; without it a rejection test proves nothing.
func requireValid(t *testing.T, msg proto.Message) {
	t.Helper()

	if got := validateMessage(t, msg); len(got) != 0 {
		t.Fatalf("message was rejected, want accepted: %v", got)
	}
}

// requireOnlyViolation asserts a message is rejected for exactly one reason:
// the named rule on the named field. "Exactly one" catches malformed fixtures.
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

// declaredRules reads the buf.validate rules a field carries in the schema, so
// a boundary test cannot drift from the number declared in the .proto.
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

// declaredMaxItems reads a repeated.max_items bound, failing when it is absent.
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

	str := declaredRules(t, msg, field).GetString()
	if !str.HasMaxLen() {
		t.Fatalf("%s.%s declares no string.max_len",
			msg.ProtoReflect().Descriptor().FullName(), field)
	}

	return int(str.GetMaxLen())
}
