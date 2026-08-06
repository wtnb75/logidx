package rules

import (
	"regexp"
	"strings"
	"testing"
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
				Fields: map[string]Field{
					"a": {Type: "string"},
					"b": {Type: "string"}, // no capture group named "b"
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
				Fields: map[string]Field{
					"a": {Type: "string"},
					"b": {Type: "string"},
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
				Fields:  map[string]Field{"a": {Type: "string"}},
			},
			{
				Name:    "dup",
				Pattern: patternB,
				Regexp:  mustCompile(t, patternB),
				Fields:  map[string]Field{"a": {Type: "int"}}, // type mismatch
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
				Fields:  map[string]Field{"a": {Type: "string"}},
			},
			{
				Name:    "dup",
				Pattern: patternB,
				Regexp:  mustCompile(t, patternB),
				Fields:  map[string]Field{"a": {Type: "string"}}, // matches
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_UnknownFieldTypeIsError(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: `^(?P<a>\S+)$`,
				Regexp:  mustCompile(t, `^(?P<a>\S+)$`),
				Fields:  map[string]Field{"a": {Type: "bogus"}},
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
				Fields:  map[string]Field{"a": {Type: "timestamp"}}, // no Format
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for timestamp field without format")
	}
}
