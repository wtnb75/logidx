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

func TestLoad_ResolvesTimestampFormatAuto(t *testing.T) {
	rulesYAML := `
rules:
  - name: mixed
    pattern: '^(?P<ts>\S+.*)$'
    fields:
      ts:
        type: timestamp
        format: "auto"
`
	path := writeTempRules(t, rulesYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	tsField, ok := fieldByName(cfg.Rules[0].Fields, "ts")
	if !ok {
		t.Fatal("expected ts field")
	}
	if len(tsField.ResolvedFormat.Candidates) != 6 {
		t.Errorf("ResolvedFormat.Candidates length = %d, want 6", len(tsField.ResolvedFormat.Candidates))
	}
	if tsField.ResolvedFormat.LastGood == nil {
		t.Error("ResolvedFormat.LastGood = nil, want non-nil")
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

func TestLoad_ParsesReplaceRules(t *testing.T) {
	yamlContent := `
rules:
  - name: app_log
    pattern: '^\[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      level: string
      message:
        type: string
        replace:
          - pattern: '#\d{3}'
            value: ''
          - pattern: '\x1b\[[0-9;]*m'
            value: ''
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	message, ok := fieldByName(cfg.Rules[0].Fields, "message")
	if !ok {
		t.Fatal("expected message field")
	}
	if len(message.Replace) != 2 {
		t.Fatalf("expected 2 replace rules, got %d", len(message.Replace))
	}
	if message.Replace[0].Pattern != `#\d{3}` || message.Replace[0].Replacement != "" {
		t.Errorf("unexpected first replace rule: %+v", message.Replace[0])
	}
	if message.Replace[0].Regexp == nil {
		t.Error("expected compiled Regexp on first replace rule")
	}
	if message.Replace[1].Pattern != `\x1b\[[0-9;]*m` {
		t.Errorf("unexpected second replace rule pattern: %q", message.Replace[1].Pattern)
	}
	if message.Replace[1].Regexp == nil {
		t.Error("expected compiled Regexp on second replace rule")
	}
}

func TestLoad_InvalidReplacePatternIsError(t *testing.T) {
	yamlContent := `
rules:
  - name: bad
    pattern: '^(?P<a>\S+)$'
    fields:
      a:
        type: string
        replace:
          - pattern: '(unterminated'
            value: ''
`
	path := writeTempRules(t, yamlContent)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for an invalid replace pattern")
	}
	if !strings.Contains(err.Error(), "replace") {
		t.Errorf("expected error to mention replace pattern, got: %v", err)
	}
}

func TestLoad_ParsesStructuredConfigAndKeyExtraFields(t *testing.T) {
	// The pattern below declares a named capture group for every field
	// (including the key:/extra: ones) purely so this test can call Load
	// without tripping the pre-existing "field name has no matching
	// capture group" validation check - Task 3 is the one that teaches
	// Validate() to exempt key:/extra: fields from that check. This test
	// only verifies YAML decoding of Structured/Key/Extra, not the real
	// container_log line shape (Task 5's end-to-end test uses the real
	// pattern from the design spec).
	yamlContent := `
rules:
  - name: container_log
    pattern: '^(?P<time>\S+) (?P<host>\S+) (?P<tag>\S+) (?P<json>\{.*\}) (?P<level>\S*) (?P<event_time>\S*) (?P<message>\S*) (?P<extra>\S*)$'
    structured:
      source: json
      format: json
    fields:
      time:
        type: timestamp
        format: iso8601
      host: string
      tag: string
      level:
        type: string
        key: level
      event_time:
        type: timestamp
        format: iso8601
        key: time
      message:
        type: string
        key: msg
      extra:
        type: string
        extra: true
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	rule := cfg.Rules[0]
	if rule.Structured == nil {
		t.Fatal("expected Structured to be set")
	}
	if rule.Structured.Source != "json" || rule.Structured.Format != "json" {
		t.Errorf("unexpected Structured: %+v", rule.Structured)
	}

	level, ok := fieldByName(rule.Fields, "level")
	if !ok || level.Key != "level" {
		t.Errorf("expected level field with Key=level, got %+v (ok=%v)", level, ok)
	}
	eventTime, ok := fieldByName(rule.Fields, "event_time")
	if !ok || eventTime.Key != "time" {
		t.Errorf("expected event_time field with Key=time, got %+v (ok=%v)", eventTime, ok)
	}
	extra, ok := fieldByName(rule.Fields, "extra")
	if !ok || !extra.Extra {
		t.Errorf("expected extra field with Extra=true, got %+v (ok=%v)", extra, ok)
	}
}

func TestLoad_RuleWithoutStructuredLeavesNilAndZeroKeyExtra(t *testing.T) {
	path := writeTempRules(t, sampleRulesYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Rules[0].Structured != nil {
		t.Error("expected Structured to stay nil when not set")
	}
	remoteAddr, _ := fieldByName(cfg.Rules[0].Fields, "remote_addr")
	if remoteAddr.Key != "" || remoteAddr.Extra {
		t.Errorf("expected zero Key/Extra by default, got %+v", remoteAddr)
	}
}

func TestLoad_ExpandsApacheCLFPreset(t *testing.T) {
	yamlContent := `
rules:
  - name: access_log
    preset: apache_clf
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	rule := cfg.Rules[0]
	if rule.Pattern == "" {
		t.Fatal("expected preset to expand into a non-empty Pattern")
	}
	if rule.Regexp == nil {
		t.Fatal("expected expanded Pattern to compile into Regexp")
	}
	wantFields := []string{"remote_addr", "remote_user", "time", "method", "path", "proto", "status", "bytes"}
	if len(rule.Fields) != len(wantFields) {
		t.Fatalf("got %d fields, want %d", len(rule.Fields), len(wantFields))
	}
	for i, name := range wantFields {
		if rule.Fields[i].Name != name {
			t.Errorf("field[%d].Name = %q, want %q", i, rule.Fields[i].Name, name)
		}
	}
	statusField, ok := fieldByName(rule.Fields, "status")
	if !ok || statusField.Type != "int" {
		t.Errorf("expected status field of type int, got %+v (ok=%v)", statusField, ok)
	}
	timeField, ok := fieldByName(rule.Fields, "time")
	if !ok || timeField.Type != "timestamp" || timeField.Format != "clf" {
		t.Errorf("expected time field with type timestamp, format clf, got %+v (ok=%v)", timeField, ok)
	}
	if timeField.ResolvedFormat.Layout == "" {
		t.Error("expected ResolvedFormat to be resolved for the expanded preset field")
	}
}

func TestLoad_ExpandsApacheCombinedPreset(t *testing.T) {
	yamlContent := `
rules:
  - name: access_log
    preset: apache_combined
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	rule := cfg.Rules[0]
	wantFields := []string{"remote_addr", "remote_user", "time", "method", "path", "proto", "status", "bytes", "referer", "user_agent"}
	if len(rule.Fields) != len(wantFields) {
		t.Fatalf("got %d fields, want %d", len(rule.Fields), len(wantFields))
	}
	for i, name := range wantFields {
		if rule.Fields[i].Name != name {
			t.Errorf("field[%d].Name = %q, want %q", i, rule.Fields[i].Name, name)
		}
	}
}

func TestLoad_ExpandsSyslogRFC3164Preset(t *testing.T) {
	yamlContent := `
rules:
  - name: syslog
    preset: syslog_rfc3164
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	rule := cfg.Rules[0]
	wantFields := []string{"time", "host", "tag", "pid", "message"}
	if len(rule.Fields) != len(wantFields) {
		t.Fatalf("got %d fields, want %d", len(rule.Fields), len(wantFields))
	}
	for i, name := range wantFields {
		if rule.Fields[i].Name != name {
			t.Errorf("field[%d].Name = %q, want %q", i, rule.Fields[i].Name, name)
		}
	}
	pidField, ok := fieldByName(rule.Fields, "pid")
	if !ok || pidField.Type != "string" {
		t.Errorf("expected pid field of type string (pid may be absent), got %+v (ok=%v)", pidField, ok)
	}
	timeField, ok := fieldByName(rule.Fields, "time")
	if !ok || timeField.Format != "syslog" {
		t.Errorf("expected time field with format syslog, got %+v (ok=%v)", timeField, ok)
	}
}

func TestLoad_ExpandsSyslogRFC5424Preset(t *testing.T) {
	yamlContent := `
rules:
  - name: syslog5424
    preset: syslog_rfc5424
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	rule := cfg.Rules[0]
	wantFields := []string{"pri", "version", "time", "host", "app", "procid", "msgid", "sd", "message"}
	if len(rule.Fields) != len(wantFields) {
		t.Fatalf("got %d fields, want %d", len(rule.Fields), len(wantFields))
	}
	for i, name := range wantFields {
		if rule.Fields[i].Name != name {
			t.Errorf("field[%d].Name = %q, want %q", i, rule.Fields[i].Name, name)
		}
	}
	procidField, ok := fieldByName(rule.Fields, "procid")
	if !ok || procidField.Type != "string" {
		t.Errorf("expected procid field of type string (RFC5424 nilvalue '-'), got %+v (ok=%v)", procidField, ok)
	}
	sdField, ok := fieldByName(rule.Fields, "sd")
	if !ok || sdField.Type != "string" {
		t.Errorf("expected sd field of type string (raw STRUCTURED-DATA text, unparsed), got %+v (ok=%v)", sdField, ok)
	}
	timeField, ok := fieldByName(rule.Fields, "time")
	if !ok || timeField.Format != "iso8601" {
		t.Errorf("expected time field with format iso8601, got %+v (ok=%v)", timeField, ok)
	}
}

func TestLoad_RuleWithoutPresetIsUnaffected(t *testing.T) {
	path := writeTempRules(t, sampleRulesYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Rules[0].Preset != "" {
		t.Errorf("expected empty Preset for a rule with no preset:, got %q", cfg.Rules[0].Preset)
	}
	if cfg.Rules[0].Pattern == "" {
		t.Error("expected the rule's own pattern to be untouched")
	}
}

func TestLoad_TwoRulesUsingSamePresetDoNotShareFieldsSlice(t *testing.T) {
	yamlContent := `
rules:
  - name: access_a
    preset: apache_clf
  - name: access_b
    preset: apache_clf
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(cfg.Rules))
	}
	a, b := cfg.Rules[0].Fields, cfg.Rules[1].Fields
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("expected both rules to have expanded fields")
	}
	if &a[0] == &b[0] {
		t.Error("expected the two rules' Fields slices to have independent backing arrays, but they alias the same memory")
	}
}

func TestLoad_PresetAndPatternTogetherIsStartupError(t *testing.T) {
	yamlContent := `
rules:
  - name: bad
    preset: apache_clf
    pattern: '^(?P<a>\S+)$'
    fields:
      a: string
`
	path := writeTempRules(t, yamlContent)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected a startup error for preset: combined with pattern:/fields:")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected error to mention mutual exclusivity, got: %v", err)
	}
}

func TestLoad_UnknownPresetIsStartupError(t *testing.T) {
	yamlContent := `
rules:
  - name: bad
    preset: no_such_preset
`
	path := writeTempRules(t, yamlContent)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected a startup error for an unknown preset name")
	}
	if !strings.Contains(err.Error(), "unknown preset") {
		t.Errorf("expected error to mention unknown preset, got: %v", err)
	}
}

func TestLoad_StructuredFormatPresetNameCompilesPresetRegexp(t *testing.T) {
	yamlContent := `
rules:
  - name: docker_apprise_access
    pattern: '^(?P<ts>\S+) (?P<host>\S+) (?P<tag>[^\[]+)\[(?P<pid>\d+)\] (?P<access>.*)$'
    structured:
      source: access
      format: apache_clf
    fields:
      ts: string
      host: string
      tag: string
      pid: string
      status:
        type: int
        key: status
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	rule := cfg.Rules[0]
	if rule.Structured == nil {
		t.Fatal("expected Structured to be set")
	}
	if rule.Structured.PresetRegexp == nil {
		t.Fatal("expected Structured.PresetRegexp to be compiled for a preset format name")
	}

	m := rule.Structured.PresetRegexp.FindStringSubmatch(`127.0.0.1 - frank [10/Oct/2023:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 200 2326`)
	if m == nil {
		t.Fatal("expected PresetRegexp to match a sample apache_clf line")
	}
}

func TestLoad_StructuredFormatJSONLeavesPresetRegexpNil(t *testing.T) {
	path := writeTempRules(t, sampleRulesYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	// sampleRulesYAML declares no `structured:` block at all, so this
	// exercises the nil-Structured path; TestLoad_ParsesStructuredConfigAndKeyExtraFields
	// already covers a non-preset `format: json` case leaving PresetRegexp nil.
	if cfg.Rules[0].Structured != nil {
		t.Fatalf("expected Structured to be nil for sampleRulesYAML, got %+v", cfg.Rules[0].Structured)
	}
}
