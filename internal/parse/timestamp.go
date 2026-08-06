package parse

import (
	"strings"
	"time"
)

// parseTimestamp parses raw using format. If format contains no year token
// ("2006"), the parsed year is resolved to the nearest year that is not in
// the future relative to now: try now.Year(), and if that combined with the
// parsed month/day/time would be after now, use the previous year instead.
func parseTimestamp(raw, format string, now time.Time) (time.Time, error) {
	t, err := time.ParseInLocation(format, raw, now.Location())
	if err != nil {
		return time.Time{}, err
	}

	if strings.Contains(format, "2006") {
		return t, nil
	}

	candidate := time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	if candidate.After(now) {
		candidate = candidate.AddDate(-1, 0, 0)
	}
	return candidate, nil
}
