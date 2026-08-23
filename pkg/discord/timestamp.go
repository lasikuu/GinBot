package discord

import (
	"strconv"
	"time"
)

// timestampStyle selects how Discord renders a timestamp tag.
type timestampStyle string

// The documented styles. The whole set is named here rather than only the two in
// use, because the wire format is a single opaque letter: a call site written
// with a bare "R" is unreviewable, and the next one would re-derive the letter
// from the docs instead of from here.
//
// https://discord.com/developers/docs/reference#message-formatting-timestamp-styles
const (
	timestampShortTime     timestampStyle = "t"
	timestampLongTime      timestampStyle = "T"
	timestampShortDate     timestampStyle = "d"
	timestampLongDate      timestampStyle = "D"
	timestampShortDateTime timestampStyle = "f"
	timestampLongDateTime  timestampStyle = "F"
	timestampRelative      timestampStyle = "R"
)

// timestampTag renders an instant as a Discord timestamp tag,
// <t:UNIX_SECONDS:STYLE>.
//
// Discord renders the tag client-side, so EVERY VIEWER SEES IT IN THEIR OWN
// timezone and locale. That is the reason to prefer it over a formatted string:
// the bot no longer has to pick one zone to print for an audience that may not
// share it.
//
// Seconds is the only resolution the tag has; a sub-second remainder is dropped,
// which is what Unix does.
func timestampTag(instant time.Time, style timestampStyle) string {
	return "<t:" + strconv.FormatInt(instant.Unix(), 10) + ":" + string(style) + ">"
}

// timestampWithRelative renders an instant as `<t:N:F> (<t:N:R>)`.
//
// This is the idiomatic Discord pairing for a scheduled moment: the long form
// answers "when exactly", and the relative form answers "how soon" without the
// reader doing arithmetic against their own clock.
func timestampWithRelative(instant time.Time) string {
	return timestampTag(instant, timestampLongDateTime) +
		" (" + timestampTag(instant, timestampRelative) + ")"
}
