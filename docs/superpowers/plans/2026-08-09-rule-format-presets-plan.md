# よく使われるログ形式のプリセット機能 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `rules.yaml`で`preset: apache_clf`のように1行書くだけで、Apache/nginx CLF・Combined・BSD syslog(RFC3164)・syslog protocol(RFC5424)の4形式を`pattern:`/`fields:`を手書きせずに使えるようにする。

**Architecture:** 新規`internal/rules/presets.go`に4プリセットの`{Pattern string, Fields []Field}`を固定データとして持つレジストリを置く。`rules.Load()`は`Rule.Preset`が設定されていれば、既存の`regexp.Compile`より前にレジストリから`Pattern`/`Fields`を展開してから、以降は無変更の既存コンパイル・検証パスに乗せる。`Config.Validate()`に`preset:`と`pattern:`/`fields:`の排他チェック・未知プリセット名チェックを追加する。

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`(既存)、標準ライブラリの`slices`(プリセットの`Fields`をルールごとに複製するため)。

## Global Constraints

- 対応するプリセットは`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`の4つのみ。
- プリセットの内容は完全固定。部分カスタマイズ(特定フィールドの`format`だけ上書き等)は非対応 — カスタマイズしたい場合は本doc記載の内容をコピーして自分の`pattern`/`fields`として書き換える運用とする。
- `preset:`と`pattern:`/`fields:`の同時指定は起動時バリデーションエラー(`rule %q: preset and pattern/fields are mutually exclusive`)。
- 未知のプリセット名は起動時バリデーションエラー(`rule %q: unknown preset %q`)。
- `continuation:`/`structured:`/`compression:`など他の設定とは独立して併用できる(プリセットは`pattern`/`fields`だけを置き換える)。
- プリセットのパターンが実際のログ行にマッチしない場合は、既存の「パターン不一致」と全く同じ扱いで`unmatched.txt`へ。プリセット専用のエラー処理は追加しない。
- RFC5424のSTRUCTURED-DATA部分の中身はパースせず、生テキストのまま`sd`列(`string`型)に格納する。
- `syslog_rfc3164`の`pid`は`string`型(pid省略行を空文字として許容するため、`int`型にはしない)。`syslog_rfc5424`の`procid`/`msgid`も`string`型(RFC上「値なし」の`-`が入りうるため)。
- 複数のルールが同じプリセット名を使う場合、各ルールの`Fields`スライスは互いに独立したコピーでなければならない(プリセットレジストリの同じ裏付け配列を共有すると、`Load()`の`ResolvedFormat`書き込みが他ルールのフィールドを意図せず変更しうる)。

---

## Task 1: プリセットレジストリと`Rule.Preset`の展開(`internal/rules/presets.go`新規 + `internal/rules/rules.go`)

**Files:**
- Create: `internal/rules/presets.go`
- Modify: `internal/rules/rules.go`
- Test: `internal/rules/rules_test.go`

**Interfaces:**
- Produces:
  - `internal/rules/presets.go`: 非公開`presetRegistry map[string]presetDefinition`(`presetDefinition struct { Pattern string; Fields []Field }`)。キーは`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`。
  - `Rule.Preset string`(yamlキー`preset`、任意)。
  - 非公開`Rule.declaredPatternOrFields bool`(yaml decode時に`pattern:`または`fields:`が実際にYAML上で指定されていたかを記録。`Load()`によるプリセット展開で`Pattern`/`Fields`が上書きされた「後」でも、展開「前」に指定されていたかどうかをTask 2のバリデーションが判定できるようにするためのフラグ)。
- Consumed by: Task 2 (`internal/rules/validate.go`が`presetRegistry`と`Rule.declaredPatternOrFields`を参照)。

- [ ] **Step 1: Write the failing tests**

`internal/rules/rules_test.go`の末尾に追記:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rules/... -run TestLoad_Expands -v` および `go test ./internal/rules/... -run TestLoad_TwoRulesUsingSamePreset -v`
Expected: コンパイルエラーまたは失敗(`Rule.Preset`が未定義、あるいは`preset:`を指定しても`Pattern`が空のまま`regexp.Compile("")`が成功してしまい`rule.Regexp`はnilではないが期待するフィールド展開が一切起きていない)。

- [ ] **Step 3: Implement the preset registry**

`internal/rules/presets.go`を新規作成:

```go
package rules

// presetDefinition holds the fixed Pattern/Fields a named preset expands
// into. See the design doc for the exact source of each definition.
type presetDefinition struct {
	Pattern string
	Fields  []Field
}

// presetRegistry maps a Rule.Preset name to its fixed definition. Looked up
// by Load (to expand Pattern/Fields before compiling) and by Validate (to
// reject unknown preset names). Presets are intentionally all-or-nothing:
// there is no partial-override mechanism - see the design doc's Non-goals.
var presetRegistry = map[string]presetDefinition{
	"apache_clf": {
		Pattern: `^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$`,
		Fields: []Field{
			{Name: "remote_addr", Type: "string"},
			{Name: "remote_user", Type: "string"},
			{Name: "time", Type: "timestamp", Format: "clf"},
			{Name: "method", Type: "string"},
			{Name: "path", Type: "string"},
			{Name: "proto", Type: "string"},
			{Name: "status", Type: "int"},
			{Name: "bytes", Type: "int"},
		},
	},
	"apache_combined": {
		Pattern: `^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+) "(?P<referer>[^"]*)" "(?P<user_agent>[^"]*)"$`,
		Fields: []Field{
			{Name: "remote_addr", Type: "string"},
			{Name: "remote_user", Type: "string"},
			{Name: "time", Type: "timestamp", Format: "clf"},
			{Name: "method", Type: "string"},
			{Name: "path", Type: "string"},
			{Name: "proto", Type: "string"},
			{Name: "status", Type: "int"},
			{Name: "bytes", Type: "int"},
			{Name: "referer", Type: "string"},
			{Name: "user_agent", Type: "string"},
		},
	},
	"syslog_rfc3164": {
		// pid is deliberately `string`, not `int`: many daemons omit the
		// `[pid]` suffix, and an int field would fail type conversion on
		// every such line, sending it to unmatched.txt.
		Pattern: `^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<tag>[^:\[\s]+)(?:\[(?P<pid>\d+)\])?: (?P<message>.*)$`,
		Fields: []Field{
			{Name: "time", Type: "timestamp", Format: "syslog"},
			{Name: "host", Type: "string"},
			{Name: "tag", Type: "string"},
			{Name: "pid", Type: "string"},
			{Name: "message", Type: "string"},
		},
	},
	"syslog_rfc5424": {
		// procid/msgid are `string`, not `int`: RFC 5424 allows the
		// nilvalue "-" for either. sd (STRUCTURED-DATA) is kept as raw,
		// unparsed text - see the design doc's Non-goals.
		Pattern: `^<(?P<pri>\d+)>(?P<version>\d+) (?P<time>\S+) (?P<host>\S+) (?P<app>\S+) (?P<procid>\S+) (?P<msgid>\S+) (?P<sd>-|(?:\[[^\]]*\])+) (?P<message>.*)$`,
		Fields: []Field{
			{Name: "pri", Type: "int"},
			{Name: "version", Type: "int"},
			{Name: "time", Type: "timestamp", Format: "iso8601"},
			{Name: "host", Type: "string"},
			{Name: "app", Type: "string"},
			{Name: "procid", Type: "string"},
			{Name: "msgid", Type: "string"},
			{Name: "sd", Type: "string"},
			{Name: "message", Type: "string"},
		},
	},
}
```

- [ ] **Step 4: Wire preset expansion into `rules.go`**

In `internal/rules/rules.go`, add `Preset` and the unexported `declaredPatternOrFields` field to `Rule` (after `Regexp`, before the `Structured` field):

```go
type Rule struct {
	Name    string         `yaml:"name"`
	Pattern string         `yaml:"pattern"`
	Fields  []Field        `yaml:"-"`
	Regexp  *regexp.Regexp `yaml:"-"`

	// Preset, if set, names a fixed pattern/fields definition from
	// presetRegistry (see presets.go) that Load expands into Pattern/
	// Fields before compiling. Mutually exclusive with declaring pattern/
	// fields directly - Validate checks this using
	// declaredPatternOrFields, captured here at YAML-decode time, before
	// Load's expansion overwrites Pattern/Fields with the preset's
	// values.
	Preset                  string `yaml:"preset"`
	declaredPatternOrFields bool   `yaml:"-"`

	// Structured optionally parses one of this rule's captured fields
	// (Structured.Source) as JSON/LTSV/logfmt, letting other fields pull
	// values out of it by key (see Field.Key/Field.Extra) instead of by
	// capture group position. Populated by Rule's UnmarshalYAML, the only
	// place that reads the `structured:` key.
	Structured *StructuredConfig `yaml:"-"`

	// Continuation is an optional regexp pattern. A line matching it while
	// this rule has an in-progress multi-line entry open (see
	// internal/convert.fileCursor) is folded into that entry instead of
	// starting a new one; the pattern's named capture groups say which
	// field(s) receive the matched content. Unset means this rule is
	// always single-line, matching pre-existing behavior.
	Continuation       string         `yaml:"continuation"`
	ContinuationRegexp *regexp.Regexp `yaml:"-"`
}
```

Update `Rule.UnmarshalYAML` to decode `preset:` and capture `declaredPatternOrFields`:

```go
func (r *Rule) UnmarshalYAML(value *yaml.Node) error {
	var alias struct {
		Name         string            `yaml:"name"`
		Pattern      string            `yaml:"pattern"`
		Preset       string            `yaml:"preset"`
		Continuation string            `yaml:"continuation"`
		Structured   *StructuredConfig `yaml:"structured"`
		Fields       yaml.Node         `yaml:"fields"`
	}
	if err := value.Decode(&alias); err != nil {
		return err
	}
	r.Name = alias.Name
	r.Pattern = alias.Pattern
	r.Preset = alias.Preset
	r.Continuation = alias.Continuation
	r.Structured = alias.Structured
	r.declaredPatternOrFields = alias.Pattern != "" || alias.Fields.Kind != 0

	if alias.Fields.Kind == 0 {
		return nil // no `fields:` key present
	}
	if alias.Fields.Kind != yaml.MappingNode {
		return fmt.Errorf("rule %q: fields must be a mapping", r.Name)
	}

	r.Fields = make([]Field, 0, len(alias.Fields.Content)/2)
	for i := 0; i+1 < len(alias.Fields.Content); i += 2 {
		nameNode, defNode := alias.Fields.Content[i], alias.Fields.Content[i+1]

		var field Field
		if err := field.UnmarshalYAML(defNode); err != nil {
			return fmt.Errorf("rule %q: field %q: %w", r.Name, nameNode.Value, err)
		}
		field.Name = nameNode.Value
		r.Fields = append(r.Fields, field)
	}
	return nil
}
```

Add `"slices"` to the import block at the top of `rules.go` (alongside `fmt`, `os`, `regexp`). In `Load`, expand the preset at the very top of the existing per-rule loop, before the pattern is compiled:

```go
	for i := range cfg.Rules {
		if cfg.Rules[i].Preset != "" {
			if preset, ok := presetRegistry[cfg.Rules[i].Preset]; ok {
				cfg.Rules[i].Pattern = preset.Pattern
				cfg.Rules[i].Fields = slices.Clone(preset.Fields)
			}
			// An unknown preset name is left un-expanded (Pattern/Fields
			// stay whatever the user wrote, possibly empty) - Validate
			// reports "unknown preset" and Load returns that error, so
			// this rule's half-expanded state never reaches a caller.
		}

		re, err := regexp.Compile(cfg.Rules[i].Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q: compile pattern: %w", cfg.Rules[i].Name, err)
		}
		cfg.Rules[i].Regexp = re

		// ... rest of the existing loop body is unchanged from here on ...
```

(Everything after the `regexp.Compile` call - continuation compilation, field replace/normalize compilation, timestamp format resolution - stays exactly as it already is; only the preset-expansion block above is new, inserted before the existing `re, err := regexp.Compile(...)` line.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/rules/... -v`
Expected: PASS (existing tests plus all new preset-expansion tests, including the aliasing test).

- [ ] **Step 6: Commit**

```bash
git add internal/rules/presets.go internal/rules/rules.go internal/rules/rules_test.go
git commit -m "feat(rules): add preset registry and expand Rule.Preset in Load"
```

---

## Task 2: バリデーション(`internal/rules/validate.go`)

**Files:**
- Modify: `internal/rules/validate.go`
- Test: `internal/rules/validate_test.go`

**Interfaces:**
- Consumes: Task 1の`presetRegistry`と`Rule.Preset`/`Rule.declaredPatternOrFields`.
- Produces: `Validate()`のエラーメッセージにプリセット関連のケースを追加(戻り値の型・呼び出し方は変更なし)。

- [ ] **Step 1: Write the failing tests**

`internal/rules/validate_test.go`の末尾に追記:

```go
func TestValidate_UnknownPresetIsError(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{Name: "bad", Preset: "no_such_preset"},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "no_such_preset") {
		t.Errorf("expected error mentioning unknown preset %q, got: %v", "no_such_preset", err)
	}
}

func TestValidate_PresetAndPatternTogetherIsError(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:                    "bad",
				Preset:                  "apache_clf",
				Pattern:                 pattern,
				Regexp:                  mustCompile(t, pattern),
				Fields:                  []Field{{Name: "a", Type: "string"}},
				declaredPatternOrFields: true,
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually-exclusive error, got: %v", err)
	}
}

func TestValidate_PresetAndFieldsTogetherIsError(t *testing.T) {
	// fields: alone (no pattern:) combined with preset: must also be
	// rejected - declaredPatternOrFields is true whenever either key was
	// present in the source YAML.
	pattern := `^(?P<a>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:                    "bad",
				Preset:                  "apache_clf",
				Regexp:                  mustCompile(t, pattern),
				Fields:                  []Field{{Name: "a", Type: "string"}},
				declaredPatternOrFields: true,
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually-exclusive error, got: %v", err)
	}
}

func TestValidate_PresetAloneWithExpandedFieldsPasses(t *testing.T) {
	// Simulates the post-Load state: preset expansion already ran
	// (declaredPatternOrFields stays false, since the source YAML only
	// had `preset:`), Pattern/Fields/Regexp are the expanded ones.
	preset := presetRegistry["apache_clf"]
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "access_log",
				Preset:  "apache_clf",
				Pattern: preset.Pattern,
				Regexp:  mustCompile(t, preset.Pattern),
				Fields:  preset.Fields,
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rules/... -run TestValidate_UnknownPreset -v` および `go test ./internal/rules/... -run TestValidate_Preset -v`
Expected: FAIL(現状の`Validate()`はプリセット関連のチェックを一切行わないため、いずれのテストも期待通りのエラー/非エラーにならない)。

- [ ] **Step 3: Implement the validation rules**

In `internal/rules/validate.go`, add the following block at the very top of the existing per-rule `for _, rule := range c.Rules` loop in `Validate()` (before the `captureNames := map[string]bool{}` line):

```go
		if rule.Preset != "" {
			if _, ok := presetRegistry[rule.Preset]; !ok {
				errs = append(errs, fmt.Errorf("rule %q: unknown preset %q", rule.Name, rule.Preset))
			}
			if rule.declaredPatternOrFields {
				errs = append(errs, fmt.Errorf("rule %q: preset and pattern/fields are mutually exclusive", rule.Name))
			}
		}
```

The rest of `Validate()` (capture-group matching, field type checks, structured/key/extra checks, continuation checks, same-name-rule schema equality, compression/row_group validation) stays completely unchanged - it operates on `rule.Pattern`/`rule.Fields`/`rule.Regexp` exactly as before, regardless of whether those came from a preset expansion or were written directly.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rules/... -v`
Expected: PASS (existing tests plus all new preset-validation tests).

- [ ] **Step 5: Commit**

```bash
git add internal/rules/validate.go internal/rules/validate_test.go
git commit -m "feat(rules): validate preset exclusivity and unknown preset names"
```

---

## Task 3: 各プリセットのサンプルログ行に対する回帰テスト(`internal/parse`)

**Files:**
- Create: `internal/parse/presets_test.go`

**Interfaces:**
- Consumes: Task 1の`rules.Load()`(プリセット展開込みの完全なロード)、既存の`parse.Match(ruleList []rules.Rule, line string, now time.Time) (name string, values map[string]any, ok bool)`。
- このタスクはプロダクションコードを一切変更しない - Task 1・Task 2が正しく実装されていれば、この結合テストは追加コードなしでパスするはずの検証タスク。

- [ ] **Step 1: Write the test**

`internal/parse/presets_test.go`を新規作成:

```go
package parse

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"logidx/internal/rules"
)

func writeTempPresetRules(t *testing.T, ruleName, preset string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	content := "rules:\n  - name: " + ruleName + "\n    preset: " + preset + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp rules file: %v", err)
	}
	return path
}

func TestPresets_MatchAndConvertSampleLines(t *testing.T) {
	cases := []struct {
		name   string
		preset string
		line   string
		now    time.Time
		want   map[string]any
	}{
		{
			name:   "apache_clf",
			preset: "apache_clf",
			line:   `127.0.0.1 - frank [10/Oct/2023:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 200 2326`,
			now:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"remote_addr": "127.0.0.1",
				"remote_user": "frank",
				"time":        time.Date(2023, 10, 10, 13, 55, 36, 0, time.FixedZone("", -7*3600)),
				"method":      "GET",
				"path":        "/apache_pb.gif",
				"proto":       "HTTP/1.0",
				"status":      int64(200),
				"bytes":       int64(2326),
			},
		},
		{
			name:   "apache_combined",
			preset: "apache_combined",
			line:   `127.0.0.1 - frank [10/Oct/2023:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 200 2326 "http://www.example.com/start.html" "Mozilla/4.08 [en] (Win98; I ;Nav)"`,
			now:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"remote_addr": "127.0.0.1",
				"remote_user": "frank",
				"time":        time.Date(2023, 10, 10, 13, 55, 36, 0, time.FixedZone("", -7*3600)),
				"method":      "GET",
				"path":        "/apache_pb.gif",
				"proto":       "HTTP/1.0",
				"status":      int64(200),
				"bytes":       int64(2326),
				"referer":     "http://www.example.com/start.html",
				"user_agent":  "Mozilla/4.08 [en] (Win98; I ;Nav)",
			},
		},
		{
			name:   "syslog_rfc3164_with_pid",
			preset: "syslog_rfc3164",
			line:   `Oct 11 22:14:15 mymachine su[1234]: 'su root' failed for lonvick on /dev/pts/8`,
			now:    time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"time":    time.Date(2023, 10, 11, 22, 14, 15, 0, time.UTC),
				"host":    "mymachine",
				"tag":     "su",
				"pid":     "1234",
				"message": "'su root' failed for lonvick on /dev/pts/8",
			},
		},
		{
			name:   "syslog_rfc3164_without_pid",
			preset: "syslog_rfc3164",
			line:   `Oct 11 22:14:15 mymachine su: 'su root' failed for lonvick on /dev/pts/8`,
			now:    time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"time":    time.Date(2023, 10, 11, 22, 14, 15, 0, time.UTC),
				"host":    "mymachine",
				"tag":     "su",
				"pid":     "",
				"message": "'su root' failed for lonvick on /dev/pts/8",
			},
		},
		{
			name:   "syslog_rfc5424_with_structured_data",
			preset: "syslog_rfc5424",
			line:   `<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut="3" eventSource="Application" eventID="1011"] An application event log entry`,
			now:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"pri":     int64(165),
				"version": int64(1),
				"time":    time.Date(2003, 10, 11, 22, 14, 15, 3000000, time.UTC),
				"host":    "mymachine.example.com",
				"app":     "evntslog",
				"procid":  "-",
				"msgid":   "ID47",
				"sd":      `[exampleSDID@32473 iut="3" eventSource="Application" eventID="1011"]`,
				"message": "An application event log entry",
			},
		},
		{
			name:   "syslog_rfc5424_without_structured_data",
			preset: "syslog_rfc5424",
			line:   `<13>1 2023-10-11T22:14:15Z host1 myapp - - - Simple message here`,
			now:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"pri":     int64(13),
				"version": int64(1),
				"time":    time.Date(2023, 10, 11, 22, 14, 15, 0, time.UTC),
				"host":    "host1",
				"app":     "myapp",
				"procid":  "-",
				"msgid":   "-",
				"sd":      "-",
				"message": "Simple message here",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempPresetRules(t, tc.name, tc.preset)
			cfg, err := rules.Load(path)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}

			ruleName, values, ok := Match(cfg.Rules, tc.line, tc.now)
			if !ok {
				t.Fatalf("expected line to match preset %q, got no match: %q", tc.preset, tc.line)
			}
			if ruleName != tc.name {
				t.Errorf("matched rule name = %q, want %q", ruleName, tc.name)
			}

			for field, want := range tc.want {
				got, present := values[field]
				if !present {
					t.Errorf("field %q missing from converted values", field)
					continue
				}
				if wantTime, isTime := want.(time.Time); isTime {
					gotTime, ok := got.(time.Time)
					if !ok || !gotTime.Equal(wantTime) {
						t.Errorf("field %q = %v, want %v", field, got, wantTime)
					}
					continue
				}
				if got != want {
					t.Errorf("field %q = %v (%T), want %v (%T)", field, got, got, want, want)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/parse/... -run TestPresets_MatchAndConvertSampleLines -v`
Expected: PASS, for all 6 subtests, with no production code changes needed (Task 1 and Task 2 already implemented the full preset pipeline). If any subtest fails, that indicates a gap in Task 1/Task 2 - report the specific failure rather than modifying `internal/rules` from this task (that would be out of scope; escalate instead).

- [ ] **Step 3: Run the full test suite**

Run: `go test ./...`
Expected: PASS (all packages).

- [ ] **Step 4: Commit**

```bash
git add internal/parse/presets_test.go
git commit -m "test: add regression coverage for all 4 log-format presets"
```

---

## Task 4: README.md ドキュメント追記

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 1〜3で確定した挙動(4プリセットの`pattern`/`fields`展開内容と`preset:`のバリデーション規則)。

- [ ] **Step 1: Add the documentation section**

`README.md`の「ルール定義の書き方は `docs/superpowers/specs/2026-08-06-log-to-parquet-converter-design.md` を参照。」という行(現在30行目)の直後、「### タイムスタンプの`format`指定」節の直前に、以下のセクションを挿入する:

```markdown
### よく使われるログ形式のプリセット(`preset:`)

Apache/nginx Common Log Format・Combined Log Format、BSD syslog(RFC 3164)、syslog protocol(RFC 5424)は、`pattern:`/`fields:`を手書きせず`preset:`の1行で使える。

```yaml
rules:
  - name: access_log
    preset: apache_clf
  - name: syslog
    preset: syslog_rfc3164
```

- `preset:`と`pattern:`/`fields:`は同時に指定できない(起動時エラー)。存在しないプリセット名を指定した場合も起動時エラーになる。
- プリセットの内容は完全固定で、部分的なカスタマイズ(特定フィールドの`format`だけ上書きする等)はできない。カスタマイズしたい場合は、下表の`pattern`/`fields`をそのまま自分の`pattern:`/`fields:`としてコピーし、書き換えて使う。
- `continuation:`/`structured:`/`compression:`など他の設定とは独立して併用できる。

利用可能なプリセット一覧:

| プリセット名 | 形式 |
|---|---|
| `apache_clf` | Apache/nginx Common Log Format |
| `apache_combined` | Apache/nginx Combined Log Format(CLF + referer/user-agent) |
| `syslog_rfc3164` | BSD syslog(RFC 3164) |
| `syslog_rfc5424` | syslog protocol(RFC 5424) |

#### `apache_clf`

```yaml
pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
fields:
  remote_addr: string
  remote_user: string
  time:
    type: timestamp
    format: clf
  method: string
  path: string
  proto: string
  status: int
  bytes: int
```

#### `apache_combined`

```yaml
pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+) "(?P<referer>[^"]*)" "(?P<user_agent>[^"]*)"$'
fields:
  remote_addr: string
  remote_user: string
  time:
    type: timestamp
    format: clf
  method: string
  path: string
  proto: string
  status: int
  bytes: int
  referer: string
  user_agent: string
```

#### `syslog_rfc3164`

`tag[pid]:`の`[pid]`は多くのデーモンで省略されることがあるため、`pid`は`string`型(未指定時は空文字)。

```yaml
pattern: '^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<tag>[^:\[\s]+)(?:\[(?P<pid>\d+)\])?: (?P<message>.*)$'
fields:
  time:
    type: timestamp
    format: syslog
  host: string
  tag: string
  pid: string
  message: string
```

#### `syslog_rfc5424`

`procid`/`msgid`/STRUCTURED-DATA(`sd`)はRFC上「値なし」を表す`-`が入りうるため`string`型。`sd`は中身をパースせず、生テキストのまま1カラムに格納する(構造化データのキー抽出をしたい場合は`docs/superpowers/specs/2026-08-08-preset-as-structured-format-design.md`を参照)。

```yaml
pattern: '^<(?P<pri>\d+)>(?P<version>\d+) (?P<time>\S+) (?P<host>\S+) (?P<app>\S+) (?P<procid>\S+) (?P<msgid>\S+) (?P<sd>-|(?:\[[^\]]*\])+) (?P<message>.*)$'
fields:
  pri: int
  version: int
  time:
    type: timestamp
    format: iso8601
  host: string
  app: string
  procid: string
  msgid: string
  sd: string
  message: string
```
```

- [ ] **Step 2: Verify the addition renders as intended**

Run: `rg -n "preset:" README.md`
Expected: 新しいセクション見出しと本文中の`preset:`への言及がヒットする。

- [ ] **Step 3: Run the full verification suite**

Run: `task fmt && task lint && task test && task build`
Expected: すべて成功(`gofmt`差分なし、`golangci-lint`エラーなし、全テストPASS、ビルド成功)。

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document preset: log-format shortcuts"
```
