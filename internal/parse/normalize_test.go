package parse

import (
	"regexp"
	"testing"

	"github.com/wtnb75/logidx/internal/rules"
)

func normRule(t *testing.T, pattern, value string) rules.NormalizeRule {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return rules.NormalizeRule{Pattern: pattern, Value: value, Regexp: re}
}

func TestApplyNormalize_FirstMatchWinsCaseInsensitive(t *testing.T) {
	rulesList := []rules.NormalizeRule{
		normRule(t, `(?i)^warn(ing)?$`, "WARN"),
		normRule(t, `(?i)^info$`, "INFO"),
	}

	cases := map[string]string{
		"warn":    "WARN",
		"Warn":    "WARN",
		"WARNING": "WARN",
		"info":    "INFO",
	}
	for input, want := range cases {
		got := applyNormalize(input, rulesList)
		if got != want {
			t.Errorf("applyNormalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestApplyNormalize_NoMatchReturnsOriginal(t *testing.T) {
	rulesList := []rules.NormalizeRule{
		normRule(t, `(?i)^warn(ing)?$`, "WARN"),
	}
	got := applyNormalize("DEBUG", rulesList)
	if got != "DEBUG" {
		t.Errorf("applyNormalize(%q) = %q, want unchanged", "DEBUG", got)
	}
}
