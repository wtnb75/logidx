package rules

import (
	"os"
	"path/filepath"
	"testing"
)

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
	remoteAddr, ok := nginx.Fields["remote_addr"]
	if !ok {
		t.Fatal("expected remote_addr field")
	}
	if remoteAddr.Type != "string" {
		t.Errorf("shorthand field: expected type string, got %q", remoteAddr.Type)
	}
	timeField, ok := nginx.Fields["time"]
	if !ok || timeField.Type != "timestamp" || timeField.Format != "02/Jan/2006:15:04:05 -0700" {
		t.Errorf("expected timestamp field with format, got %+v (ok=%v)", timeField, ok)
	}

	app := cfg.Rules[1]
	level, ok := app.Fields["level"]
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
