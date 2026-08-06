# テキストログ→Parquet変換ツール(logidx) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** バッチCLI `logidx` を実装する。複数種別が混在するテキストログを、YAMLで定義した正規表現ルールでパターンマッチして構造化し、ルール名(type)ごとにParquetファイルへ変換する。

**Architecture:** `internal/rules`(ルール読み込み・検証)→`internal/parse`(1行のマッチング・正規化・型変換)→`internal/schema`(Parquetスキーマ導出)→`internal/writer`(type別Parquet書き込み+unmatched raw書き込み)→`internal/convert`(1ファイルのオーケストレーション)→`cmd/logidx`(CLIエントリポイント)。ログ出力は`internal/logging`で`log/slog`をラップする。

**Tech Stack:** Go 1.22+, `github.com/parquet-go/parquet-go`(純Go製、cgo非依存のParquet書き込み), `gopkg.in/yaml.v3`(ルール設定パース), 標準ライブラリ `regexp`(RE2)/`log/slog`/`bufio`/`flag`。

## Global Constraints

- 対象spec: `docs/superpowers/specs/2026-08-06-log-to-parquet-converter-design.md`(このplanの正)
- Parquet書き出しは純Go実装(`parquet-go`)のみを使用し、cgo依存を持ち込まない
- ルールは配列の**上から順に**マッチを試行し、最初にマッチしたものを採用する(grok的動作)
- マッチしたが型変換に失敗した行、およびどのルールにもマッチしなかった行は**unmatched**として扱う(行単位では処理を止めない)
- 入力ファイル単位のエラーはそのファイルをスキップして処理継続、最終exit codeのみ非0にする
- 出力ファイル名: `<入力ファイルのbasename(拡張子除く)>.<ルールname>.parquet`、unmatchedは`<basename>.unmatched.txt`。該当行が0件のtype/unmatchedはファイルを作らない
- ログ出力はすべて`log/slog`経由、出力先はstderr、`--log-format text|json`で切替、`-v`でDebugレベル
- `gofmt`・`golangci-lint`に準拠したコードを書く

---

## Task 1: プロジェクトの雛形

**Files:**
- Create: `go.mod`
- Create: `Taskfile.yml`
- Create: `.golangci.yml`
- Create: `internal/rules/doc.go`
- Create: `internal/parse/doc.go`
- Create: `internal/schema/doc.go`
- Create: `internal/writer/doc.go`
- Create: `internal/logging/doc.go`
- Create: `internal/convert/doc.go`
- Create: `cmd/logidx/main.go`(仮の`main`のみ)

**Interfaces:**
- Produces: Goモジュール`logidx`、以降のタスクが依存する`go.mod`/ディレクトリ構成

- [ ] **Step 1: モジュール初期化**

```bash
cd /Users/watanabe/x/copilot-cli/project-logidx
go mod init logidx
go get github.com/parquet-go/parquet-go@latest
go get gopkg.in/yaml.v3@latest
```

- [ ] **Step 2: パッケージディレクトリと`doc.go`を作成**

`internal/rules/doc.go`:
```go
// Package rules loads and validates the YAML rule configuration used to
// match and structure log lines.
package rules
```

同様に以下を作成する(パッケージ名以外は同じ内容):
- `internal/parse/doc.go` (`package parse` — 1行のマッチング・正規化・型変換)
- `internal/schema/doc.go` (`package schema` — ルール定義からParquetスキーマを導出)
- `internal/writer/doc.go` (`package writer` — type別Parquet書き込みとunmatched raw書き込み)
- `internal/logging/doc.go` (`package logging` — slogセットアップ)
- `internal/convert/doc.go` (`package convert` — 1入力ファイルの処理オーケストレーション)

- [ ] **Step 3: 仮の`main`を作成(ビルド確認用)**

`cmd/logidx/main.go`:
```go
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
```

Task 12で`run`を実装するまでの間、ビルドを通すための最小プレースホルダとして次のファイルも作成する。

`cmd/logidx/run_placeholder.go`:
```go
package main

import "io"

func run(args []string, stdout, stderr io.Writer) int {
	return 0
}
```

(Task 12で本実装に置き換える際にこのファイルは削除する)

- [ ] **Step 4: Taskfileとlint設定を作成**

`Taskfile.yml`:
```yaml
version: '3'

tasks:
  fmt:
    cmds:
      - gofmt -l -w .

  lint:
    cmds:
      - golangci-lint run ./...

  test:
    cmds:
      - go test ./...

  build:
    cmds:
      - go build -o bin/logidx ./cmd/logidx
```

`.golangci.yml`:
```yaml
run:
  timeout: 5m

linters:
  enable:
    - govet
    - staticcheck
    - unused
    - errcheck
```

- [ ] **Step 5: ビルド確認**

Run: `go build ./...`
Expected: エラーなく成功する

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum Taskfile.yml .golangci.yml internal cmd
git commit -m "Scaffold logidx Go module and package layout"
```

---

## Task 2: internal/rules — ルール設定の読み込み(正常系)

**Files:**
- Create: `internal/rules/rules.go`
- Test: `internal/rules/rules_test.go`

**Interfaces:**
- Produces:
  - `type Field struct { Type string; Format string; Normalize []NormalizeRule }`
  - `type NormalizeRule struct { Pattern string; Value string; Regexp *regexp.Regexp }`
  - `type Rule struct { Name string; Pattern string; Fields map[string]Field; Regexp *regexp.Regexp }`
  - `type Config struct { Rules []Rule }`
  - `func Load(path string) (*Config, error)` — YAML読み込み+正規表現コンパイル+Validate()呼び出し(Validate自体はTask 3で実装。Task 2時点では常にnilを返すダミーで可)

- [ ] **Step 1: 失敗するテストを書く**

`internal/rules/rules_test.go`:
```go
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
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/rules/... -run TestLoad_ParsesRulesAndFieldShorthand -v`
Expected: FAIL(`rules.Load` などが未定義のためコンパイルエラー)

- [ ] **Step 3: 実装**

`internal/rules/rules.go`:
```go
package rules

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// NormalizeRule maps a captured raw string to a canonical value when Pattern matches.
type NormalizeRule struct {
	Pattern string `yaml:"pattern"`
	Value   string `yaml:"value"`
	Regexp  *regexp.Regexp `yaml:"-"`
}

// Field describes how a named capture group should be typed and normalized.
type Field struct {
	Type      string          `yaml:"type"`
	Format    string          `yaml:"format"`
	Normalize []NormalizeRule `yaml:"normalize"`
}

// UnmarshalYAML supports both the shorthand `name: string` form and the
// full mapping form `name: {type: ..., format: ..., normalize: [...]}`.
func (f *Field) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		f.Type = value.Value
		return nil
	}

	type fieldAlias Field
	var alias fieldAlias
	if err := value.Decode(&alias); err != nil {
		return err
	}
	*f = Field(alias)
	return nil
}

// Rule is a single pattern-match rule: a name (output type), the regexp
// pattern used to match lines, and the fields extracted from named
// capture groups.
type Rule struct {
	Name    string          `yaml:"name"`
	Pattern string          `yaml:"pattern"`
	Fields  map[string]Field `yaml:"fields"`
	Regexp  *regexp.Regexp  `yaml:"-"`
}

// Config is the top-level rules.yaml document.
type Config struct {
	Rules []Rule `yaml:"rules"`
}

// Load reads, parses, compiles, and validates a rules YAML file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse rules YAML: %w", err)
	}

	for i := range cfg.Rules {
		re, err := regexp.Compile(cfg.Rules[i].Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q: compile pattern: %w", cfg.Rules[i].Name, err)
		}
		cfg.Rules[i].Regexp = re

		for name, field := range cfg.Rules[i].Fields {
			for j := range field.Normalize {
				nre, err := regexp.Compile(field.Normalize[j].Pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: compile normalize pattern: %w", cfg.Rules[i].Name, name, err)
				}
				field.Normalize[j].Regexp = nre
			}
			cfg.Rules[i].Fields[name] = field
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
```

Task 3までの一時的なビルド通過用に、`internal/rules/validate.go`へ最小限の`Validate`を用意する:

```go
package rules

// Validate is filled in by Task 3. For now it always succeeds so Task 2's
// tests can run in isolation.
func (c *Config) Validate() error {
	return nil
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/rules/... -run TestLoad_ParsesRulesAndFieldShorthand -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/rules/rules.go internal/rules/validate.go internal/rules/rules_test.go
git commit -m "Add rules.Load: YAML parsing with field-type shorthand support"
```

---

## Task 3: internal/rules — 起動時検証

**Files:**
- Modify: `internal/rules/validate.go`
- Test: `internal/rules/validate_test.go`

**Interfaces:**
- Consumes: `Config`, `Rule`, `Field`, `NormalizeRule`(Task 2で定義)
- Produces: `func (c *Config) Validate() error`(検証失敗時は`errors.Join`で全違反をまとめて返す)

検証内容(spec準拠):
1. `fields`の各キーに対応する名前付きキャプチャグループが`pattern`に存在するか
2. `pattern`側にのみ存在する名前付きグループは無視してよい
3. 同じ`name`を持つルール同士は`fields`(列名+型のみ、Format/Normalizeは比較対象外)が完全一致しているか
4. `type`が`string`/`int`/`float`/`timestamp`のいずれかか
5. `timestamp`型フィールドに`format`が指定されているか
6. `pattern`および`normalize`内の`pattern`がコンパイル可能か(Task 2の`Load`内で既にコンパイル済みのため、コンパイルエラーは`Load`側で捕捉される。`Validate`では追加のコンパイル検証は行わない)

- [ ] **Step 1: 失敗するテストを書く**

`internal/rules/validate_test.go`:
```go
package rules

import (
	"regexp"
	"strings"
	"testing"
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
				Fields: map[string]Field{
					"a": {Type: "string"},
					"b": {Type: "string"}, // no capture group named "b"
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
				Fields: map[string]Field{
					"a": {Type: "string"},
					"b": {Type: "string"},
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
				Fields:  map[string]Field{"a": {Type: "string"}},
			},
			{
				Name:    "dup",
				Pattern: patternB,
				Regexp:  mustCompile(t, patternB),
				Fields:  map[string]Field{"a": {Type: "int"}}, // type mismatch
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
				Fields:  map[string]Field{"a": {Type: "string"}},
			},
			{
				Name:    "dup",
				Pattern: patternB,
				Regexp:  mustCompile(t, patternB),
				Fields:  map[string]Field{"a": {Type: "string"}}, // matches
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_UnknownFieldTypeIsError(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: `^(?P<a>\S+)$`,
				Regexp:  mustCompile(t, `^(?P<a>\S+)$`),
				Fields:  map[string]Field{"a": {Type: "bogus"}},
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
				Fields:  map[string]Field{"a": {Type: "timestamp"}}, // no Format
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for timestamp field without format")
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/rules/... -run TestValidate -v`
Expected: FAIL(`FieldWithoutCaptureGroupIsError`と`SameNameRulesMustHaveIdenticalFields`・`UnknownFieldTypeIsError`・`TimestampFieldWithoutFormatIsError`がすべて通ってしまう。現状の`Validate`は常にnilを返すダミーのため)

- [ ] **Step 3: 実装**

`internal/rules/validate.go`:
```go
package rules

import (
	"errors"
	"fmt"
)

var allowedTypes = map[string]bool{
	"string":    true,
	"int":       true,
	"float":     true,
	"timestamp": true,
}

// Validate checks all fail-fast startup invariants described in the design
// spec and returns a joined error listing every violation found.
func (c *Config) Validate() error {
	var errs []error
	firstFieldsByName := map[string]map[string]Field{}

	for _, rule := range c.Rules {
		captureNames := map[string]bool{}
		for _, n := range rule.Regexp.SubexpNames() {
			if n != "" {
				captureNames[n] = true
			}
		}

		for fieldName, field := range rule.Fields {
			if !captureNames[fieldName] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has no matching named capture group in pattern", rule.Name, fieldName))
			}
			if !allowedTypes[field.Type] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has unsupported type %q", rule.Name, fieldName, field.Type))
			}
			if field.Type == "timestamp" && field.Format == "" {
				errs = append(errs, fmt.Errorf("rule %q: field %q is type timestamp but has no format", rule.Name, fieldName))
			}
		}

		if existing, ok := firstFieldsByName[rule.Name]; ok {
			if !fieldsEqualForSchema(existing, rule.Fields) {
				errs = append(errs, fmt.Errorf("rule %q: multiple rules share this name but declare different fields (name+type must match exactly)", rule.Name))
			}
		} else {
			firstFieldsByName[rule.Name] = rule.Fields
		}
	}

	return errors.Join(errs...)
}

// fieldsEqualForSchema compares two field sets by name+type only, ignoring
// Format and Normalize, per the design's schema-consistency rule.
func fieldsEqualForSchema(a, b map[string]Field) bool {
	if len(a) != len(b) {
		return false
	}
	for name, fa := range a {
		fb, ok := b[name]
		if !ok || fa.Type != fb.Type {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/rules/... -v`
Expected: PASS(Task 2, Task 3のテストすべて)

- [ ] **Step 5: Commit**

```bash
git add internal/rules/validate.go internal/rules/validate_test.go
git commit -m "Add rules.Validate: fail-fast startup checks"
```

---

## Task 4: internal/parse — 値の正規化(normalize)

**Files:**
- Create: `internal/parse/normalize.go`
- Test: `internal/parse/normalize_test.go`

**Interfaces:**
- Consumes: `rules.NormalizeRule`(Task 2)
- Produces: `func applyNormalize(raw string, rules []rules.NormalizeRule) string`

- [ ] **Step 1: 失敗するテストを書く**

`internal/parse/normalize_test.go`:
```go
package parse

import (
	"regexp"
	"testing"

	"logidx/internal/rules"
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
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/parse/... -run TestApplyNormalize -v`
Expected: FAIL(`applyNormalize`未定義)

- [ ] **Step 3: 実装**

`internal/parse/normalize.go`:
```go
package parse

import "logidx/internal/rules"

// applyNormalize tries each normalize rule in order and returns the value
// of the first one whose pattern matches raw. If none match, raw is
// returned unchanged.
func applyNormalize(raw string, rulesList []rules.NormalizeRule) string {
	for _, r := range rulesList {
		if r.Regexp.MatchString(raw) {
			return r.Value
		}
	}
	return raw
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/parse/... -run TestApplyNormalize -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parse/normalize.go internal/parse/normalize_test.go
git commit -m "Add parse.applyNormalize for field value normalization"
```

---

## Task 5: internal/parse — 年情報のないタイムスタンプの解決

**Files:**
- Create: `internal/parse/timestamp.go`
- Test: `internal/parse/timestamp_test.go`

**Interfaces:**
- Produces: `func parseTimestamp(raw, format string, now time.Time) (time.Time, error)`

- [ ] **Step 1: 失敗するテストを書く**

`internal/parse/timestamp_test.go`:
```go
package parse

import (
	"testing"
	"time"
)

func TestParseTimestamp_FormatWithYear(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("06/Aug/2026:12:00:00 +0900", "02/Jan/2006:15:04:05 -0700", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2026 || got.Month() != time.August || got.Day() != 6 {
		t.Errorf("unexpected parsed time: %v", got)
	}
}

func TestParseTimestamp_NoYear_UsesCurrentYearWhenNotFuture(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("Aug  1 09:00:00", "Jan _2 15:04:05", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2026 {
		t.Errorf("expected year 2026, got %d", got.Year())
	}
}

func TestParseTimestamp_NoYear_FallsBackToPreviousYearWhenFuture(t *testing.T) {
	// "now" is Jan 2, log line says Dec 31 -> should resolve to previous year.
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("Dec 31 23:59:59", "Jan _2 15:04:05", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2025 {
		t.Errorf("expected year 2025 (previous year), got %d", got.Year())
	}
	if got.Month() != time.December || got.Day() != 31 {
		t.Errorf("unexpected month/day: %v", got)
	}
}

func TestParseTimestamp_InvalidInputReturnsError(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, err := parseTimestamp("not-a-timestamp", "Jan _2 15:04:05", now)
	if err == nil {
		t.Fatal("expected error for unparsable timestamp")
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/parse/... -run TestParseTimestamp -v`
Expected: FAIL(`parseTimestamp`未定義)

- [ ] **Step 3: 実装**

`internal/parse/timestamp.go`:
```go
package parse

import (
	"strings"
	"time"
)

// parseTimestamp parses raw using format. If format contains no year token
// ("2006"), the parsed year is resolved to the nearest year that is not in
// the future relative to now: try now.Year(), and if that combined with the
// parsed month/day/time would be after now, use the previous year instead.
func parseTimestamp(raw, format string, now time.Time) (time.Time, error) {
	t, err := time.Parse(format, raw)
	if err != nil {
		return time.Time{}, err
	}

	if strings.Contains(format, "2006") {
		return t, nil
	}

	candidate := time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	if candidate.After(now) {
		candidate = candidate.AddDate(-1, 0, 0)
	}
	return candidate, nil
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/parse/... -run TestParseTimestamp -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parse/timestamp.go internal/parse/timestamp_test.go
git commit -m "Add parse.parseTimestamp with nearest-past-year resolution"
```

---

## Task 6: internal/parse — フィールド値の型変換

**Files:**
- Create: `internal/parse/convertvalue.go`
- Test: `internal/parse/convertvalue_test.go`

**Interfaces:**
- Consumes: `rules.Field`(Task 2), `applyNormalize`(Task 4), `parseTimestamp`(Task 5)
- Produces: `func convertValue(raw string, field rules.Field, now time.Time) (any, error)` — 戻り値の型は `string`(string型)/`int64`(int型)/`float64`(float型)/`time.Time`(timestamp型)

- [ ] **Step 1: 失敗するテストを書く**

`internal/parse/convertvalue_test.go`:
```go
package parse

import (
	"testing"
	"time"

	"logidx/internal/rules"
)

func TestConvertValue_String(t *testing.T) {
	now := time.Now()
	v, err := convertValue("hello", rules.Field{Type: "string"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "hello" {
		t.Errorf("got %v, want %q", v, "hello")
	}
}

func TestConvertValue_StringWithNormalize(t *testing.T) {
	field := rules.Field{Type: "string", Normalize: []rules.NormalizeRule{
		normRule(t, `(?i)^warn(ing)?$`, "WARN"),
	}}
	now := time.Now()
	v, err := convertValue("Warning", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "WARN" {
		t.Errorf("got %v, want %q", v, "WARN")
	}
}

func TestConvertValue_Int(t *testing.T) {
	now := time.Now()
	v, err := convertValue("512", rules.Field{Type: "int"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != int64(512) {
		t.Errorf("got %v (%T), want int64(512)", v, v)
	}
}

func TestConvertValue_IntInvalidIsError(t *testing.T) {
	now := time.Now()
	_, err := convertValue("not-a-number", rules.Field{Type: "int"}, now)
	if err == nil {
		t.Fatal("expected error for invalid int")
	}
}

func TestConvertValue_Float(t *testing.T) {
	now := time.Now()
	v, err := convertValue("3.14", rules.Field{Type: "float"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 3.14 {
		t.Errorf("got %v, want 3.14", v)
	}
}

func TestConvertValue_Timestamp(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	field := rules.Field{Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"}
	v, err := convertValue("2026-08-06T12:00:01+09:00", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tm, ok := v.(time.Time)
	if !ok {
		t.Fatalf("got %T, want time.Time", v)
	}
	if tm.Year() != 2026 || tm.Month() != time.August || tm.Day() != 6 {
		t.Errorf("unexpected parsed time: %v", tm)
	}
}

func TestConvertValue_TimestampInvalidIsError(t *testing.T) {
	now := time.Now()
	field := rules.Field{Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"}
	_, err := convertValue("not-a-timestamp", field, now)
	if err == nil {
		t.Fatal("expected error for invalid timestamp")
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/parse/... -run TestConvertValue -v`
Expected: FAIL(`convertValue`未定義)

- [ ] **Step 3: 実装**

`internal/parse/convertvalue.go`:
```go
package parse

import (
	"fmt"
	"strconv"
	"time"

	"logidx/internal/rules"
)

// convertValue applies normalization (if configured) and then converts the
// resulting string into the Go value matching field.Type. Returns an error
// if the value cannot be converted, in which case the caller should treat
// the whole line as unmatched.
func convertValue(raw string, field rules.Field, now time.Time) (any, error) {
	normalized := raw
	if len(field.Normalize) > 0 {
		normalized = applyNormalize(raw, field.Normalize)
	}

	switch field.Type {
	case "string":
		return normalized, nil
	case "int":
		v, err := strconv.ParseInt(normalized, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse int: %w", err)
		}
		return v, nil
	case "float":
		v, err := strconv.ParseFloat(normalized, 64)
		if err != nil {
			return nil, fmt.Errorf("parse float: %w", err)
		}
		return v, nil
	case "timestamp":
		v, err := parseTimestamp(normalized, field.Format, now)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp: %w", err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported field type %q", field.Type)
	}
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/parse/... -run TestConvertValue -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parse/convertvalue.go internal/parse/convertvalue_test.go
git commit -m "Add parse.convertValue: normalize + typed conversion per field"
```

---

## Task 7: internal/parse — 行マッチングエンジン

**Files:**
- Create: `internal/parse/match.go`
- Test: `internal/parse/match_test.go`

**Interfaces:**
- Consumes: `rules.Rule`, `convertValue`(Task 6)
- Produces: `func Match(ruleList []rules.Rule, line string, now time.Time) (name string, values map[string]any, ok bool)`

動作: ルールを上から順に試す。正規表現がマッチした**最初の**ルールを採用する。採用後、そのルールの全フィールドの型変換を試みる。1つでも失敗したら、他のルールにはフォールバックせずその行はunmatched(`ok=false`)とする。

- [ ] **Step 1: 失敗するテストを書く**

`internal/parse/match_test.go`:
```go
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
```

`regexpT`/`regexpCompile`は標準`regexp`パッケージのエイリアスとして`internal/parse`内のテストヘルパーで定義する。

`internal/parse/testhelpers_test.go`:
```go
package parse

import "regexp"

type regexpT = regexp.Regexp

func regexpCompile(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(pattern)
}
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/parse/... -run TestMatch -v`
Expected: FAIL(`Match`未定義)

- [ ] **Step 3: 実装**

`internal/parse/match.go`:
```go
package parse

import (
	"time"

	"logidx/internal/rules"
)

// Match tries each rule in ruleList in order and returns the extracted,
// type-converted field values of the first rule whose pattern matches line.
// If that rule's pattern matches but any field fails type conversion, the
// line is treated as unmatched (ok=false) — there is no fallthrough to
// subsequent rules, since "first match" refers to the regex match, not to
// conversion success.
func Match(ruleList []rules.Rule, line string, now time.Time) (name string, values map[string]any, ok bool) {
	for _, rule := range ruleList {
		m := rule.Regexp.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		raw := map[string]string{}
		for i, groupName := range rule.Regexp.SubexpNames() {
			if i == 0 || groupName == "" {
				continue
			}
			raw[groupName] = m[i]
		}

		converted := make(map[string]any, len(rule.Fields))
		for fieldName, field := range rule.Fields {
			v, err := convertValue(raw[fieldName], field, now)
			if err != nil {
				return "", nil, false
			}
			converted[fieldName] = v
		}

		return rule.Name, converted, true
	}

	return "", nil, false
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/parse/... -v`
Expected: PASS(Task 4〜7のテストすべて)

- [ ] **Step 5: Commit**

```bash
git add internal/parse/match.go internal/parse/match_test.go internal/parse/testhelpers_test.go
git commit -m "Add parse.Match: ordered rule matching with no-fallthrough on conversion failure"
```

---

## Task 8: internal/schema — Parquetスキーマ導出

**Files:**
- Create: `internal/schema/schema.go`
- Test: `internal/schema/schema_test.go`

**Interfaces:**
- Consumes: `rules.Field`, `rules.Rule`(Task 2)
- Produces:
  - `type Built struct { Schema *parquet.Schema; Columns []string }`
  - `func Build(name string, fields map[string]rules.Field) (*Built, error)`
  - `func BuildAll(ruleList []rules.Rule) (map[string]*Built, error)` — 同名ルールは最初の1件のみ使用(Validateにより同名ルール間のfieldsは一致済みという前提)

列順序は**フィールド名のアルファベット順**で決定論的にする(Goのmapはイテレーション順が不定なため)。

- [ ] **Step 0: parquet-goのスキーマ構築APIを確認する**

実装前に以下を実行し、`parquet.Group`・`parquet.String`・`parquet.Int`・`parquet.Timestamp`・`parquet.Required`の正確なシグネチャを確認する(バージョンによりAPIが変わる可能性があるため、下記Step 3のコードがコンパイルできない場合はこの出力を元に修正すること)。

Run: `go doc github.com/parquet-go/parquet-go Group`
Run: `go doc github.com/parquet-go/parquet-go String`
Run: `go doc github.com/parquet-go/parquet-go Int`
Run: `go doc github.com/parquet-go/parquet-go Timestamp`
Run: `go doc github.com/parquet-go/parquet-go Required`
Run: `go doc github.com/parquet-go/parquet-go NewSchema`

- [ ] **Step 1: 失敗するテストを書く**

`internal/schema/schema_test.go`:
```go
package schema

import (
	"testing"

	"logidx/internal/rules"
)

func TestBuild_ColumnsAreSortedByName(t *testing.T) {
	fields := map[string]rules.Field{
		"status": {Type: "int"},
		"path":   {Type: "string"},
		"time":   {Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"},
	}

	built, err := Build("nginx_access", fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"path", "status", "time"}
	if len(built.Columns) != len(want) {
		t.Fatalf("got %d columns, want %d", len(built.Columns), len(want))
	}
	for i, name := range want {
		if built.Columns[i] != name {
			t.Errorf("column[%d] = %q, want %q", i, built.Columns[i], name)
		}
	}
	if built.Schema == nil {
		t.Fatal("expected non-nil Schema")
	}
}

func TestBuild_UnsupportedTypeIsError(t *testing.T) {
	fields := map[string]rules.Field{"a": {Type: "bogus"}}
	_, err := Build("bad", fields)
	if err == nil {
		t.Fatal("expected error for unsupported field type")
	}
}

func TestBuildAll_DeduplicatesByName(t *testing.T) {
	ruleList := []rules.Rule{
		{Name: "dup", Fields: map[string]rules.Field{"a": {Type: "string"}}},
		{Name: "dup", Fields: map[string]rules.Field{"a": {Type: "string"}}},
		{Name: "other", Fields: map[string]rules.Field{"b": {Type: "int"}}},
	}

	all, err := BuildAll(ruleList)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 built schemas, got %d", len(all))
	}
	if _, ok := all["dup"]; !ok {
		t.Error("expected schema for name dup")
	}
	if _, ok := all["other"]; !ok {
		t.Error("expected schema for name other")
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/schema/... -v`
Expected: FAIL(`Build`/`BuildAll`/`Built`未定義)

- [ ] **Step 3: 実装**

`internal/schema/schema.go`:
```go
package schema

import (
	"fmt"
	"sort"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/rules"
)

// Built holds a derived Parquet schema together with the sorted field-name
// order used to build it, so callers can construct rows in matching order.
type Built struct {
	Schema  *parquet.Schema
	Columns []string
}

// Build derives a Parquet schema for the given rule name from its field
// definitions. Columns are ordered alphabetically by field name for
// deterministic output.
func Build(name string, fields map[string]rules.Field) (*Built, error) {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)

	group := parquet.Group{}
	for _, n := range names {
		node, err := nodeForType(fields[n].Type)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", n, err)
		}
		group[n] = parquet.Required(node)
	}

	return &Built{
		Schema:  parquet.NewSchema(name, group),
		Columns: names,
	}, nil
}

// BuildAll derives one Built schema per distinct rule name in ruleList,
// using the first rule's field definitions for each name (rules.Validate
// guarantees same-name rules declare identical name+type fields).
func BuildAll(ruleList []rules.Rule) (map[string]*Built, error) {
	result := map[string]*Built{}
	for _, r := range ruleList {
		if _, exists := result[r.Name]; exists {
			continue
		}
		built, err := Build(r.Name, r.Fields)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		result[r.Name] = built
	}
	return result, nil
}

func nodeForType(t string) (parquet.Node, error) {
	switch t {
	case "string":
		return parquet.String(), nil
	case "int":
		return parquet.Int(64), nil
	case "float":
		return parquet.Leaf(parquet.DoubleType), nil
	case "timestamp":
		return parquet.Timestamp(parquet.Microsecond), nil
	default:
		return nil, fmt.Errorf("unsupported field type %q", t)
	}
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/schema/... -v`
Expected: PASS。コンパイルが通らない場合はStep 0の`go doc`出力を参照して`nodeForType`のノード構築コードを実際のAPIに合わせて修正する。

- [ ] **Step 5: Commit**

```bash
git add internal/schema/schema.go internal/schema/schema_test.go
git commit -m "Add schema.Build/BuildAll: deterministic Parquet schema derivation"
```

---

## Task 9: internal/writer — type別Parquet書き込みとunmatched raw書き込み

**Files:**
- Create: `internal/writer/writer.go`
- Test: `internal/writer/writer_test.go`

**Interfaces:**
- Consumes: `schema.Built`(Task 8)
- Produces:
  - `type Summary struct { Counts map[string]int; Unmatched int }`
  - `func NewSet(outDir, basename string, built map[string]*schema.Built) *Set`
  - `func (s *Set) WriteMatched(name string, values map[string]any) error`
  - `func (s *Set) WriteUnmatched(lineNum int, raw string) error`
  - `func (s *Set) Close() (Summary, error)`

ファイルはtype/unmatchedとも**遅延作成**(最初の該当行が来るまでファイルを作らない)。

- [ ] **Step 0: parquet-goの行書き込みAPIを確認する**

Run: `go doc github.com/parquet-go/parquet-go Row`
Run: `go doc github.com/parquet-go/parquet-go Value`
Run: `go doc github.com/parquet-go/parquet-go ValueOf`
Run: `go doc github.com/parquet-go/parquet-go GenericWriter`
Run: `go doc github.com/parquet-go/parquet-go NewGenericWriter`
Run: `go doc github.com/parquet-go/parquet-go NewGenericReader`
Run: `go doc github.com/parquet-go/parquet-go Compression`

zstdコーデックが別パッケージのblank importでの登録を必要とするか確認する:

Run: `go doc github.com/parquet-go/parquet-go/compress/zstd`

もし登録が必要な場合、`internal/writer/writer.go`の先頭に`import _ "github.com/parquet-go/parquet-go/compress/zstd"`を追加する。

- [ ] **Step 1: 失敗するテストを書く**

`internal/writer/writer_test.go`:
```go
package writer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/rules"
	"logidx/internal/schema"
)

func buildTestSchemas(t *testing.T) map[string]*schema.Built {
	t.Helper()
	built, err := schema.BuildAll([]rules.Rule{
		{
			Name: "app_log",
			Fields: map[string]rules.Field{
				"level":   {Type: "string"},
				"message": {Type: "string"},
				"time":    {Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildTestSchemas: %v", err)
	}
	return built
}

func TestSet_WriteMatched_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, "access", built)

	ts := time.Date(2026, 8, 6, 12, 0, 1, 0, time.UTC)
	err := set.WriteMatched("app_log", map[string]any{
		"level":   "WARN",
		"message": "disk almost full",
		"time":    ts,
	})
	if err != nil {
		t.Fatalf("WriteMatched: %v", err)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["app_log"] != 1 {
		t.Errorf("expected 1 app_log row, got %d", summary.Counts["app_log"])
	}

	outPath := filepath.Join(dir, "access.app_log.parquet")
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected output file %s: %v", outPath, err)
	}

	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	reader := parquet.NewGenericReader[map[string]any](f, built["app_log"].Schema)
	defer reader.Close()
	rows := make([]map[string]any, 1)
	for i := range rows {
		rows[i] = map[string]any{}
	}
	n, err := reader.Read(rows)
	if n != 1 {
		t.Fatalf("expected to read 1 row, got %d (err=%v, size=%d)", n, err, stat.Size())
	}
	if rows[0]["level"] != "WARN" || rows[0]["message"] != "disk almost full" {
		t.Errorf("unexpected row content: %+v", rows[0])
	}
}

func TestSet_NoFileCreatedForUnusedName(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, "access", built)

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "access.app_log.parquet")); !os.IsNotExist(err) {
		t.Errorf("expected no output file to be created, stat err = %v", err)
	}
}

func TestSet_WriteUnmatched_CreatesFileLazilyWithLineNumbers(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, "access", built)

	if err := set.WriteUnmatched(3, "garbled line"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Unmatched != 1 {
		t.Errorf("expected Unmatched=1, got %d", summary.Unmatched)
	}

	content, err := os.ReadFile(filepath.Join(dir, "access.unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "3\tgarbled line\n"
	if string(content) != want {
		t.Errorf("got %q, want %q", string(content), want)
	}
}

func TestSet_NoUnmatchedFileWhenNoUnmatchedLines(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, "access", built)

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "access.unmatched.txt")); !os.IsNotExist(err) {
		t.Errorf("expected no unmatched file to be created, stat err = %v", err)
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/writer/... -v`
Expected: FAIL(`NewSet`等未定義)

- [ ] **Step 3: 実装**

`internal/writer/writer.go`:
```go
package writer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/schema"
)

// Summary reports per-name matched row counts and the unmatched line count
// for one processed input file.
type Summary struct {
	Counts    map[string]int
	Unmatched int
}

// Set lazily manages one Parquet writer per rule name plus one unmatched
// raw-text writer, all scoped to a single input file's basename.
type Set struct {
	outDir   string
	basename string
	built    map[string]*schema.Built

	parquetWriters map[string]*parquet.GenericWriter[parquet.Row]
	parquetFiles   map[string]*os.File
	counts         map[string]int

	unmatchedFile  *os.File
	unmatchedCount int
}

// NewSet creates a writer Set for input file basename, writing outputs into
// outDir. built maps rule name -> derived Parquet schema (from
// schema.BuildAll), used lazily when the first row for that name arrives.
func NewSet(outDir, basename string, built map[string]*schema.Built) *Set {
	return &Set{
		outDir:         outDir,
		basename:       basename,
		built:          built,
		parquetWriters: map[string]*parquet.GenericWriter[parquet.Row]{},
		parquetFiles:   map[string]*os.File{},
		counts:         map[string]int{},
	}
}

// WriteMatched writes one row of values (keyed by field name) for the given
// rule name, creating that name's Parquet file on first use.
func (s *Set) WriteMatched(name string, values map[string]any) error {
	w, err := s.writerFor(name)
	if err != nil {
		return err
	}

	built := s.built[name]
	row := make(parquet.Row, len(built.Columns))
	for i, col := range built.Columns {
		v := values[col]
		if t, ok := v.(time.Time); ok {
			v = t.UnixMicro()
		}
		row[i] = parquet.ValueOf(v)
	}

	if _, err := w.Write([]parquet.Row{row}); err != nil {
		return fmt.Errorf("write row for %q: %w", name, err)
	}
	s.counts[name]++
	return nil
}

func (s *Set) writerFor(name string) (*parquet.GenericWriter[parquet.Row], error) {
	if w, ok := s.parquetWriters[name]; ok {
		return w, nil
	}

	built, ok := s.built[name]
	if !ok {
		return nil, fmt.Errorf("no schema registered for rule name %q", name)
	}

	path := filepath.Join(s.outDir, fmt.Sprintf("%s.%s.parquet", s.basename, name))
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}

	w := parquet.NewGenericWriter[parquet.Row](f, built.Schema, parquet.Compression(&parquet.Zstd))

	s.parquetFiles[name] = f
	s.parquetWriters[name] = w
	return w, nil
}

// WriteUnmatched appends one "<lineNum>\t<raw>\n" record to this input
// file's unmatched raw-text sidecar, creating it on first use.
func (s *Set) WriteUnmatched(lineNum int, raw string) error {
	if s.unmatchedFile == nil {
		path := filepath.Join(s.outDir, s.basename+".unmatched.txt")
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		s.unmatchedFile = f
	}

	if _, err := fmt.Fprintf(s.unmatchedFile, "%d\t%s\n", lineNum, raw); err != nil {
		return fmt.Errorf("write unmatched line: %w", err)
	}
	s.unmatchedCount++
	return nil
}

// Close flushes and closes every writer/file opened by this Set and
// returns a Summary of what was written.
func (s *Set) Close() (Summary, error) {
	var errs []error

	for name, w := range s.parquetWriters {
		if err := w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close parquet writer %q: %w", name, err))
		}
	}
	for name, f := range s.parquetFiles {
		if err := f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close parquet file %q: %w", name, err))
		}
	}
	if s.unmatchedFile != nil {
		if err := s.unmatchedFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close unmatched file: %w", err))
		}
	}

	return Summary{Counts: s.counts, Unmatched: s.unmatchedCount}, errors.Join(errs...)
}
```

**注意**: `parquet.Compression(&parquet.Zstd)`の正確な呼び出し方(値渡しかポインタか、`parquet.Zstd`という名前の変数/定数が実在するか)はStep 0の`go doc`確認結果に従って修正すること。コンパイルが通らない場合、圧縮コーデック指定を一旦省略した`parquet.NewGenericWriter[parquet.Row](f, built.Schema)`(デフォルト圧縮)から始めてテストを通し、その後で正しいオプション名を`go doc github.com/parquet-go/parquet-go Compression`で確認して追加する。

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/writer/... -v`
Expected: PASS。コンパイル/実行エラーが出た場合はStep 0で確認したAPIに沿ってコードを調整し、再実行する。

- [ ] **Step 5: Commit**

```bash
git add internal/writer/writer.go internal/writer/writer_test.go
git commit -m "Add writer.Set: lazy per-name Parquet writers and unmatched sidecar"
```

---

## Task 10: internal/logging — slogセットアップ

**Files:**
- Create: `internal/logging/logging.go`
- Test: `internal/logging/logging_test.go`

**Interfaces:**
- Produces: `func New(w io.Writer, format string, verbose bool) *slog.Logger`(`format`は`"text"`または`"json"`。未知の値は`"text"`として扱う)

- [ ] **Step 1: 失敗するテストを書く**

`internal/logging/logging_test.go`:
```go
package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestNew_TextFormatWritesKeyValueLines(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "text", false)
	logger.Info("file processed", "file", "access.log", "unmatched", 3)

	out := buf.String()
	if !strings.Contains(out, "msg=\"file processed\"") {
		t.Errorf("expected text-formatted msg, got: %s", out)
	}
	if !strings.Contains(out, "file=access.log") {
		t.Errorf("expected file attribute, got: %s", out)
	}
}

func TestNew_JSONFormatWritesJSONLines(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "json", false)
	logger.Info("file processed", "file", "access.log")

	out := buf.String()
	if !strings.Contains(out, `"msg":"file processed"`) {
		t.Errorf("expected JSON-formatted msg, got: %s", out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("expected JSON object, got: %s", out)
	}
}

func TestNew_VerboseEnablesDebugLevel(t *testing.T) {
	var buf bytes.Buffer
	quiet := New(&buf, "text", false)
	quiet.Debug("should not appear")
	if buf.Len() != 0 {
		t.Errorf("expected no output at Info level for Debug message, got: %s", buf.String())
	}

	buf.Reset()
	verbose := New(&buf, "text", true)
	verbose.Debug("should appear")
	if buf.Len() == 0 {
		t.Error("expected Debug output when verbose=true")
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/logging/... -v`
Expected: FAIL(`New`未定義)

- [ ] **Step 3: 実装**

`internal/logging/logging.go`:
```go
package logging

import (
	"io"
	"log/slog"
)

// New builds a slog.Logger writing to w. format selects the handler
// ("json" for slog.NewJSONHandler, anything else defaults to text via
// slog.NewTextHandler). verbose lowers the level to Debug; otherwise Info.
func New(w io.Writer, format string, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	return slog.New(handler)
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/logging/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/logging/logging.go internal/logging/logging_test.go
git commit -m "Add logging.New: slog handler/level setup for text and json output"
```

---

## Task 11: internal/convert — 1入力ファイルのオーケストレーション

**Files:**
- Create: `internal/convert/convert.go`
- Test: `internal/convert/convert_test.go`

**Interfaces:**
- Consumes: `rules.Config`(Task 2/3), `parse.Match`(Task 7), `schema.BuildAll`(Task 8), `writer.NewSet`(Task 9), `*slog.Logger`(Task 10)
- Produces: `func File(inputPath, outDir string, cfg *rules.Config, logger *slog.Logger) error`

- [ ] **Step 1: 失敗するテストを書く**

`internal/convert/convert_test.go`:
```go
package convert

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/logging"
	"logidx/internal/rules"
)

const specExampleRulesYAML = `
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
      level: string
      message: string
`

const specExampleLog = `192.168.1.1 - - [06/Aug/2026:12:00:00 +0900] "GET /index.html HTTP/1.1" 200 512
2026-08-06T12:00:01+09:00 [INFO] user logged in
this is a garbled line that matches nothing
192.168.1.2 - - [06/Aug/2026:12:00:02 +0900] "GET /api HTTP/1.1" 200 128
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestFile_SpecExample_ProducesExpectedOutputs(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", specExampleRulesYAML)
	logPath := writeFile(t, dir, "access.log", specExampleLog)
	outDir := filepath.Join(dir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)

	if err := File(logPath, outDir, cfg, logger); err != nil {
		t.Fatalf("File: %v", err)
	}

	nginxPath := filepath.Join(outDir, "access.nginx_access.parquet")
	if countParquetRows(t, nginxPath) != 2 {
		t.Errorf("expected 2 rows in %s", nginxPath)
	}

	appPath := filepath.Join(outDir, "access.app_log.parquet")
	if countParquetRows(t, appPath) != 1 {
		t.Errorf("expected 1 row in %s", appPath)
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "access.unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "3\tthis is a garbled line that matches nothing\n"
	if string(unmatchedContent) != want {
		t.Errorf("got %q, want %q", string(unmatchedContent), want)
	}
}

func countParquetRows(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	reader := parquet.NewGenericReader[map[string]any](f)
	defer reader.Close()

	total := 0
	buf := make([]map[string]any, 8)
	for i := range buf {
		buf[i] = map[string]any{}
	}
	for {
		n, err := reader.Read(buf)
		total += n
		if err != nil {
			break
		}
	}
	return total
}
```

- [ ] **Step 2: テストを実行して失敗を確認**

Run: `go test ./internal/convert/... -v`
Expected: FAIL(`File`未定義)

- [ ] **Step 3: 実装**

`internal/convert/convert.go`:
```go
package convert

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"logidx/internal/parse"
	"logidx/internal/rules"
	"logidx/internal/schema"
	"logidx/internal/writer"
)

// File processes a single input log file: it matches each line against
// cfg.Rules, writes matched rows into per-rule-name Parquet files and
// unmatched lines into a raw-text sidecar, both under outDir, then logs a
// summary at Info level.
func File(inputPath, outDir string, cfg *rules.Config, logger *slog.Logger) error {
	in, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer in.Close()

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		return fmt.Errorf("build schemas: %w", err)
	}

	basename := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	set := writer.NewSet(outDir, basename, built)

	now := time.Now()
	scanner := bufio.NewScanner(in)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		name, values, ok := parse.Match(cfg.Rules, line, now)
		if !ok {
			logger.Debug("line did not match any rule", "file", inputPath, "line", lineNum)
			if err := set.WriteUnmatched(lineNum, line); err != nil {
				return fmt.Errorf("write unmatched line %d: %w", lineNum, err)
			}
			continue
		}

		if err := set.WriteMatched(name, values); err != nil {
			return fmt.Errorf("write matched row (rule %q, line %d): %w", name, lineNum, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	summary, err := set.Close()
	if err != nil {
		return fmt.Errorf("close writers: %w", err)
	}

	args := []any{"file", inputPath}
	for name, count := range summary.Counts {
		args = append(args, name, count)
	}
	args = append(args, "unmatched", summary.Unmatched)
	logger.Info("file processed", args...)

	return nil
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./internal/convert/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/convert/convert.go internal/convert/convert_test.go
git commit -m "Add convert.File: single-file line-by-line processing pipeline"
```

---

## Task 12: cmd/logidx — CLIエントリポイント

**Files:**
- Modify: `cmd/logidx/main.go`
- Delete: `cmd/logidx/run_placeholder.go`(Task 1のプレースホルダを置き換える)
- Test: `cmd/logidx/main_test.go`
- Create: `README.md`

**Interfaces:**
- Consumes: `rules.Load`(Task 2/3), `convert.File`(Task 11), `logging.New`(Task 10)
- Produces: `func run(args []string, stdout, stderr io.Writer) int`(`main`は`os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))`のみ)

CLIフラグ: `--rules <path>`(必須)、`--out <dir>`(デフォルト`./out`)、`--log-format text|json`(デフォルト`text`)、`-v/--verbose`。位置引数は入力ログファイル(複数可)。

終了コード: 起動時検証エラーや使い方エラーは`2`、入力ファイル単位のエラーが1件でもあれば`1`、すべて成功すれば`0`。

- [ ] **Step 1: 失敗するテストを書く**

`cmd/logidx/main_test.go`:
```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cliRulesYAML = `
rules:
  - name: app_log
    pattern: '^\[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      level: string
      message: string
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestRun_MissingRulesFlagReturnsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"somefile.log"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("expected usage message on stderr, got: %s", stderr.String())
	}
}

func TestRun_InvalidRulesFileReturnsExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", "not: [valid, yaml: structure")
	logPath := writeFile(t, dir, "app.log", "[INFO] hello\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--rules", rulesPath, "--out", filepath.Join(dir, "out"), logPath}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for invalid rules, got %d", code)
	}
}

func TestRun_ProcessesInputAndWritesOutput(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)
	logPath := writeFile(t, dir, "app.log", "[INFO] hello\n[WARN] careful\n")
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(outDir, "app.app_log.parquet")); err != nil {
		t.Errorf("expected output parquet file: %v", err)
	}
	if !strings.Contains(stderr.String(), "file processed") {
		t.Errorf("expected summary log on stderr, got: %s", stderr.String())
	}
}

func TestRun_MissingInputFileSkipsAndReturnsExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--rules", rulesPath, "--out", outDir, filepath.Join(dir, "does-not-exist.log")}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for missing input file, got %d", code)
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認**

まず古いプレースホルダを削除する:

```bash
rm cmd/logidx/run_placeholder.go
```

Run: `go test ./cmd/logidx/... -v`
Expected: FAIL(`run`が`main.go`にまだ実装されていないためビルドエラー、またはプレースホルダの`run`が常に`0`を返しテストが失敗する)

- [ ] **Step 3: 実装**

`cmd/logidx/main.go`:
```go
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"logidx/internal/convert"
	"logidx/internal/logging"
	"logidx/internal/rules"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logidx", flag.ContinueOnError)
	fs.SetOutput(stderr)

	rulesPath := fs.String("rules", "", "path to rules YAML file (required)")
	outDir := fs.String("out", "./out", "output directory")
	logFormat := fs.String("log-format", "text", "log format: text or json")
	verbose := fs.Bool("v", false, "verbose (debug) logging")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *rulesPath == "" || fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: logidx --rules <path> [--out <dir>] [--log-format text|json] [-v] <input-log-file>...")
		return 2
	}

	logger := logging.New(stderr, *logFormat, *verbose)

	cfg, err := rules.Load(*rulesPath)
	if err != nil {
		logger.Error("invalid rules config", "error", err)
		return 1
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		logger.Error("cannot create output directory", "dir", *outDir, "error", err)
		return 1
	}

	exitCode := 0
	for _, inputPath := range fs.Args() {
		if err := convert.File(inputPath, *outDir, cfg, logger); err != nil {
			logger.Error("failed to process file", "file", inputPath, "error", err)
			exitCode = 1
		}
	}

	return exitCode
}
```

- [ ] **Step 4: テストを実行して成功を確認**

Run: `go test ./cmd/logidx/... -v`
Expected: PASS

- [ ] **Step 5: 全パッケージのテスト・lint・formatを通す**

Run: `go build ./...`
Expected: 成功

Run: `gofmt -l .`
Expected: 出力なし(整形不要)。出力があれば`gofmt -w .`を実行する

Run: `go vet ./...`
Expected: 問題なし

Run: `go test ./...`
Expected: 全パッケージPASS

もし`golangci-lint`がローカルにインストールされていれば以下も実行する:

Run: `golangci-lint run ./...`
Expected: 問題なし(あれば修正する)

- [ ] **Step 6: README作成**

`README.md`:
```markdown
# logidx

複数種別が混在するテキストログを、YAMLで定義した正規表現ルールでパターンマッチして構造化し、
ルール名(type)ごとにParquetファイルへ変換するバッチCLIツール。

## Build

    task build
    # または
    go build -o bin/logidx ./cmd/logidx

## Usage

    logidx --rules rules.yaml --out ./out access.log app.log

- `--rules <path>` (必須): ルール定義YAMLファイル
- `--out <dir>` (デフォルト `./out`): 出力先ディレクトリ
- `--log-format text|json` (デフォルト `text`)
- `-v`: Debugレベルまでログを出す

出力ファイル名は `<入力ファイルのbasename>.<ルールname>.parquet`。
どのルールにもマッチしなかった行は `<basename>.unmatched.txt` に行番号付きで保存される。

ルール定義の書き方は `docs/superpowers/specs/2026-08-06-log-to-parquet-converter-design.md` を参照。

## Development

    task test   # go test ./...
    task lint   # golangci-lint run ./...
    task fmt    # gofmt -l -w .
```

- [ ] **Step 7: Commit**

```bash
git add cmd/logidx/main.go cmd/logidx/main_test.go README.md
git rm cmd/logidx/run_placeholder.go
git commit -m "Add logidx CLI entrypoint with flags, exit codes, and README"
```

---

## 完了確認

全タスク完了後、以下を実行してspec全体をエンドツーエンドで満たしていることを確認する。

Run: `go build -o bin/logidx ./cmd/logidx`
Expected: 成功

Run:
```bash
mkdir -p /tmp/logidx-smoke
cat > /tmp/logidx-smoke/rules.yaml <<'EOF'
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
      message: string
EOF
cat > /tmp/logidx-smoke/access.log <<'EOF'
192.168.1.1 - - [06/Aug/2026:12:00:00 +0900] "GET /index.html HTTP/1.1" 200 512
2026-08-06T12:00:01+09:00 [Warning] disk almost full
this is a garbled line that matches nothing
EOF
./bin/logidx --rules /tmp/logidx-smoke/rules.yaml --out /tmp/logidx-smoke/out -v /tmp/logidx-smoke/access.log
ls /tmp/logidx-smoke/out
```
Expected: `access.nginx_access.parquet`、`access.app_log.parquet`、`access.unmatched.txt`が生成され、stderrに`file processed`のサマリログが出力される
