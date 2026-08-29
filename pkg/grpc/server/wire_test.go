package server

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Wire-level guard for the identity rule: a request message must never name its
// subject. Reflection-driven, so a message added later is covered automatically.

// wirePackage keeps the walk off google.protobuf and buf.validate.
const wirePackage protoreflect.FullName = "ginbot.v1"

// subjectFieldNames are banned on requests only; they are legitimate on entities.
var subjectFieldNames = []protoreflect.Name{"user_id", "actor_id"}

// rangeWireMessages fatals on an empty registry, which would otherwise pass vacuously.
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

// isRequestMessage applies the Req/Resp naming convention proto/ginbot/v1 follows.
func isRequestMessage(message protoreflect.MessageDescriptor) bool {
	name := string(message.Name())
	return len(name) > 3 && name[len(name)-3:] == "Req"
}

// The guard above passes trivially for a message that is not declared at all.
func TestTheFormerlyOffendingRequestMessagesStillExist(t *testing.T) {
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

// Nothing else asserts these owner fields exist, so a sweep would remove them silently.
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
