# マッチ行にソースファイル名・行番号を保存する `meta` フィールド Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a `rules.yaml` field opt in to `meta: source_file` or `meta: source_line` so its value comes from the input line's originating file path / 1-based line number instead of a pattern capture group or structured data, and that value is written as an ordinary Parquet column (`unmatched.txt` is out of scope and unchanged).

**Architecture:** `rules.Field` gains a `Meta string` attribute, validated at `rules.Validate()` time (type must match the meta kind, mutually exclusive with `key:`/`extra:`, skips the "needs a matching capture group" check like `key:`/`extra:` already do, but — unlike `key:`/`extra:` — does NOT require the rule to declare `structured:`, since a meta field never reads structured data). `internal/parse.Convert`/`MatchAndConvert` gain a new `source SourceMeta` parameter (a `{File string; Line int}` struct) and resolve `field.Meta` in the same value-source switch that already branches on `field.Extra`/`field.Key`, before falling through to the existing `convertValue` pipeline (so `type:`/`replace:`/`normalize:` all apply to meta fields exactly like any other field). `internal/convert/merge.go` passes each cursor's real input path and the correct line number (the entry's *first* physical line for continuation-merged entries) at its two call sites. Design doc: `docs/superpowers/specs/2026-08-10-source-location-fields-design.md`.

**Tech Stack:** Go 1.x, `gopkg.in/yaml.v3`, table-driven tests (existing style in `internal/rules/validate_test.go`, `internal/parse/match_test.go`, `internal/convert/merge_test.go`).

## Global Constraints

- `Field.Meta` accepts exactly three values: `""` (not a meta field), `"source_file"`, `"source_line"`. Any other non-empty value is a startup validation error.
- `meta: source_file` requires `type: string`; `meta: source_line` requires `type: int`. A mismatch is a startup validation error.
- `field.Meta != ""` combined with `field.Key != ""` or `field.Extra == true` on the same field is a startup validation error (value source must be exactly one of: pattern capture / structured key / structured extra / meta).
- A meta field is exempted from the "field has no matching named capture group in pattern" check (like `key:`/`extra:` fields already are) — **but, unlike `key:`/`extra:`, a meta field must NOT require `rule.Structured != nil`.** A meta field never reads structured data; a rule with a meta field and no `structured:` block at all must validate cleanly. Do not fold `field.Meta != ""` into the same boolean that gates the "rule has no structured config" check — that check must stay keyed on `field.Key != "" || field.Extra` alone.
- A `continuation:` pattern's named capture group targeting a meta field is a startup validation error, for the same reason it's already an error for `key:`/`extra:` fields (the continuation pattern's capture would silently do nothing, since the field's value never comes from a capture group).
- `parse.SourceMeta{File string; Line int}` is the exact type. `Convert`'s and `MatchAndConvert`'s new parameter is named `source SourceMeta`, inserted immediately before the existing `now time.Time` parameter (not at the end, not before `raw`/`line`).
- `meta: source_line`'s value flows through the same `convertValue` → `strconv.ParseInt` path as any other `int`-typed field: `Convert` sets `rawValue = strconv.Itoa(source.Line)` (a string), not `source.Line` (an int) directly into the output map.
- For a continuation-merged entry, `meta: source_line`'s value is the entry's first physical line number (`entry.rawLines[0].lineNum`), never a continuation line's own number.
- `unmatched.txt`'s format is unchanged — it already carries file/line via `writer.Set.WriteUnmatched`. This plan touches only the Parquet (matched-row) output path.
- `meta:` is a per-field opt-in. No rule gets a meta column unless a field explicitly declares `meta:`. Existing `rules.yaml` files with no `meta:` fields are unaffected.

---

## File Structure

- `internal/rules/rules.go` — add `Field.Meta string` (`yaml:"meta"`) and the `FieldMetaSourceFile`/`FieldMetaSourceLine` constants.
- `internal/rules/validate.go` — add the meta validation rules described above.
- `internal/rules/validate_test.go` — unit tests for every new validation rule.
- `internal/parse/match.go` — add `SourceMeta`, extend `Convert`/`MatchAndConvert` signatures, add the meta branch to the value-source switch.
- `internal/parse/match_test.go` — update every existing `Convert`/`MatchAndConvert` call site for the new parameter; add new tests for meta resolution.
- `internal/parse/presets_test.go` — update its one `MatchAndConvert` call site.
- `internal/convert/merge.go` — pass `parse.SourceMeta{...}` at `finalizeEntry`'s and `advance`'s call sites.
- `internal/convert/merge_test.go` — integration tests proving `meta` columns carry the real input path and correct line numbers (including the continuation-entry case).
- `README.md` — document `meta:` near the existing `key:`/`extra:` section.

---

## Task 1: `rules.Field.Meta` and validation

**Files:**
- Modify: `internal/rules/rules.go`
- Modify: `internal/rules/validate.go`
- Test: `internal/rules/validate_test.go`

**Interfaces:**
- Produces: `rules.Field.Meta string` (new exported field, `yaml:"meta"` tag), `rules.FieldMetaSourceFile = "source_file"`, `rules.FieldMetaSourceLine = "source_line"` (new exported constants). `rules.Config.Validate()` rejects the cases in Global Constraints.
- Consumes: nothing new — this task only touches `internal/rules`.

- [ ] **Step 1: Add `Field.Meta` and the constants**

In `internal/rules/rules.go`, add `Meta` to the `Field` struct (right after the existing `Key`/`Extra` fields, around line 66):

```go
	// Key, if set, takes this field's raw value from the rule's parsed
	// structured data (see Rule.Structured) under this key name, instead
	// of from a same-named pattern capture group.
	Key string `yaml:"key"`
	// Extra, if true, collects every structured-data key not consumed by
	// another field's Key into this field as a JSON string. At most one
	// field per rule may set Extra.
	Extra bool `yaml:"extra"`
	// Meta, if set to FieldMetaSourceFile or FieldMetaSourceLine, takes
	// this field's raw value from the current input line's source
	// metadata (see parse.SourceMeta) instead of a pattern capture group
	// or structured data. Unlike Key/Extra, a Meta field never reads
	// structured data, so it does not require the rule to declare
	// Structured. Empty for every field that isn't opted in.
	Meta string `yaml:"meta"`
```

Add the constants near the top of the file, after the imports and before `NormalizeRule` (or any other reasonable top-level spot consistent with the file's existing organization):

```go
// FieldMetaSourceFile and FieldMetaSourceLine are the only two values
// Field.Meta accepts. See parse.SourceMeta for how they're resolved.
const (
	FieldMetaSourceFile = "source_file"
	FieldMetaSourceLine = "source_line"
)
```

- [ ] **Step 2: Write the failing validation tests**

Add to `internal/rules/validate_test.go` (place near the existing `TestValidate_Key*`/`TestValidate_Extra*` tests, e.g. after `TestValidate_KeyFieldDoesNotRequireMatchingCaptureGroup` at line 451):

```go
func TestValidate_MetaSourceFileValidConfigPasses(t *testing.T) {
	pattern := `^(?P<remote>\S+) (?P<msg>.*)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "access",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields: []Field{
					{Name: "remote", Type: "string"},
					{Name: "msg", Type: "string"},
					{Name: "log_file", Type: "string", Meta: FieldMetaSourceFile},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_MetaSourceLineValidConfigPasses(t *testing.T) {
	pattern := `^(?P<remote>\S+) (?P<msg>.*)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "access",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields: []Field{
					{Name: "remote", Type: "string"},
					{Name: "msg", Type: "string"},
					{Name: "log_line", Type: "int", Meta: FieldMetaSourceLine},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_MetaFieldDoesNotRequireStructuredConfig(t *testing.T) {
	// Unlike key:/extra:, meta: never reads from structured data, so a
	// rule with no structured: block at all must still pass validation.
	pattern := `^(?P<remote>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "ok",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields: []Field{
					{Name: "remote", Type: "string"},
					{Name: "log_file", Type: "string", Meta: FieldMetaSourceFile},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error (meta fields don't require structured:), got: %v", err)
	}
}

func TestValidate_MetaSourceFileWrongTypeIsError(t *testing.T) {
	pattern := `^(?P<remote>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields: []Field{
					{Name: "remote", Type: "string"},
					{Name: "log_file", Type: "int", Meta: FieldMetaSourceFile},
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "log_file") {
		t.Errorf("expected error mentioning field %q, got: %v", "log_file", err)
	}
}

func TestValidate_MetaSourceLineWrongTypeIsError(t *testing.T) {
	pattern := `^(?P<remote>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields: []Field{
					{Name: "remote", Type: "string"},
					{Name: "log_line", Type: "string", Meta: FieldMetaSourceLine},
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "log_line") {
		t.Errorf("expected error mentioning field %q, got: %v", "log_line", err)
	}
}

func TestValidate_UnsupportedMetaValueIsError(t *testing.T) {
	pattern := `^(?P<remote>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "bad",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields: []Field{
					{Name: "remote", Type: "string"},
					{Name: "weird", Type: "string", Meta: "source_host"},
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "source_host") {
		t.Errorf("expected error mentioning unsupported meta value %q, got: %v", "source_host", err)
	}
}

func TestValidate_MetaAndKeyBothSetIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields:     []Field{{Name: "weird", Type: "string", Key: "level", Meta: FieldMetaSourceFile}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "weird") {
		t.Errorf("expected error mentioning field %q, got: %v", "weird", err)
	}
}

func TestValidate_MetaAndExtraBothSetIsError(t *testing.T) {
	pattern := `^(?P<json>\{.*\})$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:       "bad",
				Pattern:    pattern,
				Regexp:     mustCompile(t, pattern),
				Structured: &StructuredConfig{Source: "json", Format: "json"},
				Fields:     []Field{{Name: "weird", Type: "string", Extra: true, Meta: FieldMetaSourceFile}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "weird") {
		t.Errorf("expected error mentioning field %q, got: %v", "weird", err)
	}
}

func TestValidate_MetaFieldDoesNotRequireMatchingCaptureGroup(t *testing.T) {
	pattern := `^(?P<remote>\S+)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:    "ok",
				Pattern: pattern,
				Regexp:  mustCompile(t, pattern),
				Fields: []Field{
					{Name: "remote", Type: "string"},
					{Name: "log_file_not_a_capture_group", Type: "string", Meta: FieldMetaSourceFile},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_ContinuationCaptureGroupTargetingMetaFieldIsError(t *testing.T) {
	pattern := `^(?P<a>\S+)$`
	contPattern := `^\s+(?P<log_file>.*)$`
	cfg := &Config{
		Rules: []Rule{
			{
				Name:               "bad",
				Pattern:            pattern,
				Regexp:             mustCompile(t, pattern),
				Continuation:       contPattern,
				ContinuationRegexp: mustCompile(t, contPattern),
				Fields: []Field{
					{Name: "a", Type: "string"},
					{Name: "log_file", Type: "string", Meta: FieldMetaSourceFile},
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error: continuation targets a field sourced from meta")
	}
	if !strings.Contains(err.Error(), "log_file") {
		t.Errorf("expected error to mention field %q, got: %v", "log_file", err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/rules/... -run TestValidate_Meta -v` and `go test ./internal/rules/... -run TestValidate_ContinuationCaptureGroupTargetingMetaFieldIsError -v`
Expected: the package compiles (Step 1 already added `Field.Meta` and the constants), but the tests FAIL on assertions: the "should error" tests (e.g. `TestValidate_MetaSourceFileWrongTypeIsError`, `TestValidate_UnsupportedMetaValueIsError`) get `err == nil` because `validate.go` doesn't check `Meta` yet, and `TestValidate_ContinuationCaptureGroupTargetingMetaFieldIsError` similarly gets no error. This is the expected RED state — proceed to Step 4.

- [ ] **Step 4: Write the validation logic**

In `internal/rules/validate.go`, inside the per-field loop (replacing the current block from `usesStructured := field.Key != "" || field.Extra` through `if field.Extra { extraCount++ }`, currently lines 56-74):

```go
		extraCount := 0
		for _, field := range rule.Fields {
			usesStructured := field.Key != "" || field.Extra
			usesDerived := usesStructured || field.Meta != ""
			if !usesDerived && !captureNames[field.Name] {
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

			switch field.Meta {
			case "":
				// not a meta field
			case FieldMetaSourceFile:
				if field.Type != "string" {
					errs = append(errs, fmt.Errorf("rule %q: field %q has meta: source_file but type %q (must be string)", rule.Name, field.Name, field.Type))
				}
			case FieldMetaSourceLine:
				if field.Type != "int" {
					errs = append(errs, fmt.Errorf("rule %q: field %q has meta: source_line but type %q (must be int)", rule.Name, field.Name, field.Type))
				}
			default:
				errs = append(errs, fmt.Errorf("rule %q: field %q has unsupported meta value %q (must be %q or %q)", rule.Name, field.Name, field.Meta, FieldMetaSourceFile, FieldMetaSourceLine))
			}
			if field.Meta != "" && (field.Key != "" || field.Extra) {
				errs = append(errs, fmt.Errorf("rule %q: field %q sets both meta and key/extra", rule.Name, field.Name))
			}
		}
```

Note `usesDerived` (not `usesStructured`) is what gates the capture-group check; `usesStructured` is unchanged and still alone gates the "rule has no structured config" check two lines below it. This split is deliberate — see this task's Global Constraints entry about it.

Then, in the continuation-pattern loop (currently around line 114), change:

```go
				if field.Key != "" || field.Extra {
					errs = append(errs, fmt.Errorf("rule %q: continuation pattern has named capture group %q targets field %q, which takes its value from structured data (key/extra) instead of the pattern", rule.Name, n, n))
				}
```

to:

```go
				if field.Key != "" || field.Extra || field.Meta != "" {
					errs = append(errs, fmt.Errorf("rule %q: continuation pattern has named capture group %q targets field %q, which takes its value from structured data (key/extra) or source metadata (meta) instead of the pattern", rule.Name, n, n))
				}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/rules/... -run TestValidate_Meta -v` and `go test ./internal/rules/... -run TestValidate_ContinuationCaptureGroupTargetingMetaFieldIsError -v`
Expected: PASS

- [ ] **Step 6: Run the full package test suite to check for regressions**

Run: `go test ./internal/rules/...`
Expected: PASS (all existing tests unaffected — in particular `TestValidate_KeyFieldWithoutStructuredIsError` and `TestValidate_ExtraFieldWithoutStructuredIsError` must still fail validation for `key:`/`extra:` fields without `structured:`, proving `usesStructured` wasn't accidentally widened)

- [ ] **Step 7: Commit**

```bash
git add internal/rules/rules.go internal/rules/validate.go internal/rules/validate_test.go
git commit -m "feat(rules): add meta: source_file/source_line field attribute and validation"
```

---

## Task 2: `parse.SourceMeta` and `Convert`/`MatchAndConvert` signature extension

**Files:**
- Modify: `internal/parse/match.go`
- Modify: `internal/parse/match_test.go`
- Modify: `internal/parse/presets_test.go`

**Interfaces:**
- Consumes: `rules.Field.Meta`, `rules.FieldMetaSourceFile`, `rules.FieldMetaSourceLine` from Task 1.
- Produces: `parse.SourceMeta{File string; Line int}` (new exported type). `parse.Convert(rule rules.Rule, raw map[string]string, source SourceMeta, now time.Time) (map[string]any, error)` and `parse.MatchAndConvert(ruleList []rules.Rule, line string, source SourceMeta, now time.Time) (*rules.Rule, map[string]string, map[string]any, []MatchAttempt, bool)` — both gain the `source SourceMeta` parameter positioned immediately before `now`. Task 3's `internal/convert/merge.go` will call these with real `SourceMeta{File: ..., Line: ...}` values.

This task touches only `internal/parse`. It has two parts: (A) the actual feature (the `SourceMeta` type, the signature change, the new switch branch), and (B) a mechanical update of every existing call site in this package's own test files, since both functions are exported and this is a breaking signature change. Do part (A) first with TDD, then part (B) — the Go compiler will refuse to build until every call site is fixed, so part (B) is self-verifying: `go build ./...` / `go vet ./...` will list every remaining broken call site.

- [ ] **Step 1: Write the failing tests for the new meta-resolution behavior**

Add to `internal/parse/match_test.go` (near the existing `TestConvert_KeyFieldTakesValueFromStructuredData` test, e.g. after it):

```go
func TestConvert_MetaSourceFileTakesValueFromSourceMeta(t *testing.T) {
	rule := mustRule(t, "access", `^(?P<msg>.*)$`, []rules.Field{
		{Name: "msg", Type: "string"},
		{Name: "log_file", Type: "string", Meta: rules.FieldMetaSourceFile},
	})
	now := time.Now()
	source := SourceMeta{File: "/var/log/app.log", Line: 42}

	values, err := Convert(rule, map[string]string{"msg": "hello"}, source, now)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["log_file"] != "/var/log/app.log" {
		t.Errorf("log_file = %v, want %q", values["log_file"], "/var/log/app.log")
	}
}

func TestConvert_MetaSourceLineTakesValueFromSourceMeta(t *testing.T) {
	rule := mustRule(t, "access", `^(?P<msg>.*)$`, []rules.Field{
		{Name: "msg", Type: "string"},
		{Name: "log_line", Type: "int", Meta: rules.FieldMetaSourceLine},
	})
	now := time.Now()
	source := SourceMeta{File: "/var/log/app.log", Line: 42}

	values, err := Convert(rule, map[string]string{"msg": "hello"}, source, now)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["log_line"] != int64(42) {
		t.Errorf("log_line = %v, want int64(42)", values["log_line"])
	}
}

func TestConvert_MetaSourceFileWithReplaceExtractsBasename(t *testing.T) {
	fields := []rules.Field{
		{Name: "msg", Type: "string"},
		{
			Name: "log_file",
			Type: "string",
			Meta: rules.FieldMetaSourceFile,
			Replace: []rules.ReplaceRule{
				{Pattern: `^.*/`, Replacement: ""},
			},
		},
	}
	for i := range fields {
		for j := range fields[i].Replace {
			fields[i].Replace[j].Regexp = mustCompileT(t, fields[i].Replace[j].Pattern)
		}
	}
	rule := mustRule(t, "access", `^(?P<msg>.*)$`, fields)
	now := time.Now()
	source := SourceMeta{File: "/var/log/app.log", Line: 1}

	values, err := Convert(rule, map[string]string{"msg": "hello"}, source, now)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["log_file"] != "app.log" {
		t.Errorf("log_file = %v, want %q (replace should strip the directory, proving meta fields flow through the normal convertValue pipeline)", values["log_file"], "app.log")
	}
}

func TestMatchAndConvert_PassesSourceMetaThroughToMetaFields(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		mustRule(t, "access", `^(?P<msg>.*)$`, []rules.Field{
			{Name: "msg", Type: "string"},
			{Name: "log_file", Type: "string", Meta: rules.FieldMetaSourceFile},
			{Name: "log_line", Type: "int", Meta: rules.FieldMetaSourceLine},
		}),
	}
	source := SourceMeta{File: "input.log", Line: 7}

	_, _, values, _, ok := MatchAndConvert(ruleList, "hello world", source, now)
	if !ok {
		t.Fatal("expected match")
	}
	if values["log_file"] != "input.log" {
		t.Errorf("log_file = %v, want %q", values["log_file"], "input.log")
	}
	if values["log_line"] != int64(7) {
		t.Errorf("log_line = %v, want int64(7)", values["log_line"])
	}
}
```

These 4 new tests won't compile yet (`SourceMeta` doesn't exist, `Convert`/`MatchAndConvert` don't take a third/fourth argument). That's expected — you'll fix the whole file's call sites together in Step 4, since a partially-migrated file won't compile either way. Proceed to Step 2.

- [ ] **Step 2: Confirm the new type/signatures don't exist yet**

Run: `go build ./internal/parse/...`
Expected: FAIL — compile errors about `SourceMeta` undefined and wrong argument counts to `Convert`/`MatchAndConvert`. This confirms the RED state; the errors will persist (and multiply) until both Step 3 (production code) and Step 4 (test call sites) are done, since Go requires the whole package to compile together.

- [ ] **Step 3: Implement `SourceMeta` and extend `Convert`/`MatchAndConvert`**

In `internal/parse/match.go`, add `"strconv"` to the import block:

```go
import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"logidx/internal/rules"
)
```

Add the `SourceMeta` type just before `Convert`'s doc comment:

```go
// SourceMeta carries the input file's path and the current line number,
// for fields declared with meta: source_file / meta: source_line (see
// rules.Field.Meta). File matches unmatched.txt's convention: "-" for
// stdin, otherwise the input path as given. Line is 1-based; for a
// continuation-merged entry it's the entry's first physical line, not
// whichever continuation line happens to be current.
type SourceMeta struct {
	File string
	Line int
}
```

Change `Convert`'s signature and its value-source switch (the doc comment, the signature line, and the `switch` block inside the `for _, field := range rule.Fields` loop — everything else in the function body is unchanged):

```go
// Convert type-converts raw's captured values according to rule's field
// definitions. If rule.Structured is set, raw[rule.Structured.Source] is
// first parsed into a flat key/value map (see ParseStructured); fields with
// Key set pull their raw value from that map instead of from raw, fields
// with Meta set pull their raw value from source instead (see
// rules.Field.Meta / SourceMeta), and the field with Extra set (if any)
// receives a JSON object of every structured key not consumed by a Key
// field. Returns an error if any field fails conversion - callers treat
// that the same way a failed match is treated (write to unmatched).
func Convert(rule rules.Rule, raw map[string]string, source SourceMeta, now time.Time) (values map[string]any, err error) {
```

```go
	converted := make(map[string]any, len(rule.Fields))
	for _, field := range rule.Fields {
		rawValue := raw[field.Name]
		switch {
		case field.Meta == rules.FieldMetaSourceFile:
			rawValue = source.File
		case field.Meta == rules.FieldMetaSourceLine:
			rawValue = strconv.Itoa(source.Line)
		case field.Extra:
			rawValue = extraJSON
		case field.Key != "":
			v, ok := structuredValues[field.Key]
			if !ok {
				return nil, fmt.Errorf("structured data missing key %q", field.Key)
			}
			rawValue = v
		}
		v, err := convertValue(rawValue, field, now)
		if err != nil {
			return nil, err
		}
		converted[field.Name] = v
	}
	return converted, nil
}
```

Change `MatchAndConvert`'s signature and its internal call to `Convert`:

```go
func MatchAndConvert(ruleList []rules.Rule, line string, source SourceMeta, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	for i := range ruleList {
		r := &ruleList[i]
		captured, matched := matchRule(r, line)
		if !matched {
			continue
		}

		if r.ContinuationRegexp != nil {
			return r, captured, nil, attempts, true
		}

		v, err := Convert(*r, captured, source, now)
		if err != nil {
			attempts = append(attempts, MatchAttempt{RuleName: r.Name, Err: err})
			continue
		}
		return r, captured, v, attempts, true
	}
	return nil, nil, nil, attempts, false
}
```

- [ ] **Step 4: Update every existing call site in this package's tests**

`internal/parse/match_test.go` has 5 existing `MatchAndConvert(...)` calls and 13 existing `Convert(...)` calls (before this task's Step 1 additions); `internal/parse/presets_test.go` has 1 `MatchAndConvert(...)` call. Every one of them needs a `SourceMeta{}` (the zero value — these tests don't care about source metadata) inserted as the new argument immediately before the trailing `now` argument. Two examples of the transformation:

Before (match_test.go, `TestMatchAndConvert_TypeConversionFailureFallsThroughToNextRule`):
```go
	rule, _, values, attempts, ok := MatchAndConvert(ruleList, "not-a-number", now)
```
After:
```go
	rule, _, values, attempts, ok := MatchAndConvert(ruleList, "not-a-number", SourceMeta{}, now)
```

Before (match_test.go, `TestConvert_SuccessConvertsEveryDeclaredField`):
```go
	values, err := Convert(rule, map[string]string{"status": "200"}, now)
```
After:
```go
	values, err := Convert(rule, map[string]string{"status": "200"}, SourceMeta{}, now)
```

Apply the same transformation — insert `SourceMeta{}` right before the final `now` (or `tc.now`, in presets_test.go's case) argument — to every remaining `Convert(...)` and `MatchAndConvert(...)` call in both files. Do not touch `MatchRaw(...)` calls (`TestMatchRaw_ReturnsRawCapturesWithoutConversion`, `TestMatchRaw_NoRuleMatches`) — `MatchRaw` doesn't call `Convert` and its signature is unchanged.

- [ ] **Step 5: Run the tests to verify everything compiles and passes**

Run: `go build ./internal/parse/...` (expect success — this is the signal every call site was found), then `go test ./internal/parse/... -v`
Expected: all tests PASS, including the 4 new ones from Step 1.

- [ ] **Step 6: Run the full test suite to check for regressions**

Run: `go test ./...`
Expected: PASS. (`internal/convert` will fail to build at this point, since Task 3 hasn't updated its call sites yet — that's expected and fine; Task 3 handles it. If any *other* package fails, that's a real regression to investigate.)

- [ ] **Step 7: Commit**

```bash
git add internal/parse/match.go internal/parse/match_test.go internal/parse/presets_test.go
git commit -m "feat(parse): add SourceMeta and resolve meta: source_file/source_line in Convert"
```

---

## Task 3: Wire `internal/convert/merge.go`, integration tests, and README

**Files:**
- Modify: `internal/convert/merge.go`
- Test: `internal/convert/merge_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `parse.SourceMeta{File string; Line int}`, `parse.Convert(rule, raw, source, now)`, `parse.MatchAndConvert(ruleList, line, source, now)` from Task 2. `fileCursor.inputPath string`, `scannedLine.lineNum int`, `openEntry.rawLines []scannedLine` (all pre-existing, unchanged).
- Produces: nothing new — this task wires existing pieces together and documents the feature.

- [ ] **Step 1: Update the two `parse.Convert`/`parse.MatchAndConvert` call sites**

In `internal/convert/merge.go`, `finalizeEntry` (around line 247):

Before:
```go
	values, convErr := parse.Convert(*entry.rule, entry.raw, c.now)
```
After:
```go
	values, convErr := parse.Convert(*entry.rule, entry.raw, parse.SourceMeta{File: c.inputPath, Line: entry.rawLines[0].lineNum}, c.now)
```

In `advance` (around line 310):

Before:
```go
		rule, raw, values, attempts, matched := parse.MatchAndConvert(c.cfg.Rules, line.text, c.now)
```
After:
```go
		rule, raw, values, attempts, matched := parse.MatchAndConvert(c.cfg.Rules, line.text, parse.SourceMeta{File: c.inputPath, Line: line.lineNum}, c.now)
```

`entry.rawLines[0].lineNum` is the continuation entry's first physical line, matching this plan's Global Constraints. `line.lineNum` is the current line's own number, for the direct (non-continuation) match path.

- [ ] **Step 2: Run the build to confirm the package compiles**

Run: `go build ./...`
Expected: success — this closes out the "internal/convert fails to build" note left at the end of Task 2.

- [ ] **Step 3: Write the failing integration tests**

Add to `internal/convert/merge_test.go` (near `TestFileCursor_Advance_SplitsEligibleFromIneligibleRows`, e.g. right after it):

```go
func TestFileCursor_Advance_MetaFieldsCaptureSourceFileAndLineNumber(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: access
    pattern: '^(?P<time>\S+) (?P<msg>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      msg: string
      log_file:
        type: string
        meta: source_file
      log_line:
        type: int
        meta: source_line
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "2026-08-06T12:00:00Z first\n2026-08-06T12:00:01Z second\n")

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
	if cand.values["log_file"] != logPath {
		t.Errorf("log_file = %v, want %q", cand.values["log_file"], logPath)
	}
	if cand.values["log_line"] != int64(1) {
		t.Errorf("log_line = %v, want int64(1)", cand.values["log_line"])
	}

	cand2, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("second advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand2.values["log_file"] != logPath {
		t.Errorf("second log_file = %v, want %q", cand2.values["log_file"], logPath)
	}
	if cand2.values["log_line"] != int64(2) {
		t.Errorf("second log_line = %v, want int64(2)", cand2.values["log_line"])
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFileCursor_Advance_MetaSourceLineUsesEntryStartLineForContinuation(t *testing.T) {
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
      log_line:
        type: int
        meta: source_line
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
	if cand.values["log_line"] != int64(1) {
		t.Errorf("log_line = %v, want int64(1) (the entry's starting physical line, not a continuation line)", cand.values["log_line"])
	}

	cand2, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("second advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand2.values["log_line"] != int64(4) {
		t.Errorf("second log_line = %v, want int64(4)", cand2.values["log_line"])
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/convert/... -run TestFileCursor_Advance_Meta -v`
Expected: PASS (Task 3's Step 1 wiring already makes these pass — there's no further production-code change expected here. If either fails, re-check the `entry.rawLines[0].lineNum` vs `line.lineNum` wiring from Step 1 before changing the tests).

- [ ] **Step 5: Run the full test suite to check for regressions**

Run: `go test ./...`
Expected: PASS, no regressions anywhere in the repo.

- [ ] **Step 6: Update README.md**

Insert a new `###`-level section right after the existing "構造化データの部分パース" section ends and before "### 圧縮設定" begins (i.e. after the line containing `ルールレベルの` preset `ショートカット...` and its blank line, currently ending around README.md:299, before `### 圧縮設定` at README.md:301):

```markdown
### マッチ行の入力元情報を保存する(`meta:`)

`logidx import`は複数の入力ファイルを1つのParquet出力にマージするため、通常はマッチした行がどの入力ファイルの何行目に由来するかという情報が出力に残らない(`unmatched.txt`側は元々`<source>\t<lineNum>\t<raw>\n`形式でこれを持っている)。フィールドに`meta:`を設定すると、その情報をカラムとして保存できる。

```yaml
rules:
  - name: access
    pattern: '^(?P<remote>\S+) (?P<msg>.*)$'
    fields:
      remote: string
      msg: string
      log_file:
        type: string
        meta: source_file
      log_line:
        type: int
        meta: source_line
```

- `meta: source_file`は`type: string`必須。値はその行が由来する入力パス(`-`はstdinのまま、`unmatched.txt`と同じ表記)。
- `meta: source_line`は`type: int`必須。値はその行の1始まりの行番号。`continuation:`で複数行を1エントリに束ねるルールの場合は、エントリの先頭物理行番号になる(継続行自体の行番号ではない)。
- カラム名は`fields:`のキー名で自由に決められる(`log_file`/`log_line`という名前に限らない)。
- `replace:`/`normalize:`は`meta`フィールドにもそのまま適用できる(例: フルパスからファイル名だけ取り出す正規表現置換)。
- `meta:`はルールごとのオプトインで、全ルールに自動付与されることはない。既存のルールは無変更で動作する。
- `meta:`と`key:`/`extra:`は同じフィールドに同時設定できない(値の取得元は1つだけ)。
```

- [ ] **Step 7: Proofread the README diff**

Run: `git diff README.md`
Expected: the new section reads naturally between the structured-data section and the compression-settings section, with consistent terminology (`フィールド`, `オプトイン`, etc.) and no formatting breakage (check the YAML code fence renders correctly, since it's now nested inside this response's own considerations — verify by eye that the fence markers are exactly ` ```yaml ` / ` ``` ` and not accidentally doubled).

- [ ] **Step 8: Commit**

```bash
git add internal/convert/merge.go internal/convert/merge_test.go README.md
git commit -m "feat(convert): wire source file/line into meta fields, document meta:"
```

---

## Final Verification

- [ ] **Step 1: Run the full test suite**

Run: `go test ./...`
Expected: PASS, no regressions.

- [ ] **Step 2: Run linters if configured**

Run: `golangci-lint run ./...` (this repo has `.golangci.yml`, so this should run cleanly)
Expected: no new findings introduced by this change.

- [ ] **Step 3: `gofmt` check**

Run: `gofmt -l internal/rules/rules.go internal/rules/validate.go internal/rules/validate_test.go internal/parse/match.go internal/parse/match_test.go internal/parse/presets_test.go internal/convert/merge.go internal/convert/merge_test.go`
Expected: no output (all files already gofmt-clean).

- [ ] **Step 4: Confirm `unmatched.txt` behavior is untouched**

Run: `git diff --stat` over the whole branch and confirm `internal/writer/*.go` (where `WriteUnmatched` lives) does not appear — this feature must not touch the unmatched-lines path at all, per this plan's Global Constraints and the design's explicit non-goal.
