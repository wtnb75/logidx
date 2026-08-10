# プリセットを`structured.format`として使う Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `structured.format`に`json`/`ltsv`/`logfmt`に加えてプリセット名(`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`)を指定できるようにし、ログ行の一部だけがプリセット形式(例: syslog転送されたコンテナログの末尾がCLFアクセスログ)であるケースを、既存の`structured:`の`key:`/`extra:`フィールドマッピングの枠組みでそのまま扱えるようにする。

**Architecture:** `internal/rules`の`presetRegistry`(`internal/rules/presets.go`)に既に登録済みのプリセットパターンを、`StructuredConfig.Format`がjson/ltsv/logfmtのいずれでもない場合に流用する。`rules.Load()`でプリセットのパターン文字列を1回だけコンパイルして`StructuredConfig.PresetRegexp *regexp.Regexp`にキャッシュし(`Rule.Regexp`と同じ方針)、`internal/parse`に新設する`ParsePreset(re, raw)`が`re.FindStringSubmatch`の結果を名前付きキャプチャグループのmapに変換する。`parse.Convert()`は`rule.Structured.PresetRegexp != nil`かどうかで`ParsePreset`/`ParseStructured`を呼び分けるだけで、`key:`/`extra:`によるフィールドマッピング・型変換ロジックには一切手を加えない。

**Tech Stack:** Go 1.x, `regexp`(標準ライブラリ), 既存の`gopkg.in/yaml.v3`。新規の外部依存は追加しない。

## Global Constraints

- 本docが依存する2つの機能(`internal/rules/presets.go`のプリセットレジストリ、`internal/parse/structured.go`の`structured:`/`key:`/`extra:`)は既に実装済み — 本プランはその上に分岐を追加するだけ。
- プリセット自体の追加・変更・パターン定義は対象外(`presetRegistry`は変更しない)。
- ルールレベルの`preset:`ショートカット(行全体をプリセットに置き換える機能)との統合・特別扱いはしない。両者は独立して動作する。
- `key:`で参照する名前は各プリセットの`presetRegistry`に列挙されたフィールド名(例: `apache_clf`なら`remote_addr`/`remote_user`/`time`/`method`/`path`/`proto`/`status`/`bytes`)。
- プリセットのパターンが`raw`にマッチしない場合は、既存の「構造化データのパース失敗」と全く同じ扱いでunmatchedになる(新しいエラー分類を追加しない)。

---

## File Structure

- Modify: `internal/rules/rules.go` — `StructuredConfig`に`PresetRegexp`フィールドを追加し、`Load()`でプリセット名のパターンをコンパイルしてキャッシュする。
- Modify: `internal/rules/validate.go` — `Structured.Format`の許容値チェックをプリセット名も許容するよう拡張する。
- Test: `internal/rules/rules_test.go` — `Load()`がプリセット名の`Structured.Format`から`PresetRegexp`を正しくコンパイルすることを検証。
- Test: `internal/rules/validate_test.go` — 既知のプリセット名は`Validate()`を通り、未知の名前は引き続きエラーになることを検証。
- Modify: `internal/parse/structured.go` — `ParsePreset(re *regexp.Regexp, raw string) (map[string]string, error)`を追加。
- Test: `internal/parse/structured_test.go` — `ParsePreset`の単体テスト(マッチ成功・失敗)。
- Modify: `internal/parse/match.go` — `Convert()`に`rule.Structured.PresetRegexp != nil`の分岐を追加。
- Test: `internal/parse/match_test.go` — `Convert()`がプリセット正規表現経由で`key:`フィールドに値を詰めること、マッチ失敗時にエラーを返すことを検証。
- Test: `cmd/logidx/main_test.go` — 概要に挙げたsyslog転送コンテナログ(末尾がCLF)のEnd-to-endの回帰テストを追加。
- Modify: `README.md` — `structured.format`にプリセット名を指定するケースの書き方・実例を追記。

---

## Task 1: `StructuredConfig.PresetRegexp`の追加とLoad()での解決

**Files:**
- Modify: `internal/rules/rules.go:32-38`(`StructuredConfig`定義)、`internal/rules/rules.go:192-244`(`Load()`のルールループ)
- Test: `internal/rules/rules_test.go`

**Interfaces:**
- Consumes: `presetRegistry map[string]presetDefinition`(`internal/rules/presets.go:14`、`presetDefinition{Pattern string; Fields []Field}`) — 既存のプリセットレジストリをそのまま参照する。
- Produces: `StructuredConfig.PresetRegexp *regexp.Regexp`(新規フィールド、yaml `-`)。`nil`のままなら`Structured.Format`はjson/ltsv/logfmtのいずれか(またはLoad時点で未解決の不正な値)。非nilならプリセットのコンパイル済み正規表現。Task 3の`parse.Convert()`がこのフィールドで分岐する。

- [ ] **Step 1: 失敗するテストを書く — Load()がプリセット名からPresetRegexpをコンパイルする**

`internal/rules/rules_test.go`の末尾に追加:

```go
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
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/rules/... -run TestLoad_StructuredFormat -v`
Expected: FAIL — `rule.Structured.PresetRegexp undefined (type *StructuredConfig has no field or method PresetRegexp)`

- [ ] **Step 3: `StructuredConfig`に`PresetRegexp`を追加する**

`internal/rules/rules.go:32-38`を置き換え:

```go
// StructuredConfig configures parsing embedded structured data (JSON, LTSV,
// logfmt, or a preset's fixed pattern) out of one of a rule's
// pattern-captured fields, named by Source. Format selects the parser:
// "json", "ltsv", "logfmt", or the name of an entry in presetRegistry (see
// presets.go).
type StructuredConfig struct {
	Source string `yaml:"source"`
	Format string `yaml:"format"`

	// PresetRegexp is set by Load, once, when Format names a registered
	// preset instead of json/ltsv/logfmt: it's presetRegistry[Format]'s
	// Pattern, compiled (same "compile once at Load time" approach as
	// Rule.Regexp). nil when Format is json/ltsv/logfmt. parse.Convert
	// branches on this to pick ParsePreset over ParseStructured.
	PresetRegexp *regexp.Regexp `yaml:"-"`
}
```

- [ ] **Step 4: `Load()`でプリセット名の`Structured.Format`をコンパイルする**

`internal/rules/rules.go`のルールループ内、既存の`Rule.Preset`展開ブロック(`if cfg.Rules[i].Preset != "" { ... }`、11-16行目相当)の直後、`re, err := regexp.Compile(cfg.Rules[i].Pattern)`より前に追加:

```go
		if cfg.Rules[i].Structured != nil {
			format := cfg.Rules[i].Structured.Format
			if !builtinStructuredFormats[format] {
				if preset, ok := presetRegistry[format]; ok {
					pre, err := regexp.Compile(preset.Pattern)
					if err != nil {
						return nil, fmt.Errorf("rule %q: compile structured preset pattern %q: %w", cfg.Rules[i].Name, format, err)
					}
					cfg.Rules[i].Structured.PresetRegexp = pre
				}
				// An unknown format (neither builtin nor a registered
				// preset) is left unresolved - Validate reports "not
				// json/ltsv/logfmt or a known preset name" and Load
				// returns that error, so this rule's nil PresetRegexp
				// never reaches a caller.
			}
		}
```

`builtinStructuredFormats`は Task で参照する共有マップ(現状`validate.go`の`allowedStructuredFormats`)。Task 1のStep 5で`validate.go`側をこの名前にリネームし、`rules.go`から参照できるようにする(同一パッケージ内なので追加の import は不要)。

- [ ] **Step 5: `validate.go`の`allowedStructuredFormats`を`builtinStructuredFormats`にリネームする**

`internal/rules/validate.go:15-19`を置き換え:

```go
// builtinStructuredFormats holds the non-preset Structured.Format values.
// Referenced by both Load (to decide whether a format name should resolve
// against presetRegistry) and Validate (to accept builtin-or-preset format
// names).
var builtinStructuredFormats = map[string]bool{
	"json":   true,
	"ltsv":   true,
	"logfmt": true,
}
```

`internal/rules/validate.go:71`の`if !allowedStructuredFormats[rule.Structured.Format] {`は現状のまま残す(Step 6でこのif文自体を書き換えるので、ここでは変数名の参照だけ更新):

```go
			if !builtinStructuredFormats[rule.Structured.Format] {
```

この時点ではまだプリセット名は弾かれる(Step 6で緩和する)。

- [ ] **Step 6: テストを実行して通ることを確認する**

Run: `go test ./internal/rules/... -run TestLoad_StructuredFormat -v`
Expected: PASS

- [ ] **Step 7: `Validate()`の`Structured.Format`チェックをプリセット名も許容するよう拡張する — まず失敗するテストを書く**

`internal/rules/validate_test.go`の末尾に追加(既存の`TestValidate_StructuredFormatXXX`の直後を想定):

```go
func TestValidate_StructuredFormatPresetNameIsAccepted(t *testing.T) {
	pattern := `^(?P<access>.*)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "ok",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "access", Format: "apache_clf"},
				Fields:     []Field{{Name: "access", Type: "string"}},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error for a preset structured format, got: %v", err)
	}
}

func TestValidate_StructuredFormatUnknownNameIsStillError(t *testing.T) {
	pattern := `^(?P<access>.*)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "access", Format: "not_a_real_preset"},
				Fields:     []Field{{Name: "access", Type: "string"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "not_a_real_preset") {
		t.Errorf("expected error mentioning the unknown structured format, got: %v", err)
	}
}
```

- [ ] **Step 8: テストを実行して失敗を確認する**

Run: `go test ./internal/rules/... -run TestValidate_StructuredFormat -v`
Expected: `TestValidate_StructuredFormatPresetNameIsAccepted` FAILs (`structured format "apache_clf" is not one of json/ltsv/logfmt`); `TestValidate_StructuredFormatUnknownNameIsStillError`も既存メッセージのままなら一旦PASSしている可能性があるが、Step 9のメッセージ文言変更後に再確認する。

- [ ] **Step 9: `Validate()`のチェックをプリセット名も許容するよう書き換える**

`internal/rules/validate.go:70-77`を置き換え:

```go
		if rule.Structured != nil {
			_, isPreset := presetRegistry[rule.Structured.Format]
			if !builtinStructuredFormats[rule.Structured.Format] && !isPreset {
				errs = append(errs, fmt.Errorf("rule %q: structured format %q is not json/ltsv/logfmt or a known preset name", rule.Name, rule.Structured.Format))
			}
			if !captureNames[rule.Structured.Source] {
				errs = append(errs, fmt.Errorf("rule %q: structured source %q has no matching named capture group in pattern", rule.Name, rule.Structured.Source))
			}
		}
```

- [ ] **Step 10: テストを実行して通ることを確認する**

Run: `go test ./internal/rules/... -v`
Expected: PASS(全テスト)

- [ ] **Step 11: パッケージ全体をビルド・テストする**

Run: `go build ./... && go test ./internal/rules/...`
Expected: ビルド成功、全テストPASS

- [ ] **Step 12: コミット**

```bash
git add internal/rules/rules.go internal/rules/validate.go internal/rules/rules_test.go internal/rules/validate_test.go
git commit -m "feat(rules): allow structured.format to name a preset, compiling it into StructuredConfig.PresetRegexp"
```

---

## Task 2: `internal/parse.ParsePreset`の追加

**Files:**
- Modify: `internal/parse/structured.go`
- Test: `internal/parse/structured_test.go`

**Interfaces:**
- Consumes: なし(標準ライブラリ`regexp`のみ)。Task 1で追加された`rules.StructuredConfig.PresetRegexp`が渡す`*regexp.Regexp`を受け取る想定だが、この関数自体は`rules`パッケージに依存しない(`internal/parse/structured.go`の既存方針を踏襲)。
- Produces: `ParsePreset(re *regexp.Regexp, raw string) (map[string]string, error)` — `re.FindStringSubmatch(raw)`の結果を名前付きキャプチャグループ名→値のmapに変換する。マッチしなければエラーを返す。戻り値の形は`ParseStructured`と同じ`map[string]string`なので、Task 3で呼び分けても後続の`key:`/`extra:`ロジックは変更不要。

- [ ] **Step 1: 失敗するテストを書く**

`internal/parse/structured_test.go`の末尾に追加(import追加が必要 — 現状`import "testing"`のみなので`"regexp"`を追加する):

```go
func TestParsePreset_MatchReturnsNamedCaptureGroups(t *testing.T) {
	re := regexp.MustCompile(`^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$`)
	got, err := ParsePreset(re, `127.0.0.1 - frank [10/Oct/2023:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 200 2326`)
	if err != nil {
		t.Fatalf("ParsePreset returned error: %v", err)
	}
	if got["remote_addr"] != "127.0.0.1" || got["status"] != "200" || got["method"] != "GET" {
		t.Errorf("got %+v, missing/wrong expected keys", got)
	}
}

func TestParsePreset_NoMatchIsError(t *testing.T) {
	re := regexp.MustCompile(`^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$`)
	_, err := ParsePreset(re, "this is not a CLF access log line")
	if err == nil {
		t.Error("expected an error when the preset pattern does not match")
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/parse/... -run TestParsePreset -v`
Expected: FAIL — `undefined: ParsePreset`

- [ ] **Step 3: `ParsePreset`を実装する**

`internal/parse/structured.go`の先頭のimportに`"regexp"`を追加:

```go
import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)
```

`ParseStructured`関数の直後(29行目の`}`の後)に追加:

```go
// ParsePreset runs re (a preset's compiled pattern, from
// rules.StructuredConfig.PresetRegexp) against raw and returns its named
// capture groups as a flat map, the same shape ParseStructured returns for
// json/ltsv/logfmt - so callers (see Convert) can treat a preset-format
// structured source identically to json/ltsv/logfmt once parsed. Returns an
// error if raw doesn't match re.
func ParsePreset(re *regexp.Regexp, raw string) (map[string]string, error) {
	m := re.FindStringSubmatch(raw)
	if m == nil {
		return nil, fmt.Errorf("preset pattern did not match structured source: %q", raw)
	}

	result := make(map[string]string, len(m))
	for i, name := range re.SubexpNames() {
		if i == 0 || name == "" {
			continue
		}
		result[name] = m[i]
	}
	return result, nil
}
```

- [ ] **Step 4: テストを実行して通ることを確認する**

Run: `go test ./internal/parse/... -run TestParsePreset -v`
Expected: PASS

- [ ] **Step 5: コミット**

```bash
git add internal/parse/structured.go internal/parse/structured_test.go
git commit -m "feat(parse): add ParsePreset to parse a structured source via a preset's compiled pattern"
```

---

## Task 3: `Convert()`をPresetRegexpで分岐させる

**Files:**
- Modify: `internal/parse/match.go:43-50`
- Test: `internal/parse/match_test.go`

**Interfaces:**
- Consumes: `rules.StructuredConfig.PresetRegexp *regexp.Regexp`(Task 1)、`ParsePreset(re *regexp.Regexp, raw string) (map[string]string, error)`(Task 2)。
- Produces: `Convert()`の外部シグネチャ・戻り値は変更なし。`rule.Structured.PresetRegexp != nil`のとき`ParsePreset`を、そうでなければ従来通り`ParseStructured`を呼ぶ。

- [ ] **Step 1: 失敗するテストを書く**

`internal/parse/match_test.go`の`TestConvert_StructuredParseFailureReturnsError`の直後に追加(`rules`パッケージは既にimport済み):

```go
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
```

`match_test.go`の先頭importに`"regexp"`が無ければ追加する(既存のimportブロックを確認し、無ければ`"regexp"`を追加)。

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/parse/... -run TestConvert_PresetFormat -v`
Expected: FAIL — `status`が`0`または空文字になる(現状`ParseStructured("apache_clf", ...)`が呼ばれ`unsupported structured format "apache_clf"`エラーになるため、テストは`Convert returned error`側でFatalする)

- [ ] **Step 3: `Convert()`にPresetRegexpの分岐を追加する**

`internal/parse/match.go:43-50`を置き換え:

```go
func Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error) {
	var structuredValues map[string]string
	if rule.Structured != nil {
		source := raw[rule.Structured.Source]
		if rule.Structured.PresetRegexp != nil {
			structuredValues, err = ParsePreset(rule.Structured.PresetRegexp, source)
		} else {
			structuredValues, err = ParseStructured(rule.Structured.Format, source)
		}
		if err != nil {
			return nil, fmt.Errorf("parse structured data: %w", err)
		}
	}
```

(関数のシグネチャ行`func Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error) {`はそのまま、中身のみ置き換え)

- [ ] **Step 4: テストを実行して通ることを確認する**

Run: `go test ./internal/parse/... -v`
Expected: PASS(全テスト)

- [ ] **Step 5: コミット**

```bash
git add internal/parse/match.go internal/parse/match_test.go
git commit -m "feat(parse): route Convert through ParsePreset when a rule's structured format names a preset"
```

---

## Task 4: End-to-endの回帰テスト(`cmd/logidx`)

**Files:**
- Modify: `cmd/logidx/main_test.go`

**Interfaces:**
- Consumes: `run([]string, io.Writer, io.Writer) int`(既存、`main.go`)、`writeFile(t, dir, name, content) string`(既存ヘルパー、`main_test.go`内)。他タスクの新規シンボルは直接は使わない — rules.yaml経由でTask 1〜3の変更を通しで検証する。
- Produces: なし(テストのみ)。

- [ ] **Step 1: 失敗するテストを書く**

`cmd/logidx/main_test.go`の`TestRun_ImportExtractsStructuredJSONFieldsAndCollectsExtra`(883行目)の直後に追加。設計docの概要の実例(syslog転送されたコンテナログの末尾がCLFアクセスログ)をそのまま使う:

```go
func TestRun_ImportExtractsPresetStructuredFormatFields(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: docker_apprise_access
    pattern: '^(?P<ts>\S+) (?P<host>\S+) (?P<tag>[^\[]+)\[(?P<pid>\d+)\] (?P<access>.*)$'
    structured:
      source: access
      format: apache_clf
    fields:
      ts:
        type: timestamp
        format: iso8601
      host: string
      tag: string
      pid: string
      remote_addr:
        type: string
        key: remote_addr
      method:
        type: string
        key: method
      path:
        type: string
        key: path
      status:
        type: int
        key: status
      access_time:
        type: timestamp
        format: clf
        key: time
      extra:
        type: string
        extra: true
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)

	// Note: the design doc's overview example line ends with a quoted
	// referer/user-agent suffix (`"-" "Deno/2.2.4"`), but apache_clf's
	// preset pattern is anchored with a trailing `$` right after `bytes`
	// (it's CLF, not Combined) - that suffix would make the preset
	// pattern fail to match, sending the whole line to unmatched
	// instead of demonstrating a successful conversion. This line drops
	// that suffix so it's consistent with the `format: apache_clf`
	// rules.yaml in the design doc's own "1. rules.yaml設定" section,
	// which is what this test exercises.
	logContent := `2026-01-01T11:19:03.727584+09:00 wtnb4 container/apprise/209c6867d22d[1019] 172.20.0.20 - - [01/Jan/2026:11:19:03 +0900] "POST /notify/ HTTP/1.1" 200 113
`
	logPath := writeFile(t, dir, "container.log", logContent)
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", filepath.Join(outDir, "docker_apprise_access.parquet"), "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 2 { // 1 header + 1 row
		t.Fatalf("expected 2 dump lines (header + 1 row), got %d: %q", len(lines), stdout.String())
	}

	var row map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &row); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[1], err)
	}

	if row["host"] != "wtnb4" || row["tag"] != "container/apprise/209c6867d22d" || row["pid"] != "1019" {
		t.Errorf("host/tag/pid = %v/%v/%v, want wtnb4/container/apprise/209c6867d22d/1019", row["host"], row["tag"], row["pid"])
	}
	if row["remote_addr"] != "172.20.0.20" || row["method"] != "POST" || row["path"] != "/notify/" {
		t.Errorf("remote_addr/method/path = %v/%v/%v, want 172.20.0.20/POST//notify/", row["remote_addr"], row["method"], row["path"])
	}
	if row["status"] != int64(200) {
		t.Errorf("status = %v, want int64(200)", row["status"])
	}
	if accessTime, _ := row["access_time"].(string); !strings.HasPrefix(accessTime, "2026-01-01T11:19:03") {
		t.Errorf("access_time = %v, want prefix 2026-01-01T11:19:03", row["access_time"])
	}
	wantExtra := `{"bytes":"113","proto":"HTTP/1.1","remote_user":"-"}`
	if row["extra"] != wantExtra {
		t.Errorf("extra = %v, want %q", row["extra"], wantExtra)
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./cmd/logidx/... -run TestRun_ImportExtractsPresetStructuredFormatFields -v`
Expected: FAIL — `rules.Load`が`structured format "apache_clf" is not one of json/ltsv/logfmt`エラーを返し、`exit code 0`のFatalで落ちる(Task 1〜3が未実装の場合)。Task 1〜3実装後にこのタスクへ進む場合は、最初からPASSする可能性がある — その場合は一旦Task 1のコード変更を`git stash`して失敗を確認してからstash popしてもよいし、既にTask 1〜3のテストで同等の赤→緑を確認済みであれば本タスクはこのステップを「実装済みの状態でPASSすることの確認」に読み替えてよい。

- [ ] **Step 3: テストを実行して通ることを確認する(Task 1〜3が実装済みであることの確認)**

Run: `go test ./cmd/logidx/... -run TestRun_ImportExtractsPresetStructuredFormatFields -v`
Expected: PASS

- [ ] **Step 4: パッケージ全体をテストする**

Run: `go build ./... && go test ./...`
Expected: ビルド成功、全テストPASS

- [ ] **Step 5: コミット**

```bash
git add cmd/logidx/main_test.go
git commit -m "test: add end-to-end regression test for preset-as-structured-format (syslog-wrapped CLF access log)"
```

---

## Task 5: README.mdへのドキュメント追記

**Files:**
- Modify: `README.md`(196-235行目付近、`### 構造化データの部分パース(structured:/key:/extra:)`セクションの末尾)

**Interfaces:**
- Consumes: なし。
- Produces: なし(ドキュメントのみ)。

- [ ] **Step 1: README.mdの`### 構造化データの部分パース`セクション末尾(235行目、`unmatched.txt`に書かれる云々の行の後)に新しいサブセクションを追記する**

`README.md:235`の直後、`### 圧縮設定`(237行目)の直前に挿入:

```markdown

#### `structured.format`にプリセット名を指定する

`structured.format`には`json`/`ltsv`/`logfmt`に加えて、`preset:`(前述)で使えるプリセット名(`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`)も指定できる。ログ行全体ではなく、一部だけがプリセット形式になっているケース(例: syslog転送されたコンテナログの末尾がCLFアクセスログ)向け。

```yaml
rules:
  - name: docker_apprise_access
    pattern: '^(?P<ts>\S+) (?P<host>\S+) (?P<tag>[^\[]+)\[(?P<pid>\d+)\] (?P<access>.*)$'
    structured:
      source: access
      format: apache_clf
    fields:
      ts:
        type: timestamp
        format: iso8601
      host: string
      tag: string
      pid: string
      remote_addr:
        type: string
        key: remote_addr
      method:
        type: string
        key: method
      path:
        type: string
        key: path
      status:
        type: int
        key: status
      access_time:
        type: timestamp
        format: clf
        key: time
      extra:
        type: string
        extra: true
```

- `key:`で参照する名前は、そのプリセット定義の`fields:`に列挙されているフィールド名(`apache_clf`/`apache_combined`なら`remote_addr`/`remote_user`/`time`/`method`/`path`/`proto`/`status`/`bytes`、`syslog_rfc3164`なら`time`/`host`/`tag`/`pid`/`message`、`syslog_rfc5424`なら`pri`/`version`/`time`/`host`/`app`/`procid`/`msgid`/`sd`/`message`)。既存の`structured:`と同じく、必要なキーだけ選んで好きなフィールド名・型で受け取れる(上記例では`time`を`access_time`という名前で受けている)。
- プリセットの固定パターンが`structured.source`のキャプチャ内容にマッチしない場合は、既存の「構造化データのパース失敗」と同じ扱いで`unmatched.txt`に書かれる。
- ルールレベルの`preset:`ショートカット(行全体をプリセットに置き換える機能)とは独立した機能で、組み合わせや特別な連携はない。
```

- [ ] **Step 2: 差分を確認する**

Run: `git diff README.md`
Expected: 上記のセクションが`### 構造化データの部分パース`と`### 圧縮設定`の間に挿入されている

- [ ] **Step 3: コミット**

```bash
git add README.md
git commit -m "docs: document specifying a preset name as structured.format"
```

---

## Self-Review Checklist (実施済み)

- **Spec coverage:** 「1. rules.yaml設定」→Task 4/5の実例、「2. 実装」の4項目→Task 1(StructuredConfig.PresetRegexp/Load)・Task 1(validate.go)・Task 2(ParsePreset)・Task 3(Convert分岐)、「エラーハンドリング」→Task 1 Step 9(Validate)・Task 2/3のno-matchテスト、「テスト方針」の4項目→Task 2(internal/parse単体)・Task 1(internal/rules Load/Validate)・Task 4(cmd/logidx E2E)・Task 5(README)。すべて対応済み。
- **Placeholder scan:** 全ステップに実コード・実コマンドを記載済み、TODO/TBD等なし。
- **Type consistency:** `StructuredConfig.PresetRegexp *regexp.Regexp`(Task 1で定義)は Task 3の`match.go`変更・テストで同じ型・同じフィールド名を使用。`ParsePreset(re *regexp.Regexp, raw string) (map[string]string, error)`(Task 2で定義)はTask 3の`Convert()`変更で同じシグネチャのまま呼び出している。`builtinStructuredFormats`(Task 1 Step 5でリネーム)はTask 1 Step 4・Step 9の両方で同じ名前を参照している。
