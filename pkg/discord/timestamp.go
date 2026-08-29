package discord

import (
	"strconv"
	"time"
)

// timestampStyle selects how Discord renders a timestamp tag.
type timestampStyle string

// The documented styles, named in full since the wire format is a single letter.
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

// timestampTag renders an instant as <t:UNIX_SECONDS:STYLE>. Discord renders it
// client-side, so every viewer sees it in their own timezone and locale.
func timestampTag(instant time.Time, style timestampStyle) string {
	return "<t:" + strconv.FormatInt(instant.Unix(), 10) + ":" + string(style) + ">"
}

// timestampWithRelative renders an instant as `<t:N:F> (<t:N:R>)`: exact time
// paired with a relative "how soon".
func timestampWithRelative(instant time.Time) string {
	return timestampTag(instant, timestampLongDateTime) +
		" (" + timestampTag(instant, timestampRelative) + ")"
}
