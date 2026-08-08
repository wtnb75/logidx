# 複数行ログエントリのマージ機能 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a `rules.yaml` rule declare a `continuation` regex so that a logical log entry spanning several physical lines (e.g. macOS syslog's `Configuration Notice:` followed by indented detail lines) is folded into one Parquet row instead of one row per physical line.

**Architecture:** `fileCursor` (the existing per-file streaming scanner in `internal/convert/merge.go`) gains an `open *openEntry` field that holds at most one in-progress multi-line entry, plus a one-line `pending` read-back buffer so the line that closes an entry can be reprocessed as a fresh line. `internal/parse.Match` is split into `MatchRaw` (regex only) and `Convert` (type conversion only) so `fileCursor` can accumulate raw string captures across several lines before converting them once, at the point the entry closes.

**Tech Stack:** Go 1.25, `regexp` (standard library), existing `internal/rules`, `internal/parse`, `internal/convert`, `internal/writer` packages.

## Global Constraints

- Continuation detection is explicit-regex-only per rule (`continuation:` in rules.yaml) — no indentation-based auto-detection, and no "line matches nothing = continuation" fallback.
- The continuation regex's own named capture groups decide which field(s) receive the matched content — there is no fixed "always append to the last/message field" rule. A capture group name must match a field already declared in that rule's `fields:`, checked at startup (`Config.Validate`).
- A continuation pattern with **zero** named capture groups is valid: the line is recognized as a continuation (the entry stays open) but nothing is appended to any field — useful for absorbing decorative separator lines.
- The join separator between an entry's accumulated field value and a newly-appended continuation capture is a fixed newline (`\n`) — not configurable.
- A continuation-pattern match while no entry is currently open (orphan continuation line) is treated exactly like any other non-matching line: written to `unmatched.txt`.
- A rule without `continuation` configured behaves exactly as before (one line = one entry, immediate finalize) — this must not regress.
- Continuation never crosses between different rules: only the same rule's own `ContinuationRegexp` is tried against lines while its entry is open.
- `unmatched.txt` stays one-record-per-physical-line (`source\tlineNum\traw\n`): a multi-line entry that fails type conversion is split back into its original per-line records, never written as a single record with embedded newlines.

## File Structure

- `internal/rules/rules.go` — `Rule` gains `Continuation string` / `ContinuationRegexp *regexp.Regexp`; `Load()` compiles it.
- `internal/rules/validate.go` — `Config.Validate()` checks continuation capture-group names against declared fields.
- `internal/parse/match.go` — split into `MatchRaw` (regex + raw captures) and `Convert` (type conversion); `Match` becomes a thin wrapper of both, unchanged signature/behavior.
- `internal/convert/merge.go` — `fileCursor` gains `open`/`pending` state, `nextLine()`, `matchContinuation()`, `appendContinuation()`, `writeUnmatchedLine()`, `finalizeEntry()`, and a rewritten `advance()`. `mergeKeyField`, `candidate`, `candidateHeap`, `mergeFiles`, `advanceOrRecord`, `logFileProcessed` are unchanged.
- `cmd/logidx/main_test.go` — end-to-end test with a macOS-syslog-shaped sample.
- `README.md` — new `continuation` documentation section.

---

### Task 1: `rules.Rule` gains `continuation` config

**Files:**
- Modify: `internal/rules/rules.go`
- Test: `internal/rules/rules_test.go`

**Interfaces:**
- Produces: `rules.Rule.Continuation string` (raw pattern text, empty when unset), `rules.Rule.ContinuationRegexp *regexp.Regexp` (compiled by `Load()`, `nil` when `Continuation == ""`).

- [ ] **Step 1: Write the failing tests**

Add to `internal/rules/rules_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rules/... -run TestLoad_.*Continuation -v`
Expected: FAIL — `rule.Continuation`/`rule.ContinuationRegexp` don't exist yet (compile error).

- [ ] **Step 3: Add `Continuation`/`ContinuationRegexp` to `Rule`**

In `internal/rules/rules.go`, change the `Rule` struct:

```go
type Rule struct {
	Name    string         `yaml:"name"`
	Pattern string         `yaml:"pattern"`
	Fields  []Field        `yaml:"-"`
	Regexp  *regexp.Regexp `yaml:"-"`

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

- [ ] **Step 4: Decode `continuation` in `UnmarshalYAML`**

In `internal/rules/rules.go`, change `Rule.UnmarshalYAML`'s alias struct and assignment:

```go
func (r *Rule) UnmarshalYAML(value *yaml.Node) error {
	var alias struct {
		Name         string    `yaml:"name"`
		Pattern      string    `yaml:"pattern"`
		Continuation string    `yaml:"continuation"`
		Fields       yaml.Node `yaml:"fields"`
	}
	if err := value.Decode(&alias); err != nil {
		return err
	}
	r.Name = alias.Name
	r.Pattern = alias.Pattern
	r.Continuation = alias.Continuation
```

(the rest of the function is unchanged — it continues with the `if alias.Fields.Kind == 0 { ... }` block exactly as before.)

- [ ] **Step 5: Compile `Continuation` in `Load()`**

In `internal/rules/rules.go`, inside `Load()`'s `for i := range cfg.Rules` loop, right after `cfg.Rules[i].Regexp = re`, add:

```go
		cfg.Rules[i].Regexp = re

		if cfg.Rules[i].Continuation != "" {
			cre, err := regexp.Compile(cfg.Rules[i].Continuation)
			if err != nil {
				return nil, fmt.Errorf("rule %q: compile continuation pattern: %w", cfg.Rules[i].Name, err)
			}
			cfg.Rules[i].ContinuationRegexp = cre
		}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/rules/... -v`
Expected: PASS (all tests, including the 3 new ones and every pre-existing test in the package).

- [ ] **Step 7: Commit**

```bash
git add internal/rules/rules.go internal/rules/rules_test.go
git commit -m "Add continuation pattern config to rules.Rule"
```

---

### Task 2: Validate continuation capture-group names against declared fields

**Files:**
- Modify: `internal/rules/validate.go`
- Test: `internal/rules/validate_test.go`

**Interfaces:**
- Consumes: `rules.Rule.ContinuationRegexp` (Task 1).
- Produces: no new symbols — `Config.Validate()`'s existing contract (`error`, joined) gains one more class of violation.

- [ ] **Step 1: Write the failing tests**

Add to `internal/rules/validate_test.go`:

```go
func TestValidate_ContinuationCaptureGroupNotDeclaredFieldIsError(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	contPattern := `^  (?P<b>.*)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:               "bad",
				Pattern:            pattern,
				Regexp:             mustCompile(t, pattern),
				Continuation:       contPattern,
				ContinuationRegexp: mustCompile(t, contPattern),
				Fields:             []Field{{Name: "a", Type: "string"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Errorf("expected error to mention capture group %q, got: %v", "b", err)
	}
}

func TestValidate_ContinuationWithMatchingCaptureGroupsPasses(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	contPattern := `^  (?P<a>.*)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:               "ok",
				Pattern:            pattern,
				Regexp:             mustCompile(t, pattern),
				Continuation:       contPattern,
				ContinuationRegexp: mustCompile(t, contPattern),
				Fields:             []Field{{Name: "a", Type: "string"}},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_ContinuationWithZeroCaptureGroupsPasses(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	contPattern := `^----$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:               "ok",
				Pattern:            pattern,
				Regexp:             mustCompile(t, pattern),
				Continuation:       contPattern,
				ContinuationRegexp: mustCompile(t, contPattern),
				Fields:             []Field{{Name: "a", Type: "string"}},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rules/... -run TestValidate_Continuation -v`
Expected: FAIL — `TestValidate_ContinuationCaptureGroupNotDeclaredFieldIsError` fails because `Validate()` doesn't check continuation capture names yet (no error returned).

- [ ] **Step 3: Add the validation check**

In `internal/rules/validate.go`, inside the `for _, rule := range c.Rules` loop, right after the `for _, field := range rule.Fields { ... }` block closes (and before the `if existing, ok := firstFieldsByName[rule.Name]; ok {` block), add:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rules/... -v`
Expected: PASS (all tests in the package).

- [ ] **Step 5: Commit**

```bash
git add internal/rules/validate.go internal/rules/validate_test.go
git commit -m "Validate continuation capture-group names against declared fields"
```

---

### Task 3: Split `parse.Match` into `MatchRaw` + `Convert`

**Files:**
- Modify: `internal/parse/match.go`
- Test: `internal/parse/match_test.go`

**Interfaces:**
- Produces:
  - `parse.MatchRaw(ruleList []rules.Rule, line string) (rule *rules.Rule, raw map[string]string, ok bool)` — tries each rule's `Regexp` in order, returns the first match's rule pointer and raw (un-converted) named captures. No type conversion.
  - `parse.Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error)` — type-converts `raw` per `rule.Fields`; `now` resolves year-less timestamps.
  - `parse.Match(ruleList []rules.Rule, line string, now time.Time) (name string, values map[string]any, ok bool)` — unchanged signature and behavior, now implemented as `MatchRaw` + `Convert`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/parse/match_test.go`:

```go
func TestMatchRaw_ReturnsRawCapturesWithoutConversion(t *testing.T) {
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "time", Type: "string"},
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
		}),
	}

	rule, raw, ok := MatchRaw(ruleList, "2026-08-06T12:00:01+09:00 [INFO] user logged in")
	if !ok {
		t.Fatal("expected match, got none")
	}
	if rule.Name != "app_log" {
		t.Errorf("rule.Name = %q, want app_log", rule.Name)
	}
	if raw["level"] != "INFO" || raw["message"] != "user logged in" {
		t.Errorf("unexpected raw captures: %+v", raw)
	}
}

func TestMatchRaw_NoRuleMatches(t *testing.T) {
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^\[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
		}),
	}

	_, _, ok := MatchRaw(ruleList, "this line matches nothing")
	if ok {
		t.Error("expected no match")
	}
}

func TestConvert_SuccessConvertsEveryDeclaredField(t *testing.T) {
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

func TestConvert_TypeConversionFailureReturnsError(t *testing.T) {
	rule := mustRule(t, "strict", `^(?P<status>\S+)$`, []rules.Field{
		{Name: "status", Type: "int"},
	})
	now := time.Now()

	_, err := Convert(rule, map[string]string{"status": "not-a-number"}, now)
	if err == nil {
		t.Error("expected an error converting a non-numeric value to int")
	}
}
```

Note: `mustRule` returns `rules.Rule` (not a pointer), but `MatchRaw` internally must index into the slice to return a stable `*rules.Rule` — see Step 3.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parse/... -run 'TestMatchRaw|TestConvert' -v`
Expected: FAIL with "undefined: MatchRaw" / "undefined: Convert" (compile error).

- [ ] **Step 3: Replace `internal/parse/match.go`**

Replace the entire contents of `internal/parse/match.go` with:

```go
package parse

import (
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
// definitions. Returns an error if any field fails conversion - callers
// treat that the same way a failed match is treated (write to unmatched).
func Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error) {
	converted := make(map[string]any, len(rule.Fields))
	for _, field := range rule.Fields {
		v, err := convertValue(raw[field.Name], field, now)
		if err != nil {
			return nil, err
		}
		converted[field.Name] = v
	}
	return converted, nil
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
Expected: PASS (all tests in the package, including the pre-existing `TestMatch_*` tests — they must keep passing unmodified, proving `Match` is behavior-identical).

- [ ] **Step 5: Commit**

```bash
git add internal/parse/match.go internal/parse/match_test.go
git commit -m "Split parse.Match into MatchRaw and Convert"
```

---

### Task 4: `fileCursor` accumulates and finalizes multi-line entries

**Files:**
- Modify: `internal/convert/merge.go`
- Test: `internal/convert/merge_test.go`

**Interfaces:**
- Consumes: `parse.MatchRaw`, `parse.Convert` (Task 3); `rules.Rule.ContinuationRegexp` (Task 1).
- Produces: no new exported symbols — `fileCursor.advance()` keeps its existing signature `(cand *candidate, ok bool, err error)` and existing external behavior for rules without `Continuation`. `mergeFiles`, `candidate`, `candidateHeap` are untouched by this task.

- [ ] **Step 1: Write the failing tests**

Add `"os"` to the import block of `internal/convert/merge_test.go` (needed to read back `unmatched.txt`), then add these test functions:

```go
func TestFileCursor_Advance_MergesContinuationLinesIntoOneEntry(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: syslog
    pattern: '^TS (?P<time>\S+) (?P<host>\S+) (?P<message>.*)$'
    continuation: '^  (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      host: string
      message: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z host1 Configuration Notice:\n  ASL Module claims messages.\n  Those messages may not appear.\nTS 2026-08-06T12:00:05Z host1 next entry\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	wantMsg := "Configuration Notice:\nASL Module claims messages.\nThose messages may not appear."
	if cand.values["message"] != wantMsg {
		t.Errorf("message = %q, want %q", cand.values["message"], wantMsg)
	}
	if cand.lineNum != 1 {
		t.Errorf("lineNum = %d, want 1 (entry's starting line)", cand.lineNum)
	}

	cand2, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("second advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand2.values["message"] != "next entry" {
		t.Errorf("second message = %q, want %q", cand2.values["message"], "next entry")
	}

	_, ok, err = cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false at EOF")
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFileCursor_Advance_OrphanContinuationLineIsUnmatched(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
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

	logPath := writeFile(t, dir, "in.log", "  orphan continuation line\nTS 2026-08-06T12:00:00Z real entry\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["message"] != "real entry" {
		t.Errorf("message = %q, want %q", cand.values["message"], "real entry")
	}
	if cursor.unmatched != 1 {
		t.Errorf("unmatched = %d, want 1", cursor.unmatched)
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t1\t  orphan continuation line\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}

func TestFileCursor_Advance_MultiLineEntryFlushedAtEOF(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
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

	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z Notice:\n  continuation line\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["message"] != "Notice:\ncontinuation line" {
		t.Errorf("message = %q, want %q", cand.values["message"], "Notice:\ncontinuation line")
	}

	_, ok, err = cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false at EOF")
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFileCursor_Advance_ConversionFailureSplitsIntoIndividualUnmatchedRecords(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: counter
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    continuation: '^MORE (?P<count>\d+)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      count: int
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// Folding two "count" captures together with a newline ("5\n6") is not
	// parseable as an int, forcing a type-conversion failure on the closed
	// multi-line entry.
	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z START 5\nMORE 6\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	_, ok, err := cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false: the only entry failed conversion and became unmatched")
	}
	if cursor.unmatched != 2 {
		t.Errorf("unmatched = %d, want 2 (one record per original line)", cursor.unmatched)
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t1\tTS 2026-08-06T12:00:00Z START 5\n" + logPath + "\t2\tMORE 6\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}

func TestFileCursor_Advance_ZeroCaptureContinuationAbsorbsDecorativeLine(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: syslog
    pattern: '^TS (?P<time>\S+) (?P<message>.*)$'
    continuation: '^----$'
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

	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z hello\n----\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["message"] != "hello" {
		t.Errorf("message = %q, want %q (decorative line must not be appended)", cand.values["message"], "hello")
	}

	_, ok, err = cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false at EOF")
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
```

The pre-existing `TestFileCursor_Advance_SplitsEligibleFromIneligibleRows` and `TestFileCursor_Advance_ReturnsErrorOnMissingFile` in the same file must be left untouched — they cover rules with no `continuation` configured and are this task's regression check.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/convert/... -run TestFileCursor_Advance -v`
Expected: the 5 new tests FAIL (wrong `message`/`unmatched` values — `advance()` doesn't fold continuation lines yet); the 2 pre-existing tests still PASS.

- [ ] **Step 3: Replace `internal/convert/merge.go`**

Replace the entire contents of `internal/convert/merge.go` with:

```go
package convert

import (
	"bufio"
	"container/heap"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"logidx/internal/parse"
	"logidx/internal/rules"
	"logidx/internal/writer"
)

// mergeKeyField returns, for each distinct rule name in ruleList, the name
// of its first Type == "timestamp" field in declaration order — the field
// internal/convert.mergeFiles uses to globally order that rule's matched
// rows across every input file. Rules with no timestamp field are omitted
// from the result; their matched rows are written in plain file-arrival
// order instead (see fileCursor.advance).
func mergeKeyField(ruleList []rules.Rule) map[string]string {
	result := map[string]string{}
	for _, r := range ruleList {
		if _, exists := result[r.Name]; exists {
			continue
		}
		for _, field := range r.Fields {
			if field.Type == "timestamp" {
				result[r.Name] = field.Name
				break
			}
		}
	}
	return result
}

// candidate is one matched row held back from immediate writing because
// its rule has a merge key (see mergeKeyField): mergeFiles compares
// candidates from every open fileCursor and writes the earliest one first.
type candidate struct {
	cursor    *fileCursor
	name      string
	values    map[string]any
	sortValue time.Time
	lineNum   int
}

// scannedLine is one physical line read from a fileCursor's underlying
// scanner, tagged with its 1-based line number.
type scannedLine struct {
	text    string
	lineNum int
}

// openEntry accumulates one in-progress multi-line log entry: a matched
// rule plus its raw (un-converted) field captures, updated in place as
// continuation lines are folded in. rawLines keeps every physical line
// that contributed to the entry, in original order, so a type-conversion
// failure can still report each one as its own unmatched.txt record (see
// fileCursor.finalizeEntry).
type openEntry struct {
	rule     *rules.Rule
	raw      map[string]string
	rawLines []scannedLine
}

// fileCursor scans one input file's lines in order. Lines that don't match
// any rule, or that match a rule with no merge key, are written
// immediately as advance() passes over them — exactly like the old
// sequential processInput did. Lines that match a rule with a merge key
// are held as the cursor's returned candidate instead, so mergeFiles can
// compare candidates across every input file before any of them is
// actually written.
//
// A rule with a Continuation pattern (see rules.Rule) can span several
// physical lines: while one of its entries is open (open != nil),
// subsequent lines are matched against the rule's ContinuationRegexp
// instead of the full rule list, and folded into the entry, until a
// non-continuation line, a new rule match, or EOF closes it. pending holds
// one line read back so the line that closed an entry can be reprocessed
// from scratch as a fresh candidate for a new entry.
//
// logger must be non-nil: advance() logs through it unconditionally.
type fileCursor struct {
	inputPath string
	fileIndex int
	file      *os.File // nil when reading os.Stdin
	scanner   *bufio.Scanner
	lineNum   int

	cfg      *rules.Config
	mergeKey map[string]string
	set      *writer.Set
	logger   *slog.Logger
	now      time.Time

	counts    map[string]int
	unmatched int

	open    *openEntry
	pending *scannedLine
}

// newFileCursor opens inputPath (or os.Stdin if inputPath is "-") and
// returns a cursor ready for advance(). fileIndex is inputPath's position
// among the inputPaths mergeFiles was given, used only to break ties when
// two candidates from different files have the exact same sortValue.
func newFileCursor(inputPath string, fileIndex int, cfg *rules.Config, mergeKey map[string]string, set *writer.Set, logger *slog.Logger, now time.Time) (*fileCursor, error) {
	var f *os.File
	in := io.Reader(os.Stdin)
	if inputPath != "-" {
		var err error
		f, err = os.Open(inputPath)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		in = f
	}

	return &fileCursor{
		inputPath: inputPath,
		fileIndex: fileIndex,
		file:      f,
		scanner:   bufio.NewScanner(in),
		cfg:       cfg,
		mergeKey:  mergeKey,
		set:       set,
		logger:    logger,
		now:       now,
		counts:    map[string]int{},
	}, nil
}

// nextLine returns the next physical line to process: a previously pushed
// back line, if any (see fileCursor.pending), otherwise the next line from
// the underlying scanner. ok is false at EOF.
func (c *fileCursor) nextLine() (line scannedLine, ok bool, err error) {
	if c.pending != nil {
		line = *c.pending
		c.pending = nil
		return line, true, nil
	}
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return scannedLine{}, false, fmt.Errorf("read input: %w", err)
		}
		return scannedLine{}, false, nil
	}
	c.lineNum++
	return scannedLine{text: c.scanner.Text(), lineNum: c.lineNum}, true, nil
}

// matchContinuation tries rule's continuation pattern against a line and,
// if it matches, returns its named captures. The returned map is non-nil
// but may be empty: a continuation pattern with zero named capture groups
// is valid (it still ends the search for a new entry and keeps this one
// open) and simply contributes nothing to any field — useful for
// absorbing decorative separator lines.
func matchContinuation(rule *rules.Rule, line string) (raw map[string]string, matched bool) {
	m := rule.ContinuationRegexp.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	raw = map[string]string{}
	for i, name := range rule.ContinuationRegexp.SubexpNames() {
		if i == 0 || name == "" {
			continue
		}
		raw[name] = m[i]
	}
	return raw, true
}

// appendContinuation folds a continuation line's captures into entry's
// accumulated raw values: each captured field is newline-joined onto its
// existing value, or set outright if this is that field's first
// contribution.
func appendContinuation(entry *openEntry, raw map[string]string) {
	for name, v := range raw {
		if existing, ok := entry.raw[name]; ok {
			entry.raw[name] = existing + "\n" + v
		} else {
			entry.raw[name] = v
		}
	}
}

// writeUnmatchedLine writes one physical line to the shared unmatched.txt
// sidecar and updates this cursor's unmatched count.
func (c *fileCursor) writeUnmatchedLine(line scannedLine) error {
	if err := c.set.WriteUnmatched(c.inputPath, line.lineNum, line.text); err != nil {
		return fmt.Errorf("write unmatched line %d: %w", line.lineNum, err)
	}
	c.unmatched++
	return nil
}

// finalizeEntry converts entry's accumulated raw values and disposes of
// the result. A type-conversion failure splits the entry back into its
// original per-line unmatched.txt records instead of writing one record
// with embedded newlines, preserving unmatched.txt's one-record-per-line
// format. A successfully converted row is either returned as a candidate
// (its rule has a merge key, see mergeKeyField) for the caller to hand to
// mergeFiles, or written immediately otherwise — the same two outcomes a
// single-line match without Continuation configured has always had. The
// returned error is only non-nil for a genuine write/I-O failure; a
// conversion failure is reported by returning (nil, nil), same as any
// other row finalizeEntry disposed of by writing it out itself.
func (c *fileCursor) finalizeEntry(entry *openEntry) (*candidate, error) {
	values, convErr := parse.Convert(*entry.rule, entry.raw, c.now)
	if convErr != nil {
		c.logger.Debug("multi-line entry failed type conversion", "file", c.inputPath, "rule", entry.rule.Name, "start_line", entry.rawLines[0].lineNum, "error", convErr)
		for _, rl := range entry.rawLines {
			if err := c.writeUnmatchedLine(rl); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}

	name := entry.rule.Name
	startLine := entry.rawLines[0].lineNum

	keyField, hasMergeKey := c.mergeKey[name]
	if !hasMergeKey {
		if err := c.set.WriteMatched(name, values); err != nil {
			return nil, fmt.Errorf("write matched row (rule %q, line %d): %w", name, startLine, err)
		}
		c.counts[name]++
		return nil, nil
	}

	sortValue, isTime := values[keyField].(time.Time)
	if !isTime {
		// Defensively unreachable: parse.Convert and rules.Validate
		// guarantee a timestamp-typed field always yields a time.Time. If
		// this ever did fire, degrade to skipping just this one row rather
		// than aborting the rest of the file.
		c.logger.Error("merge key value is not a timestamp, skipping row", "rule", name, "field", keyField, "file", c.inputPath, "line", startLine)
		return nil, nil
	}
	c.counts[name]++
	return &candidate{cursor: c, name: name, values: values, sortValue: sortValue, lineNum: startLine}, nil
}

// advance reads forward from where it last stopped until it finds a row
// eligible for merging, writing every ineligible row it passes along the
// way (unmatched lines to the shared sidecar, matched-but-no-merge-key
// rows straight to their rule's writer). A rule with Continuation
// configured accumulates matching lines into an open entry (see
// fileCursor.open) instead of finalizing on the first line; the entry is
// finalized once a non-continuation line, a fresh rule match, or EOF ends
// it. ok is false once the file is exhausted, at which point every one of
// its rows has been written or returned as a candidate — there is nothing
// left to do with this cursor but close() it.
func (c *fileCursor) advance() (*candidate, bool, error) {
	for {
		line, hasLine, err := c.nextLine()
		if err != nil {
			return nil, false, err
		}
		if !hasLine {
			if c.open == nil {
				return nil, false, nil
			}
			entry := c.open
			c.open = nil
			cand, err := c.finalizeEntry(entry)
			if err != nil {
				return nil, false, err
			}
			return cand, cand != nil, nil
		}

		if c.open != nil {
			if raw, matched := matchContinuation(c.open.rule, line.text); matched {
				appendContinuation(c.open, raw)
				c.open.rawLines = append(c.open.rawLines, line)
				continue
			}

			entry := c.open
			c.open = nil
			c.pending = &line
			cand, err := c.finalizeEntry(entry)
			if err != nil {
				return nil, false, err
			}
			if cand != nil {
				return cand, true, nil
			}
			continue
		}

		rule, raw, matched := parse.MatchRaw(c.cfg.Rules, line.text)
		if !matched {
			c.logger.Debug("line did not match any rule", "file", c.inputPath, "line", line.lineNum)
			if err := c.writeUnmatchedLine(line); err != nil {
				return nil, false, err
			}
			continue
		}

		if rule.ContinuationRegexp != nil {
			c.open = &openEntry{rule: rule, raw: raw, rawLines: []scannedLine{line}}
			continue
		}

		cand, err := c.finalizeEntry(&openEntry{rule: rule, raw: raw, rawLines: []scannedLine{line}})
		if err != nil {
			return nil, false, err
		}
		if cand != nil {
			return cand, true, nil
		}
	}
}

// close closes the underlying file, if any (nothing to close for os.Stdin).
func (c *fileCursor) close() error {
	if c.file == nil {
		return nil
	}
	return c.file.Close()
}

// candidateHeap is a min-heap of candidates ordered by sortValue, with the
// originating file's position among mergeFiles' inputPaths as a tiebreak,
// so two candidates with the exact same timestamp still pop in a fixed,
// repeatable order across runs.
type candidateHeap []*candidate

func (h candidateHeap) Len() int { return len(h) }

func (h candidateHeap) Less(i, j int) bool {
	if !h[i].sortValue.Equal(h[j].sortValue) {
		return h[i].sortValue.Before(h[j].sortValue)
	}
	return h[i].cursor.fileIndex < h[j].cursor.fileIndex
}

func (h candidateHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *candidateHeap) Push(x any) {
	*h = append(*h, x.(*candidate))
}

func (h *candidateHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// mergeFiles processes every input, merging rows from rules with a merge
// key (see mergeKeyField) into ascending-timestamp order across all inputs
// combined, while rows from rules without one are written in each file's
// own arrival order — matching Files' pre-merge behavior exactly when no
// rule has a merge key at all, or when there's only one input file (the
// heap never holds more than one candidate at a time in either case).
//
// Processing continues past a failed input: its cursor is dropped from the
// merge and its error is joined into the returned error, so one bad input
// doesn't stop the others from being merged and written.
func mergeFiles(inputPaths []string, cfg *rules.Config, set *writer.Set, logger *slog.Logger, now time.Time) error {
	mergeKey := mergeKeyField(cfg.Rules)

	var errs []error
	h := candidateHeap{}

	for i, inputPath := range inputPaths {
		cursor, err := newFileCursor(inputPath, i, cfg, mergeKey, set, logger, now)
		if err != nil {
			logger.Error("failed to process file", "file", inputPath, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", inputPath, err))
			continue
		}
		advanceOrRecord(cursor, &h, logger, &errs)
	}

	for h.Len() > 0 {
		cand := heap.Pop(&h).(*candidate)
		if err := set.WriteMatched(cand.name, cand.values); err != nil {
			err = fmt.Errorf("write matched row (rule %q, line %d): %w", cand.name, cand.lineNum, err)
			logger.Error("failed to process file", "file", cand.cursor.inputPath, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", cand.cursor.inputPath, err))
			if closeErr := cand.cursor.close(); closeErr != nil {
				closeErr = fmt.Errorf("%s: close: %w", cand.cursor.inputPath, closeErr)
				logger.Error("failed to close input file", "file", cand.cursor.inputPath, "error", closeErr)
				errs = append(errs, closeErr)
			}
			continue
		}

		advanceOrRecord(cand.cursor, &h, logger, &errs)
	}

	return errors.Join(errs...)
}

// advanceOrRecord calls cursor.advance(), pushing a new candidate onto h on
// success. Once the cursor has nothing left to contribute (EOF or error) it
// closes the cursor itself — logging and recording any close error onto
// errs the same way as every other exit path in mergeFiles — and, for EOF,
// logs its "file processed" summary.
func advanceOrRecord(cursor *fileCursor, h *candidateHeap, logger *slog.Logger, errs *[]error) {
	cand, ok, err := cursor.advance()
	if err != nil {
		logger.Error("failed to process file", "file", cursor.inputPath, "error", err)
		*errs = append(*errs, fmt.Errorf("%s: %w", cursor.inputPath, err))
		if closeErr := cursor.close(); closeErr != nil {
			closeErr = fmt.Errorf("%s: close: %w", cursor.inputPath, closeErr)
			logger.Error("failed to close input file", "file", cursor.inputPath, "error", closeErr)
			*errs = append(*errs, closeErr)
		}
		return
	}
	if !ok {
		logFileProcessed(logger, cursor)
		if closeErr := cursor.close(); closeErr != nil {
			closeErr = fmt.Errorf("%s: close: %w", cursor.inputPath, closeErr)
			logger.Error("failed to close input file", "file", cursor.inputPath, "error", closeErr)
			*errs = append(*errs, closeErr)
		}
		return
	}
	heap.Push(h, cand)
}

// logFileProcessed logs the same "file processed" summary the old
// sequential processInput logged once it finished a file: its own
// per-rule-name match counts (not the merged Set's running totals) and how
// many of its lines matched no rule.
func logFileProcessed(logger *slog.Logger, c *fileCursor) {
	args := []any{"file", c.inputPath}
	for name, count := range c.counts {
		args = append(args, name, count)
	}
	args = append(args, "unmatched", c.unmatched)
	logger.Info("file processed", args...)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/convert/... -v`
Expected: PASS (every test in the package, including all pre-existing `TestFiles_*`/`TestFileCursor_*`/`TestMergeKeyField_*` tests — these prove no-`continuation` behavior is unchanged).

- [ ] **Step 5: Run the full test suite and vet/lint**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass, no vet warnings.

- [ ] **Step 6: Commit**

```bash
git add internal/convert/merge.go internal/convert/merge_test.go
git commit -m "Fold continuation lines into one entry in fileCursor.advance"
```

---

### Task 5: End-to-end CLI test with a macOS-syslog-shaped sample

**Files:**
- Modify: `cmd/logidx/main_test.go`

**Interfaces:**
- Consumes: `run()` (existing CLI entrypoint used by every other test in the file), `pqinfo.Read` (existing).
- Produces: nothing new — this is a black-box test of the whole `import`→`dump` pipeline.

- [ ] **Step 1: Write the failing test**

Add to `cmd/logidx/main_test.go`:

```go
const macSyslogRulesYAML = `
rules:
  - name: syslog
    pattern: '^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<process>\S+): (?P<message>.*)$'
    continuation: '^\s+(?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "syslog"
      host: string
      process: string
      message: string
`

const macSyslogSample = `Aug  8 00:30:05 WatanabenoMacBook-Pro syslogd[149]: Configuration Notice:
        ASL Module "com.apple.cdscheduler" claims selected messages.
        Those messages may not appear in standard system log files or in the ASL database.
Aug  8 00:30:10 WatanabenoMacBook-Pro syslogd[149]: single line entry
`

func TestRun_ImportMergesMultiLineSyslogEntryIntoOneRow(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", macSyslogRulesYAML)
	logPath := writeFile(t, dir, "syslog.log", macSyslogSample)
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	outPath := filepath.Join(outDir, "syslog.parquet")
	info, err := pqinfo.Read(outPath)
	if err != nil {
		t.Fatalf("pqinfo.Read(%s): %v", outPath, err)
	}
	if info.NumRows != 2 {
		t.Fatalf("NumRows = %d, want 2 (one merged multi-line entry + one single-line entry)", info.NumRows)
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", outPath, "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 3 { // 1 header + 2 rows
		t.Fatalf("expected 3 dump lines (header + 2 rows), got %d: %q", len(lines), stdout.String())
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &first); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[1], err)
	}
	wantMsg := "Configuration Notice:\n" +
		`ASL Module "com.apple.cdscheduler" claims selected messages.` + "\n" +
		"Those messages may not appear in standard system log files or in the ASL database."
	if first["message"] != wantMsg {
		t.Errorf("first row message = %q, want %q", first["message"], wantMsg)
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &second); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[2], err)
	}
	if second["message"] != "single line entry" {
		t.Errorf("second row message = %q, want %q", second["message"], "single line entry")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/logidx/... -run TestRun_ImportMergesMultiLineSyslogEntryIntoOneRow -v`
Expected: FAIL before Tasks 1-4 land (n/a if run after them — at that point this is the first test written against the finished feature, so run it once as a sanity check that it currently PASSES; if it fails, that means Tasks 1-4 have a gap this test exposes).

- [ ] **Step 3: Run the full test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/logidx/main_test.go
git commit -m "Add end-to-end test for multi-line syslog entry merging"
```

---

### Task 6: Document `continuation` in README

**Files:**
- Modify: `README.md`

**Interfaces:**
- None (documentation only).

- [ ] **Step 1: Add the `continuation` section**

In `README.md`, insert a new subsection right after the existing `### 複数ファイルのマージ順` section and before `### info: Parquetファイルの中身を見る` (i.e. right after the `k-wayマージの性質上...` paragraph, before the `### info:` heading):

```markdown
### 複数行ログエントリのマージ(`continuation`)

1つの論理ログエントリが複数行にまたがる場合(macOSのsyslogの`Configuration Notice:`に続くインデント行など)、ルールに`continuation`(継続行を検出する正規表現)を設定すると、継続行の内容を該当フィールドへ改行(`\n`)区切りで追記してから1つのParquet行として書き込む。

```yaml
rules:
  - name: syslog
    pattern: '^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<process>\S+): (?P<message>.*)$'
    continuation: '^\s+(?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: syslog
      host: string
      process: string
      message: string
```

- `continuation`の名前付きキャプチャグループが、追記先のフィールドを表す(`fields:`に宣言済みのフィールド名と一致している必要があり、一致しない場合は起動時エラーになる)。1つの継続行パターンに複数の名前付きキャプチャを持たせて、同じ継続行で複数フィールドへ同時に追記することもできる。
- 名前付きキャプチャが0個の継続行パターンも書ける。その行はエントリの継続として認識される(エントリはまだ確定しない)が、どのフィールドにも追記されない — 装飾的な区切り行を読み飛ばしたい場合に使える。
- 連結時の区切り文字は改行(`\n`)固定(設定不可)。
- 継続行パターンにマッチしない行・新しいエントリの開始行・ファイル末尾のいずれかに到達した時点でエントリが確定し、型変換される。
- まだどのエントリも開いていない状態で継続行パターンにのみマッチする行(孤立継続行)は、通常の未マッチ行と同様に`unmatched.txt`へ書かれる。
- 確定に失敗した(型変換エラーになった)複数行エントリは、`unmatched.txt`の1行1レコード形式を保つため、蓄積していた元の行それぞれを個別のレコードとして(各行本来の行番号で)書き出す。
- `continuation`を設定しないルールの挙動は従来通り(1行=1エントリ)。
```

- [ ] **Step 2: Verify the README renders sensibly**

Run: `rg -n "^### " README.md` and confirm the new heading appears between `複数ファイルのマージ順` and `info: Parquetファイルの中身を見る`.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "Document the continuation multi-line log entry config"
```
