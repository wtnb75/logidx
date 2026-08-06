package parse

import (
	"testing"
	"time"

	"logidx/internal/rules"
)

func TestConvertValue_String(t *testing.T) {
	now := time.Now()
	v, err := convertValue("hello", rules.Field{Type: "string"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "hello" {
		t.Errorf("got %v, want %q", v, "hello")
	}
}

func TestConvertValue_StringWithNormalize(t *testing.T) {
	field := rules.Field{Type: "string", Normalize: []rules.NormalizeRule{
		normRule(t, `(?i)^warn(ing)?$`, "WARN"),
	}}
	now := time.Now()
	v, err := convertValue("Warning", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "WARN" {
		t.Errorf("got %v, want %q", v, "WARN")
	}
}

func TestConvertValue_Int(t *testing.T) {
	now := time.Now()
	v, err := convertValue("512", rules.Field{Type: "int"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != int64(512) {
		t.Errorf("got %v (%T), want int64(512)", v, v)
	}
}

func TestConvertValue_IntInvalidIsError(t *testing.T) {
	now := time.Now()
	_, err := convertValue("not-a-number", rules.Field{Type: "int"}, now)
	if err == nil {
		t.Fatal("expected error for invalid int")
	}
}

func TestConvertValue_Float(t *testing.T) {
	now := time.Now()
	v, err := convertValue("3.14", rules.Field{Type: "float"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 3.14 {
		t.Errorf("got %v, want 3.14", v)
	}
}

func TestConvertValue_Timestamp(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	field := rules.Field{Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"}
	v, err := convertValue("2026-08-06T12:00:01+09:00", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tm, ok := v.(time.Time)
	if !ok {
		t.Fatalf("got %T, want time.Time", v)
	}
	if tm.Year() != 2026 || tm.Month() != time.August || tm.Day() != 6 {
		t.Errorf("unexpected parsed time: %v", tm)
	}
}

func TestConvertValue_TimestampInvalidIsError(t *testing.T) {
	now := time.Now()
	field := rules.Field{Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"}
	_, err := convertValue("not-a-timestamp", field, now)
	if err == nil {
		t.Fatal("expected error for invalid timestamp")
	}
}
