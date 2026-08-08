package parse

import (
	"testing"
	"time"

	"logidx/internal/rules"
)

func mustRule(t *testing.T, name, pattern string, fields []rules.Field) rules.Rule {
	t.Helper()
	re := mustCompileT(t, pattern)
	for i := range fields {
		for j := range fields[i].Normalize {
			fields[i].Normalize[j].Regexp = mustCompileT(t, fields[i].Normalize[j].Pattern)
		}
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
		mustRule(t, "app_log", `^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "time", Type: "string"},
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
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
		mustRule(t, "app_log", `^\[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
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
		mustRule(t, "strict", `^(?P<status>\S+)$`, []rules.Field{
			{Name: "status", Type: "int"},
		}),
		// Second rule would also match the same line if we fell through to it.
		mustRule(t, "loose", `^(?P<status>\S+)$`, []rules.Field{
			{Name: "status", Type: "string"},
		}),
	}

	_, _, ok := Match(ruleList, "not-a-number", now)
	if ok {
		t.Error("expected unmatched: first rule's regex matched but type conversion failed, and there must be no fallthrough to later rules")
	}
}

func TestMatchRaw_ReturnsRawCapturesWithoutConversion(t *testing.T) {
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "time", Type: "string"},
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
		}),
	}

	rule, raw, ok := MatchRaw(ruleList, "2026-08-06T12:00:01+09:00 [INFO] user logged in")
	if !ok {
		t.Fatal("expected match, got none")
	}
	if rule.Name != "app_log" {
		t.Errorf("rule.Name = %q, want app_log", rule.Name)
	}
	if raw["level"] != "INFO" || raw["message"] != "user logged in" {
		t.Errorf("unexpected raw captures: %+v", raw)
	}
}

func TestMatchRaw_NoRuleMatches(t *testing.T) {
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^\[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
		}),
	}

	_, _, ok := MatchRaw(ruleList, "this line matches nothing")
	if ok {
		t.Error("expected no match")
	}
}

func TestConvert_SuccessConvertsEveryDeclaredField(t *testing.T) {
	rule := mustRule(t, "app_log", `^(?P<status>\S+)$`, []rules.Field{
		{Name: "status", Type: "int"},
	})
	now := time.Now()

	values, err := Convert(rule, map[string]string{"status": "200"}, now)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["status"] != int64(200) {
		t.Errorf("status = %v, want int64(200)", values["status"])
	}
}

func TestConvert_TypeConversionFailureReturnsError(t *testing.T) {
	rule := mustRule(t, "strict", `^(?P<status>\S+)$`, []rules.Field{
		{Name: "status", Type: "int"},
	})
	now := time.Now()

	_, err := Convert(rule, map[string]string{"status": "not-a-number"}, now)
	if err == nil {
		t.Error("expected an error converting a non-numeric value to int")
	}
}

func TestConvert_KeyFieldTakesValueFromStructuredData(t *testing.T) {
	rule := rules.Rule{
		Name:       "container_log",
		Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
		Fields: []rules.Field{
			{Name: "json", Type: "string"},
			{Name: "level", Type: "string", Key: "level"},
			{Name: "message", Type: "string", Key: "msg"},
		},
	}
	now := time.Now()

	values, err := Convert(rule, map[string]string{
		"json": `{"level":"INFO","msg":"caught signal","signal":15}`,
	}, now)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", values["level"])
	}
	if values["message"] != "caught signal" {
		t.Errorf("message = %v, want %q", values["message"], "caught signal")
	}
}

func TestConvert_ExtraFieldCollectsUnconsumedKeysAsSortedJSON(t *testing.T) {
	rule := rules.Rule{
		Name:       "container_log",
		Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
		Fields: []rules.Field{
			{Name: "level", Type: "string", Key: "level"},
			{Name: "extra", Type: "string", Extra: true},
		},
	}
	now := time.Now()

	values, err := Convert(rule, map[string]string{
		"json": `{"level":"INFO","msg":"server starting","signal":15,"pid":1}`,
	}, now)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	want := `{"msg":"server starting","pid":"1","signal":"15"}`
	if values["extra"] != want {
		t.Errorf("extra = %v, want %q", values["extra"], want)
	}
}

func TestConvert_StructuredRuleWithoutExtraFieldSkipsMarshalUnconsumed(t *testing.T) {
	rule := rules.Rule{
		Name:       "container_log",
		Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
		Fields: []rules.Field{
			{Name: "level", Type: "string", Key: "level"},
		},
	}
	now := time.Now()

	values, err := Convert(rule, map[string]string{
		"json": `{"level":"INFO","msg":"server starting","pid":1}`,
	}, now)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", values["level"])
	}
}

func TestConvert_StructuredParseFailureReturnsError(t *testing.T) {
	rule := rules.Rule{
		Name:       "container_log",
		Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
		Fields: []rules.Field{
			{Name: "level", Type: "string", Key: "level"},
		},
	}
	now := time.Now()

	_, err := Convert(rule, map[string]string{"json": "not json"}, now)
	if err == nil {
		t.Error("expected error for malformed structured data")
	}
}

func TestConvert_RuleWithoutStructuredIsUnaffected(t *testing.T) {
	rule := mustRule(t, "app_log", `^(?P<status>\S+)$`, []rules.Field{
		{Name: "status", Type: "int"},
	})
	now := time.Now()

	values, err := Convert(rule, map[string]string{"status": "200"}, now)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["status"] != int64(200) {
		t.Errorf("status = %v, want int64(200)", values["status"])
	}
}
