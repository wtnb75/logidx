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

func TestValidate_StructuredWithInvalidFormatIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "xml"},
				Fields:     []Field{{Name: "json", Type: "string"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "xml") {
		t.Errorf("expected error mentioning unsupported structured format, got: %v", err)
	}
}

func TestValidate_StructuredSourceNotACaptureGroupIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "nope", Format: "json"},
				Fields:     []Field{{Name: "json", Type: "string"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error mentioning structured source %q, got: %v", "nope", err)
	}
}

func TestValidate_StructuredValidConfigPasses(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "ok",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields: []Field{
					{Name: "json", Type: "string"},
					{Name: "level", Type: "string", Key: "level"},
					{Name: "extra", Type: "string", Extra: true},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_KeyFieldWithoutStructuredIsError(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields:  []Field{{Name: "level", Type: "string", Key: "level"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "level") {
		t.Errorf("expected error mentioning field %q, got: %v", "level", err)
	}
}

func TestValidate_ExtraFieldWithoutStructuredIsError(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields:  []Field{{Name: "extra", Type: "string", Extra: true}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "extra") {
		t.Errorf("expected error mentioning field %q, got: %v", "extra", err)
	}
}

func TestValidate_KeyAndExtraBothSetIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields:     []Field{{Name: "weird", Type: "string", Key: "level", Extra: true}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "weird") {
		t.Errorf("expected error mentioning field %q, got: %v", "weird", err)
	}
}

func TestValidate_MultipleExtraFieldsIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields: []Field{
					{Name: "extra1", Type: "string", Extra: true},
					{Name: "extra2", Type: "string", Extra: true},
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Errorf("expected error about more than one extra field, got: %v", err)
	}
}

func TestValidate_KeyFieldDoesNotRequireMatchingCaptureGroup(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "ok",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields:     []Field{{Name: "level_field_name_not_a_capture_group", Type: "string", Key: "level"}},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}
