package parse

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"logidx/internal/rules"
)

// parseTimestamp parses raw according to tf, which was resolved once from
// a Field's Format string by rules.ResolveFormat (see
// internal/rules/timeformat.go) rather than re-interpreted here on every
// call. now is used both as the timezone reference for layout-based
// parsing and as the reference instant for filling in a missing year (see
// parseTimestampLayout); epoch-based formats ignore it, since an epoch
// value is always absolute and has no ambiguity to resolve against now.
func parseTimestamp(raw string, tf rules.TimeFormat, now time.Time) (time.Time, error) {
	if tf.EpochUnit != 0 {
		return parseTimestampEpoch(raw, tf.EpochUnit)
	}
	return parseTimestampLayout(raw, tf.Layout, now)
}

// parseTimestampLayout parses raw using a Go reference-time layout. If the
// layout contains no year token ("2006"), the parsed year is resolved to
// the nearest year that is not in the future relative to now: try
// now.Year(), and if that combined with the parsed month/day/time would be
// after now, use the previous year instead.
func parseTimestampLayout(raw, layout string, now time.Time) (time.Time, error) {
	t, err := time.ParseInLocation(layout, raw, now.Location())
	if err != nil {
		return time.Time{}, err
	}

	if strings.Contains(layout, "2006") {
		return t, nil
	}

	candidate := time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	if candidate.After(now) {
		candidate = candidate.AddDate(-1, 0, 0)
	}
	return candidate, nil
}

// parseTimestampEpoch parses raw as a number of unit ticks since the Unix
// epoch (e.g. unit=time.Millisecond for milliseconds-since-epoch). Integer
// input is parsed exactly via int64 arithmetic; only input containing a
// decimal point falls back to float64, which is adequate for fractional
// seconds but would lose precision on very large integer nanosecond values
// (float64 has ~53 bits of exact integer precision, and nanosecond epoch
// values today are already around 1.7e18).
func parseTimestampEpoch(raw string, unit time.Duration) (time.Time, error) {
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(0, n*int64(unit)), nil
	}

	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse epoch value: %w", err)
	}
	return time.Unix(0, int64(f*float64(unit))), nil
}
