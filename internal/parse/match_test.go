package parse

import (
	"regexp"
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

func TestMatchAndConvert_TypeConversionFailureFallsThroughToNextRule(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		// First rule's pattern matches but "status" won't parse as int.
		mustRule(t, "strict", `^(?P<status>\S+)$`, []rules.Field{
			{Name: "status", Type: "int"},
		}),
		// Second rule matches the same line and succeeds.
		mustRule(t, "loose", `^(?P<status>\S+)$`, []rules.Field{
			{Name: "status", Type: "string"},
		}),
	}

	rule, _, values, attempts, ok := MatchAndConvert(ruleList, "not-a-number", now)
	if !ok {
		t.Fatal("expected the second rule to succeed after the first fails conversion")
	}
	if rule.Name != "loose" {
		t.Errorf("rule.Name = %q, want loose", rule.Name)
	}
	if values["status"] != "not-a-number" {
		t.Errorf("values[status] = %v, want %q", values["status"], "not-a-number")
	}
	if len(attempts) != 1 || attempts[0].RuleName != "strict" || attempts[0].Err == nil {
		t.Errorf("attempts = %+v, want one failed attempt for rule %q", attempts, "strict")
	}
}

func TestMatchAndConvert_FirstMatchingRuleWins(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "time", Type: "string"},
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
		}),
	}

	rule, _, values, _, ok := MatchAndConvert(ruleList, "2026-08-06T12:00:01+09:00 [INFO] user logged in", now)
	if !ok {
		t.Fatal("expected match, got none")
	}
	if rule.Name != "app_log" {
		t.Errorf("expected rule name app_log, got %q", rule.Name)
	}
	if values["level"] != "INFO" || values["message"] != "user logged in" {
		t.Errorf("unexpected values: %+v", values)
	}
}

func TestMatchAndConvert_NoRuleMatches(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^\[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
		}),
	}

	_, _, _, attempts, ok := MatchAndConvert(ruleList, "this line matches nothing", now)
	if ok {
		t.Error("expected no match")
	}
	if len(attempts) != 0 {
		t.Errorf("expected no attempts when no rule's pattern even matches, got %+v", attempts)
	}
}

func TestMatchAndConvert_MissingStructuredKeyFallsThroughToNextRule(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		{
			Name:       "container_log",
			Regexp:     regexp.MustCompile(`^(?P<json>\{.*\})$`),
			Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
			Fields: []rules.Field{
				{Name: "json", Type: "string"},
				{Name: "level", Type: "string", Key: "level"},
			},
		},
		{
			Name:   "raw_line",
			Regexp: regexp.MustCompile(`^(?P<line>.*)$`),
			Fields: []rules.Field{
				{Name: "line", Type: "string"},
			},
		},
	}

	line := `{"msg":"no level field here"}`
	rule, _, values, attempts, ok := MatchAndConvert(ruleList, line, now)
	if !ok {
		t.Fatal("expected the fallback rule to match after the structured rule's missing key fails")
	}
	if rule.Name != "raw_line" {
		t.Errorf("rule.Name = %q, want raw_line", rule.Name)
	}
	if values["line"] != line {
		t.Errorf("values[line] = %v, want %q", values["line"], line)
	}
	if len(attempts) != 1 || attempts[0].RuleName != "container_log" || attempts[0].Err == nil {
		t.Errorf("attempts = %+v, want one failed attempt for rule %q", attempts, "container_log")
	}
}

func TestMatchAndConvert_AllCandidatesFailBecomesUnmatchedWithAttempts(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		{
			Name:       "container_log",
			Regexp:     regexp.MustCompile(`^(?P<json>\{.*\})$`),
			Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
			Fields: []rules.Field{
				{Name: "json", Type: "string"},
				{Name: "level", Type: "string", Key: "level"},
			},
		},
		mustRule(t, "strict", `^(?P<status>\{.*\})$`, []rules.Field{
			{Name: "status", Type: "int"},
		}),
	}

	line := `{"msg":"no level field here"}`
	rule, raw, values, attempts, ok := MatchAndConvert(ruleList, line, now)
	if ok {
		t.Fatalf("expected no candidate to succeed, got rule=%v raw=%v values=%v", rule, raw, values)
	}
	if rule != nil {
		t.Errorf("rule = %v, want nil", rule)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %+v, want 2 entries", attempts)
	}
	if attempts[0].RuleName != "container_log" || attempts[0].Err == nil {
		t.Errorf("attempts[0] = %+v, want failed attempt for container_log", attempts[0])
	}
	if attempts[1].RuleName != "strict" || attempts[1].Err == nil {
		t.Errorf("attempts[1] = %+v, want failed attempt for strict", attempts[1])
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

func TestConvert_PresetFormatTakesKeyFieldFromPresetMatch(t *testing.T) {
	presetRe := regexp.MustCompile(`^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$`)
	rule := rules.Rule{
		Name:       "docker_apprise_access",
		Structured: &rules.StructuredConfig{Source: "access", Format: "apache_clf", PresetRegexp: presetRe},
		Fields: []rules.Field{
			{Name: "access", Type: "string"},
			{Name: "status", Type: "int", Key: "status"},
			{Name: "method", Type: "string", Key: "method"},
		},
	}
	now := time.Now()

	values, err := Convert(rule, map[string]string{
		"access": `127.0.0.1 - frank [10/Oct/2023:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 200 2326`,
	}, now)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["status"] != int64(200) {
		t.Errorf("status = %v, want int64(200)", values["status"])
	}
	if values["method"] != "GET" {
		t.Errorf("method = %v, want GET", values["method"])
	}
}

func TestConvert_PresetFormatNoMatchReturnsError(t *testing.T) {
	presetRe := regexp.MustCompile(`^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$`)
	rule := rules.Rule{
		Name:       "docker_apprise_access",
		Structured: &rules.StructuredConfig{Source: "access", Format: "apache_clf", PresetRegexp: presetRe},
		Fields: []rules.Field{
			{Name: "access", Type: "string"},
			{Name: "status", Type: "int", Key: "status"},
		},
	}
	now := time.Now()

	_, err := Convert(rule, map[string]string{"access": "not a CLF line"}, now)
	if err == nil {
		t.Error("expected an error when the preset pattern doesn't match the structured source")
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

func TestConvert_MissingStructuredKeyReturnsError(t *testing.T) {
	rule := rules.Rule{
		Name:       "container_log",
		Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
		Fields: []rules.Field{
			{Name: "level", Type: "string", Key: "level"},
		},
	}
	now := time.Now()

	_, err := Convert(rule, map[string]string{
		"json": `{"msg":"no level field here"}`,
	}, now)
	if err == nil {
		t.Fatal("expected an error when structured data has no value for field.Key")
	}
}
