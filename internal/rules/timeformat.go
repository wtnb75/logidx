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
//
// Candidates != nil means "format: auto": Layout and EpochUnit are unused,
// and internal/parse tries each layout in Candidates (see LastGood) instead.
type TimeFormat struct {
	Layout    string
	EpochUnit time.Duration

	// Candidates holds the ordered layout strings to try when this
	// TimeFormat came from format: "auto". Empty for every other format.
	Candidates []string

	// LastGood indexes into Candidates: the position that parsed
	// successfully last time, tried first on the next call. Shared via
	// pointer across every copy of this TimeFormat (Field is copied by
	// value on each call), since input processing is single-threaded (no
	// goroutines) - see internal/convert. If that ever changes, updates to
	// *LastGood need to become atomic or field-scoped locking needs to be
	// added. nil for every non-auto format. Exported (like Layout/
	// EpochUnit) so internal/parse, a different package, can read and
	// update it.
	LastGood *int
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

// autoCandidateLayouts is the fixed, ordered list of layout-preset layouts
// that format: "auto" tries. Order matters: it is the order candidates are
// attempted in when no LastGood cache hit is available. Epoch presets
// (unix/unix_ms/unix_us/unix_ns) and strptime/raw layouts are deliberately
// excluded - see the Non-goals section of
// docs/superpowers/specs/2026-08-10-auto-timestamp-format-design.md.
var autoCandidateLayouts = []string{
	presetLayouts["iso8601"],
	presetLayouts["rfc2822"],
	presetLayouts["rfc822"],
	presetLayouts["clf"],
	presetLayouts["syslog"],
	presetLayouts["pylog"],
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
// which of four styles it is:
//
//  0. The literal string "auto": returns a TimeFormat whose Candidates
//     holds the fixed, ordered auto-detection layout list (see
//     autoCandidateLayouts) and whose LastGood is a freshly allocated
//     pointer, independent from every other ResolveFormat call.
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
	if format == "auto" {
		lastGood := 0
		return TimeFormat{Candidates: autoCandidateLayouts, LastGood: &lastGood}, nil
	}
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
