package rules

import (
	"regexp"
	"testing"
)

func TestIgnoreConfig_Reason_NoConditionsConfiguredNeverIgnores(t *testing.T) {
	var ic IgnoreConfig
	for _, line := range []string{"", "   ", "normal line", "\xff\xfe not utf8"} {
		if reason := ic.Reason(line); reason != "" {
			t.Errorf("line %q: Reason() = %q, want \"\" (zero-value IgnoreConfig)", line, reason)
		}
	}
}

func TestIgnoreConfig_Reason_Empty(t *testing.T) {
	ic := IgnoreConfig{Empty: true}
	for _, line := range []string{"", "   ", "\t\n"} {
		if reason := ic.Reason(line); reason != "empty" {
			t.Errorf("line %q: Reason() = %q, want \"empty\"", line, reason)
		}
	}
	if reason := ic.Reason("not empty"); reason != "" {
		t.Errorf(`Reason("not empty") = %q, want ""`, reason)
	}
}

func TestIgnoreConfig_Reason_InvalidUTF8(t *testing.T) {
	ic := IgnoreConfig{InvalidUTF8: true}
	if reason := ic.Reason("\xff\xfe not valid utf8"); reason != "invalid_utf8" {
		t.Errorf("Reason() = %q, want \"invalid_utf8\"", reason)
	}
	if reason := ic.Reason("valid utf8 é"); reason != "" {
		t.Errorf(`Reason("valid utf8 é") = %q, want ""`, reason)
	}
}

func TestIgnoreConfig_Reason_MaxLength(t *testing.T) {
	ic := IgnoreConfig{MaxLength: 5}
	if reason := ic.Reason("123456"); reason != "max_length" {
		t.Errorf("Reason(6 bytes) = %q, want \"max_length\"", reason)
	}
	if reason := ic.Reason("12345"); reason != "" {
		t.Errorf("Reason(5 bytes, at the limit) = %q, want \"\"", reason)
	}
}

func TestIgnoreConfig_Reason_MaxLengthZeroMeansUnlimited(t *testing.T) {
	ic := IgnoreConfig{MaxLength: 0}
	if reason := ic.Reason(string(make([]byte, 10000))); reason != "" {
		t.Errorf("Reason() = %q, want \"\" (max_length: 0 = unlimited)", reason)
	}
}

func TestIgnoreConfig_Reason_Pattern(t *testing.T) {
	ic := IgnoreConfig{
		PatternsRe: []*regexp.Regexp{
			regexp.MustCompile(`^#`),
			regexp.MustCompile(`^--`),
		},
	}
	if reason := ic.Reason("# a comment"); reason != "pattern" {
		t.Errorf("Reason() = %q, want \"pattern\"", reason)
	}
	if reason := ic.Reason("-- a comment"); reason != "pattern" {
		t.Errorf("Reason() = %q, want \"pattern\"", reason)
	}
	if reason := ic.Reason("not a comment"); reason != "" {
		t.Errorf(`Reason("not a comment") = %q, want ""`, reason)
	}
}

func TestIgnoreConfig_Reason_PriorityOrderEmptyBeatsAllOthers(t *testing.T) {
	// A whitespace-only line trivially passes invalid_utf8/max_length checks
	// too - this locks in that Empty is checked first, in case a pattern
	// like '^\s*$' would otherwise also match and report "pattern" instead.
	ic := IgnoreConfig{
		Empty:      true,
		MaxLength:  1,
		PatternsRe: []*regexp.Regexp{regexp.MustCompile(`^\s*$`)},
	}
	if reason := ic.Reason("   "); reason != "empty" {
		t.Errorf("Reason() = %q, want \"empty\"", reason)
	}
}

func TestIgnoreConfig_Reason_PriorityOrderInvalidUTF8BeatsMaxLengthAndPattern(t *testing.T) {
	ic := IgnoreConfig{
		InvalidUTF8: true,
		MaxLength:   1,
		PatternsRe:  []*regexp.Regexp{regexp.MustCompile(`.`)},
	}
	if reason := ic.Reason("\xff\xfe"); reason != "invalid_utf8" {
		t.Errorf("Reason() = %q, want \"invalid_utf8\"", reason)
	}
}

func TestIgnoreConfig_Reason_PriorityOrderMaxLengthBeatsPattern(t *testing.T) {
	ic := IgnoreConfig{
		MaxLength:  3,
		PatternsRe: []*regexp.Regexp{regexp.MustCompile(`.`)},
	}
	if reason := ic.Reason("abcdef"); reason != "max_length" {
		t.Errorf("Reason() = %q, want \"max_length\"", reason)
	}
}
