// Package reminder holds the platform-neutral, dependency-light logic shared by
// the reminder server, the cron delivery loop and the platform clients:
//
//   - parsing a human-supplied "when" into a concrete UTC instant;
//   - validating and reasoning about a repeat_cron string;
//   - rendering a stored UTC instant in a reminder's timezone;
//   - the field names of the untyped delivery payload pushed over the reverse
//     stream.
//
// It deliberately imports no database, no discordgo and no gRPC: everything here
// is a pure function of its inputs so it can be unit-tested in isolation, and so
// both ends of the reverse stream (the server-side cron and the Discord client)
// can import it without pulling in each other's dependencies or creating an
// import cycle.
package reminder

import (
	"time"

	"google.golang.org/protobuf/types/known/structpb"
)

// Delivery payload field contract.
//
// OpenClientActionStreamResp.content is a google.protobuf.Struct rather than a
// typed message, so the two ends only agree by convention. These constants are
// that convention, defined once and imported by both the server-side cron
// (which builds the Struct) and the client handler (which reads it). A divergent
// literal on either end silently breaks delivery, so the literal exists in
// exactly one place.
const (
	// PayloadKeyReminderID carries the reminder's UUID so the client can call
	// ConfirmDelivery after posting. Required.
	PayloadKeyReminderID = "reminder_id"

	// PayloadKeyMessage carries the reminder text to post. May be empty.
	PayloadKeyMessage = "message"

	// PayloadKeyDestinationUID carries the platform channel id (the destination's
	// destination_meta.destination_uid), i.e. where to post. May be empty when
	// the reminder has no resolvable channel, in which case the client falls
	// back to a direct message.
	PayloadKeyDestinationUID = "destination_uid"

	// PayloadKeyUserID carries the reminder owner's platform user id (e.g. a
	// Discord snowflake). It is used both for the direct-message fallback and to
	// build the caller metadata on the outgoing ConfirmDelivery call. May be
	// empty when the owner has no resolvable platform identity.
	PayloadKeyUserID = "user_id"
)

// NewDeliveryPayload builds the untyped delivery Struct for one reminder push.
//
// This is the ONLY writer of that Struct. It lives here, next to the key
// constants and beside the client that reads them, so the contract has a single
// implementation that tests on both ends can exercise — rather than the server
// building one shape and a test asserting another.
//
// Absent optional fields are carried as empty strings rather than omitted, so
// the client can read every key unconditionally without a presence check. The
// parameters are plain strings, not a database row, to keep this package free of
// a database import.
func NewDeliveryPayload(reminderID, message, destinationUID, ownerPlatformUID string) *structpb.Struct {
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			PayloadKeyReminderID:     structpb.NewStringValue(reminderID),
			PayloadKeyMessage:        structpb.NewStringValue(message),
			PayloadKeyDestinationUID: structpb.NewStringValue(destinationUID),
			PayloadKeyUserID:         structpb.NewStringValue(ownerPlatformUID),
		},
	}
}

// renderLayout is the wall-clock format used by RenderInZone: an unambiguous
// year-first date, 24-hour time and the zone abbreviation.
const renderLayout = "2006-01-02 15:04 MST"

// RenderInZone renders a stored UTC instant in a reminder's IANA timezone,
// falling back to UTC when the zone is empty or unknown.
//
// It is the fallback for platforms with no native timestamp format of their own.
// The Discord client no longer calls it: Discord has <t:UNIX:STYLE>, which each
// viewer's client renders in their own zone, so rendering server-side there
// would print one zone to an audience that does not share it. Matrix and
// anything else without such a tag still need a formatted string, and the
// reminder's stored timezone is what they must format in — which is why this
// stays here rather than moving into pkg/discord. A wire format for one platform
// does not belong in this package either way.
//
// It never errors — an unresolvable zone yields the UTC render rather than a
// failure, so a display path can always produce a string.
func RenderInZone(instant time.Time, timezone string) string {
	loc := time.UTC
	if timezone != "" {
		if resolved, err := time.LoadLocation(timezone); err == nil {
			loc = resolved
		}
	}

	return instant.In(loc).Format(renderLayout)
}
