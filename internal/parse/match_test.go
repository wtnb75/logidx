package parse

import (
	"testing"
	"time"

	"logidx/internal/rules"
)

func mustRule(t *testing.T, name, pattern string, fields map[string]rules.Field) rules.Rule {
	t.Helper()
	re := mustCompileT(t, pattern)
	for fname, f := range fields {
		for i := range f.Normalize {
			f.Normalize[i].Regexp = mustCompileT(t, f.Normalize[i].Pattern)
		}
		fields[fname] = f
	}
	return rules.Rule{Name: name, Pattern: pattern, Regexp: re, Fields: fields}
}

func mustCompileT(t *testing.T, pattern string) *regexpT {
	t.Helper()
	re, err := regexpCompile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return re
}

func TestMatch_FirstMatchingRuleWins(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$`, map[string]rules.Field{
			"time":    {Type: "string"},
			"level":   {Type: "string"},
			"message": {Type: "string"},
		}),
	}

	name, values, ok := Match(ruleList, "2026-08-06T12:00:01+09:00 [INFO] user logged in", now)
	if !ok {
		t.Fatal("expected match, got none")
	}
	if name != "app_log" {
		t.Errorf("expected rule name app_log, got %q", name)
	}
	if values["level"] != "INFO" || values["message"] != "user logged in" {
		t.Errorf("unexpected values: %+v", values)
	}
}

func TestMatch_NoRuleMatches(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^\[(?P<level>\w+)\] (?P<message>.*)$`, map[string]rules.Field{
			"level":   {Type: "string"},
			"message": {Type: "string"},
		}),
	}

	_, _, ok := Match(ruleList, "this line matches nothing", now)
	if ok {
		t.Error("expected no match")
	}
}

func TestMatch_TypeConversionFailureIsUnmatched_NoFallthrough(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		// First rule's pattern matches but "status" won't parse as int.
		mustRule(t, "strict", `^(?P<status>\S+)$`, map[string]rules.Field{
			"status": {Type: "int"},
		}),
		// Second rule would also match the same line if we fell through to it.
		mustRule(t, "loose", `^(?P<status>\S+)$`, map[string]rules.Field{
			"status": {Type: "string"},
		}),
	}

	_, _, ok := Match(ruleList, "not-a-number", now)
	if ok {
		t.Error("expected unmatched: first rule's regex matched but type conversion failed, and there must be no fallthrough to later rules")
	}
}
