package server

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// This file guards the wire-level half of the project-wide identity rule that
// pkg/grpc/callermeta and every handler in this package implement: the caller
// is named by gRPC metadata and never by a request field.
//
// It lives here, next to the handlers, because this package is where the rule
// is enforced. It is driven by protobuf reflection rather than by a list of
// generated accessors on purpose: a hand-written list only fails when somebody
// remembers to extend it, whereas the descriptor walk below fails the moment a
// forbidden field exists, including on a request message that does not exist
// yet.
//
// The failure this protects against is specific. Eight request messages carried
// a user_id or actor_id naming a SUBJECT in the request body. Their numbers and
// names are now `reserved`, which stops the wire numbers being recycled — but
// `reserved` says nothing about a field being re-added under the same name at a
// NEW number, which would reintroduce the hole while looking like an ordinary
// addition in review.

// wirePackage is the only proto package this repository owns. Filtering to it
// keeps the walk off google.protobuf and buf.validate descriptors, which are
// registered in the same global registry and are not ours to police.
const wirePackage protoreflect.FullName = "ginbot.v1"

// subjectFieldNames are the field names that may never appear on a request
// message. Both are legitimate on entity and response messages — Trigger.user_id
// and ActionRecord.actor_id identify the row's owner, which is data rather than
// an instruction — so the ban is scoped to requests, not to the names as such.
var subjectFieldNames = []protoreflect.Name{"user_id", "actor_id"}

// rangeWireMessages calls fn for every message declared in wirePackage,
// including nested ones.
//
// The descriptors come from protoregistry.GlobalFiles, which the generated
// package populates from its init — reached here because this package's
// production code imports it. That is an indirect dependency, so the walk
// fatals rather than reporting success when it finds nothing: an empty registry
// would otherwise satisfy every assertion in this file.
func rangeWireMessages(t *testing.T, fn func(protoreflect.MessageDescriptor)) {
	t.Helper()

	var walk func(protoreflect.MessageDescriptors)
	walk = func(messages protoreflect.MessageDescriptors) {
		for i := range messages.Len() {
			message := messages.Get(i)
			fn(message)
			walk(message.Messages())
		}
	}

	found := false
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package() == wirePackage {
			found = true
			walk(file.Messages())
		}
		return true
	})

	if !found {
		t.Fatalf("no file descriptors registered for proto package %q; the walk below would pass vacuously", wirePackage)
	}
}

// TestNoRequestMessageCarriesASubjectField is the guard proper.
//
// A request that names its own subject is the pattern callermeta exists to
// forbid: the server would be taking "who this is about" from the least
// trustworthy part of the call. The eight fields that did this are gone, and
// this fails if any of them — or a ninth, on a message added later — comes back.
//
// Scoped to messages whose name ends in Req because that is this repository's
// request-message convention, and it is the request side that decides authority.
func TestNoRequestMessageCarriesASubjectField(t *testing.T) {
	rangeWireMessages(t, func(message protoreflect.MessageDescriptor) {
		if !isRequestMessage(message) {
			return
		}

		for _, name := range subjectFieldNames {
			if field := message.Fields().ByName(name); field != nil {
				t.Errorf("%s carries a %s field (number %d).\n"+
					"A request must not name its subject: caller identity travels as gRPC metadata,\n"+
					"owned end to end by pkg/grpc/callermeta. A subject in the request body lets a\n"+
					"caller act on someone else's account. Read the caller with callerUser(ctx) instead.",
					message.FullName(), name, field.Number())
			}
		}
	})
}

// isRequestMessage reports whether a descriptor is one of this wire's request
// messages, by the Req/Resp naming convention every RPC in proto/ginbot/v1
// follows.
func isRequestMessage(message protoreflect.MessageDescriptor) bool {
	name := string(message.Name())
	return len(name) > 3 && name[len(name)-3:] == "Req"
}

// TestTheFormerlyOffendingRequestMessagesStillExist keeps the test above
// honest.
//
// TestNoRequestMessageCarriesASubjectField passes trivially for a message that
// is not there at all, so renaming or deleting one of the eight would look like
// a fix rather than a gap. These are the messages the reservations were written
// for; if one of them is genuinely retired, this list is the place that has to
// be updated deliberately.
func TestTheFormerlyOffendingRequestMessagesStillExist(t *testing.T) {
	// The eight messages whose user_id / actor_id was deleted and reserved.
	wanted := []protoreflect.Name{
		"SetBirthdayReq",
		"TryTriggerReq",
		"CreateTriggerReq",
		"UpdateTriggerReq",
		"ListTriggersReq",
		"ListRemindersReq",
		"CreateActionRecordReq",
		"ListActionRecordsReq",
	}

	present := make(map[protoreflect.Name]bool, len(wanted))
	rangeWireMessages(t, func(message protoreflect.MessageDescriptor) {
		present[message.Name()] = true
	})

	for _, name := range wanted {
		if !present[name] {
			t.Errorf("message %s.%s is no longer declared; TestNoRequestMessageCarriesASubjectField "+
				"cannot assert anything about a message that does not exist", wirePackage, name)
		}
	}
}

// TestEntityMessagesKeepTheirOwnerFields is the other side of the boundary, and
// the reason the guard above is scoped to requests rather than to the field
// names.
//
// Deleting the request fields must not turn into deleting the columns they were
// confused with. Trigger.user_id is the creator, ActionRecord.actor_id is who
// acted, and both are read by pkg/discord's rendering and by the ownership
// checks in this package. A well-meaning follow-up sweep for "identity fields
// in protobuf" would break every one of those, silently, because nothing else
// asserts they are present.
func TestEntityMessagesKeepTheirOwnerFields(t *testing.T) {
	wanted := map[protoreflect.Name]protoreflect.Name{
		"Trigger":         "user_id",
		"Reminder":        "user_id",
		"ActionRecord":    "actor_id",
		"SetBirthdayResp": "user_id",
		"RegisterResp":    "user_id",
	}

	seen := make(map[protoreflect.Name]bool, len(wanted))
	rangeWireMessages(t, func(message protoreflect.MessageDescriptor) {
		field, ok := wanted[message.Name()]
		if !ok {
			return
		}
		seen[message.Name()] = true

		if message.Fields().ByName(field) == nil {
			t.Errorf("%s lost its %s field; it identifies the row's owner and is not a request-side subject",
				message.FullName(), field)
		}
	})

	for name := range wanted {
		if !seen[name] {
			t.Errorf("message %s.%s is no longer declared", wirePackage, name)
		}
	}
}
