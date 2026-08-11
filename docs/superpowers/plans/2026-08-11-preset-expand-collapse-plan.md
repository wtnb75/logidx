# `preset:`展開・圧縮サブコマンド(`expand`/`collapse`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ルールの`preset: <名前>`を`pattern:`/`fields:`に展開する`logidx expand`と、手書きの`pattern:`/`fields:`がプリセット定義と完全一致する場合に`preset: <名前>`へ圧縮する`logidx collapse`を追加する。

**Architecture:** `internal/rules`パッケージ内に新規`convert.go`を置き、`yaml.Node`ツリーを直接書き換えることで変換対象以外のコメント・キー順・インデントを保持する(`gopkg.in/yaml.v3`のNode API)。既存`Load(path)`の本体を`loadConfig(data []byte) (*Config, error)`に切り出し(挙動は変えない)、`Expand`/`Collapse`はどちらも変換後のYAMLを最後に`loadConfig`へ通して壊れていないことを検証してから返す。`collapse`の一致判定は`regexp/syntax.Parse(...).String()`による正規化文字列同士の比較を使う — 詳細はGlobal Constraintsを参照。`cmd/logidx/main.go`は`dump`/`restore`と同じ`src`/`dst`(`-`でstdin/stdout)規約のCLI層を追加するだけで、変換ロジックには一切関与しない。

**Tech Stack:** Go 1.25、`gopkg.in/yaml.v3`(既存依存、追加なし)、標準ライブラリ`regexp`/`regexp/syntax`。

## Global Constraints

- 新規外部依存は追加しない。`convert.go`は`internal/rules`パッケージ内に置く(`presetRegistry`/`Field`など非公開の型に直接アクセスするため、新パッケージには分離しない)。
- CLI引数の個数が2個でない場合は使用方法をstderrに出し`exitCodeError{2}`。それ以外の失敗は`exitCodeError{1}`。`cmd/logidx`の既存コマンドと同じ`SilenceUsage: true`/`SilenceErrors: true`を使う。
- `src`/`dst`は`dump`/`restore`と同じ規約: `src`に`-`で標準入力(`os.Stdin`)から読み、`dst`に`-`で標準出力(`run()`に渡された`stdout io.Writer`— `os.Stdout`を直接書かない。`newDumpCmd`と同じテスト容易性のパターン)に書く。
- `--log-format`/`-v|--verbose`は`import`と同じ意味・実装(`logging.New(stderr, logFormat, verbose)`)。
- 完了時、`logger.Info("expanded rules", "count", n)` / `logger.Info("collapsed rules", "count", n)`を出す。対象0件でも同じログを出して正常終了(exit 0)。
- `--in-place`/`-w`フラグは追加しない(`<src> <dst>`の位置引数のみ。同一パスを指定すればインプレースと同等)。
- unknown presetのエラーメッセージは`Validate`と同じ形式`rule %q: unknown preset %q`を再利用する。
- **正規化の実装詳細(設計時に検証済み・要注意点)**: Go標準の`regexp.Regexp.String()`は`re.expr`(コンパイル前のソース文字列)をそのまま返すだけで、実際には一切正規化しない(stdlibのdoc comment "String returns the source text used to compile the regular expression" の通り)。そのため`normalizePattern`は`regexp.Compile`ではなく`regexp/syntax.Parse(pattern, syntax.Perl)`を使う — こちらはASTベースで実際に正規化する(例: `\/foo` → `/foo`、文字クラスの範囲の並び替えなど)。出力形式は人間が書くような形にはならない(`^` → `\A`、`(?-m:...)`でラップされる等)が、比較にしか使わないので問題ない — **collapseの出力にpatternのテキストが書かれることはない**(`preset: <name>`に置き換わるだけ)し、expandの出力は常に`presetRegistry`のPattern文字列そのもの(正規化とは無関係)なので、正規化後の奇妙な見た目の文字列が最終出力に混ざることはない。
- `yaml.Marshal(&doc)`(package-level関数)はデフォルトで4スペースインデントを使うため、未変更のルールも含めて全行が再インデントされてしまう(実際に確認済み)。必ず`yaml.NewEncoder(w)` + `enc.SetIndent(2)`を使うこと(このリポジトリのrules.yamlは2スペースインデント規約 — `internal/rules/rules_test.go`の`sampleRulesYAML`参照)。この設定で変更対象外のノードはbyte-for-byte同一の出力になることを確認済み。

---

## File Structure

- Modify: `internal/rules/rules.go` — `Load`の本体を`loadConfig(data []byte) (*Config, error)`に切り出し、`Load`はファイル読み込み+`loadConfig`呼び出しのラッパーにする。
- Create: `internal/rules/convert.go` — `Expand`/`Collapse`と、それを支える非公開ヘルパー(`normalizePattern`/`marshalDoc`/`findKeyIndex`/`findRulesSequence`/`sortedPresetNames`/`fieldsEqual`/`encodeFieldNode`など)。
- Create: `internal/rules/convert_test.go` — 上記すべての単体テスト。
- Modify: `cmd/logidx/main.go` — `newExpandCmd`/`newCollapseCmd`(共通実装`newConvertCmd`)を追加し、`root.AddCommand`に登録。
- Modify: `cmd/logidx/main_test.go` — `expand`/`collapse`サブコマンドの基本動作テストを追加。
- Modify: `README.md` — `expand`/`collapse`の使い方セクションを追加。

---

## Task 1: `Load`を`loadConfig(data []byte)`に分割する

**Files:**
- Modify: `internal/rules/rules.go:203-292`(`Load`関数全体)

**Interfaces:**
- Produces: `loadConfig(data []byte) (*Config, error)` — Task 4/5の`Expand`/`Collapse`がこれを直接呼ぶ(ファイルI/Oなしで、メモリ上のYAMLバイト列をロード・検証するため)。

この変更は挙動を一切変えないリファクタリング(責務分割のみ)なので、新規のred/greenテストサイクルではなく既存テストスイートの前後比較で検証する。

- [ ] **Step 1: 既存テストスイートがgreenであることを確認する(ベースライン)**

Run: `go test ./...`
Expected: PASS(全パッケージ)

- [ ] **Step 2: `Load`を`loadConfig`に分割する**

`internal/rules/rules.go`の`Load`関数(203〜292行目)を以下に置き換える:

```go
// loadConfig parses, compiles, and validates a rules YAML document already
// read into memory. Load is a thin wrapper that reads the file and calls
// this directly; Expand/Collapse (see convert.go) call it on YAML they've
// rewritten in memory, without touching disk.
func loadConfig(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse rules YAML: %w", err)
	}

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

		re, err := regexp.Compile(cfg.Rules[i].Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q: compile pattern: %w", cfg.Rules[i].Name, err)
		}
		cfg.Rules[i].Regexp = re

		if cfg.Rules[i].Continuation != "" {
			cre, err := regexp.Compile(cfg.Rules[i].Continuation)
			if err != nil {
				return nil, fmt.Errorf("rule %q: compile continuation pattern: %w", cfg.Rules[i].Name, err)
			}
			cfg.Rules[i].ContinuationRegexp = cre
		}

		for fi := range cfg.Rules[i].Fields {
			field := &cfg.Rules[i].Fields[fi]
			for j := range field.Replace {
				rre, err := regexp.Compile(field.Replace[j].Pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: compile replace pattern: %w", cfg.Rules[i].Name, field.Name, err)
				}
				field.Replace[j].Regexp = rre
			}

			for j := range field.Normalize {
				nre, err := regexp.Compile(field.Normalize[j].Pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: compile normalize pattern: %w", cfg.Rules[i].Name, field.Name, err)
				}
				field.Normalize[j].Regexp = nre
			}

			if field.Type == "timestamp" {
				tf, err := ResolveFormat(field.Format)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: %w", cfg.Rules[i].Name, field.Name, err)
				}
				field.ResolvedFormat = tf
			}
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Load reads, parses, compiles, and validates a rules YAML file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules file: %w", err)
	}
	return loadConfig(data)
}
```

- [ ] **Step 3: テストスイートが引き続きgreenであることを確認する**

Run: `go test ./...`
Expected: PASS(全パッケージ、Step 1と同じ結果 — 挙動が変わっていないことの確認)

- [ ] **Step 4: コミット**

```bash
git add internal/rules/rules.go
git commit -m "refactor(rules): extract loadConfig from Load for in-memory YAML reuse"
```

---

## Task 2: `convert.go`の土台 — ノード操作・正規化ヘルパー

**Files:**
- Create: `internal/rules/convert.go`
- Create: `internal/rules/convert_test.go`

**Interfaces:**
- Produces:
  - `findKeyIndex(mapping *yaml.Node, key string) int` — Task 4/5が`pattern`/`fields`/`preset`/`name`キーの位置を探すのに使う。
  - `findRulesSequence(doc *yaml.Node) *yaml.Node` — `rules:`キーの値ノード(`SequenceNode`)を返す。`rules:`キーが無ければ`nil`。
  - `marshalDoc(doc *yaml.Node) ([]byte, error)` — 2スペースインデントでの再シリアライズ(Task 4/5が最終出力に使う)。
  - `normalizePattern(pattern string) (string, error)` — Task 5(collapse)の比較に使う正規化文字列を返す。
  - `sortedPresetNames() []string` — Task 5がプリセットを決定的な順序で走査するのに使う。

- [ ] **Step 1: 失敗するテストを書く**

`internal/rules/convert_test.go`を新規作成:

```go
package rules

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizePattern_CollapsesRedundantEscape(t *testing.T) {
	a, err := normalizePattern(`^(?P<a>\/foo)$`)
	if err != nil {
		t.Fatalf("normalizePattern returned error: %v", err)
	}
	b, err := normalizePattern(`^(?P<a>/foo)$`)
	if err != nil {
		t.Fatalf("normalizePattern returned error: %v", err)
	}
	if a != b {
		t.Errorf("normalizePattern(%q) = %q, normalizePattern(%q) = %q, want equal", `^(?P<a>\/foo)$`, a, `^(?P<a>/foo)$`, b)
	}
}

func TestNormalizePattern_InvalidPatternIsError(t *testing.T) {
	if _, err := normalizePattern("(unclosed"); err == nil {
		t.Fatal("expected error for invalid pattern")
	}
}

func TestFindKeyIndex_FindsKeyAndReportsMissing(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("name: app_log\npattern: 'x'\n"), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	mapping := doc.Content[0]

	if idx := findKeyIndex(mapping, "pattern"); idx != 2 {
		t.Errorf("findKeyIndex(pattern) = %d, want 2", idx)
	}
	if idx := findKeyIndex(mapping, "fields"); idx != -1 {
		t.Errorf("findKeyIndex(fields) = %d, want -1", idx)
	}
}

func TestFindRulesSequence_NoRulesKeyReturnsNil(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("compression:\n  codec: zstd\n"), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if seq := findRulesSequence(&doc); seq != nil {
		t.Errorf("findRulesSequence = %v, want nil", seq)
	}
}

func TestFindRulesSequence_ReturnsSequenceNode(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("rules:\n  - name: a\n"), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	seq := findRulesSequence(&doc)
	if seq == nil {
		t.Fatal("findRulesSequence returned nil")
	}
	if seq.Kind != yaml.SequenceNode {
		t.Errorf("seq.Kind = %v, want SequenceNode", seq.Kind)
	}
	if len(seq.Content) != 1 {
		t.Errorf("len(seq.Content) = %d, want 1", len(seq.Content))
	}
}

func TestSortedPresetNames_ReturnsAllFourSorted(t *testing.T) {
	got := sortedPresetNames()
	want := []string{"apache_clf", "apache_combined", "syslog_rfc3164", "syslog_rfc5424"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMarshalDoc_PreservesTwoSpaceIndentAndComments(t *testing.T) {
	src := `# top comment
rules:
  - name: app_log # inline comment
    pattern: '^\[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      level: string
      message: string
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	out, err := marshalDoc(&doc)
	if err != nil {
		t.Fatalf("marshalDoc returned error: %v", err)
	}
	if string(out) != src {
		t.Errorf("marshalDoc round-trip changed output:\nwant:\n%s\ngot:\n%s", src, out)
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/rules/... -run 'TestNormalizePattern|TestFindKeyIndex|TestFindRulesSequence|TestSortedPresetNames|TestMarshalDoc' -v`
Expected: FAIL — `undefined: normalizePattern` (以下同様、convert.goが存在しないため)

- [ ] **Step 3: `convert.go`を作成しヘルパーを実装する**

`internal/rules/convert.go`を新規作成:

```go
package rules

import (
	"bytes"
	"regexp/syntax"
	"slices"

	"gopkg.in/yaml.v3"
)

// findKeyIndex returns the flat Content index of key's key node within
// mapping (a yaml.Node of Kind MappingNode), or -1 if mapping has no such
// key. mapping.Content alternates key, value, key, value... in document
// order regardless of key name, so the paired value node is always at the
// returned index + 1.
func findKeyIndex(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// findRulesSequence returns the `rules:` sequence node of a parsed rules
// YAML document, or nil if the document has no rules: key (a valid,
// rules-less config with nothing to convert) or no content at all (an
// empty document).
func findRulesSequence(doc *yaml.Node) *yaml.Node {
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	idx := findKeyIndex(root, "rules")
	if idx < 0 {
		return nil
	}
	return root.Content[idx+1]
}

// marshalDoc re-serializes doc with 2-space indentation, matching this
// repo's rules.yaml convention (see sampleRulesYAML in rules_test.go).
// Plain yaml.Marshal defaults to 4-space indent, which would silently
// reindent every line of an unmodified rules.yaml - this is why
// Expand/Collapse don't call it directly.
func marshalDoc(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// normalizePattern parses pattern with regexp/syntax and returns the
// canonical form of its parse tree (e.g. "\/" -> "/", reordered
// character-class ranges). Plain regexp.Regexp.String() can't be used for
// this: per its doc comment it "returns the source text used to compile
// the regular expression" - i.e. the verbatim input - so two source
// strings that compile to the same regexp but are spelled differently
// would never compare equal. The canonical form isn't meant to be read by
// humans (^ becomes \A, the whole thing gets wrapped in (?-m:...)) and is
// only ever used for equality checks in Collapse - see matchingPreset.
func normalizePattern(pattern string) (string, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", err
	}
	return re.String(), nil
}

// sortedPresetNames returns presetRegistry's keys sorted, so Collapse
// scans presets in a deterministic order when deciding which preset a rule
// matches (see the design doc's note on multiple matches).
func sortedPresetNames() []string {
	names := make([]string, 0, len(presetRegistry))
	for name := range presetRegistry {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
```

- [ ] **Step 4: テストを実行してパスを確認する**

Run: `go test ./internal/rules/... -run 'TestNormalizePattern|TestFindKeyIndex|TestFindRulesSequence|TestSortedPresetNames|TestMarshalDoc' -v`
Expected: PASS(5テストすべて)

- [ ] **Step 5: コミット**

```bash
git add internal/rules/convert.go internal/rules/convert_test.go
git commit -m "feat(rules): add convert.go node/normalization helpers for expand/collapse"
```

---

## Task 3: フィールドの比較・エンコードヘルパー

**Files:**
- Modify: `internal/rules/convert.go`(追記)
- Modify: `internal/rules/convert_test.go`(追記)

**Interfaces:**
- Consumes: `Field`/`ReplaceRule`/`NormalizeRule`(`internal/rules/rules.go`)
- Produces:
  - `fieldsEqual(a, b []Field) bool` — Task 5(collapse)がルールとプリセットのフィールド完全一致判定に使う。
  - `encodeFieldsNode(fields []Field) *yaml.Node` — Task 4(expand)が`fields:`の値ノードを組み立てるのに使う。

- [ ] **Step 1: 失敗するテストを書く**

`internal/rules/convert_test.go`の末尾に追加:

```go
func TestFieldsEqual_IdenticalFieldsMatch(t *testing.T) {
	a := []Field{{Name: "status", Type: "int"}, {Name: "time", Type: "timestamp", Format: "clf"}}
	b := []Field{{Name: "status", Type: "int"}, {Name: "time", Type: "timestamp", Format: "clf"}}
	if !fieldsEqual(a, b) {
		t.Error("expected identical fields to be equal")
	}
}

func TestFieldsEqual_DifferentTypeDoesNotMatch(t *testing.T) {
	a := []Field{{Name: "status", Type: "int"}}
	b := []Field{{Name: "status", Type: "string"}}
	if fieldsEqual(a, b) {
		t.Error("expected fields with different Type to not be equal")
	}
}

func TestFieldsEqual_DifferentOrderDoesNotMatch(t *testing.T) {
	a := []Field{{Name: "a", Type: "string"}, {Name: "b", Type: "string"}}
	b := []Field{{Name: "b", Type: "string"}, {Name: "a", Type: "string"}}
	if fieldsEqual(a, b) {
		t.Error("expected fields in a different order to not be equal")
	}
}

func TestFieldsEqual_ExtraNormalizeEntryDoesNotMatch(t *testing.T) {
	a := []Field{{Name: "level", Type: "string", Normalize: []NormalizeRule{{Pattern: "(?i)^warn$", Value: "WARN"}}}}
	b := []Field{{Name: "level", Type: "string"}}
	if fieldsEqual(a, b) {
		t.Error("expected fields with different Normalize entries to not be equal")
	}
}

func TestEncodeFieldsNode_ShorthandForPlainTypeField(t *testing.T) {
	fields := []Field{{Name: "status", Type: "int"}}
	node := encodeFieldsNode(fields)
	if len(node.Content) != 2 {
		t.Fatalf("len(node.Content) = %d, want 2 (one key/value pair)", len(node.Content))
	}
	valueNode := node.Content[1]
	if valueNode.Kind != yaml.ScalarNode {
		t.Errorf("value node Kind = %v, want ScalarNode (shorthand)", valueNode.Kind)
	}
	if valueNode.Value != "int" {
		t.Errorf("value node Value = %q, want %q", valueNode.Value, "int")
	}
}

func TestEncodeFieldsNode_FullMappingForFieldWithFormat(t *testing.T) {
	fields := []Field{{Name: "time", Type: "timestamp", Format: "clf"}}
	node := encodeFieldsNode(fields)
	valueNode := node.Content[1]
	if valueNode.Kind != yaml.MappingNode {
		t.Fatalf("value node Kind = %v, want MappingNode (full form)", valueNode.Kind)
	}
	if idx := findKeyIndex(valueNode, "format"); idx < 0 || valueNode.Content[idx+1].Value != "clf" {
		t.Errorf("expected format: clf in encoded field, got node with content %+v", valueNode.Content)
	}
}

func TestEncodeFieldsNode_RoundTripsKeyExtraReplaceNormalize(t *testing.T) {
	original := []Field{
		{
			Name: "extra",
			Type: "string",
			Key:  "level",
			Replace: []ReplaceRule{
				{Pattern: `\s+`, Replacement: " "},
			},
			Normalize: []NormalizeRule{
				{Pattern: "(?i)^warn$", Value: "WARN"},
			},
		},
		{Name: "raw", Type: "string", Extra: true},
	}

	node := encodeFieldsNode(original)
	out, err := marshalDoc(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{node}})
	if err != nil {
		t.Fatalf("marshalDoc returned error: %v", err)
	}

	var rule Rule
	ruleSrc := "name: r\nfields:\n"
	for _, line := range bytesSplitLinesIndent(out) {
		ruleSrc += "  " + line + "\n"
	}
	if err := yaml.Unmarshal([]byte(ruleSrc), &rule); err != nil {
		t.Fatalf("yaml.Unmarshal(ruleSrc): %v\n---\n%s", err, ruleSrc)
	}

	if !fieldsEqual(rule.Fields, original) {
		t.Errorf("round-tripped fields = %+v, want %+v", rule.Fields, original)
	}
}

// bytesSplitLinesIndent splits s (already a valid, newline-terminated YAML
// mapping's byte output) into its non-empty lines, for re-indenting it
// under a synthetic wrapper key in a test.
func bytesSplitLinesIndent(s []byte) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(s), "\n"), "\n") {
		lines = append(lines, line)
	}
	return lines
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/rules/... -run 'TestFieldsEqual|TestEncodeFieldsNode' -v`
Expected: FAIL — `undefined: fieldsEqual` / `undefined: encodeFieldsNode`

`internal/rules/convert_test.go`の先頭に`"strings"`のimportを追加すること(`bytesSplitLinesIndent`が使う)。

- [ ] **Step 3: 比較・エンコードヘルパーを実装する**

`internal/rules/convert.go`の末尾に追加:

```go
// fieldsEqual reports whether a and b are identical for every attribute a
// preset definition can set (Name, Type, Format, Key, Extra, Replace,
// Normalize), element-for-element in order. Deliberately excludes Meta,
// ResolvedFormat, and the compiled Regexp inside Replace/Normalize
// entries: Meta is never set by a preset definition (see the design doc's
// collapse section), and the other two are derived at Load time, not part
// of the YAML declaration being compared.
func fieldsEqual(a, b []Field) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			a[i].Type != b[i].Type ||
			a[i].Format != b[i].Format ||
			a[i].Key != b[i].Key ||
			a[i].Extra != b[i].Extra {
			return false
		}
		if !replaceRulesEqual(a[i].Replace, b[i].Replace) {
			return false
		}
		if !normalizeRulesEqual(a[i].Normalize, b[i].Normalize) {
			return false
		}
	}
	return true
}

func replaceRulesEqual(a, b []ReplaceRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Pattern != b[i].Pattern || a[i].Replacement != b[i].Replacement {
			return false
		}
	}
	return true
}

func normalizeRulesEqual(a, b []NormalizeRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Pattern != b[i].Pattern || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

func boolNode(value bool) *yaml.Node {
	v := "false"
	if value {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

func appendKV(mapping *yaml.Node, key string, value *yaml.Node) {
	mapping.Content = append(mapping.Content, scalarNode(key), value)
}

// fieldUsesOnlyShorthand reports whether f can be written as the `name:
// type` scalar shorthand: none of the attributes that force the full
// mapping form are set. Mirrors Field.UnmarshalYAML's two accepted forms
// (rules.go) in reverse.
func fieldUsesOnlyShorthand(f Field) bool {
	return f.Format == "" && f.Key == "" && !f.Extra && len(f.Replace) == 0 && len(f.Normalize) == 0
}

// encodeFieldNode renders f as a yaml.Node: the shorthand scalar form when
// possible, otherwise the full mapping form listing only the attributes f
// actually sets. Generic over every Field attribute so it keeps working if
// a future preset uses replace/normalize/key/extra, even though today's
// presets only use type/format.
func encodeFieldNode(f Field) *yaml.Node {
	if fieldUsesOnlyShorthand(f) {
		return scalarNode(f.Type)
	}

	mapping := &yaml.Node{Kind: yaml.MappingNode}
	appendKV(mapping, "type", scalarNode(f.Type))
	if f.Format != "" {
		appendKV(mapping, "format", scalarNode(f.Format))
	}
	if f.Key != "" {
		appendKV(mapping, "key", scalarNode(f.Key))
	}
	if f.Extra {
		appendKV(mapping, "extra", boolNode(true))
	}
	if len(f.Replace) > 0 {
		appendKV(mapping, "replace", encodeReplaceRulesNode(f.Replace))
	}
	if len(f.Normalize) > 0 {
		appendKV(mapping, "normalize", encodeNormalizeRulesNode(f.Normalize))
	}
	return mapping
}

func encodeReplaceRulesNode(rules []ReplaceRule) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, r := range rules {
		entry := &yaml.Node{Kind: yaml.MappingNode}
		appendKV(entry, "pattern", scalarNode(r.Pattern))
		appendKV(entry, "value", scalarNode(r.Replacement))
		seq.Content = append(seq.Content, entry)
	}
	return seq
}

func encodeNormalizeRulesNode(rules []NormalizeRule) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, r := range rules {
		entry := &yaml.Node{Kind: yaml.MappingNode}
		appendKV(entry, "pattern", scalarNode(r.Pattern))
		appendKV(entry, "value", scalarNode(r.Value))
		seq.Content = append(seq.Content, entry)
	}
	return seq
}

// encodeFieldsNode renders fields as a `fields:` mapping node's value, in
// declaration order.
func encodeFieldsNode(fields []Field) *yaml.Node {
	mapping := &yaml.Node{Kind: yaml.MappingNode}
	for _, f := range fields {
		appendKV(mapping, f.Name, encodeFieldNode(f))
	}
	return mapping
}
```

- [ ] **Step 4: テストを実行してパスを確認する**

Run: `go test ./internal/rules/... -run 'TestFieldsEqual|TestEncodeFieldsNode' -v`
Expected: PASS(全テスト)

- [ ] **Step 5: `gofmt`とテストスイート全体を確認する**

Run: `gofmt -l internal/rules/convert.go internal/rules/convert_test.go`
Expected: 出力なし(整形済み)

Run: `go test ./internal/rules/...`
Expected: PASS

- [ ] **Step 6: コミット**

```bash
git add internal/rules/convert.go internal/rules/convert_test.go
git commit -m "feat(rules): add field equality and YAML encoding helpers for expand/collapse"
```

---

## Task 4: `Expand`の実装

**Files:**
- Modify: `internal/rules/convert.go`(追記)
- Modify: `internal/rules/convert_test.go`(追記)

**Interfaces:**
- Consumes: `findKeyIndex`/`findRulesSequence`/`marshalDoc`/`encodeFieldsNode`(Task 2/3)、`presetRegistry`/`loadConfig`(既存)
- Produces: `Expand(data []byte) (out []byte, count int, err error)` — Task 6のCLI層(`newExpandCmd`)が呼ぶ。

- [ ] **Step 1: 失敗するテストを書く**

`internal/rules/convert_test.go`の末尾に追加:

```go
func TestExpand_AllPresetsExpandCorrectly(t *testing.T) {
	for _, name := range sortedPresetNames() {
		t.Run(name, func(t *testing.T) {
			input := []byte("rules:\n  - name: r\n    preset: " + name + "\n")
			out, count, err := Expand(input)
			if err != nil {
				t.Fatalf("Expand returned error: %v", err)
			}
			if count != 1 {
				t.Fatalf("count = %d, want 1", count)
			}

			cfg, err := loadConfig(out)
			if err != nil {
				t.Fatalf("loadConfig(expanded) returned error: %v\n---\n%s", err, out)
			}
			rule := cfg.Rules[0]
			if rule.Preset != "" {
				t.Errorf("expanded rule still has Preset = %q, want empty", rule.Preset)
			}
			want := presetRegistry[name]
			if rule.Pattern != want.Pattern {
				t.Errorf("Pattern = %q, want %q", rule.Pattern, want.Pattern)
			}
			if !fieldsEqual(rule.Fields, want.Fields) {
				t.Errorf("Fields = %+v, want %+v", rule.Fields, want.Fields)
			}
			if strings.Contains(string(out), "preset:") {
				t.Errorf("expanded output still contains \"preset:\":\n%s", out)
			}
		})
	}
}

func TestExpand_UnknownPresetIsError(t *testing.T) {
	input := []byte("rules:\n  - name: access_log\n    preset: no_such_preset\n")
	_, _, err := Expand(input)
	if err == nil {
		t.Fatal("expected error for unknown preset")
	}
	want := `rule "access_log": unknown preset "no_such_preset"`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestExpand_NonPresetRulesAndCommentsUnchanged(t *testing.T) {
	input := []byte(`# top comment
rules:
  - name: app_log # inline comment
    pattern: '^\[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      level: string
      message: string
    continuation: '^  (?P<message>.*)$'
`)
	out, count, err := Expand(input)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	for _, want := range []string{"# top comment", "# inline comment", "continuation:"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("expanded output missing %q:\n%s", want, out)
		}
	}
}

func TestExpand_PresetHeadCommentMovesToPatternKey(t *testing.T) {
	input := []byte("rules:\n  - name: access_log\n    # which format this is\n    preset: apache_clf\n")
	out, _, err := Expand(input)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if !strings.Contains(string(out), "# which format this is\n    pattern:") {
		t.Errorf("expected head comment to move to the pattern key, got:\n%s", out)
	}
}

func TestExpand_EmptyInputIsNoop(t *testing.T) {
	out, count, err := Expand(nil)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if len(out) != 0 {
		t.Errorf("out = %q, want empty", out)
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/rules/... -run 'TestExpand' -v`
Expected: FAIL — `undefined: Expand`

- [ ] **Step 3: `Expand`を実装する**

`internal/rules/convert.go`の末尾に追加:

```go
// Expand rewrites every rule's `preset:` into the pattern/fields it names,
// leaving everything else in data byte-for-byte where possible (comments,
// key order, indentation, non-preset rules). Returns the rewritten YAML and
// the number of rules it expanded.
func Expand(data []byte) ([]byte, int, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("parse rules YAML: %w", err)
	}
	if doc.Kind == 0 {
		// Empty input decodes to a zero Node - nothing to walk, and
		// re-marshaling a zero Node produces "null\n" instead of "",
		// which would be a spurious change to a genuinely empty file.
		if _, err := loadConfig(data); err != nil {
			return nil, 0, fmt.Errorf("parse rules YAML: %w", err)
		}
		return data, 0, nil
	}

	rulesSeq := findRulesSequence(&doc)
	count := 0
	if rulesSeq != nil {
		for _, ruleNode := range rulesSeq.Content {
			presetIdx := findKeyIndex(ruleNode, "preset")
			if presetIdx < 0 {
				continue
			}

			presetName := ruleNode.Content[presetIdx+1].Value
			preset, ok := presetRegistry[presetName]
			if !ok {
				name := ""
				if nameIdx := findKeyIndex(ruleNode, "name"); nameIdx >= 0 {
					name = ruleNode.Content[nameIdx+1].Value
				}
				return nil, 0, fmt.Errorf("rule %q: unknown preset %q", name, presetName)
			}

			patternKey := scalarNode("pattern")
			patternKey.HeadComment = ruleNode.Content[presetIdx].HeadComment
			patternKey.LineComment = ruleNode.Content[presetIdx].LineComment
			patternValue := &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.SingleQuotedStyle, Value: preset.Pattern}

			fieldsKey := scalarNode("fields")
			fieldsValue := encodeFieldsNode(preset.Fields)

			newContent := make([]*yaml.Node, 0, len(ruleNode.Content)+2)
			for i := 0; i+1 < len(ruleNode.Content); i += 2 {
				if i == presetIdx {
					newContent = append(newContent, patternKey, patternValue, fieldsKey, fieldsValue)
					continue
				}
				newContent = append(newContent, ruleNode.Content[i], ruleNode.Content[i+1])
			}
			ruleNode.Content = newContent
			count++
		}
	}

	out, err := marshalDoc(&doc)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal expanded rules: %w", err)
	}
	if _, err := loadConfig(out); err != nil {
		return nil, 0, fmt.Errorf("expanded rules failed validation (this is a bug): %w", err)
	}
	return out, count, nil
}
```

`internal/rules/convert.go`の`import`ブロックに`"fmt"`を追加すること。

- [ ] **Step 4: テストを実行してパスを確認する**

Run: `go test ./internal/rules/... -run 'TestExpand' -v`
Expected: PASS(全テスト)

- [ ] **Step 5: コミット**

```bash
git add internal/rules/convert.go internal/rules/convert_test.go
git commit -m "feat(rules): implement Expand to rewrite preset: into pattern/fields"
```

---

## Task 5: `Collapse`の実装

**Files:**
- Modify: `internal/rules/convert.go`(追記)
- Modify: `internal/rules/convert_test.go`(追記)

**Interfaces:**
- Consumes: `normalizePattern`/`sortedPresetNames`/`fieldsEqual`/`findKeyIndex`/`findRulesSequence`/`marshalDoc`(Task 2/3)、`loadConfig`(既存)、`rule.declaredPatternOrFields`(既存、`internal/rules/rules.go`の非公開フィールド)
- Produces: `Collapse(data []byte) (out []byte, count int, err error)` — Task 6のCLI層(`newCollapseCmd`)が呼ぶ。

- [ ] **Step 1: 失敗するテストを書く**

`internal/rules/convert_test.go`の末尾に追加:

```go
func TestCollapse_ExactMatchCollapsesToPreset(t *testing.T) {
	input := []byte(`rules:
  - name: access_log
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
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if !strings.Contains(string(out), "preset: apache_clf") {
		t.Errorf("collapsed output missing \"preset: apache_clf\":\n%s", out)
	}
	if strings.Contains(string(out), "pattern:") || strings.Contains(string(out), "fields:") {
		t.Errorf("collapsed output should not contain pattern:/fields::\n%s", out)
	}

	cfg, err := loadConfig(out)
	if err != nil {
		t.Fatalf("loadConfig(collapsed) returned error: %v", err)
	}
	if cfg.Rules[0].Preset != "apache_clf" {
		t.Errorf("Rules[0].Preset = %q, want %q", cfg.Rules[0].Preset, "apache_clf")
	}
}

func TestCollapse_TrivialEscapeDifferenceStillCollapses(t *testing.T) {
	const tempPresetName = "test_escape_variance"
	presetRegistry[tempPresetName] = presetDefinition{
		Pattern: `^(?P<msg>\/test)$`,
		Fields:  []Field{{Name: "msg", Type: "string"}},
	}
	t.Cleanup(func() { delete(presetRegistry, tempPresetName) })

	input := []byte("rules:\n  - name: r\n    pattern: '^(?P<msg>/test)$'\n    fields:\n      msg: string\n")
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if !strings.Contains(string(out), "preset: "+tempPresetName) {
		t.Errorf("collapsed output missing preset %q:\n%s", tempPresetName, out)
	}
}

func TestCollapse_SingleFieldDifferenceDoesNotCollapse(t *testing.T) {
	input := []byte(`rules:
  - name: access_log
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
      status: string
      bytes: int
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if strings.Contains(string(out), "preset:") {
		t.Errorf("output should not have collapsed:\n%s", out)
	}
}

func TestCollapse_AlreadyPresetRuleIsNoop(t *testing.T) {
	input := []byte("rules:\n  - name: access_log\n    preset: apache_clf\n")
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if string(out) != string(input) {
		t.Errorf("output changed for an already-preset rule:\nwant:\n%s\ngot:\n%s", input, out)
	}
}

func TestExpandThenCollapse_RoundTripsBackToPreset(t *testing.T) {
	original := []byte("rules:\n  - name: access_log\n    preset: apache_clf\n")

	expanded, count, err := Expand(original)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("Expand count = %d, want 1", count)
	}

	collapsed, count, err := Collapse(expanded)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("Collapse count = %d, want 1", count)
	}

	cfg, err := loadConfig(collapsed)
	if err != nil {
		t.Fatalf("loadConfig(collapsed) returned error: %v", err)
	}
	if cfg.Rules[0].Preset != "apache_clf" {
		t.Errorf("round-tripped Preset = %q, want %q", cfg.Rules[0].Preset, "apache_clf")
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/rules/... -run 'TestCollapse|TestExpandThenCollapse' -v`
Expected: FAIL — `undefined: Collapse`

- [ ] **Step 3: `Collapse`を実装する**

`internal/rules/convert.go`の末尾に追加:

```go
// Collapse rewrites every rule whose pattern/fields exactly match a
// registered preset (after normalization, see normalizePattern) into
// `preset: <name>`, leaving everything else untouched. Returns the
// rewritten YAML and the number of rules it collapsed.
func Collapse(data []byte) ([]byte, int, error) {
	cfg, err := loadConfig(data)
	if err != nil {
		return nil, 0, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("parse rules YAML: %w", err)
	}
	if doc.Kind == 0 {
		return data, 0, nil
	}

	rulesSeq := findRulesSequence(&doc)
	count := 0
	if rulesSeq != nil {
		for i, rule := range cfg.Rules {
			if rule.Preset != "" || !rule.declaredPatternOrFields {
				continue
			}

			presetName, matched, err := matchingPreset(rule)
			if err != nil {
				return nil, 0, err
			}
			if !matched {
				continue
			}

			ruleNode := rulesSeq.Content[i]
			patIdx := findKeyIndex(ruleNode, "pattern")
			fieldsIdx := findKeyIndex(ruleNode, "fields")

			presetKey := scalarNode("preset")
			presetKey.HeadComment = ruleNode.Content[patIdx].HeadComment
			presetKey.LineComment = ruleNode.Content[patIdx].LineComment
			presetValue := scalarNode(presetName)

			newContent := make([]*yaml.Node, 0, len(ruleNode.Content))
			for j := 0; j+1 < len(ruleNode.Content); j += 2 {
				switch j {
				case patIdx:
					newContent = append(newContent, presetKey, presetValue)
				case fieldsIdx:
					// dropped: folded into the preset: pair above
				default:
					newContent = append(newContent, ruleNode.Content[j], ruleNode.Content[j+1])
				}
			}
			ruleNode.Content = newContent
			count++
		}
	}

	out, err := marshalDoc(&doc)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal collapsed rules: %w", err)
	}
	if _, err := loadConfig(out); err != nil {
		return nil, 0, fmt.Errorf("collapsed rules failed validation (this is a bug): %w", err)
	}
	return out, count, nil
}

// matchingPreset reports the name of the first (sorted) preset whose
// Pattern and Fields exactly match rule's, or ok=false if none does.
func matchingPreset(rule Rule) (name string, ok bool, err error) {
	ruleNorm, err := normalizePattern(rule.Pattern)
	if err != nil {
		return "", false, fmt.Errorf("rule %q: %w", rule.Name, err)
	}

	for _, presetName := range sortedPresetNames() {
		preset := presetRegistry[presetName]
		presetNorm, err := normalizePattern(preset.Pattern)
		if err != nil {
			return "", false, fmt.Errorf("preset %q: %w", presetName, err)
		}
		if ruleNorm == presetNorm && fieldsEqual(rule.Fields, preset.Fields) {
			return presetName, true, nil
		}
	}
	return "", false, nil
}
```

- [ ] **Step 4: テストを実行してパスを確認する**

Run: `go test ./internal/rules/... -run 'TestCollapse|TestExpandThenCollapse' -v`
Expected: PASS(全テスト)

- [ ] **Step 5: `internal/rules`パッケージ全体を確認する**

Run: `gofmt -l internal/rules/convert.go internal/rules/convert_test.go`
Expected: 出力なし

Run: `go test ./internal/rules/... -v`
Expected: PASS(全テスト、既存分含む)

- [ ] **Step 6: コミット**

```bash
git add internal/rules/convert.go internal/rules/convert_test.go
git commit -m "feat(rules): implement Collapse to rewrite matching pattern/fields into preset:"
```

---

## Task 6: CLIサブコマンド`expand`/`collapse`

**Files:**
- Modify: `cmd/logidx/main.go`
- Modify: `cmd/logidx/main_test.go`

**Interfaces:**
- Consumes: `rules.Expand(data []byte) ([]byte, int, error)` / `rules.Collapse(data []byte) ([]byte, int, error)`(Task 4/5)
- Produces: `expand`/`collapse`サブコマンド(`root.AddCommand`経由でCLIに登録)

- [ ] **Step 1: 失敗するテストを書く**

`cmd/logidx/main_test.go`の末尾に追加:

```go
const expandableRulesYAML = `rules:
  - name: access_log
    preset: apache_clf
`

const collapsibleRulesYAML = `rules:
  - name: access_log
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
`

func TestExpandCmd_ExpandsPresetToPatternAndFields(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "rules.yaml", expandableRulesYAML)
	dst := filepath.Join(dir, "expanded.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{"expand", src, dst}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read expanded output: %v", err)
	}
	if strings.Contains(string(got), "preset:") {
		t.Errorf("expanded output still has preset::\n%s", got)
	}
	if !strings.Contains(string(got), "pattern:") {
		t.Errorf("expanded output missing pattern::\n%s", got)
	}
	if !strings.Contains(stderr.String(), "expanded rules") {
		t.Errorf("stderr missing completion log, got: %s", stderr.String())
	}
}

func TestExpandCmd_StdinToStdout(t *testing.T) {
	withStdin(t, expandableRulesYAML)

	var stdout, stderr bytes.Buffer
	code := run([]string{"expand", "-", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "pattern:") {
		t.Errorf("stdout missing pattern::\n%s", stdout.String())
	}
}

func TestExpandCmd_UnknownPresetIsError(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "rules.yaml", "rules:\n  - name: r\n    preset: nope\n")
	dst := filepath.Join(dir, "out.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{"expand", src, dst}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1, stderr = %s", code, stderr.String())
	}
}

func TestExpandCmd_WrongArgCountIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"expand", "onlyone.yaml"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: logidx expand") {
		t.Errorf("stderr missing usage message, got: %s", stderr.String())
	}
}

func TestCollapseCmd_CollapsesMatchingPatternToPreset(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "rules.yaml", collapsibleRulesYAML)
	dst := filepath.Join(dir, "collapsed.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{"collapse", src, dst}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read collapsed output: %v", err)
	}
	if !strings.Contains(string(got), "preset: apache_clf") {
		t.Errorf("collapsed output missing preset: apache_clf:\n%s", got)
	}
	if !strings.Contains(stderr.String(), "collapsed rules") {
		t.Errorf("stderr missing completion log, got: %s", stderr.String())
	}
}

func TestCollapseCmd_StdinToStdout(t *testing.T) {
	withStdin(t, collapsibleRulesYAML)

	var stdout, stderr bytes.Buffer
	code := run([]string{"collapse", "-", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "preset: apache_clf") {
		t.Errorf("stdout missing preset: apache_clf:\n%s", stdout.String())
	}
}

func TestCollapseCmd_WrongArgCountIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"collapse"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: logidx collapse") {
		t.Errorf("stderr missing usage message, got: %s", stderr.String())
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./cmd/logidx/... -run 'TestExpandCmd|TestCollapseCmd' -v`
Expected: FAIL — `unknown command "expand" for "logidx"`

- [ ] **Step 3: `newExpandCmd`/`newCollapseCmd`を実装する**

`cmd/logidx/main.go`の`run()`関数内、`root.AddCommand(newRestoreCmd(stdout, stderr))`の次の行に追加:

```go
	root.AddCommand(newExpandCmd(stdout, stderr))
	root.AddCommand(newCollapseCmd(stdout, stderr))
```

`cmd/logidx/main.go`の末尾(`newRestoreCmd`の後)に追加:

```go
// convertCmdSpec is the per-subcommand configuration newConvertCmd needs:
// expand and collapse are identical in shape (src/dst args, --log-format,
// -v, completion log line) and differ only in which rules.Expand/
// rules.Collapse function does the rewriting and what verb describes it.
type convertCmdSpec struct {
	use   string
	short string
	verb  string
	fn    func([]byte) ([]byte, int, error)
}

func newExpandCmd(stdout, stderr io.Writer) *cobra.Command {
	return newConvertCmd(stdout, stderr, convertCmdSpec{
		use:   "expand <src.yaml> <dst.yaml>",
		short: "Rewrite every rule's preset: into the pattern/fields it names (- reads/writes stdin/stdout)",
		verb:  "expanded",
		fn:    rules.Expand,
	})
}

func newCollapseCmd(stdout, stderr io.Writer) *cobra.Command {
	return newConvertCmd(stdout, stderr, convertCmdSpec{
		use:   "collapse <src.yaml> <dst.yaml>",
		short: "Rewrite rules whose pattern/fields exactly match a preset into preset: <name> (- reads/writes stdin/stdout)",
		verb:  "collapsed",
		fn:    rules.Collapse,
	})
}

func newConvertCmd(stdout, stderr io.Writer, spec convertCmdSpec) *cobra.Command {
	var (
		logFormat string
		verbose   bool
	)

	cmd := &cobra.Command{
		Use:           spec.use,
		Short:         spec.short,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) != 2 {
				_, _ = fmt.Fprintf(stderr, "usage: logidx %s [--log-format text|json] [-v|--verbose] <src.yaml|-> <dst.yaml|->\n", strings.Fields(spec.use)[0])
				return &exitCodeError{2}
			}
			src, dst := args[0], args[1]
			logger := logging.New(stderr, logFormat, verbose)

			data, err := readSrcFile(src)
			if err != nil {
				logger.Error("cannot read source", "error", err)
				return &exitCodeError{1}
			}

			out, count, err := spec.fn(data)
			if err != nil {
				logger.Error("conversion failed", "error", err)
				return &exitCodeError{1}
			}

			if err := writeDstFile(stdout, dst, out); err != nil {
				logger.Error("cannot write destination", "error", err)
				return &exitCodeError{1}
			}

			logger.Info(spec.verb+" rules", "count", count)
			return nil
		},
	}

	cmd.Flags().StringVar(&logFormat, "log-format", "text", "log format: text or json")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose (debug) logging")

	return cmd
}

// readSrcFile reads path, or os.Stdin if path is "-" - the same convention
// used by restore's src (see newRestoreCmd).
func readSrcFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// writeDstFile writes data to path, or stdout if path is "-" - the same
// convention used by dump's dst (see newDumpCmd). Writes through the
// injected stdout io.Writer, not os.Stdout directly, so tests can capture
// it via a bytes.Buffer.
func writeDstFile(stdout io.Writer, path string, data []byte) error {
	if path == "-" {
		_, err := stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
```

`cmd/logidx/main.go`の`import`ブロックに`"logidx/internal/logging"`を追加すること(まだ無ければ)。

- [ ] **Step 4: テストを実行してパスを確認する**

Run: `go test ./cmd/logidx/... -run 'TestExpandCmd|TestCollapseCmd' -v`
Expected: PASS(全テスト)

- [ ] **Step 5: プロジェクト全体のビルド・テスト・lintを確認する**

Run: `task build`
Expected: `bin/logidx`が生成される、エラーなし

Run: `task test`
Expected: PASS(全パッケージ)

Run: `task fmt && git diff --stat`
Expected: `gofmt -l -w .`で差分が出ないこと(すでに整形済み)

Run: `task lint`
Expected: 指摘なし

- [ ] **Step 6: コミット**

```bash
git add cmd/logidx/main.go cmd/logidx/main_test.go
git commit -m "feat(cmd): add expand/collapse subcommands"
```

---

## Task 7: README更新

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: なし(ドキュメントのみ)

- [ ] **Step 1: `dump / restore`セクションの直後に`expand / collapse`セクションを追加する**

`README.md`の"### dump / restore: Parquetファイルをテキスト形式で書き出す・復元する"セクション末尾(`## Development`の直前)に追加:

```markdown
### expand / collapse: `preset:`とpattern/fieldsを相互変換する

    logidx expand   [--log-format text|json] [-v|--verbose] <src.yaml> <dst.yaml>
    logidx collapse [--log-format text|json] [-v|--verbose] <src.yaml> <dst.yaml>

`expand`はルールの`preset: <名前>`を、そのプリセットが展開する`pattern:`/`fields:`に書き換える。プリセットの内容を確認したり部分カスタマイズしたい場合に使う(プリセットは全体一致のみでNon-goalsとして部分上書きは対象外 - `expand`してから手で編集する運用になる)。

`collapse`はルールの`pattern:`/`fields:`が(正規表現の表記揺れを正規化した上で)プリセットの定義と完全一致する場合、`preset: <名前>`の1行に書き換える。手書きのパターンがプリセットとたまたま一致している場合に、読みやすく圧縮する用途。

- どちらも変換対象以外のYAML(コメント、キー順、インデント、他のルール)はそのまま保持する
- `src`/`dst`は`dump`/`restore`と同じ規約: `src`に`-`を指定すると標準入力から読み、`dst`に`-`を指定すると標準出力に書く
- 完了後、変換したルール数をログに出す(`expanded rules count=N` / `collapsed rules count=N`)。対象0件でも正常終了する
- `expand`で未知のプリセット名を指定したルールがあるとエラーで打ち切る
- `collapse`は一致しなければそのルールをスキップするだけで、通常はエラーにならない
- インプレース編集用のフラグは無い(`<src> <dst>`に同じパスを指定すればインプレースと同等)
```

- [ ] **Step 2: 表記を確認する**

Run: `git diff README.md`
Expected: 追加したセクションが意図通り表示される(誤字・リンク切れがないか目視確認)

- [ ] **Step 3: コミット**

```bash
git add README.md
git commit -m "docs: document expand/collapse subcommands"
```
