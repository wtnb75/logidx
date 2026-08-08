# フィールド値の部分文字列置換(`replace:`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a `rules.yaml` field declare `replace:` — a chain of regexp-based substring replacements applied to the field's value before `normalize:` — so noise like octal control-char escapes (`#015`) or ANSI color codes (`\x1b[31m`) can be stripped from the middle of a value while the rest of the value is preserved.

**Architecture:** `internal/rules.Field` gains a `Replace []ReplaceRule` slice (same shape as the existing `NormalizeRule`, compiled by `Load()` the same way). `internal/parse.convertValue` applies each `Replace` rule in declaration order — each rule's output feeds the next rule's input — before running the existing `normalize:` step, then continues into the existing type-conversion `switch` unchanged.

**Tech Stack:** Go 1.25, `regexp` (standard library only — no new dependency; `regexp.ReplaceAllString` already supports `$1`-style capture group backreferences).

## Global Constraints

- YAML shape: `replace:` is a list of `{pattern: <regexp>, value: <replacement string>}` under a field, sibling to `normalize:`. `value: ''` deletes the matched substring.
- Go naming: `ReplaceRule{Pattern string (yaml `pattern`), Replacement string (yaml `value`), Regexp *regexp.Regexp (yaml `-`)}` — same shape as `NormalizeRule`, but the Go field is named `Replacement` (not `Value`) while the YAML tag stays `value` to match `normalize:`'s YAML vocabulary.
- Apply order is fixed: `replace` rules run in declaration order, chained (rule N's output is rule N+1's input) — *then* `normalize` runs once on the fully-replaced string. This order is not configurable.
- Pattern compile failures are a startup error, in `rules.Load()`, using the exact message shape `"rule %q field %q: compile replace pattern: %w"` (mirrors the existing normalize/continuation compile-error messages).
- A `replace` rule that breaks a later type conversion (e.g. deletes the digits a `type: int` field needs) is **not** a new error class — it flows through the existing type-conversion failure path, and that line becomes unmatched, exactly as an unparseable raw value already does today.
- No change to `Config.Validate()` — `normalize` has no validation entry either; the same reasoning applies to `replace`.
- Non-goals (do not implement): whole-line replacement before pattern matching; named presets like `sanitize: ansi`; merging/replacing the `normalize:` concept. `replace` and `normalize` stay two independent mechanisms.

---

### Task 1: `internal/rules` — `ReplaceRule` type, `Field.Replace`, and `Load()` compilation

**Files:**
- Modify: `internal/rules/rules.go`
- Test: `internal/rules/rules_test.go`

**Interfaces:**
- Produces: `rules.ReplaceRule{Pattern string, Replacement string, Regexp *regexp.Regexp}` (yaml tags `pattern`/`value`/`-`). `rules.Field.Replace []ReplaceRule` (yaml tag `replace`), populated in YAML declaration order by the existing `Field.UnmarshalYAML`, and compiled (`.Regexp` set) by `Load()`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/rules/rules_test.go` (after `TestLoad_InvalidContinuationPatternIsError`):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rules/... -run 'TestLoad_ParsesReplaceRules|TestLoad_InvalidReplacePatternIsError' -v`
Expected: FAIL to compile — `message.Replace` is undefined (`Field` has no `Replace` field yet).

- [ ] **Step 3: Add `ReplaceRule`, `Field.Replace`, and compile them in `Load()`**

In `internal/rules/rules.go`, add the `ReplaceRule` type right after `NormalizeRule` (currently lines 14-19):

```go
// ReplaceRule replaces every regexp match of Pattern within a field's raw
// value with Replacement (Go's regexp.ReplaceAllString - $1-style capture
// group backreferences work without any extra code). Declared rules chain:
// each rule's output becomes the next rule's input.
type ReplaceRule struct {
	Pattern     string         `yaml:"pattern"`
	Replacement string         `yaml:"value"`
	Regexp      *regexp.Regexp `yaml:"-"`
}
```

Change the `Field` struct (currently lines 24-33) to add `Replace` before `Normalize`:

```go
type Field struct {
	Name      string          `yaml:"-"`
	Type      string          `yaml:"type"`
	Format    string          `yaml:"format"`
	Replace   []ReplaceRule   `yaml:"replace"`
	Normalize []NormalizeRule `yaml:"normalize"`

	// ResolvedFormat is Format resolved once by ResolveFormat, at Load
	// time - see TimeFormat. Only meaningful when Type == "timestamp".
	ResolvedFormat TimeFormat `yaml:"-"`
}
```

(`Field.UnmarshalYAML` needs no change — it decodes via `type fieldAlias Field`, so the new `Replace` field is picked up automatically through its yaml tag.)

In `Load()`, inside the existing `for fi := range cfg.Rules[i].Fields { field := &cfg.Rules[i].Fields[fi]` loop (currently lines 156-173), add the replace-compile loop immediately before the existing normalize-compile loop:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rules/... -v`
Expected: PASS (all tests in the package, including the 2 new ones and every pre-existing test — proving fields without `replace:` are unaffected).

- [ ] **Step 5: Full build/vet/gofmt check**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all pass, no vet warnings, no gofmt diffs.

- [ ] **Step 6: Commit**

```bash
git add internal/rules/rules.go internal/rules/rules_test.go
git commit -m "Add Field.Replace and ReplaceRule to internal/rules"
```

---

### Task 2: `internal/parse` — apply `replace:` in `convertValue`

**Files:**
- Modify: `internal/parse/convertvalue.go`
- Test: `internal/parse/convertvalue_test.go`

**Interfaces:**
- Consumes: `rules.Field.Replace []rules.ReplaceRule` (Task 1), each entry's `.Regexp` already compiled by `rules.Load()`.
- Produces: no new exported symbols. `convertValue`'s behavior changes: it now runs the `replace` chain, then `normalize` (unchanged), then the existing type-conversion `switch` (unchanged) on the result.

- [ ] **Step 1: Write the failing tests**

Add to `internal/parse/convertvalue_test.go` (a `replaceRule` test helper, mirroring `normRule` in `normalize_test.go` which lives in the same `parse` package, plus 4 new test functions — place them near the existing `TestConvertValue_StringWithNormalize`):

```go
func replaceRule(t *testing.T, pattern, replacement string) rules.ReplaceRule {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return rules.ReplaceRule{Pattern: pattern, Replacement: replacement, Regexp: re}
}

func TestConvertValue_ReplaceRemovesSubstringPreservingRest(t *testing.T) {
	field := rules.Field{Type: "string", Replace: []rules.ReplaceRule{
		replaceRule(t, `#\d{3}`, ""),
	}}
	now := time.Now()
	v, err := convertValue("line one#015line two", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "line oneline two" {
		t.Errorf("got %v, want %q", v, "line oneline two")
	}
}

func TestConvertValue_ReplaceChainsInDeclarationOrder(t *testing.T) {
	field := rules.Field{Type: "string", Replace: []rules.ReplaceRule{
		replaceRule(t, "a", "b"),
		replaceRule(t, "b", "c"),
	}}
	now := time.Now()
	v, err := convertValue("aaa", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// If the rules ran independently (not chained), "aaa" -replace a->b-> "bbb"
	// and a second independent pass over the *original* value would never see
	// any "b" to replace. Chaining forces "bbb" through rule 2, giving "ccc".
	if v != "ccc" {
		t.Errorf("got %v, want %q (rules must chain: rule 2 sees rule 1's output)", v, "ccc")
	}
}

func TestConvertValue_ReplaceSupportsCaptureGroupBackreference(t *testing.T) {
	field := rules.Field{Type: "string", Replace: []rules.ReplaceRule{
		replaceRule(t, `\((\d+)\)`, "[$1]"),
	}}
	now := time.Now()
	v, err := convertValue("retry(3) failed", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "retry[3] failed" {
		t.Errorf("got %v, want %q", v, "retry[3] failed")
	}
}

func TestConvertValue_ReplaceAppliesBeforeNormalize(t *testing.T) {
	field := rules.Field{
		Type: "string",
		Replace: []rules.ReplaceRule{
			replaceRule(t, `\x1b\[[0-9;]*m`, ""),
		},
		Normalize: []rules.NormalizeRule{
			normRule(t, `(?i)^warn(ing)?$`, "WARN"),
		},
	}
	now := time.Now()
	// The raw value only matches normalize's ^warn(ing)?$ pattern once the
	// ANSI escape codes around it are stripped by replace first.
	v, err := convertValue("\x1b[33mWARNING\x1b[0m", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "WARN" {
		t.Errorf("got %v, want %q (replace must run before normalize)", v, "WARN")
	}
}
```

Add `"regexp"` to the import block of `internal/parse/convertvalue_test.go` (needed by the new `replaceRule` helper; `rules` is already imported).

Do not modify `TestConvertValue_String` or `TestConvertValue_StringWithNormalize` — both construct a `rules.Field` with `Replace` left as its zero value (`nil`), so they are this task's regression check that fields without `replace:` behave exactly as before.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parse/... -run TestConvertValue_Replace -v`
Expected: FAIL — `convertValue` doesn't read `field.Replace` yet, so every new test's assertion on the replaced value fails (e.g. `TestConvertValue_ReplaceRemovesSubstringPreservingRest` gets back the raw unreplaced string).

- [ ] **Step 3: Apply the replace chain in `convertValue`**

Replace the top of `convertValue` in `internal/parse/convertvalue.go` (currently):

```go
func convertValue(raw string, field rules.Field, now time.Time) (any, error) {
	normalized := raw
	if len(field.Normalize) > 0 {
		normalized = applyNormalize(raw, field.Normalize)
	}
```

with:

```go
func convertValue(raw string, field rules.Field, now time.Time) (any, error) {
	replaced := raw
	for _, r := range field.Replace {
		replaced = r.Regexp.ReplaceAllString(replaced, r.Replacement)
	}

	normalized := replaced
	if len(field.Normalize) > 0 {
		normalized = applyNormalize(replaced, field.Normalize)
	}
```

The rest of the function (the `switch field.Type` block using `normalized`) is unchanged.

Update the function's doc comment to mention `replace`:

```go
// convertValue applies the replace chain (if configured), then normalization
// (if configured), then converts the resulting string into the Go value
// matching field.Type. Returns an error if the value cannot be converted, in
// which case the caller should treat the whole line as unmatched.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/parse/... -v`
Expected: PASS (every test in the package, including the 4 new ones and all pre-existing `TestConvertValue_*`/`TestApplyNormalize_*` tests — proving normalize-only and no-replace fields are unaffected).

- [ ] **Step 5: Full build/vet/gofmt check**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all pass, no vet warnings, no gofmt diffs.

- [ ] **Step 6: Commit**

```bash
git add internal/parse/convertvalue.go internal/parse/convertvalue_test.go
git commit -m "Apply replace: rules before normalize: in convertValue"
```

---

### Task 3: End-to-end CLI test for `replace:` noise stripping

**Files:**
- Modify: `cmd/logidx/main_test.go`

**Interfaces:**
- Consumes: `run()` (existing CLI entrypoint used by every other test in the file).
- Produces: nothing new — this is a black-box test of the whole `import`→`dump` pipeline.

- [ ] **Step 1: Write the test**

Add to `cmd/logidx/main_test.go` (near `TestRun_ImportDecompressesGzipInput`):

```go
func TestRun_ImportAppliesReplaceRulesToFieldValues(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
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
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)

	// "#015" is a literal 4-character octal control-char escape (not an
	// actual \r byte); "\x1b[31m"/"\x1b[0m" are real ANSI color escape
	// bytes. Both are noise the replace rules must strip while the rest
	// of the message text is preserved.
	logPath := writeFile(t, dir, "app.log", "[INFO] \x1b[31mhello#015 world\x1b[0m\n")

	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", filepath.Join(outDir, "app_log.parquet"), "-"}, &stdout, &stderr)
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
	if row["message"] != "hello world" {
		t.Errorf("message = %q, want %q", row["message"], "hello world")
	}
}
```

This does not reuse `cliRulesYAML` (it has no `message.replace:` clause) — it defines its own `rulesYAML` inline, following the same pattern `TestRun_ImportMergesMultiLineSyslogEntryIntoOneRow` and other multi-rule tests in this file already use.

- [ ] **Step 2: Run the test**

Run: `go test ./cmd/logidx/... -run TestRun_ImportAppliesReplaceRulesToFieldValues -v`
Expected: PASS (Tasks 1-2 already implement the underlying behavior; this test proves it end-to-end through the real CLI).

- [ ] **Step 3: Run the full test suite**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/logidx/main_test.go
git commit -m "Add end-to-end test for replace: noise stripping in field values"
```

---

### Task 4: Document `replace:` in README

**Files:**
- Modify: `README.md`

**Interfaces:**
- None (documentation only).

- [ ] **Step 1: Add the section**

In `README.md`, insert a new subsection right after the `### タイムスタンプの\`format\`指定` section's last paragraph (`年なしのプリセット・strptime(\`syslog\`など)は、既存の年補完ロジック(実行時刻を基準に、未来にならない直近の年を採用)がそのまま適用される。`) and before the `### 圧縮設定` heading:

```markdown
### フィールド値の変換(`normalize:`/`replace:`)

`fields:`の各フィールドには、値を変換する`replace:`と`normalize:`をそれぞれ設定できる。両者は別の概念であり、常に次の順序で適用される: `replace` → `normalize`。

- `replace:`は、値の一部を正規表現で置換する。宣言順に適用され、前のルールの出力が次のルールの入力になる(チェイン)。`value: ''`を指定すればマッチした部分文字列を削除できる。`$1`のようなキャプチャグループ後方参照も使える(Go標準の`regexp.ReplaceAllString`の機能で、追加実装は不要)。制御文字の8進エスケープ表記(`#015`など)やANSIカラーエスケープシーケンス(`\x1b[31m`など)のような、値の一部にだけ混入するノイズの除去に向く。
- `normalize:`は、値**全体**が宣言順のいずれかのパターンにマッチした場合のみ、最初にマッチしたルールの固定値に置き換える(例: `WARN`/`WARNING` → `WARN`)。値の一部だけを保持したまま変換することはできない。

```yaml
fields:
  message:
    type: string
    replace:
      - pattern: '#\d{3}'
        value: ''
      - pattern: '\x1b\[[0-9;]*m'
        value: ''
    normalize:
      - pattern: '(?i)^warn(ing)?$'
        value: WARN
```

上の例では、`message`の値からまず`#\d{3}`(制御文字の8進エスケープ表記)とANSIカラーエスケープシーケンスを`replace`で除去し、そのクリーンな値に対して`normalize`のパターンマッチングを行う。
```

- [ ] **Step 2: Verify the README renders sensibly**

Run: `rg -n "^### " README.md` and confirm the new heading appears between `タイムスタンプの\`format\`指定` and `圧縮設定`.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "Document field value replace: rules and their order relative to normalize:"
```
