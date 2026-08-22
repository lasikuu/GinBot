package reminder

import (
	"testing"
)

// The delivery payload key contract.
//
// OpenClientActionStreamResp.content is an untyped google.protobuf.Struct, so
// the cron loop that writes it and the platform client that reads it only agree
// by convention. These tests pin that convention down.
//
// They assert the LITERAL key spellings on purpose. An earlier version only
// checked that the constants were non-empty and distinct, which passes for any
// pair of strings and so could not detect a rename on one side of the wire —
// the exact failure the contract exists to prevent.
func TestPayloadKeysAreTheDocumentedWireNames(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "reminder id", got: PayloadKeyReminderID, want: "reminder_id"},
		{name: "message", got: PayloadKeyMessage, want: "message"},
		{name: "destination uid", got: PayloadKeyDestinationUID, want: "destination_uid"},
		{name: "owner platform uid", got: PayloadKeyUserID, want: "user_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("payload key = %q, want %q; renaming it breaks every deployed client", tt.got, tt.want)
			}
		})
	}
}

// TestPayloadKeysAreDistinct: four values share one Struct, so two keys that
// collide would silently overwrite each other.
func TestPayloadKeysAreDistinct(t *testing.T) {
	keys := map[string]string{
		PayloadKeyReminderID:     "PayloadKeyReminderID",
		PayloadKeyMessage:        "PayloadKeyMessage",
		PayloadKeyDestinationUID: "PayloadKeyDestinationUID",
		PayloadKeyUserID:         "PayloadKeyUserID",
	}
	if len(keys) != 4 {
		t.Errorf("payload keys collide: %d distinct values for 4 constants (%v)", len(keys), keys)
	}
}

// TestNewDeliveryPayloadCarriesEveryField: the production builder puts each
// value under its own key, and reading it back through the same constants
// round-trips. This is the writer both ends depend on, so it is exercised
// directly rather than reconstructed by a test.
func TestNewDeliveryPayloadCarriesEveryField(t *testing.T) {
	const (
		reminderID     = "0192f000-0000-7000-8000-000000000001"
		message        = "stand up and stretch"
		destinationUID = "1234567890"
		ownerUID       = "9876543210"
	)

	payload := NewDeliveryPayload(reminderID, message, destinationUID, ownerUID)
	if payload == nil {
		t.Fatal("NewDeliveryPayload returned nil")
	}

	fields := payload.GetFields()
	tests := []struct {
		key  string
		want string
	}{
		{key: PayloadKeyReminderID, want: reminderID},
		{key: PayloadKeyMessage, want: message},
		{key: PayloadKeyDestinationUID, want: destinationUID},
		{key: PayloadKeyUserID, want: ownerUID},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			value, ok := fields[tt.key]
			if !ok {
				t.Fatalf("payload has no %q field", tt.key)
			}
			if got := value.GetStringValue(); got != tt.want {
				t.Errorf("payload[%q] = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// TestNewDeliveryPayloadCarriesEmptyFieldsExplicitly: an absent optional value
// is carried as an empty string rather than omitted, which is what lets the
// client read every key unconditionally instead of presence-checking each one.
func TestNewDeliveryPayloadCarriesEmptyFieldsExplicitly(t *testing.T) {
	payload := NewDeliveryPayload("0192f000-0000-7000-8000-000000000002", "", "", "")

	for _, key := range []string{
		PayloadKeyReminderID, PayloadKeyMessage, PayloadKeyDestinationUID, PayloadKeyUserID,
	} {
		value, ok := payload.GetFields()[key]
		if !ok {
			t.Errorf("payload omits %q; the client would have to presence-check it", key)
			continue
		}
		if value == nil {
			t.Errorf("payload[%q] is a nil Value; the client would deref nil", key)
		}
	}
}
