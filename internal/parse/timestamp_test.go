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

func TestParseTimestamp_InvalidInputReturnsError(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, err := parseTimestamp("not-a-timestamp", "Jan _2 15:04:05", now)
	if err == nil {
		t.Fatal("expected error for unparsable timestamp")
	}
}
