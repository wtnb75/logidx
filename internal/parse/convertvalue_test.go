package parse

import (
	"regexp"
	"testing"
	"time"

	"github.com/wtnb75/logidx/internal/rules"
)

func TestConvertValue_String(t *testing.T) {
	now := time.Now()
	v, err := convertValue("hello", rules.Field{Type: "string"}, now, nil)
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
	v, err := convertValue("Warning", field, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "WARN" {
		t.Errorf("got %v, want %q", v, "WARN")
	}
}

func replaceRule(t *testing.T, pattern, replacement string) rules.ReplaceRule {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return rules.ReplaceRule{Pattern: pattern, Replacement: replacement, Regexp: re}
}

func TestConvertValue_ReplaceRemovesSubstringPreservingRest(t *testing.T) {
	field := rules.Field{Type: "string", Replace: []rules.ReplaceRule{
		replaceRule(t, `#\d{3}`, ""),
	}}
	now := time.Now()
	v, err := convertValue("line one#015line two", field, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "line oneline two" {
		t.Errorf("got %v, want %q", v, "line oneline two")
	}
}

func TestConvertValue_ReplaceChainsInDeclarationOrder(t *testing.T) {
	field := rules.Field{Type: "string", Replace: []rules.ReplaceRule{
		replaceRule(t, "a", "b"),
		replaceRule(t, "b", "c"),
	}}
	now := time.Now()
	v, err := convertValue("aaa", field, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// If the rules ran independently (not chained), "aaa" -replace a->b-> "bbb"
	// and a second independent pass over the *original* value would never see
	// any "b" to replace. Chaining forces "bbb" through rule 2, giving "ccc".
	if v != "ccc" {
		t.Errorf("got %v, want %q (rules must chain: rule 2 sees rule 1's output)", v, "ccc")
	}
}

func TestConvertValue_ReplaceSupportsCaptureGroupBackreference(t *testing.T) {
	field := rules.Field{Type: "string", Replace: []rules.ReplaceRule{
		replaceRule(t, `\((\d+)\)`, "[$1]"),
	}}
	now := time.Now()
	v, err := convertValue("retry(3) failed", field, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "retry[3] failed" {
		t.Errorf("got %v, want %q", v, "retry[3] failed")
	}
}

func TestConvertValue_ReplaceAppliesBeforeNormalize(t *testing.T) {
	field := rules.Field{
		Type: "string",
		Replace: []rules.ReplaceRule{
			replaceRule(t, `\x1b\[[0-9;]*m`, ""),
		},
		Normalize: []rules.NormalizeRule{
			normRule(t, `(?i)^warn(ing)?$`, "WARN"),
		},
	}
	now := time.Now()
	// The raw value only matches normalize's ^warn(ing)?$ pattern once the
	// ANSI escape codes around it are stripped by replace first.
	v, err := convertValue("\x1b[33mWARNING\x1b[0m", field, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "WARN" {
		t.Errorf("got %v, want %q (replace must run before normalize)", v, "WARN")
	}
}

func TestConvertValue_Int(t *testing.T) {
	now := time.Now()
	v, err := convertValue("512", rules.Field{Type: "int"}, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != int64(512) {
		t.Errorf("got %v (%T), want int64(512)", v, v)
	}
}

func TestConvertValue_IntInvalidIsError(t *testing.T) {
	now := time.Now()
	_, err := convertValue("not-a-number", rules.Field{Type: "int"}, now, nil)
	if err == nil {
		t.Fatal("expected error for invalid int")
	}
}

func TestConvertValue_Float(t *testing.T) {
	now := time.Now()
	v, err := convertValue("3.14", rules.Field{Type: "float"}, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 3.14 {
		t.Errorf("got %v, want 3.14", v)
	}
}

func timestampField(t *testing.T, format string) rules.Field {
	t.Helper()
	tf, err := rules.ResolveFormat(format)
	if err != nil {
		t.Fatalf("rules.ResolveFormat(%q): %v", format, err)
	}
	return rules.Field{Type: "timestamp", Format: format, ResolvedFormat: tf}
}

func TestConvertValue_Timestamp(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	field := timestampField(t, "2006-01-02T15:04:05Z07:00")
	v, err := convertValue("2026-08-06T12:00:01+09:00", field, now, nil)
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
	field := timestampField(t, "2006-01-02T15:04:05Z07:00")
	_, err := convertValue("not-a-timestamp", field, now, nil)
	if err == nil {
		t.Fatal("expected error for invalid timestamp")
	}
}

func TestConvertValue_TimestampPreset(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	field := timestampField(t, "iso8601")
	v, err := convertValue("2026-08-06T12:00:01Z", field, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tm, ok := v.(time.Time)
	if !ok {
		t.Fatalf("got %T, want time.Time", v)
	}
	if !tm.Equal(time.Date(2026, 8, 6, 12, 0, 1, 0, time.UTC)) {
		t.Errorf("unexpected parsed time: %v", tm)
	}
}

func TestConvertValue_TimestampStrptimeWithCommaFractionalSeconds(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	field := timestampField(t, "%Y-%m-%d %H:%M:%S,%f")
	v, err := convertValue("2026-08-06 12:00:01,500", field, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tm, ok := v.(time.Time)
	if !ok {
		t.Fatalf("got %T, want time.Time", v)
	}
	if tm.Nanosecond() != 500000000 {
		t.Errorf("expected 500ms fractional seconds, got nanosecond=%d (%v)", tm.Nanosecond(), tm)
	}
}

func TestConvertValue_TimestampEpoch(t *testing.T) {
	now := time.Now()
	field := timestampField(t, "unix")
	v, err := convertValue("1754557200", field, now, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tm, ok := v.(time.Time)
	if !ok {
		t.Fatalf("got %T, want time.Time", v)
	}
	if !tm.Equal(time.Unix(1754557200, 0)) {
		t.Errorf("unexpected parsed time: %v", tm)
	}
}

func TestConvertValue_PatternMaskAppliesAfterReplaceAndNormalizeForStringType(t *testing.T) {
	now := time.Now()
	patternRules := []rules.MaskRule{mustMaskRuleT(t, "pattern", `[\w.+-]+@[\w.-]+\.\w+`, "redact", "[EMAIL]", 0)}

	v, err := convertValue("contact a@example.com", rules.Field{Type: "string"}, now, patternRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "contact [EMAIL]" {
		t.Errorf("convertValue() = %v, want contact [EMAIL]", v)
	}
}

func TestConvertValue_PatternMaskNotAppliedToIntType(t *testing.T) {
	now := time.Now()
	patternRules := []rules.MaskRule{mustMaskRuleT(t, "pattern", `\d+`, "redact", "[NUM]", 0)}

	v, err := convertValue("512", rules.Field{Type: "int"}, now, patternRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != int64(512) {
		t.Errorf("convertValue() = %v, want unmasked int64(512)", v)
	}
}
