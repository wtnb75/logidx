package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fieldByName(fields []Field, name string) (Field, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

func writeTempRules(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp rules file: %v", err)
	}
	return path
}

const sampleRulesYAML = `
rules:
  - name: nginx_access
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: "02/Jan/2006:15:04:05 -0700"
      method: string
      path: string
      proto: string
      status: int
      bytes: int

  - name: app_log
    pattern: '^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      level:
        type: string
        normalize:
          - pattern: '(?i)^warn(ing)?$'
            value: WARN
          - pattern: '(?i)^info$'
            value: INFO
      message: string
`

func TestLoad_ParsesRulesAndFieldShorthand(t *testing.T) {
	path := writeTempRules(t, sampleRulesYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(cfg.Rules))
	}

	nginx := cfg.Rules[0]
	if nginx.Name != "nginx_access" {
		t.Errorf("expected name nginx_access, got %q", nginx.Name)
	}
	if nginx.Regexp == nil {
		t.Fatal("expected compiled Regexp to be set")
	}
	remoteAddr, ok := fieldByName(nginx.Fields, "remote_addr")
	if !ok {
		t.Fatal("expected remote_addr field")
	}
	if remoteAddr.Type != "string" {
		t.Errorf("shorthand field: expected type string, got %q", remoteAddr.Type)
	}
	timeField, ok := fieldByName(nginx.Fields, "time")
	if !ok || timeField.Type != "timestamp" || timeField.Format != "02/Jan/2006:15:04:05 -0700" {
		t.Errorf("expected timestamp field with format, got %+v (ok=%v)", timeField, ok)
	}

	app := cfg.Rules[1]
	level, ok := fieldByName(app.Fields, "level")
	if !ok {
		t.Fatal("expected level field")
	}
	if len(level.Normalize) != 2 {
		t.Fatalf("expected 2 normalize rules, got %d", len(level.Normalize))
	}
	if level.Normalize[0].Pattern != `(?i)^warn(ing)?$` || level.Normalize[0].Value != "WARN" {
		t.Errorf("unexpected first normalize rule: %+v", level.Normalize[0])
	}
	if level.Normalize[0].Regexp == nil {
		t.Error("expected compiled Regexp on normalize rule")
	}
}

func TestLoad_FieldsPreserveDeclarationOrder(t *testing.T) {
	path := writeTempRules(t, sampleRulesYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	nginx := cfg.Rules[0]
	wantOrder := []string{"remote_addr", "remote_user", "time", "method", "path", "proto", "status", "bytes"}
	if len(nginx.Fields) != len(wantOrder) {
		t.Fatalf("got %d fields, want %d", len(nginx.Fields), len(wantOrder))
	}
	for i, name := range wantOrder {
		if nginx.Fields[i].Name != name {
			t.Errorf("field[%d].Name = %q, want %q (YAML declaration order)", i, nginx.Fields[i].Name, name)
		}
	}
}

func TestLoad_ResolvesTimestampFormat(t *testing.T) {
	path := writeTempRules(t, sampleRulesYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	nginx := cfg.Rules[0]
	timeField, ok := fieldByName(nginx.Fields, "time")
	if !ok {
		t.Fatal("expected time field")
	}
	wantLayout := "02/Jan/2006:15:04:05 -0700"
	if timeField.ResolvedFormat.Layout != wantLayout {
		t.Errorf("ResolvedFormat.Layout = %q, want %q", timeField.ResolvedFormat.Layout, wantLayout)
	}
}

func TestLoad_InvalidStrptimeFormatIsError(t *testing.T) {
	rulesYAML := `
rules:
  - name: bad
    pattern: '^(?P<ts>\S+)$'
    fields:
      ts:
        type: timestamp
        format: "%j"
`
	path := writeTempRules(t, rulesYAML)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unsupported strptime directive")
	}
	if !strings.Contains(err.Error(), "%j") {
		t.Errorf("expected error to mention %%j, got: %v", err)
	}
}

func TestLoad_CompilesContinuationPattern(t *testing.T) {
	yamlContent := `
rules:
  - name: syslog
    pattern: '^TS (?P<time>\S+) (?P<message>.*)$'
    continuation: '^  (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      message: string
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	rule := cfg.Rules[0]
	if rule.Continuation != `^  (?P<message>.*)$` {
		t.Errorf("Continuation = %q, want the raw pattern string", rule.Continuation)
	}
	if rule.ContinuationRegexp == nil {
		t.Fatal("expected ContinuationRegexp to be compiled")
	}
	if !rule.ContinuationRegexp.MatchString("  indented text") {
		t.Error("expected compiled ContinuationRegexp to match an indented line")
	}
}

func TestLoad_RuleWithoutContinuationLeavesRegexpNil(t *testing.T) {
	path := writeTempRules(t, sampleRulesYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Rules[0].ContinuationRegexp != nil {
		t.Error("expected ContinuationRegexp to stay nil when continuation is not set")
	}
}

func TestLoad_InvalidContinuationPatternIsError(t *testing.T) {
	yamlContent := `
rules:
  - name: bad
    pattern: '^(?P<a>\S+)$'
    continuation: '^(unterminated'
    fields:
      a: string
`
	path := writeTempRules(t, yamlContent)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for an invalid continuation pattern")
	}
	if !strings.Contains(err.Error(), "continuation") {
		t.Errorf("expected error to mention continuation pattern, got: %v", err)
	}
}
