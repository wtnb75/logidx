package rules

import (
	"regexp"
	"strings"
	"testing"

	"logidx/internal/rowgroup"
)

func mustCompile(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return re
}

func TestValidate_FieldWithoutCaptureGroupIsError(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: `^(?P<a>\S+)$`,
				Regexp:  mustCompile(t, `^(?P<a>\S+)$`),
				Fields: []Field{
					{Name: "a", Type: "string"},
					{Name: "b", Type: "string"}, // no capture group named "b"
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Errorf("expected error to mention field %q, got: %v", "b", err)
	}
}

func TestValidate_UnusedCaptureGroupIsIgnored(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "ok",
				Pattern: `^(?P<a>\S+) (?P<_sep>\s*)(?P<b>\S+)$`,
				Regexp:  mustCompile(t, `^(?P<a>\S+) (?P<_sep>\s*)(?P<b>\S+)$`),
				Fields: []Field{
					{Name: "a", Type: "string"},
					{Name: "b", Type: "string"},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_SameNameRulesMustHaveIdenticalFields(t *testing.T) {
	patternA := `^(?P<a>\S+)$`
	patternB := `^(?P<a>\d+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "dup",
				Pattern: patternA,
				Regexp:  mustCompile(t, patternA),
				Fields:  []Field{{Name: "a", Type: "string"}},
			},
			{
				Name:    "dup",
				Pattern: patternB,
				Regexp:  mustCompile(t, patternB),
				Fields:  []Field{{Name: "a", Type: "int"}}, // type mismatch
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for mismatched same-name rules, got nil")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("expected error to mention rule name %q, got: %v", "dup", err)
	}
}

func TestValidate_SameNameRulesWithIdenticalFieldsPass(t *testing.T) {
	patternA := `^(?P<a>\S+)$`
	patternB := `^(?P<a>\d+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "dup",
				Pattern: patternA,
				Regexp:  mustCompile(t, patternA),
				Fields:  []Field{{Name: "a", Type: "string"}},
			},
			{
				Name:    "dup",
				Pattern: patternB,
				Regexp:  mustCompile(t, patternB),
				Fields:  []Field{{Name: "a", Type: "string"}}, // matches
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_SameNameRulesWithFieldsInDifferentOrderIsError(t *testing.T) {
	patternA := `^(?P<a>\S+) (?P<b>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "dup",
				Pattern: patternA,
				Regexp:  mustCompile(t, patternA),
				Fields:  []Field{{Name: "a", Type: "string"}, {Name: "b", Type: "string"}},
			},
			{
				Name:    "dup",
				Pattern: patternA,
				Regexp:  mustCompile(t, patternA),
				Fields:  []Field{{Name: "b", Type: "string"}, {Name: "a", Type: "string"}}, // same fields, different order
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for same-name rules declaring fields in a different order, got nil")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("expected error to mention rule name %q, got: %v", "dup", err)
	}
}

func TestValidate_UnknownFieldTypeIsError(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: `^(?P<a>\S+)$`,
				Regexp:  mustCompile(t, `^(?P<a>\S+)$`),
				Fields:  []Field{{Name: "a", Type: "bogus"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("expected error mentioning unknown type, got: %v", err)
	}
}

func TestValidate_TimestampFieldWithoutFormatIsError(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: `^(?P<a>\S+)$`,
				Regexp:  mustCompile(t, `^(?P<a>\S+)$`),
				Fields:  []Field{{Name: "a", Type: "timestamp"}}, // no Format
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for timestamp field without format")
	}
}

func TestValidate_InvalidRowGroupSettingIsError(t *testing.T) {
	badMaxRows := int64(0)
	cfg := &Config{
		RowGroup: rowgroup.Settings{MaxRows: &badMaxRows},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "row_group") {
		t.Errorf("expected error to mention row_group, got: %v", err)
	}
}

func TestValidate_ContinuationCaptureGroupNotDeclaredFieldIsError(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	contPattern := `^  (?P<b>.*)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:               "bad",
				Pattern:            pattern,
				Regexp:             mustCompile(t, pattern),
				Continuation:       contPattern,
				ContinuationRegexp: mustCompile(t, contPattern),
				Fields:             []Field{{Name: "a", Type: "string"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Errorf("expected error to mention capture group %q, got: %v", "b", err)
	}
}

func TestValidate_ContinuationWithMatchingCaptureGroupsPasses(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	contPattern := `^  (?P<a>.*)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:               "ok",
				Pattern:            pattern,
				Regexp:             mustCompile(t, pattern),
				Continuation:       contPattern,
				ContinuationRegexp: mustCompile(t, contPattern),
				Fields:             []Field{{Name: "a", Type: "string"}},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_ContinuationWithZeroCaptureGroupsPasses(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	contPattern := `^----$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:               "ok",
				Pattern:            pattern,
				Regexp:             mustCompile(t, pattern),
				Continuation:       contPattern,
				ContinuationRegexp: mustCompile(t, contPattern),
				Fields:             []Field{{Name: "a", Type: "string"}},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}
