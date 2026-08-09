# 構造化データ(JSON/LTSV/logfmt)フィールド抽出 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ログ行の一部として埋め込まれた構造化データ(JSON/LTSV/logfmt)を、キー名でフィールドにマッピングできるようにする(`structured:`/`key:`/`extra:`)。

**Architecture:** 既存の`pattern`(1本の正規表現)で構造化データ部分を1つの名前付きキャプチャグループとして切り出すのは変えない。新しい`ParseStructured`関数がそのキャプチャ文字列をフォーマット別にフラットな`map[string]string`へパースし、`Convert`がフィールドの`key:`/`extra:`設定に応じてその map から値を引き当てる。

**Tech Stack:** Go 1.25, `encoding/json`(`UseNumber`)、手書きのLTSV/logfmtパーサー(サードパーティ依存追加なし)、`gopkg.in/yaml.v3`。

## Global Constraints

- 対応する構造化データフォーマットは`json`/`ltsv`/`logfmt`の3つのみ(spec 3節)。
- `structured:`は1ルールにつき最大1個(spec Non-goals)。
- `extra: true`のフィールドは1ルールにつき最大1個(spec 3節)。
- `field.Key != ""`と`field.Extra == true`は同時設定不可(spec 3節)。
- `field.Key != ""`または`field.Extra == true`のフィールドは`rule.Structured != nil`が必須(spec 3節)。
- 残りカラム(`extra:`)の値は常に「キー→文字列」のJSON(元がJSONの数値・真偽値でも文字列としてクォートされる)(spec 2節)。
- `marshalUnconsumed`は`encoding/json.Marshal`のmapキー自動ソートに依存して出力を決定的にする(spec 2節)。
- v1ではネストしたキーパス指定は非対応。ネストしたオブジェクト/配列はまるごとコンパクトなJSON文字列として1つの値になる(spec Non-goals, 2節)。
- `structured:`未設定のルールの挙動は完全に従来通り(spec Non-goals, 4節)。
- 構造化データのパース失敗・キー不在は、新しいエラー分類を増やさず既存の「型変換失敗→unmatched」経路にそのまま乗せる(spec 4節)。

---

## Task 1: `rules.Rule.Structured` / `rules.Field.Key` / `rules.Field.Extra` の追加

**Files:**
- Modify: `internal/rules/rules.go`
- Test: `internal/rules/rules_test.go`

**Interfaces:**
- Produces:
  - `type StructuredConfig struct { Source string; Format string }` (yaml tags `source`/`format`)
  - `Rule.Structured *StructuredConfig` (yaml tag `structured`, optional)
  - `Field.Key string` (yaml tag `key`, optional)
  - `Field.Extra bool` (yaml tag `extra`, optional)
- Consumed by: Task 3 (`internal/rules/validate.go`)、Task 4 (`internal/parse/match.go`)。Task 2 (`ParseStructured`)はこのタスクに依存しない(`format`/`raw` の生文字列だけを受け取るため並行して進めてよい)。

- [ ] **Step 1: Write the failing tests**

`internal/rules/rules_test.go`の末尾に追記:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rules/... -run TestLoad_ParsesStructuredConfigAndKeyExtraFields -v`
Expected: コンパイルエラー(`rule.Structured undefined`、`level.Key undefined`など`Structured`/`Key`/`Extra`が未定義のため)。

- [ ] **Step 3: Implement the schema changes**

`internal/rules/rules.go`の`ReplaceRule`定義の直後(`Field`定義の前)に`StructuredConfig`を追加:

```go
// StructuredConfig configures parsing embedded structured data (JSON, LTSV,
// or logfmt) out of one of a rule's pattern-captured fields, named by
// Source. Format selects the parser: "json", "ltsv", or "logfmt".
type StructuredConfig struct {
	Source string `yaml:"source"`
	Format string `yaml:"format"`
}
```

`Field`構造体に`Key`/`Extra`を追加(`Normalize`の後、`ResolvedFormat`コメントの前):

```go
type Field struct {
	Name      string          `yaml:"-"`
	Type      string          `yaml:"type"`
	Format    string          `yaml:"format"`
	Replace   []ReplaceRule   `yaml:"replace"`
	Normalize []NormalizeRule `yaml:"normalize"`

	// Key, if set, takes this field's raw value from the rule's parsed
	// structured data (see Rule.Structured) under this key name, instead
	// of from a same-named pattern capture group.
	Key string `yaml:"key"`
	// Extra, if true, collects every structured-data key not consumed by
	// another field's Key into this field as a JSON string. At most one
	// field per rule may set Extra.
	Extra bool `yaml:"extra"`

	// ResolvedFormat is Format resolved once by ResolveFormat, at Load
	// time - see TimeFormat. Only meaningful when Type == "timestamp".
	ResolvedFormat TimeFormat `yaml:"-"`
}
```

`Rule`構造体に`Structured`を追加(`Regexp`の後、`Continuation`の前):

```go
type Rule struct {
	Name    string         `yaml:"name"`
	Pattern string         `yaml:"pattern"`
	Fields  []Field        `yaml:"-"`
	Regexp  *regexp.Regexp `yaml:"-"`

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

`Rule.UnmarshalYAML`のローカル`alias`構造体に`Structured`を追加し、コピーする:

```go
func (r *Rule) UnmarshalYAML(value *yaml.Node) error {
	var alias struct {
		Name         string            `yaml:"name"`
		Pattern      string            `yaml:"pattern"`
		Continuation string            `yaml:"continuation"`
		Structured   *StructuredConfig `yaml:"structured"`
		Fields       yaml.Node         `yaml:"fields"`
	}
	if err := value.Decode(&alias); err != nil {
		return err
	}
	r.Name = alias.Name
	r.Pattern = alias.Pattern
	r.Continuation = alias.Continuation
	r.Structured = alias.Structured

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

`Field.Key`/`Field.Extra`は既存の`Field.UnmarshalYAML`が使う`fieldAlias`(`type fieldAlias Field`)経由で自動的にデコードされるため、`Field.UnmarshalYAML`自体の変更は不要。

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rules/... -v`
Expected: PASS(既存テストも含めて全件)。

- [ ] **Step 5: Commit**

```bash
git add internal/rules/rules.go internal/rules/rules_test.go
git commit -m "feat(rules): add Rule.Structured and Field.Key/Field.Extra"
```

---

## Task 2: `internal/parse.ParseStructured`(json/ltsv/logfmt パーサー)

**Files:**
- Create: `internal/parse/structured.go`
- Test: `internal/parse/structured_test.go`

**Interfaces:**
- Produces: `func ParseStructured(format, raw string) (map[string]string, error)`
- Consumed by: Task 4 (`internal/parse/match.go`の`Convert`)。Task 1に依存しない(このタスクは並行して進めてよい)。

- [ ] **Step 1: Write the failing tests**

`internal/parse/structured_test.go`を新規作成:

```go
package parse

import "testing"

func TestParseStructured_JSON_FlatValues(t *testing.T) {
	got, err := ParseStructured("json", `{"level":"INFO","msg":"caught signal"}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["level"] != "INFO" || got["msg"] != "caught signal" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_JSON_NumberKeepsOriginalDigits(t *testing.T) {
	got, err := ParseStructured("json", `{"count":123456789012345678901234567890}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	want := "123456789012345678901234567890"
	if got["count"] != want {
		t.Errorf("count = %q, want %q (float64 rounding would corrupt this)", got["count"], want)
	}
}

func TestParseStructured_JSON_BooleanBecomesTrueFalseString(t *testing.T) {
	got, err := ParseStructured("json", `{"ok":true,"bad":false}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["ok"] != "true" || got["bad"] != "false" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_JSON_NullBecomesEmptyString(t *testing.T) {
	got, err := ParseStructured("json", `{"x":null}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["x"] != "" {
		t.Errorf("x = %q, want empty string", got["x"])
	}
}

func TestParseStructured_JSON_NestedObjectReencodedAsCompactJSON(t *testing.T) {
	got, err := ParseStructured("json", `{"listen":{"IP":"::","Port":3000,"Zone":""}}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	want := `{"IP":"::","Port":3000,"Zone":""}`
	if got["listen"] != want {
		t.Errorf("listen = %q, want %q", got["listen"], want)
	}
}

func TestParseStructured_JSON_ArrayReencodedAsCompactJSON(t *testing.T) {
	got, err := ParseStructured("json", `{"items":[1,"two",true]}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	want := `[1,"two",true]`
	if got["items"] != want {
		t.Errorf("items = %q, want %q", got["items"], want)
	}
}

func TestParseStructured_JSON_DuplicateKeyLastWins(t *testing.T) {
	got, err := ParseStructured("json", `{"a":"first","a":"second"}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["a"] != "second" {
		t.Errorf("a = %q, want %q (last value should win)", got["a"], "second")
	}
}

func TestParseStructured_JSON_TopLevelArrayIsError(t *testing.T) {
	_, err := ParseStructured("json", `[1,2,3]`)
	if err == nil {
		t.Error("expected error for top-level JSON array")
	}
}

func TestParseStructured_JSON_TopLevelScalarIsError(t *testing.T) {
	_, err := ParseStructured("json", `"just a string"`)
	if err == nil {
		t.Error("expected error for top-level JSON scalar")
	}
}

func TestParseStructured_JSON_TopLevelNullIsError(t *testing.T) {
	// Unmarshaling JSON null into a map target is Go's documented no-op
	// (leaves the map nil, err == nil) - ParseStructured must reject it
	// explicitly since null is not an object.
	_, err := ParseStructured("json", `null`)
	if err == nil {
		t.Error("expected error for top-level JSON null")
	}
}

func TestParseStructured_JSON_TrailingDataIsError(t *testing.T) {
	// json.Decoder.Decode only consumes one JSON value; anything left over
	// (a second value, or plain garbage) must not be silently ignored.
	_, err := ParseStructured("json", `{"a":"b"} garbage`)
	if err == nil {
		t.Error("expected error for trailing data after the top-level JSON value")
	}
}

func TestParseStructured_JSON_InvalidJSONIsError(t *testing.T) {
	_, err := ParseStructured("json", `{not valid`)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestParseStructured_JSON_EmptyInputIsError(t *testing.T) {
	_, err := ParseStructured("json", "")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseStructured_LTSV_TabSeparated(t *testing.T) {
	got, err := ParseStructured("ltsv", "host:example.com\tstatus:200\tmsg:hello world")
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["host"] != "example.com" || got["status"] != "200" || got["msg"] != "hello world" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_LTSV_ValueContainingColonSplitsOnFirstOnly(t *testing.T) {
	got, err := ParseStructured("ltsv", "url:http://example.com/path")
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["url"] != "http://example.com/path" {
		t.Errorf("url = %q, want %q", got["url"], "http://example.com/path")
	}
}

func TestParseStructured_LTSV_EmptyInputIsError(t *testing.T) {
	_, err := ParseStructured("ltsv", "")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseStructured_Logfmt_SpaceSeparated(t *testing.T) {
	got, err := ParseStructured("logfmt", "level=info pid=123")
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["level"] != "info" || got["pid"] != "123" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_Logfmt_QuotedValueWithSpaces(t *testing.T) {
	got, err := ParseStructured("logfmt", `level=info msg="hello world" pid=123`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["msg"] != "hello world" {
		t.Errorf("msg = %q, want %q", got["msg"], "hello world")
	}
	if got["level"] != "info" || got["pid"] != "123" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_Logfmt_EscapedQuoteInsideQuotedValue(t *testing.T) {
	got, err := ParseStructured("logfmt", `msg="say \"hi\" to me"`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	want := `say "hi" to me`
	if got["msg"] != want {
		t.Errorf("msg = %q, want %q", got["msg"], want)
	}
}

func TestParseStructured_Logfmt_UnterminatedQuoteIsError(t *testing.T) {
	_, err := ParseStructured("logfmt", `msg="unterminated`)
	if err == nil {
		t.Error("expected error for unterminated quoted value")
	}
}

func TestParseStructured_Logfmt_EmptyInputIsError(t *testing.T) {
	_, err := ParseStructured("logfmt", "")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseStructured_UnknownFormatIsError(t *testing.T) {
	_, err := ParseStructured("xml", "<a>1</a>")
	if err == nil {
		t.Error("expected error for unsupported format")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parse/... -run TestParseStructured -v`
Expected: コンパイルエラー(`ParseStructured undefined`)。

- [ ] **Step 3: Implement `ParseStructured`**

`internal/parse/structured.go`を新規作成:

```go
package parse

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseStructured parses raw (the captured substring named by a rule's
// Structured.Source) according to format ("json", "ltsv", or "logfmt") into
// a flat map of key to string value. Nested JSON objects/arrays are
// re-encoded as their own compact JSON string; JSON numbers keep their
// original textual digits (via json.Number, avoiding float64 formatting
// artifacts); JSON null becomes an empty string. LTSV/logfmt values are
// already flat strings and pass through unchanged. Returns an error if raw
// isn't valid for the given format.
func ParseStructured(format, raw string) (map[string]string, error) {
	switch format {
	case "json":
		return parseStructuredJSON(raw)
	case "ltsv":
		return parseStructuredLTSV(raw)
	case "logfmt":
		return parseStructuredLogfmt(raw)
	default:
		return nil, fmt.Errorf("unsupported structured format %q", format)
	}
}

func parseStructuredJSON(raw string) (map[string]string, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()

	var top map[string]any
	if err := dec.Decode(&top); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	if top == nil {
		// A top-level JSON `null` decodes into a nil map with err == nil
		// (Go's documented behavior for unmarshaling null into a map) - it
		// is not an object, so it must be rejected explicitly.
		return nil, fmt.Errorf("decode json: top-level value must be an object, got null")
	}
	if dec.More() {
		// Decoder.Decode only consumes one JSON value; trailing bytes
		// (garbage, or a second concatenated value) must be rejected too.
		return nil, fmt.Errorf("decode json: unexpected trailing data after top-level value")
	}

	result := make(map[string]string, len(top))
	for k, v := range top {
		s, err := jsonValueToString(v)
		if err != nil {
			return nil, fmt.Errorf("encode json field %q: %w", k, err)
		}
		result[k] = s
	}
	return result, nil
}

func jsonValueToString(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	case bool:
		if t {
			return "true", nil
		}
		return "false", nil
	default:
		// object or array: re-encode as compact JSON.
		compact, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(compact), nil
	}
}

func parseStructuredLTSV(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, fmt.Errorf("ltsv: empty input")
	}
	result := map[string]string{}
	for _, field := range strings.Split(raw, "\t") {
		key, value, found := strings.Cut(field, ":")
		if !found {
			continue
		}
		result[key] = value
	}
	return result, nil
}

func parseStructuredLogfmt(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, fmt.Errorf("logfmt: empty input")
	}
	result := map[string]string{}
	i, n := 0, len(raw)
	for i < n {
		for i < n && raw[i] == ' ' {
			i++
		}
		if i >= n {
			break
		}

		start := i
		for i < n && raw[i] != '=' && raw[i] != ' ' {
			i++
		}
		key := raw[start:i]
		if key == "" {
			return nil, fmt.Errorf("logfmt: unexpected %q at position %d", raw[i], i)
		}
		if i >= n || raw[i] != '=' {
			result[key] = ""
			continue
		}
		i++ // skip '='

		if i < n && raw[i] == '"' {
			i++
			var sb strings.Builder
			closed := false
			for i < n {
				c := raw[i]
				if c == '\\' && i+1 < n {
					sb.WriteByte(raw[i+1])
					i += 2
					continue
				}
				if c == '"' {
					closed = true
					i++
					break
				}
				sb.WriteByte(c)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("logfmt: unterminated quoted value for key %q", key)
			}
			result[key] = sb.String()
			continue
		}

		start = i
		for i < n && raw[i] != ' ' {
			i++
		}
		result[key] = raw[start:i]
	}
	return result, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/parse/... -run TestParseStructured -v`
Expected: PASS(全件)。

- [ ] **Step 5: Commit**

```bash
git add internal/parse/structured.go internal/parse/structured_test.go
git commit -m "feat(parse): add ParseStructured for json/ltsv/logfmt"
```

---

## Task 3: バリデーション(`internal/rules/validate.go`)

**Files:**
- Modify: `internal/rules/validate.go`
- Test: `internal/rules/validate_test.go`

**Interfaces:**
- Consumes: Task 1の`Rule.Structured`/`Field.Key`/`Field.Extra`。
- Produces: `Validate()`のエラーメッセージに構造化データ関連のケースを追加(戻り値の型・呼び出し方は変更なし)。

- [ ] **Step 1: Write the failing tests**

`internal/rules/validate_test.go`の末尾に追記:

```go
func TestValidate_StructuredWithInvalidFormatIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "xml"},
				Fields:     []Field{{Name: "json", Type: "string"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "xml") {
		t.Errorf("expected error mentioning unsupported structured format, got: %v", err)
	}
}

func TestValidate_StructuredSourceNotACaptureGroupIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "nope", Format: "json"},
				Fields:     []Field{{Name: "json", Type: "string"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error mentioning structured source %q, got: %v", "nope", err)
	}
}

func TestValidate_StructuredValidConfigPasses(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "ok",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields: []Field{
					{Name: "json", Type: "string"},
					{Name: "level", Type: "string", Key: "level"},
					{Name: "extra", Type: "string", Extra: true},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_KeyFieldWithoutStructuredIsError(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields:  []Field{{Name: "level", Type: "string", Key: "level"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "level") {
		t.Errorf("expected error mentioning field %q, got: %v", "level", err)
	}
}

func TestValidate_ExtraFieldWithoutStructuredIsError(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields:  []Field{{Name: "extra", Type: "string", Extra: true}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "extra") {
		t.Errorf("expected error mentioning field %q, got: %v", "extra", err)
	}
}

func TestValidate_KeyAndExtraBothSetIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields:     []Field{{Name: "weird", Type: "string", Key: "level", Extra: true}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "weird") {
		t.Errorf("expected error mentioning field %q, got: %v", "weird", err)
	}
}

func TestValidate_MultipleExtraFieldsIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields: []Field{
					{Name: "extra1", Type: "string", Extra: true},
					{Name: "extra2", Type: "string", Extra: true},
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Errorf("expected error about more than one extra field, got: %v", err)
	}
}

func TestValidate_KeyFieldDoesNotRequireMatchingCaptureGroup(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "ok",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields:     []Field{{Name: "level_field_name_not_a_capture_group", Type: "string", Key: "level"}},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rules/... -run TestValidate_Structured -v` および `go test ./internal/rules/... -run TestValidate_KeyFieldDoesNotRequireMatchingCaptureGroup -v`
Expected: FAIL(`StructuredConfig`は既にTask 1で追加済みなのでコンパイルは通るが、`level_field_name_not_a_capture_group`のような`Key`付きフィールドが既存の「capture groupと名前一致必須」チェックに引っかかってエラーになってしまい、`TestValidate_KeyFieldDoesNotRequireMatchingCaptureGroup`は「no error」を期待するのに失敗する。他の新規テストは「error発生」を期待するテストで、現状の`Validate()`はまだ構造化データ関連の検証を一切行わないため、こちらも失敗する)。

- [ ] **Step 3: Implement the validation rules**

`internal/rules/validate.go`を以下の内容に置き換える:

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

var allowedStructuredFormats = map[string]bool{
	"json":   true,
	"ltsv":   true,
	"logfmt": true,
}

// Validate checks all fail-fast startup invariants described in the design
// spec and returns a joined error listing every violation found.
func (c *Config) Validate() error {
	var errs []error
	firstFieldsByName := map[string][]Field{}

	for _, rule := range c.Rules {
		captureNames := map[string]bool{}
		for _, n := range rule.Regexp.SubexpNames() {
			if n != "" {
				captureNames[n] = true
			}
		}

		extraCount := 0
		for _, field := range rule.Fields {
			usesStructured := field.Key != "" || field.Extra
			if !usesStructured && !captureNames[field.Name] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has no matching named capture group in pattern", rule.Name, field.Name))
			}
			if !allowedTypes[field.Type] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has unsupported type %q", rule.Name, field.Name, field.Type))
			}
			if field.Type == "timestamp" && field.Format == "" {
				errs = append(errs, fmt.Errorf("rule %q: field %q is type timestamp but has no format", rule.Name, field.Name))
			}
			if field.Key != "" && field.Extra {
				errs = append(errs, fmt.Errorf("rule %q: field %q sets both key and extra", rule.Name, field.Name))
			}
			if usesStructured && rule.Structured == nil {
				errs = append(errs, fmt.Errorf("rule %q: field %q uses key/extra but the rule has no structured config", rule.Name, field.Name))
			}
			if field.Extra {
				extraCount++
			}
		}
		if extraCount > 1 {
			errs = append(errs, fmt.Errorf("rule %q: more than one field has extra: true (max 1 per rule)", rule.Name))
		}

		if rule.Structured != nil {
			if !allowedStructuredFormats[rule.Structured.Format] {
				errs = append(errs, fmt.Errorf("rule %q: structured format %q is not one of json/ltsv/logfmt", rule.Name, rule.Structured.Format))
			}
			if !captureNames[rule.Structured.Source] {
				errs = append(errs, fmt.Errorf("rule %q: structured source %q has no matching named capture group in pattern", rule.Name, rule.Structured.Source))
			}
		}

		if rule.ContinuationRegexp != nil {
			fieldNames := map[string]bool{}
			for _, field := range rule.Fields {
				fieldNames[field.Name] = true
			}
			for _, n := range rule.ContinuationRegexp.SubexpNames() {
				if n != "" && !fieldNames[n] {
					errs = append(errs, fmt.Errorf("rule %q: continuation pattern has named capture group %q with no matching declared field", rule.Name, n))
				}
			}
		}

		if existing, ok := firstFieldsByName[rule.Name]; ok {
			if !fieldsEqualForSchema(existing, rule.Fields) {
				errs = append(errs, fmt.Errorf("rule %q: multiple rules share this name but declare different fields (name+type, in the same order, must match exactly)", rule.Name))
			}
		} else {
			firstFieldsByName[rule.Name] = rule.Fields
		}
	}

	if err := c.Compression.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("compression: %w", err))
	}
	if err := c.RowGroup.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("row_group: %w", err))
	}

	return errors.Join(errs...)
}

// fieldsEqualForSchema compares two field sequences by name+type, in order
// (order now determines output column order, so two same-name rules
// declaring identical fields in a different order are still a conflict),
// ignoring Format and Normalize per the design's schema-consistency rule.
func fieldsEqualForSchema(a, b []Field) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Type != b[i].Type {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rules/... -v`
Expected: PASS(既存テストも含めて全件)。

- [ ] **Step 5: Commit**

```bash
git add internal/rules/validate.go internal/rules/validate_test.go
git commit -m "feat(rules): validate structured/key/extra invariants"
```

---

## Task 4: `Convert`の拡張(`internal/parse/match.go`)

**Files:**
- Modify: `internal/parse/match.go`
- Test: `internal/parse/match_test.go`

**Interfaces:**
- Consumes: Task 1の`Rule.Structured`/`Field.Key`/`Field.Extra`、Task 2の`ParseStructured(format, raw string) (map[string]string, error)`。
- Produces: `Convert`のシグネチャは変更なし(`Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error)`)。新規非公開ヘルパー`marshalUnconsumed(fields []rules.Field, structuredValues map[string]string) (string, error)`を追加。

- [ ] **Step 1: Write the failing tests**

`internal/parse/match_test.go`の末尾に追記:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parse/... -run TestConvert_KeyField -v`
Expected: FAIL(現状の`Convert`は`rule.Structured`/`field.Key`/`field.Extra`を一切見ないため、`level`/`message`/`extra`が期待通りの値にならない)。

- [ ] **Step 3: Implement the `Convert` extension**

`internal/parse/match.go`を以下の内容に置き換える:

```go
package parse

import (
	"encoding/json"
	"fmt"
	"time"

	"logidx/internal/rules"
)

// MatchRaw tries each rule's pattern against line and returns the first
// match's rule and raw (un-type-converted) captured field values, keyed by
// field name. No type conversion happens here - see Convert.
func MatchRaw(ruleList []rules.Rule, line string) (rule *rules.Rule, raw map[string]string, ok bool) {
	for i := range ruleList {
		r := &ruleList[i]
		m := r.Regexp.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		captured := map[string]string{}
		for j, groupName := range r.Regexp.SubexpNames() {
			if j == 0 || groupName == "" {
				continue
			}
			captured[groupName] = m[j]
		}
		return r, captured, true
	}
	return nil, nil, false
}

// Convert type-converts raw's captured values according to rule's field
// definitions. If rule.Structured is set, raw[rule.Structured.Source] is
// first parsed into a flat key/value map (see ParseStructured); fields with
// Key set pull their raw value from that map instead of from raw, and the
// field with Extra set (if any) receives a JSON object of every structured
// key not consumed by a Key field. Returns an error if any field fails
// conversion - callers treat that the same way a failed match is treated
// (write to unmatched).
func Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error) {
	var structuredValues map[string]string
	if rule.Structured != nil {
		structuredValues, err = ParseStructured(rule.Structured.Format, raw[rule.Structured.Source])
		if err != nil {
			return nil, fmt.Errorf("parse structured data: %w", err)
		}
	}

	var extraJSON string
	if structuredValues != nil {
		extraJSON, err = marshalUnconsumed(rule.Fields, structuredValues)
		if err != nil {
			return nil, fmt.Errorf("encode extra field: %w", err)
		}
	}

	converted := make(map[string]any, len(rule.Fields))
	for _, field := range rule.Fields {
		rawValue := raw[field.Name]
		switch {
		case field.Extra:
			rawValue = extraJSON
		case field.Key != "":
			rawValue = structuredValues[field.Key]
		}
		v, err := convertValue(rawValue, field, now)
		if err != nil {
			return nil, err
		}
		converted[field.Name] = v
	}
	return converted, nil
}

// marshalUnconsumed collects every key in structuredValues not consumed by
// a field's Key, and marshals the remainder as a JSON object. json.Marshal
// always sorts map keys, so the result is deterministic across runs.
func marshalUnconsumed(fields []rules.Field, structuredValues map[string]string) (string, error) {
	consumed := make(map[string]bool, len(fields))
	for _, f := range fields {
		if f.Key != "" {
			consumed[f.Key] = true
		}
	}

	remaining := make(map[string]string, len(structuredValues))
	for k, v := range structuredValues {
		if !consumed[k] {
			remaining[k] = v
		}
	}

	b, err := json.Marshal(remaining)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Match tries each rule in ruleList in order and returns the extracted,
// type-converted field values of the first rule whose pattern matches line.
// If that rule's pattern matches but any field fails type conversion, the
// line is treated as unmatched (ok=false) — there is no fallthrough to
// subsequent rules, since "first match" refers to the regex match, not to
// conversion success. Match is a thin wrapper of MatchRaw+Convert, kept for
// callers (and tests) that don't need the two-stage split.
func Match(ruleList []rules.Rule, line string, now time.Time) (name string, values map[string]any, ok bool) {
	rule, raw, matched := MatchRaw(ruleList, line)
	if !matched {
		return "", nil, false
	}
	values, err := Convert(*rule, raw, now)
	if err != nil {
		return "", nil, false
	}
	return rule.Name, values, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/parse/... -v`
Expected: PASS(既存テストも含めて全件)。

- [ ] **Step 5: Commit**

```bash
git add internal/parse/match.go internal/parse/match_test.go
git commit -m "feat(parse): wire structured data into Convert via key/extra"
```

---

## Task 5: End-to-endテスト(`cmd/logidx`)

**Files:**
- Modify: `cmd/logidx/main_test.go`

**Interfaces:**
- Consumes: Task 1〜4の全機能(rules.yamlの`structured:`/`key:`/`extra:`パース〜Parquet書き込みまでの完全なパイプライン)。
- 既存の`writeFile`ヘルパー(`cmd/logidx/main_test.go:27`)と`run(args []string, stdout, stderr io.Writer) int`を利用する。

- [ ] **Step 1: Write the failing test**

`cmd/logidx/main_test.go`の末尾(`TestRun_ImportAppliesReplaceRulesToFieldValues`の後)に追記:

```go
func TestRun_ImportExtractsStructuredJSONFieldsAndCollectsExtra(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: container_log
    pattern: '^(?P<time>\S+) (?P<host>\S+) (?P<tag>\S+) (?P<json>\{.*\})$'
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
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)

	logContent := `2026-08-04T23:26:39.247486+09:00 wtnb4 container/clc/137272bf8941[874] {"time":"2026-08-04T14:26:39.229216178Z","level":"INFO","msg":"caught signal","signal":15}
2026-08-04T23:26:47.661639+09:00 wtnb4 container/clc/131568006cb0[874] {"time":"2026-08-04T14:26:47.661294297Z","level":"INFO","msg":"server starting","listen":{"IP":"::","Port":3000,"Zone":""},"pid":1}
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
	code = run([]string{"dump", filepath.Join(outDir, "container_log.parquet"), "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 3 { // 1 header + 2 rows
		t.Fatalf("expected 3 dump lines (header + 2 rows), got %d: %q", len(lines), stdout.String())
	}

	var row0, row1 map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &row0); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[1], err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &row1); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[2], err)
	}

	if row0["host"] != "wtnb4" || row0["tag"] != "container/clc/137272bf8941[874]" {
		t.Errorf("row0 host/tag = %v/%v, want wtnb4/container/clc/137272bf8941[874]", row0["host"], row0["tag"])
	}
	if row0["level"] != "INFO" || row0["message"] != "caught signal" {
		t.Errorf("row0 level/message = %v/%v, want INFO/caught signal", row0["level"], row0["message"])
	}
	wantExtra0 := `{"signal":"15"}`
	if row0["extra"] != wantExtra0 {
		t.Errorf("row0 extra = %v, want %q", row0["extra"], wantExtra0)
	}
	if eventTime0, _ := row0["event_time"].(string); !strings.HasPrefix(eventTime0, "2026-08-04T14:26:39") {
		t.Errorf("row0 event_time = %v, want prefix 2026-08-04T14:26:39", row0["event_time"])
	}

	if row1["level"] != "INFO" || row1["message"] != "server starting" {
		t.Errorf("row1 level/message = %v/%v, want INFO/server starting", row1["level"], row1["message"])
	}
	wantExtra1 := `{"listen":"{\"IP\":\"::\",\"Port\":3000,\"Zone\":\"\"}","pid":"1"}`
	if row1["extra"] != wantExtra1 {
		t.Errorf("row1 extra = %v, want %q", row1["extra"], wantExtra1)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/logidx/... -run TestRun_ImportExtractsStructuredJSONFieldsAndCollectsExtra -v`
Expected: FAIL(Task 1〜4を先に完了していれば実装自体は揃っているはずなので、このテストはこの時点で既にPASSしている可能性が高い。もしFAILする場合は、Task 1〜4のいずれかの実装漏れがないか確認すること — このタスクはあくまで結合の確認であり、新しいプロダクションコードは書かない)。

- [ ] **Step 3: (実装コードの追加なし)Confirm production code needs no changes**

Task 1〜4がすべて正しく実装されていれば、`cmd/logidx`側の変更は不要。Step 2で失敗した場合のみ、失敗内容に応じてTask 1〜4に立ち戻って修正する。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/logidx/... -run TestRun_ImportExtractsStructuredJSONFieldsAndCollectsExtra -v`
Expected: PASS。

- [ ] **Step 5: Run the full test suite**

Run: `go test ./...`
Expected: PASS(全パッケージ)。

- [ ] **Step 6: Commit**

```bash
git add cmd/logidx/main_test.go
git commit -m "test: add end-to-end coverage for structured JSON field extraction"
```

---

## Task 6: README.md ドキュメント追記

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 1〜5で確定した挙動(`structured:`/`key:`/`extra:`のYAML構文と実際の変換結果)。

- [ ] **Step 1: Add the documentation section**

`README.md`の「### フィールド値の変換(`normalize:`/`replace:`)」節(76〜97行目)の直後、「### 圧縮設定」節の直前に、以下のセクションを挿入する:

```markdown
### 構造化データの部分パース(`structured:`/`key:`/`extra:`)

ログ行の一部がJSON/LTSV/logfmtになっているケース(例: 行末尾がJSONのコンテナログ)向けに、`pattern`の名前付きキャプチャグループで切り出した生テキストをさらにキー名でパースし、フィールドにマッピングできる。

```yaml
rules:
  - name: container_log
    pattern: '^(?P<time>\S+) (?P<host>\S+) (?P<tag>\S+) (?P<json>\{.*\})$'
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
```

- `structured.source`は、構造化データを含む名前付きキャプチャグループの名前(上記例では`json`)。`structured.format`は`json`/`ltsv`/`logfmt`のいずれか。1ルールにつき`structured:`は最大1個。
- `fields:`の各フィールドに`key:`を設定すると、構造化データの当該キーの値を使う。フィールド名とキー名が一致していなくてよい(上記例の`event_time`は、行先頭のタイムスタンプとは別物であるJSON側の`time`キーから値を取る)。
- `extra: true`を設定したフィールドは、`key:`で消費されなかった構造化データのキーをすべて集めてJSON文字列として格納する。1ルールにつき最大1個。
- `key:`/`extra:`のどちらも設定しないフィールドは、従来通り`pattern`の同名キャプチャグループから値を取る(既存ルールは無変更で動作する)。
- `structured.source`で指定したキャプチャグループ自体を`fields:`に列挙する必要はない。生の構造化データテキストをそのまま1列として残したい場合は、`key:`なしの通常フィールドとして追加すればよい(両立可能。上記例の`json`のように)。
- **残りカラム(`extra:`)の値は常に「キー→文字列」のJSONになる**: 元の構造化データがJSONの数値・真偽値であっても、残りカラムでは文字列としてクォートされる(例: 未マッピングの`signal`キーが元は`15`という数値でも、残りカラムでは`{"signal":"15"}`になる)。JSON/LTSV/logfmtを同じ土俵で一貫して扱うためのトレードオフ。
- 構造化データのネストしたオブジェクト・配列は、その部分をまるごとコンパクトなJSON文字列として1つの値にする(ネストしたキーパスの個別指定は非対応)。
- 構造化データのパース失敗(壊れたJSON、トップレベルがオブジェクトでないJSON、空文字など)は、既存の「型変換失敗」と同じ扱いで`unmatched.txt`に書かれる。`key:`で指定したキーが実際のログ行に存在しない場合は空文字列として扱われる(型が`string`ならそのまま空文字、`int`/`timestamp`なら型変換失敗でunmatchedになる)。
```

- [ ] **Step 2: Verify the addition renders as intended**

Run: `rg -n "structured:" README.md`
Expected: 新しいセクション見出しと本文中の`structured:`への言及がヒットする(手作業でのtypoチェック代わり)。

- [ ] **Step 3: Run the full verification suite**

Run: `task fmt && task lint && task test && task build`
Expected: すべて成功(`gofmt`差分なし、`golangci-lint`エラーなし、全テストPASS、ビルド成功)。

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document structured:/key:/extra: field mapping"
```
