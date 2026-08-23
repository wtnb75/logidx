# Line-level Ignore Rules (`ignore:`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a global `ignore:` block to `rules.yaml` that drops raw input lines — by regexp pattern, max byte length, invalid UTF-8, or emptiness — before any rule's `pattern` is tried against them, recording each dropped line in `unmatched.txt` with a reason instead of silently discarding it.

**Architecture:** A new `rules.IgnoreConfig` (parsed/compiled/validated like the existing `mask:`) exposes a single decision function, `Reason(line string) string`. `internal/convert/merge.go`'s `fileCursor.nextLine()` calls it on every physical line before returning it to the rest of the pipeline, so an ignored line is fully invisible to pattern matching and `continuation` handling. Ignored/unmatched lines both flow through the existing `unmatched.txt` sidecar, whose format gains a `reason` column (`unmatched` or `ignored:<condition>`).

**Tech Stack:** Go, `regexp`, `unicode/utf8`, `gopkg.in/yaml.v3` — no new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-23-ignore-lines-design.md`

## Global Constraints

- `ignore:` is a single top-level object in `rules.yaml` (not a list, not per-rule) — mirrors `compression:`/`row_group:`, not `mask:`.
- Four independent OR conditions: `patterns` ([]string, regexp partial match), `max_length` (int, byte length, `<= 0` = unlimited), `invalid_utf8` (bool), `empty` (bool, `strings.TrimSpace(line) == ""`).
- Fixed evaluation/priority order for the `reason` label, not user-configurable: `empty` → `invalid_utf8` → `max_length` → `patterns`.
- Ignored lines are written to `unmatched.txt`, never silently dropped and never sent to a separate file.
- `unmatched.txt` format changes from 3 columns (`<source>\t<lineNum>\t<raw>`) to 4 (`<source>\t<lineNum>\t<reason>\t<raw>`) for **every** line, including pre-existing "matched no rule" lines (`reason` = `unmatched`). This is a breaking, intentional format change — call it out in docs.
- `ignore:` absent/zero-value must ignore nothing and leave every existing behavior (including counter values) unchanged.

---

### Task 1: `rules.IgnoreConfig` — config, loading, validation, decision logic

**Files:**
- Modify: `internal/rules/rules.go` (add `IgnoreConfig` type near `MaskRule`, line 62; add `Config.Ignore` field near line 220; add pattern-compile loop in `loadConfig` near line 319)
- Modify: `internal/rules/validate.go` (add `MaxLength` check in `Validate()` near line 170)
- Create: `internal/rules/ignore.go` (`IgnoreConfig.Reason`)
- Test: `internal/rules/rules_test.go` (append)
- Test: `internal/rules/validate_test.go` (append)
- Test: `internal/rules/ignore_test.go` (new)

**Interfaces:**
- Produces:
  - `type IgnoreConfig struct { Patterns []string; PatternsRe []*regexp.Regexp; MaxLength int; InvalidUTF8 bool; Empty bool }` (package `rules`)
  - `Config.Ignore IgnoreConfig` (yaml key `ignore`)
  - `func (ic *IgnoreConfig) Reason(line string) string` — returns `""`, `"empty"`, `"invalid_utf8"`, `"max_length"`, or `"pattern"`
- Consumes: nothing new (reuses the same `regexp.Compile`-at-`Load`-time pattern already used for `Mask[i].Pattern`).

- [ ] **Step 1: Write the failing tests**

Append to `internal/rules/rules_test.go`:

```go
func TestLoad_ParsesAndCompilesIgnoreConfig(t *testing.T) {
	yamlContent := `
ignore:
  patterns:
    - '^#'
    - '^\s*$'
  max_length: 100
  invalid_utf8: true
  empty: true

rules:
  - name: app_log
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
`
	path := writeTempRules(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.Ignore.Patterns) != 2 || cfg.Ignore.Patterns[0] != "^#" {
		t.Fatalf("unexpected Ignore.Patterns: %+v", cfg.Ignore.Patterns)
	}
	if len(cfg.Ignore.PatternsRe) != 2 || cfg.Ignore.PatternsRe[0] == nil || cfg.Ignore.PatternsRe[1] == nil {
		t.Fatalf("expected compiled PatternsRe for both patterns, got: %+v", cfg.Ignore.PatternsRe)
	}
	if cfg.Ignore.MaxLength != 100 {
		t.Errorf("MaxLength = %d, want 100", cfg.Ignore.MaxLength)
	}
	if !cfg.Ignore.InvalidUTF8 {
		t.Error("expected InvalidUTF8 = true")
	}
	if !cfg.Ignore.Empty {
		t.Error("expected Empty = true")
	}
}

func TestLoad_InvalidIgnorePatternIsError(t *testing.T) {
	yamlContent := `
ignore:
  patterns:
    - '(unterminated'

rules:
  - name: app_log
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
`
	path := writeTempRules(t, yamlContent)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid ignore pattern, got nil")
	}
	if !strings.Contains(err.Error(), "ignore.patterns[0]") {
		t.Errorf("expected error to mention ignore.patterns[0], got: %v", err)
	}
}

func TestLoad_RuleWithoutIgnoreLeavesZeroValue(t *testing.T) {
	path := writeTempRules(t, sampleRulesYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Ignore.Patterns) != 0 || cfg.Ignore.MaxLength != 0 || cfg.Ignore.InvalidUTF8 || cfg.Ignore.Empty {
		t.Errorf("expected zero-value Ignore when ignore: is absent, got %+v", cfg.Ignore)
	}
}
```

Append to `internal/rules/validate_test.go`:

```go
func TestValidate_IgnoreNegativeMaxLengthIsError(t *testing.T) {
	cfg := &Config{
		Ignore: IgnoreConfig{MaxLength: -1},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "ignore.max_length") {
		t.Errorf("expected error to mention ignore.max_length, got: %v", err)
	}
}

func TestValidate_IgnoreZeroOrPositiveMaxLengthPasses(t *testing.T) {
	for _, length := range []int{0, 1, 1000} {
		cfg := &Config{
			Ignore: IgnoreConfig{MaxLength: length},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("length %d: unexpected validation error: %v", length, err)
		}
	}
}
```

Create `internal/rules/ignore_test.go`:

```go
package rules

import (
	"regexp"
	"testing"
)

func TestIgnoreConfig_Reason_NoConditionsConfiguredNeverIgnores(t *testing.T) {
	var ic IgnoreConfig
	for _, line := range []string{"", "   ", "normal line", "\xff\xfe not utf8"} {
		if reason := ic.Reason(line); reason != "" {
			t.Errorf("line %q: Reason() = %q, want \"\" (zero-value IgnoreConfig)", line, reason)
		}
	}
}

func TestIgnoreConfig_Reason_Empty(t *testing.T) {
	ic := IgnoreConfig{Empty: true}
	for _, line := range []string{"", "   ", "\t\n"} {
		if reason := ic.Reason(line); reason != "empty" {
			t.Errorf("line %q: Reason() = %q, want \"empty\"", line, reason)
		}
	}
	if reason := ic.Reason("not empty"); reason != "" {
		t.Errorf(`Reason("not empty") = %q, want ""`, reason)
	}
}

func TestIgnoreConfig_Reason_InvalidUTF8(t *testing.T) {
	ic := IgnoreConfig{InvalidUTF8: true}
	if reason := ic.Reason("\xff\xfe not valid utf8"); reason != "invalid_utf8" {
		t.Errorf("Reason() = %q, want \"invalid_utf8\"", reason)
	}
	if reason := ic.Reason("valid utf8 é"); reason != "" {
		t.Errorf(`Reason("valid utf8 é") = %q, want ""`, reason)
	}
}

func TestIgnoreConfig_Reason_MaxLength(t *testing.T) {
	ic := IgnoreConfig{MaxLength: 5}
	if reason := ic.Reason("123456"); reason != "max_length" {
		t.Errorf("Reason(6 bytes) = %q, want \"max_length\"", reason)
	}
	if reason := ic.Reason("12345"); reason != "" {
		t.Errorf("Reason(5 bytes, at the limit) = %q, want \"\"", reason)
	}
}

func TestIgnoreConfig_Reason_MaxLengthZeroMeansUnlimited(t *testing.T) {
	ic := IgnoreConfig{MaxLength: 0}
	if reason := ic.Reason(string(make([]byte, 10000))); reason != "" {
		t.Errorf("Reason() = %q, want \"\" (max_length: 0 = unlimited)", reason)
	}
}

func TestIgnoreConfig_Reason_Pattern(t *testing.T) {
	ic := IgnoreConfig{
		PatternsRe: []*regexp.Regexp{
			regexp.MustCompile(`^#`),
			regexp.MustCompile(`^--`),
		},
	}
	if reason := ic.Reason("# a comment"); reason != "pattern" {
		t.Errorf("Reason() = %q, want \"pattern\"", reason)
	}
	if reason := ic.Reason("-- a comment"); reason != "pattern" {
		t.Errorf("Reason() = %q, want \"pattern\"", reason)
	}
	if reason := ic.Reason("not a comment"); reason != "" {
		t.Errorf(`Reason("not a comment") = %q, want ""`, reason)
	}
}

func TestIgnoreConfig_Reason_PriorityOrderEmptyBeatsAllOthers(t *testing.T) {
	// A whitespace-only line trivially passes invalid_utf8/max_length checks
	// too - this locks in that Empty is checked first, in case a pattern
	// like '^\s*$' would otherwise also match and report "pattern" instead.
	ic := IgnoreConfig{
		Empty:      true,
		MaxLength:  1,
		PatternsRe: []*regexp.Regexp{regexp.MustCompile(`^\s*$`)},
	}
	if reason := ic.Reason("   "); reason != "empty" {
		t.Errorf("Reason() = %q, want \"empty\"", reason)
	}
}

func TestIgnoreConfig_Reason_PriorityOrderInvalidUTF8BeatsMaxLengthAndPattern(t *testing.T) {
	ic := IgnoreConfig{
		InvalidUTF8: true,
		MaxLength:   1,
		PatternsRe:  []*regexp.Regexp{regexp.MustCompile(`.`)},
	}
	if reason := ic.Reason("\xff\xfe"); reason != "invalid_utf8" {
		t.Errorf("Reason() = %q, want \"invalid_utf8\"", reason)
	}
}

func TestIgnoreConfig_Reason_PriorityOrderMaxLengthBeatsPattern(t *testing.T) {
	ic := IgnoreConfig{
		MaxLength:  3,
		PatternsRe: []*regexp.Regexp{regexp.MustCompile(`.`)},
	}
	if reason := ic.Reason("abcdef"); reason != "max_length" {
		t.Errorf("Reason() = %q, want \"max_length\"", reason)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rules/... -run 'Ignore' -v`
Expected: FAIL to compile — `IgnoreConfig`, `Config.Ignore`, and `Reason` don't exist yet.

- [ ] **Step 3: Implement `IgnoreConfig` type and wire it into `Config`**

In `internal/rules/rules.go`, add this type right after the `MaskRule` struct (after line 62, before the `StructuredConfig` doc comment):

```go
// IgnoreConfig declares rules.yaml-wide conditions for skipping raw input
// lines before pattern matching even begins. Declared globally under
// Config.Ignore (not nested under any rule), matching mask:/compression:/
// row_group: - but unlike MaskRule, its conditions don't chain: Reason
// (see ignore.go) evaluates them as an independent OR and returns the
// first one that matches, in a fixed priority order.
type IgnoreConfig struct {
	// Patterns is a list of regexps; a line matching ANY of them (partial
	// match, via regexp.MatchString) is ignored.
	Patterns   []string         `yaml:"patterns"`
	PatternsRe []*regexp.Regexp `yaml:"-"`
	// MaxLength ignores a line whose byte length exceeds this value. <= 0
	// means unlimited (the zero value, so an empty ignore: block ignores
	// nothing).
	MaxLength int `yaml:"max_length"`
	// InvalidUTF8 ignores a line that isn't valid UTF-8 (utf8.ValidString).
	InvalidUTF8 bool `yaml:"invalid_utf8"`
	// Empty ignores a line that's empty after strings.TrimSpace.
	Empty bool `yaml:"empty"`
}
```

In the `Config` struct, add a field right after `Mask`:

```go
	// Mask declares global, rule-independent redaction/hashing applied to
	// every rule's structured/converted data the same way - see MaskRule.
	Mask []MaskRule `yaml:"mask"`
	// Ignore declares rules.yaml-wide conditions for skipping raw lines
	// before pattern matching - see IgnoreConfig.
	Ignore IgnoreConfig `yaml:"ignore"`
```

In `loadConfig`, right after the existing `for i := range cfg.Mask { ... }` pattern-compile loop and before `if err := cfg.Validate(); err != nil {`, add:

```go
	cfg.Ignore.PatternsRe = make([]*regexp.Regexp, len(cfg.Ignore.Patterns))
	for i, p := range cfg.Ignore.Patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("ignore.patterns[%d]: compile pattern: %w", i, err)
		}
		cfg.Ignore.PatternsRe[i] = re
	}
```

In `internal/rules/validate.go`, in `Validate()`, right after the `for i, m := range c.Mask { ... }` loop and before the `c.Compression.Validate()` call, add:

```go
	if c.Ignore.MaxLength < 0 {
		errs = append(errs, fmt.Errorf("ignore.max_length: must not be negative, got %d", c.Ignore.MaxLength))
	}
```

Create `internal/rules/ignore.go`:

```go
package rules

import (
	"strings"
	"unicode/utf8"
)

// Reason returns "" when line should not be ignored, otherwise the name of
// the first matching condition, checked in this fixed priority order
// regardless of declaration order in rules.yaml: "empty", "invalid_utf8",
// "max_length", "pattern". The order is an implementation choice (cheapest
// checks first) - see the ignore: design doc.
func (ic *IgnoreConfig) Reason(line string) string {
	if ic.Empty && strings.TrimSpace(line) == "" {
		return "empty"
	}
	if ic.InvalidUTF8 && !utf8.ValidString(line) {
		return "invalid_utf8"
	}
	if ic.MaxLength > 0 && len(line) > ic.MaxLength {
		return "max_length"
	}
	for _, re := range ic.PatternsRe {
		if re.MatchString(line) {
			return "pattern"
		}
	}
	return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rules/... -v`
Expected: PASS (all tests in the package, including pre-existing ones — this confirms no regression to `mask:`/preset/etc. parsing).

- [ ] **Step 5: gofmt and commit**

```bash
gofmt -l -w internal/rules/
go build ./...
git add internal/rules/rules.go internal/rules/validate.go internal/rules/ignore.go internal/rules/rules_test.go internal/rules/validate_test.go internal/rules/ignore_test.go
git commit -m "feat: add rules.IgnoreConfig (ignore: config, loading, validation, Reason)"
```

---

### Task 2: `writer.WriteUnmatched` gains a `reason` column

**Files:**
- Modify: `internal/writer/writer.go` (`WriteUnmatched`, lines 117-137)
- Test: `internal/writer/writer_test.go` (modify existing tests at lines 190-241, add one new test)

**Interfaces:**
- Consumes: none new.
- Produces: `func (s *Set) WriteUnmatched(source string, lineNum int, reason, raw string) error` — Task 3 calls this with `reason` = `"unmatched"` or `"ignored:<condition>"`.

- [ ] **Step 1: Update/write the tests**

In `internal/writer/writer_test.go`, replace `TestSet_WriteUnmatched_CreatesFileLazilyWithSourceAndLineNumber` and `TestSet_WriteUnmatched_DisambiguatesSameLineNumberFromDifferentSources` with:

```go
func TestSet_WriteUnmatched_CreatesFileLazilyWithSourceAndLineNumber(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	if err := set.WriteUnmatched("access.log", 3, "unmatched", "garbled line"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Unmatched != 1 {
		t.Errorf("expected Unmatched=1, got %d", summary.Unmatched)
	}

	content, err := os.ReadFile(filepath.Join(dir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "access.log\t3\tunmatched\tgarbled line\n"
	if string(content) != want {
		t.Errorf("got %q, want %q", string(content), want)
	}
}

func TestSet_WriteUnmatched_DisambiguatesSameLineNumberFromDifferentSources(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	if err := set.WriteUnmatched("a.log", 5, "unmatched", "from a"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}
	if err := set.WriteUnmatched("b.log", 5, "unmatched", "from b"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "a.log\t5\tunmatched\tfrom a\nb.log\t5\tunmatched\tfrom b\n"
	if string(content) != want {
		t.Errorf("got %q, want %q", string(content), want)
	}
}

func TestSet_WriteUnmatched_WritesArbitraryReasonVerbatim(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	if err := set.WriteUnmatched("access.log", 7, "ignored:max_length", "a very long line"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}
	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "access.log\t7\tignored:max_length\ta very long line\n"
	if string(content) != want {
		t.Errorf("got %q, want %q", string(content), want)
	}
}
```

`TestSet_NoUnmatchedFileWhenNoUnmatchedLines` is unaffected (it never calls `WriteUnmatched`) — leave it as-is.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/writer/... -v`
Expected: FAIL to compile — `WriteUnmatched` still takes 3 args, tests now pass 4.

- [ ] **Step 3: Implement the signature/format change**

In `internal/writer/writer.go`, replace `WriteUnmatched`:

```go
// WriteUnmatched appends one "<source>\t<lineNum>\t<reason>\t<raw>\n" record
// to the shared unmatched raw-text sidecar, creating it on first use.
// source identifies which input the line came from (its path, or "-" for
// stdin) - necessary because a Set merges multiple inputs, so lineNum alone
// would be ambiguous (e.g. line 5 of two different input files). reason is
// "unmatched" for a line that matched no rule, or "ignored:<condition>" for
// one dropped by rules.IgnoreConfig before pattern matching even ran (see
// internal/convert.fileCursor.nextLine).
func (s *Set) WriteUnmatched(source string, lineNum int, reason, raw string) error {
	if s.unmatchedFile == nil {
		path := filepath.Join(s.outDir, "unmatched.txt")
		f, err := atomicfile.New(path)
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		s.unmatchedFile = f
	}

	if _, err := fmt.Fprintf(s.unmatchedFile, "%s\t%d\t%s\t%s\n", source, lineNum, reason, raw); err != nil {
		return fmt.Errorf("write unmatched line: %w", err)
	}
	s.unmatchedCount++
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/writer/... -v`
Expected: PASS. (This step will still show compile errors from `internal/convert`, which also calls `WriteUnmatched` — that's expected and fixed in Task 3. Run `go test ./internal/writer/...` specifically, not `./...`, to isolate this package's result.)

- [ ] **Step 5: gofmt and commit**

```bash
gofmt -l -w internal/writer/
git add internal/writer/writer.go internal/writer/writer_test.go
git commit -m "feat: add reason column to writer.Set.WriteUnmatched"
```

---

### Task 3: Wire `ignore:` into the scan loop (`internal/convert/merge.go`)

**Files:**
- Modify: `internal/convert/merge.go` (`fileCursor` struct, `nextLine`, `writeUnmatchedLine`, `tryCandidates`, `logFileProcessed`)
- Test: `internal/convert/merge_test.go` (update 4 existing assertions, append 6 new tests)
- Test: `internal/convert/convert_test.go` (update 2 existing assertions)

**Interfaces:**
- Consumes: `rules.Config.Ignore.Reason(line string) string` (Task 1), `writer.Set.WriteUnmatched(source string, lineNum int, reason, raw string) error` (Task 2).
- Produces: `fileCursor.ignored int` (new field, mirrors existing `unmatched int`) — used by `logFileProcessed`.

- [ ] **Step 1: Update existing assertions for the new `unmatched.txt` format**

In `internal/convert/merge_test.go`, four `want :=` lines need a `reason` column inserted (all four are the pre-existing "matched no rule" case, so `reason` = `unmatched`):

```go
// line ~564, in TestFileCursor_Advance_OrphanContinuationLineIsUnmatched
want := logPath + "\t1\tunmatched\t  orphan continuation line\n"
```
```go
// line ~705, in TestFileCursor_Advance_ContinuationConversionFailureFallsThroughToNextRule
want := logPath + "\t2\tunmatched\tMORE 6\n"
```
```go
// line ~856, in TestFileCursor_Advance_ContinuationAllCandidatesExhaustedFirstLineUnmatchedRestRematchIndependently
want := logPath + "\t1\tunmatched\tTS 2026-08-06T12:00:00Z START 5\n"
```
```go
// line ~1124, in TestFileCursor_WriteUnmatchedLine_AppliesPatternMask
want := logPath + "\t1\tunmatched\tcontact [EMAIL] for help\n"
```

In `internal/convert/convert_test.go`, two more:

```go
// line ~107
want := logPath + "\t3\tunmatched\tthis is a garbled line that matches nothing\n"
```
```go
// line ~158
want := logA + "\t2\tunmatched\tnot matched\n"
```

- [ ] **Step 2: Write the new ignore-behavior tests**

Append to `internal/convert/merge_test.go`:

```go
func TestFileCursor_Advance_IgnoresLineMatchingPattern(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
ignore:
  patterns:
    - '^#'

rules:
  - name: plain
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "# a comment\nreal line\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now, 0)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.name != "plain" || cand.values["msg"] != "real line" {
		t.Fatalf("candidate = %+v, want rule plain with msg=%q", cand, "real line")
	}
	if cursor.ignored != 1 {
		t.Errorf("ignored = %d, want 1", cursor.ignored)
	}
	if cursor.unmatched != 0 {
		t.Errorf("unmatched = %d, want 0 (ignored lines must not count as unmatched)", cursor.unmatched)
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t1\tignored:pattern\t# a comment\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}

func TestFileCursor_Advance_IgnoresLineExceedingMaxLength(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
ignore:
  max_length: 10

rules:
  - name: plain
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "this line is way too long\nshort\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now, 0)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["msg"] != "short" {
		t.Errorf("msg = %q, want %q", cand.values["msg"], "short")
	}
	if cursor.ignored != 1 {
		t.Errorf("ignored = %d, want 1", cursor.ignored)
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t1\tignored:max_length\tthis line is way too long\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}

func TestFileCursor_Advance_IgnoresInvalidUTF8Line(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
ignore:
  invalid_utf8: true

rules:
  - name: plain
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "valid line\n")
	// Append a line with an invalid UTF-8 byte sequence directly - writeFile
	// writes a plain string and a Go string literal can't cleanly embed a
	// lone invalid byte in source.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.Write([]byte{0xff, 0xfe, '\n'}); err != nil {
		t.Fatalf("write invalid utf8 line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now, 0)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["msg"] != "valid line" {
		t.Errorf("msg = %q, want %q", cand.values["msg"], "valid line")
	}

	_, ok, err = cursor.advance()
	if err != nil {
		t.Fatalf("second advance() error = %v", err)
	}
	if ok {
		t.Fatal("second advance() ok = true, want false at EOF")
	}
	if cursor.ignored != 1 {
		t.Errorf("ignored = %d, want 1", cursor.ignored)
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t2\tignored:invalid_utf8\t\xff\xfe\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}

func TestFileCursor_Advance_IgnoresEmptyLine(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
ignore:
  empty: true

rules:
  - name: plain
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "\n   \nreal line\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now, 0)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["msg"] != "real line" {
		t.Errorf("msg = %q, want %q", cand.values["msg"], "real line")
	}
	if cursor.ignored != 2 {
		t.Errorf("ignored = %d, want 2", cursor.ignored)
	}
}

func TestFileCursor_Advance_IgnoredLineDoesNotCloseOpenContinuationEntry(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
ignore:
  patterns:
    - '^%%'

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
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// "%% noise" sits between the entry's start line and its continuation
	// line; if it weren't filtered out before continuation matching, it
	// would look like a non-continuation line and prematurely close the
	// entry with only "start" as its message.
	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z start\n%% noise\n  continued\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now, 0)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	want := "start\ncontinued"
	if cand.values["message"] != want {
		t.Errorf("message = %q, want %q", cand.values["message"], want)
	}
	if cursor.ignored != 1 {
		t.Errorf("ignored = %d, want 1", cursor.ignored)
	}
}

func TestFileCursor_Advance_SourceLineMetaCountsIgnoredLines(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
ignore:
  patterns:
    - '^#'

rules:
  - name: plain
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
      log_line:
        type: int
        meta: source_line
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "# comment\nreal line\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now, 0)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	// "real line" is physical line 2 (the ignored "# comment" is line 1),
	// so log_line must be 2, not 1.
	if cand.values["log_line"] != int64(2) {
		t.Errorf("log_line = %v, want int64(2)", cand.values["log_line"])
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/convert/... -v`
Expected: FAIL to compile — `writeUnmatchedLine` still takes one arg and `fileCursor` has no `ignored` field; new tests reference both.

- [ ] **Step 4: Implement the wiring**

In `internal/convert/merge.go`, in the `fileCursor` struct, add `ignored` next to `unmatched`:

```go
	counts    map[string]int
	unmatched int
	ignored   int
```

Replace `nextLine`:

```go
// nextLine returns the next physical line to process: a previously pushed
// back line, if any (see fileCursor.pending / pushPending), otherwise the
// next line from the underlying scanner that isn't skipped by cfg.Ignore
// (see rules.IgnoreConfig.Reason) - a skipped line is written to
// unmatched.txt with an "ignored:<condition>" reason and never returned to
// the caller, so continuation/pattern-matching logic never sees it. ok is
// false at EOF. c.lineNum is incremented for every physical line read,
// including skipped ones, so source_line metadata (see rules.Field.Meta)
// stays a correct physical line count.
func (c *fileCursor) nextLine() (line scannedLine, ok bool, err error) {
	if len(c.pending) > 0 {
		line = c.pending[0]
		c.pending = c.pending[1:]
		return line, true, nil
	}
	for {
		if !c.scanner.Scan() {
			if err := c.scanner.Err(); err != nil {
				return scannedLine{}, false, fmt.Errorf("read input: %w", err)
			}
			return scannedLine{}, false, nil
		}
		c.lineNum++
		text := c.scanner.Text()
		if reason := c.cfg.Ignore.Reason(text); reason != "" {
			line := scannedLine{text: text, lineNum: c.lineNum}
			if err := c.writeUnmatchedLine(line, "ignored:"+reason); err != nil {
				return scannedLine{}, false, err
			}
			c.ignored++
			continue
		}
		return scannedLine{text: text, lineNum: c.lineNum}, true, nil
	}
}
```

Replace `writeUnmatchedLine`:

```go
// writeUnmatchedLine writes one physical line to the shared unmatched.txt
// sidecar, tagged with reason ("unmatched" for a line that matched no
// rule, "ignored:<condition>" for one dropped by rules.IgnoreConfig before
// pattern matching even ran - see nextLine). It does not update any
// counter itself; callers bump whichever of c.unmatched/c.ignored applies.
func (c *fileCursor) writeUnmatchedLine(line scannedLine, reason string) error {
	text := parse.ApplyPatternMask(line.text, c.patternMaskRules)
	if err := c.set.WriteUnmatched(c.inputPath, line.lineNum, reason, text); err != nil {
		return fmt.Errorf("write unmatched line %d: %w", line.lineNum, err)
	}
	return nil
}
```

In `tryCandidates`, replace the `!matched` branch:

```go
	if !matched {
		c.logger.Debug("line did not match any rule", "file", c.inputPath, "line", line.lineNum)
		if err := c.writeUnmatchedLine(line, "unmatched"); err != nil {
			return nil, err
		}
		c.unmatched++
		return nil, nil
	}
```

Replace `logFileProcessed`:

```go
func logFileProcessed(logger *slog.Logger, c *fileCursor) {
	args := []any{"file", c.inputPath}
	for name, count := range c.counts {
		args = append(args, name, count)
	}
	args = append(args, "unmatched", c.unmatched, "ignored", c.ignored)
	logger.Info("file processed", args...)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/convert/... -v`
Expected: PASS.

- [ ] **Step 6: Full-repo test pass, gofmt, and commit**

```bash
gofmt -l -w internal/convert/
go build ./...
go test ./...
git add internal/convert/merge.go internal/convert/merge_test.go internal/convert/convert_test.go
git commit -m "feat: skip ignore:-matched lines before pattern matching"
```

---

### Task 4: JSON Schema (`schema/rules.schema.json`)

**Files:**
- Modify: `schema/rules.schema.json`

**Interfaces:**
- Consumes: none (structural mirror of Task 1's config shape; hand-maintained per the schema file's own `$comment`, no generated-from-Go step exists in this repo).
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Add the `ignore` property and `$defs` entry**

In `schema/rules.schema.json`, add `"ignore"` to the top-level `properties` object, alongside `"mask"`:

```json
    "mask": {
      "type": "array",
      "items": { "$ref": "#/$defs/maskRule" }
    },
    "ignore": { "$ref": "#/$defs/ignore" },
    "compression": { "$ref": "#/$defs/compression" },
```

Add a new `$defs.ignore` entry, alongside `$defs.maskRule`:

```json
    "ignore": {
      "type": "object",
      "properties": {
        "patterns": {
          "type": "array",
          "items": { "type": "string" }
        },
        "max_length": { "type": "integer", "minimum": 0 },
        "invalid_utf8": { "type": "boolean" },
        "empty": { "type": "boolean" }
      },
      "additionalProperties": false
    },
```

- [ ] **Step 2: Verify the JSON is syntactically valid**

Run: `python3 -m json.tool schema/rules.schema.json > /dev/null && echo OK`
Expected: `OK` (no parse error).

- [ ] **Step 3: Verify the embedded copy still builds and existing tests still pass**

Run: `go build ./... && go test ./...`
Expected: PASS (the schema is embedded via `//go:embed` in `schema/embed.go` with no separate build step, and nothing in this repo validates `rules.yaml` against this schema at runtime, so no Go test exercises its content directly).

- [ ] **Step 4: Commit**

```bash
git add schema/rules.schema.json
git commit -m "docs: add ignore: to rules.yaml JSON Schema"
```

---

### Task 5: Documentation (`docs/reference.md`)

**Files:**
- Modify: `docs/reference.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: Add a Contents entry**

Insert a new line right after the `Sensitive data masking` entry and before `Source location metadata`:

```markdown
- [Sensitive data masking (`mask:`)](#sensitive-data-masking-mask)
- [Line-level ignore rules (`ignore:`)](#line-level-ignore-rules-ignore)
- [Source location metadata (`meta:`)](#source-location-metadata-meta)
```

- [ ] **Step 2: Mention `ignore:` in the `rules.yaml` structure example**

In the top code block under `## rules.yaml structure`, insert a line after `mask:`:

```yaml
mask:                    # optional, see "Sensitive data masking"

ignore:                  # optional, see "Line-level ignore rules"

rules:
```

- [ ] **Step 3: Update the `unmatched.txt` format bullet**

Replace the existing bullet:

```markdown
- Lines that don't match any rule are written to `unmatched.txt` in the output directory, as `<source-file>\t<line-number>\t<raw-line>` (tab-separated). The source file column exists because multiple input files are merged into shared output, so the line number alone wouldn't identify which file a line came from. If `mask:` is configured, be aware that `unmatched.txt` can still contain unmasked sensitive data — see the caveat in [Sensitive data masking](#sensitive-data-masking-mask).
```

with:

```markdown
- Lines that don't match any rule (or that were skipped before matching by `ignore:` — see [Line-level ignore rules](#line-level-ignore-rules-ignore)) are written to `unmatched.txt` in the output directory, as `<source-file>\t<line-number>\t<reason>\t<raw-line>` (tab-separated). `reason` is `unmatched` for a line that matched no rule, or `ignored:pattern`/`ignored:max_length`/`ignored:invalid_utf8`/`ignored:empty` for one dropped by `ignore:`. The source file column exists because multiple input files are merged into shared output, so the line number alone wouldn't identify which file a line came from. If `mask:` is configured, be aware that `unmatched.txt` can still contain unmasked sensitive data — see the caveat in [Sensitive data masking](#sensitive-data-masking-mask).
```

- [ ] **Step 4: Add the new `## Line-level ignore rules (`ignore:`)` section**

Insert this new section right before `## Source location metadata (`meta:`)`:

```markdown
## Line-level ignore rules (`ignore:`)

`ignore:` is a `rules.yaml`-wide, top-level object (like `compression:`/`row_group:`, not nested under any one rule, and not a list like `mask:`) that drops raw input lines **before** any rule's `pattern` is tried against them:

\`\`\`yaml
ignore:
  patterns:
    - '^#'
    - '^\s*--'
  max_length: 100000
  invalid_utf8: true
  empty: true
\`\`\`

- **`patterns`** (list of regexps, optional): a line matching **any** of them (`regexp.MatchString`, partial match, same semantics as `mask:`'s `type: pattern`) is ignored.
- **`max_length`** (integer, optional, default 0 = unlimited): a line whose byte length exceeds this value is ignored.
- **`invalid_utf8`** (boolean, optional, default `false`): a line that isn't valid UTF-8 (`utf8.ValidString`) is ignored.
- **`empty`** (boolean, optional, default `false`): a line that's empty after trimming leading/trailing whitespace is ignored.
- The four conditions are independent — a line is ignored if **any** of them match. There's no way to `AND` them, and no per-rule override; `ignore:` is one global list applied identically to every input line, the same way `mask:` is.
- Checked in this fixed order, regardless of which order they're written in `rules.yaml`: `empty` → `invalid_utf8` → `max_length` → `patterns`. This only matters for which `reason` ends up in `unmatched.txt` (below) when more than one condition would have matched the same line.
- An ignored line is invisible to everything downstream: pattern matching, `continuation` (an open multi-line entry stays open across an ignored line — it's treated as if the line was never in the input), and `meta: source_line` numbering still counts ignored lines as physical lines, so line numbers on rows *after* an ignored line are unaffected.
- Ignored lines are written to `unmatched.txt`, not silently dropped — see the format change below.

### `unmatched.txt` format change

Adding `ignore:` changes `unmatched.txt`'s format from 3 columns to 4: `<source-file>\t<line-number>\t<reason>\t<raw-line>`. `reason` is `unmatched` for a line that matched no rule (the only case that existed before `ignore:`), or `ignored:pattern` / `ignored:max_length` / `ignored:invalid_utf8` / `ignored:empty` for a line `ignore:` dropped. **This is a breaking change** for any external script parsing `unmatched.txt` by fixed column position (e.g. `awk -F'\t' '{print $3}'` used to print the raw line and now prints the reason instead) — such scripts need to shift their column index by one, whether or not `ignore:` is actually configured (the `reason` column is always present, even when it's always `unmatched`).
```

(When writing this section, use literal triple-backtick fences for the `yaml` code block — they're written above as `\`\`\`` only to nest inside this plan's own fence.)

- [ ] **Step 5: Update the `unmatched.txt` form mentioned in Source location metadata**

Replace:

```markdown
Because `logidx import` merges multiple input files into one Parquet output, matched rows normally don't retain which input file/line they came from (`unmatched.txt` already carries this, in `<source>\t<lineNum>\t<raw>\n` form). Setting `meta:` on a field saves that information as a column:
```

with:

```markdown
Because `logidx import` merges multiple input files into one Parquet output, matched rows normally don't retain which input file/line they came from (`unmatched.txt` already carries this, in `<source>\t<lineNum>\t<reason>\t<raw>\n` form). Setting `meta:` on a field saves that information as a column:
```

- [ ] **Step 6: Proofread and commit**

Read through the diff once to confirm the new section renders correctly (matching heading levels, code fences, and that the Contents anchor link `#line-level-ignore-rules-ignore` matches GitHub's auto-generated anchor for the `## Line-level ignore rules (`ignore:`)` heading).

```bash
git add docs/reference.md
git commit -m "docs: document ignore: and the unmatched.txt format change"
```

---

### Task 6: End-to-end verification

**Files:** none (verification only).

- [ ] **Step 1: Full test suite, vet, lint, and build**

```bash
go build ./...
go vet ./...
go test ./...
golangci-lint run ./...
```

Expected: all green. If `golangci-lint` isn't available locally, at minimum run `go build`/`go vet`/`go test`.

- [ ] **Step 2: Manual smoke test of `ignore:` end to end**

```bash
mkdir -p /tmp/logidx-ignore-smoke
cat > /tmp/logidx-ignore-smoke/rules.yaml <<'EOF'
ignore:
  patterns:
    - '^#'
  max_length: 40
  empty: true

rules:
  - name: app_log
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
EOF
printf '# a comment\n\nshort line\nthis line is definitely longer than forty bytes\n' > /tmp/logidx-ignore-smoke/in.log

go run ./cmd/logidx import --rules /tmp/logidx-ignore-smoke/rules.yaml \
  --out /tmp/logidx-ignore-smoke/out /tmp/logidx-ignore-smoke/in.log

cat /tmp/logidx-ignore-smoke/out/unmatched.txt
```

Expected `unmatched.txt` contents (`--rules`/`--out` flags and the `import <input-log-file>...`/`dump <src.parquet> <dst.txt>` subcommand shapes confirmed against `cmd/logidx/main.go`):

```
/tmp/logidx-ignore-smoke/in.log	1	ignored:pattern	# a comment
/tmp/logidx-ignore-smoke/in.log	2	ignored:empty	
/tmp/logidx-ignore-smoke/in.log	4	ignored:max_length	this line is definitely longer than forty bytes
```

And `app_log.parquet` should contain exactly one row, `msg = "short line"`. Confirm with:

```bash
go run ./cmd/logidx dump /tmp/logidx-ignore-smoke/out/app_log.parquet -
```

- [ ] **Step 3: Clean up the smoke-test scratch directory**

```bash
rm -rf /tmp/logidx-ignore-smoke
```

No commit for this task — it's verification only, not a code change.
