package rules

import (
	"fmt"
	"strings"
	"time"
)

// TimeFormat is a timestamp Field's Format string, resolved once (at
// rules.Load time, via ResolveFormat) into an efficient parsing strategy -
// mirroring how Rule.Pattern is compiled once into Rule.Regexp rather than
// re-interpreted on every log line.
//
// EpochUnit == 0 means "not an epoch format": parse with Layout via
// time.ParseInLocation. EpochUnit != 0 means "epoch format": parse as a
// number of EpochUnit ticks since the Unix epoch (Layout is unused).
type TimeFormat struct {
	Layout    string
	EpochUnit time.Duration
}

// presetLayouts maps a format preset name to the Go reference-time layout
// it resolves to.
var presetLayouts = map[string]string{
	"iso8601": "2006-01-02T15:04:05.999999999Z07:00",
	"rfc3339": "2006-01-02T15:04:05.999999999Z07:00",
	"rfc822":  "02 Jan 06 15:04 -0700",
	"rfc2822": "Mon, 02 Jan 2006 15:04:05 -0700",
	"clf":     "02/Jan/2006:15:04:05 -0700",
	"syslog":  "Jan _2 15:04:05",
	"pylog":   "2006-01-02 15:04:05,999999999",
}

// presetEpochUnits maps a format preset name to the unit its numeric value
// counts since the Unix epoch.
var presetEpochUnits = map[string]time.Duration{
	"unix":    time.Second,
	"unix_ms": time.Millisecond,
	"unix_us": time.Microsecond,
	"unix_ns": time.Nanosecond,
}

// strptimeDirectives maps a strptime %-directive (without the %) to the Go
// reference-time layout token it translates to. Directives not listed here
// have no direct Go layout equivalent (e.g. %j day-of-year, %U/%W week
// number) and are rejected by strptimeToLayout with a clear error instead
// of being silently mishandled.
var strptimeDirectives = map[rune]string{
	'Y': "2006",
	'y': "06",
	'm': "01",
	'd': "02",
	'H': "15",
	'I': "03",
	'M': "04",
	'S': "05",
	'f': "999999999",
	'z': "-0700",
	'Z': "MST",
	'a': "Mon",
	'A': "Monday",
	'b': "Jan",
	'B': "January",
	'p': "PM",
	'%': "%",
}

// ResolveFormat interprets a timestamp Field's Format string, auto-detecting
// which of three styles it is:
//
//  1. A known preset name (presetLayouts/presetEpochUnits), matched
//     case-sensitively and exactly.
//  2. A strptime pattern, if it starts with '%' (see strptimeToLayout).
//  3. Otherwise, a raw Go reference-time layout string, used as-is - this
//     is what every Format string meant before presets/strptime existed,
//     so existing rules.yaml files keep working unchanged.
//
// format == "" resolves successfully to a zero-value TimeFormat; whether a
// timestamp field requires a non-empty Format is rules.Validate's concern,
// not ResolveFormat's.
func ResolveFormat(format string) (TimeFormat, error) {
	if layout, ok := presetLayouts[format]; ok {
		return TimeFormat{Layout: layout}, nil
	}
	if unit, ok := presetEpochUnits[format]; ok {
		return TimeFormat{EpochUnit: unit}, nil
	}
	if strings.HasPrefix(format, "%") {
		layout, err := strptimeToLayout(format)
		if err != nil {
			return TimeFormat{}, err
		}
		return TimeFormat{Layout: layout}, nil
	}
	return TimeFormat{Layout: format}, nil
}

// strptimeToLayout translates a strptime-style format string (%-prefixed
// directives, see strptimeDirectives) into a Go reference-time layout
// string. Characters other than a recognized %-directive are copied
// through unchanged, so literal separators (spaces, colons, commas, ...)
// need no special handling.
func strptimeToLayout(format string) (string, error) {
	runes := []rune(format)
	var b strings.Builder
	for i := 0; i < len(runes); i++ {
		if runes[i] != '%' {
			b.WriteRune(runes[i])
			continue
		}
		i++
		if i >= len(runes) {
			return "", fmt.Errorf("strptime format %q: trailing %%", format)
		}
		token, ok := strptimeDirectives[runes[i]]
		if !ok {
			return "", fmt.Errorf("strptime format %q: unsupported directive %%%c (supported: %%Y %%y %%m %%d %%H %%I %%M %%S %%f %%z %%Z %%a %%A %%b %%B %%p %%%%)", format, runes[i])
		}
		b.WriteString(token)
	}
	return b.String(), nil
}
