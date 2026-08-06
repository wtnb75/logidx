package parse

import (
	"testing"
	"time"
)

func TestParseTimestamp_FormatWithYear(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("06/Aug/2026:12:00:00 +0900", "02/Jan/2006:15:04:05 -0700", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2026 || got.Month() != time.August || got.Day() != 6 {
		t.Errorf("unexpected parsed time: %v", got)
	}
}

func TestParseTimestamp_NoYear_UsesCurrentYearWhenNotFuture(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("Aug  1 09:00:00", "Jan _2 15:04:05", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2026 {
		t.Errorf("expected year 2026, got %d", got.Year())
	}
}

func TestParseTimestamp_NoYear_FallsBackToPreviousYearWhenFuture(t *testing.T) {
	// "now" is Jan 2, log line says Dec 31 -> should resolve to previous year.
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("Dec 31 23:59:59", "Jan _2 15:04:05", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2025 {
		t.Errorf("expected year 2025 (previous year), got %d", got.Year())
	}
	if got.Month() != time.December || got.Day() != 31 {
		t.Errorf("unexpected month/day: %v", got)
	}
}

func TestParseTimestamp_NoYear_NonUTCLocation_InterpretsWallClockInLocalZone(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	// now is Aug 7 02:00 JST; the log line is 2 hours earlier the same day.
	// The old bug (time.Parse, which always produces UTC for a zone-less
	// format) would read "Aug 7 00:00:00" as UTC, i.e. equivalent to
	// Aug 7 09:00 JST wall-clock - which is *after* a JST "now" of
	// Aug 7 02:00, so the buggy code would wrongly decrement the year to
	// 2025. Assert the fixed code interprets the wall-clock in now's (JST)
	// location and keeps the correct (current) year.
	now := time.Date(2026, 8, 7, 2, 0, 0, 0, jst)
	got, err := parseTimestamp("Aug  7 00:00:00", "Jan _2 15:04:05", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2026 {
		t.Errorf("expected year 2026 (not decremented), got %d", got.Year())
	}
	if got.Month() != time.August || got.Day() != 7 || got.Hour() != 0 {
		t.Errorf("unexpected parsed time: %v", got)
	}
	if _, offset := got.Zone(); offset != 9*60*60 {
		t.Errorf("expected result in JST (+9h offset), got offset %d", offset)
	}
}

func TestParseTimestamp_NoYear_NonUTCLocation_FutureWallClockFallsBackToPreviousYear(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	// now is Jan 2 00:30 JST; the log line "Jan 2 01:00" has no year and,
	// interpreted in JST, is a half hour in the future relative to now - so
	// it must resolve to the previous year, exercising the future-check
	// comparison in the same (JST) zone as now.
	now := time.Date(2026, 1, 2, 0, 30, 0, 0, jst)
	got, err := parseTimestamp("Jan  2 01:00:00", "Jan _2 15:04:05", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2025 {
		t.Errorf("expected year 2025 (previous year, since wall-clock is future in JST), got %d", got.Year())
	}
}

func TestParseTimestamp_FormatWithYearNoZone_UsesNowsLocation(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, jst)
	got, err := parseTimestamp("2026-08-06 09:00:00", "2006-01-02 15:04:05", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2026 || got.Month() != time.August || got.Day() != 6 || got.Hour() != 9 {
		t.Errorf("unexpected parsed time: %v", got)
	}
	if _, offset := got.Zone(); offset != 9*60*60 {
		t.Errorf("expected result interpreted in JST (+9h offset) rather than UTC, got offset %d", offset)
	}
}

func TestParseTimestamp_InvalidInputReturnsError(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, err := parseTimestamp("not-a-timestamp", "Jan _2 15:04:05", now)
	if err == nil {
		t.Fatal("expected error for unparsable timestamp")
	}
}
