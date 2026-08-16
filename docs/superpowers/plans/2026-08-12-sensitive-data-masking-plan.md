# Sensitive Data Masking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a global `mask:` section to `rules.yaml` that redacts or deterministically hashes sensitive values (by structured-data key name, or by regex pattern in string values) as part of `logidx import`, covering mapped fields, the `extra:` column, and `unmatched.txt`.

**Architecture:** `mask:` entries compile into `rules.MaskRule` at `Config` load time, exactly like existing `replace:`/`normalize:`. A new `internal/parse/mask.go` holds the masking primitives (`applyKeyMaskJSON`, `applyKeyMaskFlat`, `ApplyPatternMask`, `hashTrunc`, `SplitMaskRules`). Key-name masking hooks into JSON/LTSV/logfmt structured parsing (before/at the point a flat key→value map is produced); pattern masking hooks into `convertValue` (for `type: string` fields, mapped and `extra:` alike) and into `unmatched.txt` line writing. `rules.Rule`/`Field` are otherwise untouched — `mask:` is config-wide, not per-rule.

**Tech Stack:** Go stdlib only (`regexp`, `crypto/sha256`, `encoding/hex`, `encoding/json`), consistent with the rest of `internal/rules`/`internal/parse`.

## Global Constraints

- Design doc of record: `docs/superpowers/specs/2026-08-12-sensitive-data-masking-design.md` — follow it exactly; deviations are called out explicitly below with a rationale.
- `mask:` is a single, global, top-level `rules.yaml` list — no per-rule `mask:` override (design Non-goals).
- `action: hash` never uses a secret key — plain truncated `sha256` only (design Non-goals: no HMAC).
- No partial masking (e.g. "keep last 4 digits") — only whole-match `redact`/`hash` (design Non-goals).
- `type: key` masking does **not** apply to preset structured formats (`structured.format: apache_clf` etc.) or to `ParsePreset` — the design's "影響範囲" file list never touches `presets.go`, and preset key names are fixed/small, not user-defined free-form JSON.
- `type: pattern` masking applies only to `type: string` fields — never `int`/`float`/`timestamp` (design §1, to avoid masking breaking type conversion).
- Every existing test must keep passing unmodified in assertions (only call-site signatures change) — this is the regression guarantee for `mask:` unset.
- Go tooling: use `gofmt`, `golangci-lint run`, and `go test ./...` (per project convention) — not manual formatting.

---

### Task 1: `internal/rules` — `MaskRule` config type, `Config.Mask`, load-time compilation, and validation

**Files:**
- Modify: `internal/rules/rules.go:29-38` (insert `MaskRule` type after `ReplaceRule`), `internal/rules/rules.go:191-201` (`Config` struct), `internal/rules/rules.go:275-289` (`loadConfig`, add mask pattern compile loop)
- Modify: `internal/rules/validate.go:10-25` (add `allowedMaskTypes`/`allowedMaskActions` vars), `internal/rules/validate.go:148-156` (`Validate`, add mask checks)
- Test: `internal/rules/rules_test.go` (append new tests)
- Test: `internal/rules/validate_test.go` (append new tests)

**Interfaces:**
- Produces: `rules.MaskRule{Type, Pattern string, Regexp *regexp.Regexp, Action, Value string, Length int}`; `rules.Config.Mask []MaskRule` (yaml key `mask`). Both consumed by Task 2 (`internal/parse`) and Task 3 (`internal/convert`).

- [ ] **Step 1: Add the `MaskRule` type to `internal/rules/rules.go`**

Insert immediately after the existing `ReplaceRule` struct (currently `rules.go:29-38`):

```go
// MaskRule redacts or deterministically hashes sensitive data at import
// time. Declared globally under Config.Mask (not per-rule), and applied to
// every rule's structured/converted data the same way; declared entries
// chain in order when more than one matches the same key or value - see
// parse.SplitMaskRules, applyKeyMaskJSON/applyKeyMaskFlat, and
// parse.ApplyPatternMask.
type MaskRule struct {
	// Type is "key" (Pattern matches a structured-data key name; the whole
	// matched key's value is masked) or "pattern" (Pattern matches inside a
	// type: string field's value; only the matched substring is masked).
	Type    string         `yaml:"type"`
	Pattern string         `yaml:"pattern"`
	Regexp  *regexp.Regexp `yaml:"-"`
	// Action is "redact" (replace with Value) or "hash" (replace with a
	// truncated, unkeyed SHA-256 digest - deliberately not HMAC, since the
	// goal is "same input, same output", not dictionary-attack resistance).
	Action string `yaml:"action"`
	// Value is the literal replacement for action: redact. Empty string is
	// valid (deletes the matched content), matching replace:'s value: ''.
	Value string `yaml:"value,omitempty"`
	// Length is the SHA-256 hex digest's truncated length (1-64) for
	// action: hash.
	Length int `yaml:"length,omitempty"`
}
```

- [ ] **Step 2: Add `Mask` to `Config` in `internal/rules/rules.go`**

Change (currently `rules.go:191-201`):

```go
type Config struct {
	Rules []Rule `yaml:"rules"`
	// Compression optionally sets the output Parquet compression codec and
	// level; unset fields fall back to the CLI flags, then to the default
	// (see internal/compression).
	Compression compression.Settings `yaml:"compression"`
	// RowGroup optionally caps the number of rows per Parquet row group on
	// every output file; unset falls back to the CLI flag, then to
	// unlimited (see internal/rowgroup).
	RowGroup rowgroup.Settings `yaml:"row_group"`
}
```

to:

```go
type Config struct {
	Rules []Rule `yaml:"rules"`
	// Mask declares global, rule-independent redaction/hashing applied to
	// every rule's structured/converted data the same way - see MaskRule.
	Mask []MaskRule `yaml:"mask"`
	// Compression optionally sets the output Parquet compression codec and
	// level; unset fields fall back to the CLI flags, then to the default
	// (see internal/compression).
	Compression compression.Settings `yaml:"compression"`
	// RowGroup optionally caps the number of rows per Parquet row group on
	// every output file; unset falls back to the CLI flag, then to
	// unlimited (see internal/rowgroup).
	RowGroup rowgroup.Settings `yaml:"row_group"`
}
```

- [ ] **Step 3: Compile `Mask` patterns in `loadConfig`**

In `internal/rules/rules.go`, `loadConfig` currently ends (lines ~275-289):

```go
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
```

Change to:

```go
			if field.Type == "timestamp" {
				tf, err := ResolveFormat(field.Format)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: %w", cfg.Rules[i].Name, field.Name, err)
				}
				field.ResolvedFormat = tf
			}
		}
	}

	for i := range cfg.Mask {
		mre, err := regexp.Compile(cfg.Mask[i].Pattern)
		if err != nil {
			return nil, fmt.Errorf("mask[%d]: compile pattern: %w", i, err)
		}
		cfg.Mask[i].Regexp = mre
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
```

- [ ] **Step 4: Add mask validation to `internal/rules/validate.go`**

Change the var block (currently lines 17-25):

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

to:

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

// allowedMaskTypes and allowedMaskActions are MaskRule.Type/Action's valid
// values - see the mask: design doc.
var allowedMaskTypes = map[string]bool{
	"key":     true,
	"pattern": true,
}

var allowedMaskActions = map[string]bool{
	"redact": true,
	"hash":   true,
}
```

Then, in `Validate`, change the tail (currently lines 148-156):

```go
	if err := c.Compression.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("compression: %w", err))
	}
	if err := c.RowGroup.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("row_group: %w", err))
	}

	return errors.Join(errs...)
}
```

to:

```go
	for i, m := range c.Mask {
		if !allowedMaskTypes[m.Type] {
			errs = append(errs, fmt.Errorf("mask[%d]: unsupported type %q (must be %q or %q)", i, m.Type, "key", "pattern"))
		}
		if !allowedMaskActions[m.Action] {
			errs = append(errs, fmt.Errorf("mask[%d]: unsupported action %q (must be %q or %q)", i, m.Action, "redact", "hash"))
		}
		if m.Action == "hash" && (m.Length < 1 || m.Length > 64) {
			errs = append(errs, fmt.Errorf("mask[%d]: hash length must be 1-64, got %d", i, m.Length))
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
```

- [ ] **Step 5: Append tests to `internal/rules/rules_test.go`**

```go
func TestLoad_ParsesAndCompilesMaskRules(t *testing.T) {
	yamlContent := `
mask:
  - type: key
    pattern: '(?i)^(password|pwd)$'
    action: redact
    value: '[MASKED]'
  - type: pattern
    pattern: '[\w.+-]+@[\w.-]+\.\w+'
    action: hash
    length: 8

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

	if len(cfg.Mask) != 2 {
		t.Fatalf("expected 2 mask rules, got %d", len(cfg.Mask))
	}
	if cfg.Mask[0].Type != "key" || cfg.Mask[0].Action != "redact" || cfg.Mask[0].Value != "[MASKED]" {
		t.Errorf("unexpected first mask rule: %+v", cfg.Mask[0])
	}
	if cfg.Mask[0].Regexp == nil {
		t.Error("expected compiled Regexp on first mask rule")
	}
	if cfg.Mask[1].Type != "pattern" || cfg.Mask[1].Action != "hash" || cfg.Mask[1].Length != 8 {
		t.Errorf("unexpected second mask rule: %+v", cfg.Mask[1])
	}
	if cfg.Mask[1].Regexp == nil {
		t.Error("expected compiled Regexp on second mask rule")
	}
}

func TestLoad_InvalidMaskPatternIsError(t *testing.T) {
	yamlContent := `
mask:
  - type: key
    pattern: '(unterminated'
    action: redact
    value: ''

rules:
  - name: app_log
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
`
	path := writeTempRules(t, yamlContent)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for an invalid mask pattern")
	}
	if !strings.Contains(err.Error(), "mask") {
		t.Errorf("expected error to mention mask, got: %v", err)
	}
}
```

- [ ] **Step 6: Append tests to `internal/rules/validate_test.go`**

```go
func TestValidate_MaskUnknownTypeIsError(t *testing.T) {
	cfg := &Config{
		Mask: []MaskRule{
			{Type: "bogus", Pattern: "x", Regexp: mustCompile(t, "x"), Action: "redact", Value: ""},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "mask[0]") {
		t.Errorf("expected error to mention mask[0], got: %v", err)
	}
}

func TestValidate_MaskUnknownActionIsError(t *testing.T) {
	cfg := &Config{
		Mask: []MaskRule{
			{Type: "key", Pattern: "x", Regexp: mustCompile(t, "x"), Action: "bogus"},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "mask[0]") {
		t.Errorf("expected error to mention mask[0], got: %v", err)
	}
}

func TestValidate_MaskHashLengthOutOfRangeIsError(t *testing.T) {
	for _, length := range []int{0, 65} {
		cfg := &Config{
			Mask: []MaskRule{
				{Type: "key", Pattern: "x", Regexp: mustCompile(t, "x"), Action: "hash", Length: length},
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatalf("length %d: expected validation error, got nil", length)
		}
		if !strings.Contains(err.Error(), "mask[0]") {
			t.Errorf("length %d: expected error to mention mask[0], got: %v", length, err)
		}
	}
}

func TestValidate_MaskHashLengthInRangePasses(t *testing.T) {
	cfg := &Config{
		Mask: []MaskRule{
			{Type: "key", Pattern: "x", Regexp: mustCompile(t, "x"), Action: "hash", Length: 1},
			{Type: "pattern", Pattern: "y", Regexp: mustCompile(t, "y"), Action: "hash", Length: 64},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}
```

- [ ] **Step 7: Run the package tests**

Run: `go test ./internal/rules/...`
Expected: PASS, including all 4 new tests above.

- [ ] **Step 8: Commit**

```bash
git add internal/rules/rules.go internal/rules/validate.go internal/rules/rules_test.go internal/rules/validate_test.go
git commit -m "feat(rules): add mask: config type, load-time compilation, and validation"
```

---

### Task 2: `internal/parse` — masking primitives, and wiring into structured parsing / value conversion

This is the core data-flow change: a new `mask.go` with the masking primitives, plus every function between "structured data parsed" and "field converted" threading mask rules through. Because `Convert` calls `ParseStructured`/`parseStructuredJSONTyped` internally, and `MatchAndConvertFrom`/`MatchAndConvert` call `Convert` internally, these must land together for the package to compile at each commit.

**Files:**
- Create: `internal/parse/mask.go`
- Test: `internal/parse/mask_test.go`
- Modify: `internal/parse/structured.go` (add `keyRules []rules.MaskRule` param to `ParseStructured`, `parseStructuredJSON`, `parseStructuredJSONTyped`, `parseStructuredLTSV`, `parseStructuredLogfmt`)
- Modify: `internal/parse/match.go` (add `mask []rules.MaskRule` param to `Convert`, `MatchAndConvertFrom`, `MatchAndConvert`)
- Modify: `internal/parse/convertvalue.go` (add `patternRules []rules.MaskRule` param to `convertValue`)
- Test: `internal/parse/structured_test.go`, `internal/parse/match_test.go`, `internal/parse/convertvalue_test.go`, `internal/parse/presets_test.go` (append new mask tests; update every existing call site of the five changed functions)

**Interfaces:**
- Consumes: `rules.MaskRule` from Task 1.
- Produces (new, exported, used by Task 3): `parse.SplitMaskRules(mask []rules.MaskRule) (keyRules, patternRules []rules.MaskRule)`, `parse.ApplyPatternMask(s string, patternRules []rules.MaskRule) string`.
- Changed signatures (all in package `parse`): `Convert(rule rules.Rule, raw map[string]string, source SourceMeta, now time.Time, mask []rules.MaskRule) (map[string]any, error)`; `MatchAndConvertFrom(ruleList []rules.Rule, startIndex int, line string, source SourceMeta, now time.Time, mask []rules.MaskRule) (...)`; `MatchAndConvert(ruleList []rules.Rule, line string, source SourceMeta, now time.Time, mask []rules.MaskRule) (...)`; `ParseStructured(format, raw string, keyRules []rules.MaskRule) (map[string]string, error)`; `convertValue(raw string, field rules.Field, now time.Time, patternRules []rules.MaskRule) (any, error)`.

- [ ] **Step 1: Create `internal/parse/mask.go`**

```go
package parse

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/wtnb75/logidx/internal/rules"
)

// SplitMaskRules partitions mask (Config.Mask, in declaration order) into
// its key-targeting and pattern-targeting subsets, preserving each
// subset's relative order - callers apply each subset as its own chain
// (see applyKeyMaskJSON/applyKeyMaskFlat/ApplyPatternMask).
func SplitMaskRules(mask []rules.MaskRule) (keyRules, patternRules []rules.MaskRule) {
	for _, m := range mask {
		switch m.Type {
		case "key":
			keyRules = append(keyRules, m)
		case "pattern":
			patternRules = append(patternRules, m)
		}
	}
	return keyRules, patternRules
}

// applyKeyMaskJSON walks tree (a JSON value decoded via encoding/json with
// UseNumber(): map[string]any, []any, json.Number, string, bool, or nil)
// recursively at any depth. Every map key matching a keyRules pattern has
// its value replaced in place by the chained mask result (see
// maskKeyIfMatched); values under non-matching keys, and every array
// element, are recursed into instead.
func applyKeyMaskJSON(tree any, keyRules []rules.MaskRule) {
	switch t := tree.(type) {
	case map[string]any:
		for k, v := range t {
			if masked, matched := maskKeyIfMatched(k, v, keyRules); matched {
				t[k] = masked
				continue
			}
			applyKeyMaskJSON(v, keyRules)
		}
	case []any:
		for _, v := range t {
			applyKeyMaskJSON(v, keyRules)
		}
	}
}

// applyKeyMaskFlat is applyKeyMaskJSON's flat-map counterpart for LTSV/
// logfmt structured data, which has no nesting to recurse into: every
// top-level key matching a keyRules pattern is masked in place.
func applyKeyMaskFlat(m map[string]string, keyRules []rules.MaskRule) {
	for k, v := range m {
		if masked, matched := maskKeyIfMatched(k, v, keyRules); matched {
			m[k] = masked
		}
	}
}

// maskKeyIfMatched stringifies v (jsonValueToString - the same conversion
// structured.go's json path already uses for untouched values) and, if any
// keyRules pattern matches key, chains every matching rule's action over it
// in declaration order (a later matching rule's action input is the
// earlier one's output). matched is false, and masked is unspecified, when
// no rule matches key - callers must check matched before using masked.
func maskKeyIfMatched(key string, v any, keyRules []rules.MaskRule) (masked string, matched bool) {
	s, err := jsonValueToString(v)
	if err != nil {
		s = ""
	}
	for _, rule := range keyRules {
		if !rule.Regexp.MatchString(key) {
			continue
		}
		matched = true
		s = applyMaskAction(s, rule)
	}
	return s, matched
}

// ApplyPatternMask chains every patternRules entry over s in declaration
// order, replacing each rule's regexp matches with its masked action
// result - action: redact substitutes the whole match with rule.Value
// (Go's regexp.ReplaceAllString, so $1-style backreferences work);
// action: hash replaces each match independently with its own truncated
// SHA-256 digest (see hashTrunc).
func ApplyPatternMask(s string, patternRules []rules.MaskRule) string {
	for _, rule := range patternRules {
		switch rule.Action {
		case "redact":
			s = rule.Regexp.ReplaceAllString(s, rule.Value)
		case "hash":
			s = rule.Regexp.ReplaceAllStringFunc(s, func(match string) string {
				return hashTrunc(match, rule.Length)
			})
		}
	}
	return s
}

// applyMaskAction applies one MaskRule's action (already validated to be
// "redact" or "hash" by Config.Validate - see internal/rules/validate.go)
// to s, the already-matched value or substring being masked.
func applyMaskAction(s string, rule rules.MaskRule) string {
	if rule.Action == "hash" {
		return hashTrunc(s, rule.Length)
	}
	return rule.Value
}

// hashTrunc returns the first length hex characters (length is 1-64,
// enforced by Config.Validate) of s's SHA-256 digest. No secret key is
// used deliberately: the same input must always produce the same output,
// so masked values stay correlatable across rows without revealing the
// original value - see the design's Non-goals on HMAC.
func hashTrunc(s string, length int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:length]
}
```

- [ ] **Step 2: Create `internal/parse/mask_test.go`**

```go
package parse

import (
	"testing"

	"github.com/wtnb75/logidx/internal/rules"
)

func mustMaskRuleT(t *testing.T, typ, pattern, action, value string, length int) rules.MaskRule {
	t.Helper()
	re := mustCompileT(t, pattern)
	return rules.MaskRule{Type: typ, Pattern: pattern, Regexp: re, Action: action, Value: value, Length: length}
}

func TestSplitMaskRules_PartitionsByTypePreservingOrder(t *testing.T) {
	mask := []rules.MaskRule{
		mustMaskRuleT(t, "key", "a", "redact", "[A]", 0),
		mustMaskRuleT(t, "pattern", "b", "redact", "[B]", 0),
		mustMaskRuleT(t, "key", "c", "redact", "[C]", 0),
	}

	keyRules, patternRules := SplitMaskRules(mask)

	if len(keyRules) != 2 || keyRules[0].Pattern != "a" || keyRules[1].Pattern != "c" {
		t.Errorf("keyRules = %+v, want patterns [a c]", keyRules)
	}
	if len(patternRules) != 1 || patternRules[0].Pattern != "b" {
		t.Errorf("patternRules = %+v, want patterns [b]", patternRules)
	}
}

func TestApplyKeyMaskJSON_TopLevelKeyMatch(t *testing.T) {
	tree := map[string]any{"password": "hunter2", "user": "alice"}
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^password$", "redact", "[MASKED]", 0)}

	applyKeyMaskJSON(tree, keyRules)

	if tree["password"] != "[MASKED]" {
		t.Errorf("password = %v, want [MASKED]", tree["password"])
	}
	if tree["user"] != "alice" {
		t.Errorf("user = %v, want unchanged alice", tree["user"])
	}
}

func TestApplyKeyMaskJSON_NestedObjectKeyMatch(t *testing.T) {
	tree := map[string]any{
		"user": map[string]any{"email": "a@example.com", "name": "alice"},
	}
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^email$", "redact", "[EMAIL]", 0)}

	applyKeyMaskJSON(tree, keyRules)

	nested := tree["user"].(map[string]any)
	if nested["email"] != "[EMAIL]" {
		t.Errorf("nested email = %v, want [EMAIL]", nested["email"])
	}
	if nested["name"] != "alice" {
		t.Errorf("nested name = %v, want unchanged alice", nested["name"])
	}
}

func TestApplyKeyMaskJSON_ArrayOfObjectsKeyMatch(t *testing.T) {
	tree := map[string]any{
		"users": []any{
			map[string]any{"password": "p1"},
			map[string]any{"password": "p2"},
		},
	}
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^password$", "redact", "[MASKED]", 0)}

	applyKeyMaskJSON(tree, keyRules)

	users := tree["users"].([]any)
	for i, u := range users {
		if u.(map[string]any)["password"] != "[MASKED]" {
			t.Errorf("users[%d].password = %v, want [MASKED]", i, u.(map[string]any)["password"])
		}
	}
}

func TestApplyKeyMaskJSON_MultipleRulesChainOnSameKey(t *testing.T) {
	tree := map[string]any{"token": "secret"}
	keyRules := []rules.MaskRule{
		mustMaskRuleT(t, "key", "(?i)^token$", "hash", "", 8),
		mustMaskRuleT(t, "key", "(?i)^token$", "redact", "[CHAINED]", 0),
	}

	applyKeyMaskJSON(tree, keyRules)

	if tree["token"] != "[CHAINED]" {
		t.Errorf("token = %v, want [CHAINED] (second rule's redact must apply after first rule's hash)", tree["token"])
	}
}

func TestApplyKeyMaskFlat_TopLevelKeyMatch(t *testing.T) {
	m := map[string]string{"password": "hunter2", "user": "alice"}
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^password$", "redact", "[MASKED]", 0)}

	applyKeyMaskFlat(m, keyRules)

	if m["password"] != "[MASKED]" {
		t.Errorf("password = %q, want [MASKED]", m["password"])
	}
	if m["user"] != "alice" {
		t.Errorf("user = %q, want unchanged alice", m["user"])
	}
}

func TestApplyPatternMask_Redact(t *testing.T) {
	patternRules := []rules.MaskRule{mustMaskRuleT(t, "pattern", `[\w.+-]+@[\w.-]+\.\w+`, "redact", "[EMAIL]", 0)}

	got := ApplyPatternMask("contact admin@example.com now", patternRules)

	want := "contact [EMAIL] now"
	if got != want {
		t.Errorf("ApplyPatternMask() = %q, want %q", got, want)
	}
}

func TestApplyPatternMask_HashReplacesEachMatchIndependently(t *testing.T) {
	patternRules := []rules.MaskRule{mustMaskRuleT(t, "pattern", `\d+`, "hash", "", 8)}

	got := ApplyPatternMask("id=111 other=222", patternRules)

	h111 := hashTrunc("111", 8)
	h222 := hashTrunc("222", 8)
	want := "id=" + h111 + " other=" + h222
	if got != want {
		t.Errorf("ApplyPatternMask() = %q, want %q", got, want)
	}
}

func TestApplyPatternMask_MultipleRulesChain(t *testing.T) {
	patternRules := []rules.MaskRule{
		mustMaskRuleT(t, "pattern", `foo`, "redact", "bar", 0),
		mustMaskRuleT(t, "pattern", `bar`, "redact", "baz", 0),
	}

	got := ApplyPatternMask("foo", patternRules)

	if got != "baz" {
		t.Errorf("ApplyPatternMask() = %q, want %q (second rule must see first rule's output)", got, "baz")
	}
}

func TestHashTrunc_Deterministic(t *testing.T) {
	a := hashTrunc("same-input", 16)
	b := hashTrunc("same-input", 16)
	if a != b {
		t.Errorf("hashTrunc not deterministic: %q != %q", a, b)
	}
}

func TestHashTrunc_LengthMatchesRequest(t *testing.T) {
	for _, length := range []int{1, 8, 32, 64} {
		got := hashTrunc("x", length)
		if len(got) != length {
			t.Errorf("hashTrunc(%q, %d) length = %d, want %d", "x", length, len(got), length)
		}
	}
}
```

- [ ] **Step 3: Run the new tests to confirm they pass on the standalone primitives**

Run: `go test ./internal/parse/... -run 'TestSplitMaskRules|TestApplyKeyMask|TestApplyPatternMask|TestHashTrunc'`
Expected: PASS (these depend only on `mask.go`, which is now complete and self-contained; the rest of the package doesn't compile yet — see next step).

- [ ] **Step 4: Thread `keyRules` through `internal/parse/structured.go`**

Change the import block (currently lines 1-9):

```go
package parse

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)
```

to:

```go
package parse

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/wtnb75/logidx/internal/rules"
)
```

Change `ParseStructured` (currently lines 11-30):

```go
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
```

to:

```go
// ParseStructured parses raw (the captured substring named by a rule's
// Structured.Source) according to format ("json", "ltsv", or "logfmt") into
// a flat map of key to string value. Nested JSON objects/arrays are
// re-encoded as their own compact JSON string; JSON numbers keep their
// original textual digits (via json.Number, avoiding float64 formatting
// artifacts); JSON null becomes an empty string. LTSV/logfmt values are
// already flat strings and pass through unchanged. keyRules (Config.Mask
// entries with Type == "key", see SplitMaskRules) mask matching keys'
// values before they reach the returned map - nested JSON keys at any
// depth for "json", top-level keys only for "ltsv"/"logfmt". Returns an
// error if raw isn't valid for the given format.
func ParseStructured(format, raw string, keyRules []rules.MaskRule) (map[string]string, error) {
	switch format {
	case "json":
		return parseStructuredJSON(raw, keyRules)
	case "ltsv":
		return parseStructuredLTSV(raw, keyRules)
	case "logfmt":
		return parseStructuredLogfmt(raw, keyRules)
	default:
		return nil, fmt.Errorf("unsupported structured format %q", format)
	}
}
```

Change `parseStructuredJSON`/`parseStructuredJSONTyped` (currently lines 54-94):

```go
func parseStructuredJSON(raw string) (map[string]string, error) {
	values, _, err := parseStructuredJSONTyped(raw)
	return values, err
}

// parseStructuredJSONTyped decodes raw the same way parseStructuredJSON
// does, but returns both the flattened string map used by Key-addressed
// fields and a map preserving each value's original JSON type (json.Number,
// bool, nil, nested map[string]any/[]any). The typed map lets Extra
// remarshal unconsumed keys without losing their original JSON shape - see
// marshalUnconsumed in match.go.
func parseStructuredJSONTyped(raw string) (values map[string]string, typed map[string]any, err error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()

	var top map[string]any
	if err := dec.Decode(&top); err != nil {
		return nil, nil, fmt.Errorf("decode json: %w", err)
	}
	if top == nil {
		// A top-level JSON `null` decodes into a nil map with err == nil
		// (Go's documented behavior for unmarshaling null into a map) - it
		// is not an object, so it must be rejected explicitly.
		return nil, nil, fmt.Errorf("decode json: top-level value must be an object, got null")
	}
	if dec.More() {
		// Decoder.Decode only consumes one JSON value; trailing bytes
		// (garbage, or a second concatenated value) must be rejected too.
		return nil, nil, fmt.Errorf("decode json: unexpected trailing data after top-level value")
	}

	values = make(map[string]string, len(top))
	for k, v := range top {
		s, err := jsonValueToString(v)
		if err != nil {
			return nil, nil, fmt.Errorf("encode json field %q: %w", k, err)
		}
		values[k] = s
	}
	return values, top, nil
}
```

to:

```go
func parseStructuredJSON(raw string, keyRules []rules.MaskRule) (map[string]string, error) {
	values, _, err := parseStructuredJSONTyped(raw, keyRules)
	return values, err
}

// parseStructuredJSONTyped decodes raw the same way parseStructuredJSON
// does, but returns both the flattened string map used by Key-addressed
// fields and a map preserving each value's original JSON type (json.Number,
// bool, nil, nested map[string]any/[]any). The typed map lets Extra
// remarshal unconsumed keys without losing their original JSON shape - see
// marshalUnconsumed in match.go. keyRules masks matching keys' values in
// top (recursively, at any nesting depth) before either return value is
// derived from it, so both values and typed reflect the masked tree.
func parseStructuredJSONTyped(raw string, keyRules []rules.MaskRule) (values map[string]string, typed map[string]any, err error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()

	var top map[string]any
	if err := dec.Decode(&top); err != nil {
		return nil, nil, fmt.Errorf("decode json: %w", err)
	}
	if top == nil {
		// A top-level JSON `null` decodes into a nil map with err == nil
		// (Go's documented behavior for unmarshaling null into a map) - it
		// is not an object, so it must be rejected explicitly.
		return nil, nil, fmt.Errorf("decode json: top-level value must be an object, got null")
	}
	if dec.More() {
		// Decoder.Decode only consumes one JSON value; trailing bytes
		// (garbage, or a second concatenated value) must be rejected too.
		return nil, nil, fmt.Errorf("decode json: unexpected trailing data after top-level value")
	}

	applyKeyMaskJSON(top, keyRules)

	values = make(map[string]string, len(top))
	for k, v := range top {
		s, err := jsonValueToString(v)
		if err != nil {
			return nil, nil, fmt.Errorf("encode json field %q: %w", k, err)
		}
		values[k] = s
	}
	return values, top, nil
}
```

Change `parseStructuredLTSV` (currently lines 119-132):

```go
func parseStructuredLTSV(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, fmt.Errorf("ltsv: empty input")
	}
	result := map[string]string{}
	for field := range strings.SplitSeq(raw, "\t") {
		key, value, found := strings.Cut(field, ":")
		if !found {
			continue
		}
		result[key] = value
	}
	return result, nil
}
```

to:

```go
func parseStructuredLTSV(raw string, keyRules []rules.MaskRule) (map[string]string, error) {
	if raw == "" {
		return nil, fmt.Errorf("ltsv: empty input")
	}
	result := map[string]string{}
	for field := range strings.SplitSeq(raw, "\t") {
		key, value, found := strings.Cut(field, ":")
		if !found {
			continue
		}
		result[key] = value
	}
	applyKeyMaskFlat(result, keyRules)
	return result, nil
}
```

Change `parseStructuredLogfmt`'s signature line (currently `structured.go:134`):

```go
func parseStructuredLogfmt(raw string) (map[string]string, error) {
```

to:

```go
func parseStructuredLogfmt(raw string, keyRules []rules.MaskRule) (map[string]string, error) {
```

And change its tail (currently the last 7 lines of the function, ending the file):

```go
		start = i
		for i < n && raw[i] != ' ' {
			i++
		}
		result[key] = raw[start:i]
	}
	return result, nil
}
```

to:

```go
		start = i
		for i < n && raw[i] != ' ' {
			i++
		}
		result[key] = raw[start:i]
	}
	applyKeyMaskFlat(result, keyRules)
	return result, nil
}
```

- [ ] **Step 5: Thread `mask []rules.MaskRule` through `internal/parse/match.go`**

Change `Convert`'s signature and body (currently lines 63-122):

```go
func Convert(rule rules.Rule, raw map[string]string, source SourceMeta, now time.Time) (values map[string]any, err error) {
	var structuredValues map[string]string
	// typedStructuredValues preserves each JSON value's original type
	// (number/bool/nested object or array), instead of the flattened
	// string form structuredValues uses - see marshalUnconsumed. Only
	// populated for the plain "json" format, since ltsv/logfmt/preset
	// values have no native type richer than a string to preserve.
	var typedStructuredValues map[string]any
	if rule.Structured != nil {
		structuredRaw := raw[rule.Structured.Source]
		switch {
		case rule.Structured.PresetRegexp != nil:
			structuredValues, err = ParsePreset(rule.Structured.PresetRegexp, structuredRaw)
		case rule.Structured.Format == "json":
			structuredValues, typedStructuredValues, err = parseStructuredJSONTyped(structuredRaw)
		default:
			structuredValues, err = ParseStructured(rule.Structured.Format, structuredRaw)
		}
		if err != nil {
			return nil, fmt.Errorf("parse structured data: %w", err)
		}
	}

	var extraJSON string
	if structuredValues != nil && slices.ContainsFunc(rule.Fields, func(f rules.Field) bool { return f.Extra }) {
		extraValues := typedStructuredValues
		if extraValues == nil {
			extraValues = stringMapToAny(structuredValues)
		}
		extraJSON, err = marshalUnconsumed(rule.Fields, extraValues)
		if err != nil {
			return nil, fmt.Errorf("encode extra field: %w", err)
		}
	}

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

to:

```go
func Convert(rule rules.Rule, raw map[string]string, source SourceMeta, now time.Time, mask []rules.MaskRule) (values map[string]any, err error) {
	keyRules, patternRules := SplitMaskRules(mask)

	var structuredValues map[string]string
	// typedStructuredValues preserves each JSON value's original type
	// (number/bool/nested object or array), instead of the flattened
	// string form structuredValues uses - see marshalUnconsumed. Only
	// populated for the plain "json" format, since ltsv/logfmt/preset
	// values have no native type richer than a string to preserve.
	var typedStructuredValues map[string]any
	if rule.Structured != nil {
		structuredRaw := raw[rule.Structured.Source]
		switch {
		case rule.Structured.PresetRegexp != nil:
			structuredValues, err = ParsePreset(rule.Structured.PresetRegexp, structuredRaw)
		case rule.Structured.Format == "json":
			structuredValues, typedStructuredValues, err = parseStructuredJSONTyped(structuredRaw, keyRules)
		default:
			structuredValues, err = ParseStructured(rule.Structured.Format, structuredRaw, keyRules)
		}
		if err != nil {
			return nil, fmt.Errorf("parse structured data: %w", err)
		}
	}

	var extraJSON string
	if structuredValues != nil && slices.ContainsFunc(rule.Fields, func(f rules.Field) bool { return f.Extra }) {
		extraValues := typedStructuredValues
		if extraValues == nil {
			extraValues = stringMapToAny(structuredValues)
		}
		extraJSON, err = marshalUnconsumed(rule.Fields, extraValues)
		if err != nil {
			return nil, fmt.Errorf("encode extra field: %w", err)
		}
	}

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
		v, err := convertValue(rawValue, field, now, patternRules)
		if err != nil {
			return nil, err
		}
		converted[field.Name] = v
	}
	return converted, nil
}
```

Change `MatchAndConvertFrom` (currently lines 178-198):

```go
func MatchAndConvertFrom(ruleList []rules.Rule, startIndex int, line string, source SourceMeta, now time.Time) (rule *rules.Rule, ruleIndex int, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	for i := startIndex; i < len(ruleList); i++ {
		r := &ruleList[i]
		captured, matched := matchRule(r, line)
		if !matched {
			continue
		}

		if r.ContinuationRegexp != nil {
			return r, i, captured, nil, attempts, true
		}

		v, err := Convert(*r, captured, source, now)
		if err != nil {
			attempts = append(attempts, MatchAttempt{RuleName: r.Name, Err: err})
			continue
		}
		return r, i, captured, v, attempts, true
	}
	return nil, -1, nil, nil, attempts, false
}
```

to:

```go
func MatchAndConvertFrom(ruleList []rules.Rule, startIndex int, line string, source SourceMeta, now time.Time, mask []rules.MaskRule) (rule *rules.Rule, ruleIndex int, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	for i := startIndex; i < len(ruleList); i++ {
		r := &ruleList[i]
		captured, matched := matchRule(r, line)
		if !matched {
			continue
		}

		if r.ContinuationRegexp != nil {
			return r, i, captured, nil, attempts, true
		}

		v, err := Convert(*r, captured, source, now, mask)
		if err != nil {
			attempts = append(attempts, MatchAttempt{RuleName: r.Name, Err: err})
			continue
		}
		return r, i, captured, v, attempts, true
	}
	return nil, -1, nil, nil, attempts, false
}
```

Change `MatchAndConvert` (currently lines 209-212):

```go
func MatchAndConvert(ruleList []rules.Rule, line string, source SourceMeta, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	rule, _, raw, values, attempts, ok = MatchAndConvertFrom(ruleList, 0, line, source, now)
	return
}
```

to:

```go
func MatchAndConvert(ruleList []rules.Rule, line string, source SourceMeta, now time.Time, mask []rules.MaskRule) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	rule, _, raw, values, attempts, ok = MatchAndConvertFrom(ruleList, 0, line, source, now, mask)
	return
}
```

- [ ] **Step 6: Thread `patternRules []rules.MaskRule` through `internal/parse/convertvalue.go`**

Replace the whole file:

```go
package parse

import (
	"fmt"
	"strconv"
	"time"

	"github.com/wtnb75/logidx/internal/rules"
)

// convertValue applies the replace chain (if configured), then normalization
// (if configured), then converts the resulting string into the Go value
// matching field.Type. For field.Type == "string", patternRules (Config.Mask
// entries with Type == "pattern", see SplitMaskRules) are applied last, so
// masking sees the fully replaced/normalized value. Returns an error if the
// value cannot be converted, in which case the caller should treat the whole
// line as unmatched.
func convertValue(raw string, field rules.Field, now time.Time, patternRules []rules.MaskRule) (any, error) {
	replaced := raw
	for _, r := range field.Replace {
		replaced = r.Regexp.ReplaceAllString(replaced, r.Replacement)
	}

	normalized := replaced
	if len(field.Normalize) > 0 {
		normalized = applyNormalize(replaced, field.Normalize)
	}

	switch field.Type {
	case "string":
		return ApplyPatternMask(normalized, patternRules), nil
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
		v, err := parseTimestamp(normalized, field.ResolvedFormat, now)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp: %w", err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported field type %q", field.Type)
	}
}
```

- [ ] **Step 7: Try building — expect it to fail on stale test call sites**

Run: `go build ./... && go vet ./...`
Expected: FAIL, with "not enough arguments in call to ..." errors pointing at every unmodified test call site of `Convert`, `MatchAndConvertFrom`, `MatchAndConvert`, `ParseStructured`, and `convertValue`.

- [ ] **Step 8: Fix every stale test call site**

Every call site below ends its argument list with a trailing `)` immediately after the function's last existing argument. The fix is mechanical: insert `, nil` immediately before that final `)` (the changed functions all default correctly to "no masking" for `nil`, since ranging over a `nil` slice is a no-op). Use the `rg` commands below to re-locate exact current line numbers (they will have shifted slightly since Step 7 due to `mask.go`'s new tests), then fix each with `Edit`. As of writing this plan, the affected call sites are:

`ParseStructured(...)` — append `, nil)` in place of the final `)` — 24 call sites, all single-line, in `internal/parse/structured_test.go` (lines 9, 19, 30, 40, 50, 61, 72, 82, 89, 99, 108, 115, 122, 129, 139, 149, 156, 166, 179, 190, 201, 212, 219, 226).

`Convert(...)` — every call ends `..., now)`; change to `..., now, nil)` — 19 call sites in `internal/parse/match_test.go` (lines 270, 285, 303, 325, 342, 372, 415, 438, 461, 484, 506, 527, 546, 572, 584, 603 — some are multi-line calls; the trailing `now)` is always the very last token of the call regardless).

`MatchAndConvertFrom(...)` — same `now)` → `now, nil)` rule — 2 call sites in `internal/parse/match_test.go` (lines 190, 217).

`MatchAndConvert(...)` — same `now)` → `now, nil)` rule — 6 call sites in `internal/parse/match_test.go` (lines 44, 69, 90, 121, 154, 392) and 1 call site in `internal/parse/presets_test.go` (line 135, argument is `tc.now` — becomes `tc.now, nil)`).

`convertValue(...)` — every call ends `..., now)`; change to `..., now, nil)` — 14 call sites in `internal/parse/convertvalue_test.go` (lines 13, 27, 50, 65, 82, 104, 115, 126, 134, 155, 171, 180, 196, 212).

Re-run `rg -n "\bConvert\(|MatchAndConvertFrom\(|\bMatchAndConvert\(|\bParseStructured\(|\bconvertValue\(" internal/parse/*_test.go` after editing to confirm no call site was missed (every remaining occurrence should be a function *definition*, or already carry the new trailing `nil`/`mask`/`patternRules`/`keyRules` argument).

- [ ] **Step 9: Add masking-specific tests to `internal/parse/structured_test.go`**

```go
func TestParseStructured_JSON_KeyMaskRedactsTopLevelKey(t *testing.T) {
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^password$", "redact", "[MASKED]", 0)}

	got, err := ParseStructured("json", `{"user":"alice","password":"hunter2"}`, keyRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["password"] != "[MASKED]" {
		t.Errorf("password = %q, want [MASKED]", got["password"])
	}
	if got["user"] != "alice" {
		t.Errorf("user = %q, want unchanged alice", got["user"])
	}
}

func TestParseStructured_JSON_KeyMaskRedactsNestedKey(t *testing.T) {
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^email$", "redact", "[EMAIL]", 0)}

	got, err := ParseStructured("json", `{"user":{"email":"a@example.com","name":"alice"}}`, keyRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["user"] != `{"email":"[EMAIL]","name":"alice"}` {
		t.Errorf("user = %q, want nested email masked", got["user"])
	}
}

func TestParseStructured_LTSV_KeyMaskRedactsTopLevelKey(t *testing.T) {
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^status$", "redact", "[MASKED]", 0)}

	got, err := ParseStructured("ltsv", "host:example.com\tstatus:200", keyRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["status"] != "[MASKED]" {
		t.Errorf("status = %q, want [MASKED]", got["status"])
	}
	if got["host"] != "example.com" {
		t.Errorf("host = %q, want unchanged example.com", got["host"])
	}
}

func TestParseStructured_Logfmt_KeyMaskRedactsTopLevelKey(t *testing.T) {
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^pid$", "redact", "[MASKED]", 0)}

	got, err := ParseStructured("logfmt", "level=info pid=123", keyRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["pid"] != "[MASKED]" {
		t.Errorf("pid = %q, want [MASKED]", got["pid"])
	}
	if got["level"] != "info" {
		t.Errorf("level = %q, want unchanged info", got["level"])
	}
}
```

Note: `structured_test.go` currently imports only `"regexp"` and `"testing"` — it does **not** import `internal/rules`. Add it:

```go
import (
	"regexp"
	"testing"

	"github.com/wtnb75/logidx/internal/rules"
)
```

- [ ] **Step 10: Add masking-specific tests to `internal/parse/match_test.go`**

```go
func TestConvert_KeyMaskAppliesToKeyMappedFieldAndExtra(t *testing.T) {
	rule := rules.Rule{
		Name:       "container_log",
		Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
		Fields: []rules.Field{
			{Name: "email", Type: "string", Key: "email"},
			{Name: "extra", Type: "string", Extra: true},
		},
	}
	now := time.Now()
	mask := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^email$", "redact", "[EMAIL]", 0)}

	values, err := Convert(rule, map[string]string{
		"json": `{"email":"a@example.com","pid":1}`,
	}, SourceMeta{}, now, mask)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["email"] != "[EMAIL]" {
		t.Errorf("email = %v, want [EMAIL]", values["email"])
	}
	if values["extra"] != `{"pid":1}` {
		t.Errorf(`extra = %v, want {"pid":1} (masked key must not leak into extra either)`, values["extra"])
	}
}

func TestConvert_PatternMaskAppliesToStringFieldAndExtraNotToIntField(t *testing.T) {
	rule := rules.Rule{
		Name:       "container_log",
		Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
		Fields: []rules.Field{
			{Name: "message", Type: "string", Key: "message"},
			{Name: "status", Type: "int", Key: "status"},
			{Name: "extra", Type: "string", Extra: true},
		},
	}
	now := time.Now()
	mask := []rules.MaskRule{mustMaskRuleT(t, "pattern", `[\w.+-]+@[\w.-]+\.\w+`, "redact", "[EMAIL]", 0)}

	values, err := Convert(rule, map[string]string{
		"json": `{"message":"contact a@example.com","status":200,"note":"cc b@example.com"}`,
	}, SourceMeta{}, now, mask)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if values["message"] != "contact [EMAIL]" {
		t.Errorf("message = %v, want email masked", values["message"])
	}
	if values["status"] != int64(200) {
		t.Errorf("status = %v, want unmasked int64(200) (pattern mask must not apply to non-string fields)", values["status"])
	}
	if values["extra"] != `{"note":"cc [EMAIL]"}` {
		t.Errorf("extra = %v, want email masked inside extra string value too", values["extra"])
	}
}
```

- [ ] **Step 11: Add masking-specific tests to `internal/parse/convertvalue_test.go`**

```go
func TestConvertValue_PatternMaskAppliesAfterReplaceAndNormalizeForStringType(t *testing.T) {
	now := time.Now()
	patternRules := []rules.MaskRule{mustMaskRuleT(t, "pattern", `[\w.+-]+@[\w.-]+\.\w+`, "redact", "[EMAIL]", 0)}

	v, err := convertValue("contact a@example.com", rules.Field{Type: "string"}, now, patternRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "contact [EMAIL]" {
		t.Errorf("convertValue() = %v, want contact [EMAIL]", v)
	}
}

func TestConvertValue_PatternMaskNotAppliedToIntType(t *testing.T) {
	now := time.Now()
	patternRules := []rules.MaskRule{mustMaskRuleT(t, "pattern", `\d+`, "redact", "[NUM]", 0)}

	v, err := convertValue("512", rules.Field{Type: "int"}, now, patternRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != int64(512) {
		t.Errorf("convertValue() = %v, want unmasked int64(512)", v)
	}
}
```

- [ ] **Step 12: Build and run the full package test suite**

Run: `go build ./... && go vet ./... && go test ./internal/parse/... -v`
Expected: PASS — every pre-existing test (now passing `nil`/no mask) plus every new test above.

- [ ] **Step 13: Commit**

```bash
git add internal/parse/mask.go internal/parse/mask_test.go internal/parse/structured.go internal/parse/structured_test.go internal/parse/match.go internal/parse/match_test.go internal/parse/convertvalue.go internal/parse/convertvalue_test.go internal/parse/presets_test.go
git commit -m "feat(parse): wire mask: key/pattern masking into structured parsing and value conversion"
```

---

### Task 3: `internal/convert` — wire `Config.Mask` into `fileCursor`, mask `unmatched.txt`, end-to-end test

**Files:**
- Modify: `internal/convert/merge.go` (`fileCursor` struct, `newFileCursor`, `tryCandidates`'s `MatchAndConvertFrom` call, `finalizeEntry`'s `Convert` call, `writeUnmatchedLine`)
- Test: `internal/convert/merge_test.go` (append end-to-end masking test)

**Interfaces:**
- Consumes: `parse.SplitMaskRules`, `parse.ApplyPatternMask` (Task 2); `rules.Config.Mask` (Task 1); `parse.MatchAndConvertFrom(..., mask []rules.MaskRule)`, `parse.Convert(..., mask []rules.MaskRule)` (Task 2).

- [ ] **Step 1: Add `patternMaskRules` to the `fileCursor` struct**

Change (currently `merge.go:94-118`):

```go
type fileCursor struct {
	inputPath string
	fileIndex int
	file      *os.File // nil when reading os.Stdin
	// decompressCloser releases the decompressor wrapping file's contents,
	// if inputPath's extension named a supported compression format (see
	// decompress.Wrap). nil for uncompressed input, stdin, or a format
	// (like bzip2) whose decoder holds nothing beyond the underlying
	// reader.
	decompressCloser io.Closer
	scanner          *bufio.Scanner
	lineNum          int

	cfg      *rules.Config
	mergeKey map[string]string
	set      *writer.Set
	logger   *slog.Logger
	now      time.Time

	counts    map[string]int
	unmatched int

	open    *openEntry
	pending []scannedLine
}
```

to:

```go
type fileCursor struct {
	inputPath string
	fileIndex int
	file      *os.File // nil when reading os.Stdin
	// decompressCloser releases the decompressor wrapping file's contents,
	// if inputPath's extension named a supported compression format (see
	// decompress.Wrap). nil for uncompressed input, stdin, or a format
	// (like bzip2) whose decoder holds nothing beyond the underlying
	// reader.
	decompressCloser io.Closer
	scanner          *bufio.Scanner
	lineNum          int

	cfg      *rules.Config
	mergeKey map[string]string
	set      *writer.Set
	logger   *slog.Logger
	now      time.Time
	// patternMaskRules is cfg.Mask's Type == "pattern" subset (see
	// parse.SplitMaskRules), precomputed once here so writeUnmatchedLine
	// doesn't re-filter cfg.Mask on every unmatched line.
	patternMaskRules []rules.MaskRule

	counts    map[string]int
	unmatched int

	open    *openEntry
	pending []scannedLine
}
```

- [ ] **Step 2: Compute `patternMaskRules` in `newFileCursor`**

Change (currently `merge.go:124-154`):

```go
func newFileCursor(inputPath string, fileIndex int, cfg *rules.Config, mergeKey map[string]string, set *writer.Set, logger *slog.Logger, now time.Time) (*fileCursor, error) {
	var f *os.File
	in := io.Reader(os.Stdin)
	var decompressCloser io.Closer
	if inputPath != "-" {
		var err error
		f, err = os.Open(inputPath)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		in, decompressCloser, err = decompress.Wrap(inputPath, f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("open input: %w", err)
		}
	}

	return &fileCursor{
		inputPath:        inputPath,
		fileIndex:        fileIndex,
		file:             f,
		decompressCloser: decompressCloser,
		scanner:          bufio.NewScanner(in),
		cfg:              cfg,
		mergeKey:         mergeKey,
		set:              set,
		logger:           logger,
		now:              now,
		counts:           map[string]int{},
	}, nil
}
```

to:

```go
func newFileCursor(inputPath string, fileIndex int, cfg *rules.Config, mergeKey map[string]string, set *writer.Set, logger *slog.Logger, now time.Time) (*fileCursor, error) {
	var f *os.File
	in := io.Reader(os.Stdin)
	var decompressCloser io.Closer
	if inputPath != "-" {
		var err error
		f, err = os.Open(inputPath)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		in, decompressCloser, err = decompress.Wrap(inputPath, f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("open input: %w", err)
		}
	}

	_, patternMaskRules := parse.SplitMaskRules(cfg.Mask)

	return &fileCursor{
		inputPath:        inputPath,
		fileIndex:        fileIndex,
		file:             f,
		decompressCloser: decompressCloser,
		scanner:          bufio.NewScanner(in),
		cfg:              cfg,
		mergeKey:         mergeKey,
		set:              set,
		logger:           logger,
		now:              now,
		patternMaskRules: patternMaskRules,
		counts:           map[string]int{},
	}, nil
}
```

- [ ] **Step 3: Pass `c.cfg.Mask` at both `parse.MatchAndConvertFrom`/`parse.Convert` call sites**

Change (currently `merge.go:269`):

```go
	rule, ruleIndex, raw, values, attempts, matched := parse.MatchAndConvertFrom(c.cfg.Rules, startIndex, line.text, parse.SourceMeta{File: c.inputPath, Line: line.lineNum}, c.now)
```

to:

```go
	rule, ruleIndex, raw, values, attempts, matched := parse.MatchAndConvertFrom(c.cfg.Rules, startIndex, line.text, parse.SourceMeta{File: c.inputPath, Line: line.lineNum}, c.now, c.cfg.Mask)
```

Change (currently `merge.go:296`):

```go
	values, convErr := parse.Convert(*entry.rule, entry.raw, parse.SourceMeta{File: c.inputPath, Line: entry.rawLines[0].lineNum}, c.now)
```

to:

```go
	values, convErr := parse.Convert(*entry.rule, entry.raw, parse.SourceMeta{File: c.inputPath, Line: entry.rawLines[0].lineNum}, c.now, c.cfg.Mask)
```

- [ ] **Step 4: Apply pattern masking in `writeUnmatchedLine`**

Change (currently `merge.go:224-230`):

```go
func (c *fileCursor) writeUnmatchedLine(line scannedLine) error {
	if err := c.set.WriteUnmatched(c.inputPath, line.lineNum, line.text); err != nil {
		return fmt.Errorf("write unmatched line %d: %w", line.lineNum, err)
	}
	c.unmatched++
	return nil
}
```

to:

```go
func (c *fileCursor) writeUnmatchedLine(line scannedLine) error {
	text := parse.ApplyPatternMask(line.text, c.patternMaskRules)
	if err := c.set.WriteUnmatched(c.inputPath, line.lineNum, text); err != nil {
		return fmt.Errorf("write unmatched line %d: %w", line.lineNum, err)
	}
	c.unmatched++
	return nil
}
```

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: PASS (no other call sites in `internal/convert` touch the changed `parse` functions — confirmed by `rg -n "Convert\(|ParseStructured\(|convertValue\(|newFileCursor\(" internal/convert/merge_test.go` earlier showing no direct calls to the changed `parse` functions from tests).

- [ ] **Step 6: Add an end-to-end masking test to `internal/convert/merge_test.go`**

Append (mirrors the existing `TestFileCursor_Advance_OrphanContinuationLineIsUnmatched` in style):

```go
func TestFileCursor_WriteUnmatchedLine_AppliesPatternMask(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
mask:
  - type: pattern
    pattern: '[\w.+-]+@[\w.-]+\.\w+'
    action: redact
    value: '[EMAIL]'

rules:
  - name: with_ts
    pattern: '^TS (?P<time>\S+) (?P<msg>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      msg: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "contact admin@example.com for help\n")

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
	if err != nil || ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=false (line matches no rule)", ok, err)
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
	want := logPath + "\t1\tcontact [EMAIL] for help\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}
```

- [ ] **Step 7: Run the full package test suite**

Run: `go build ./... && go vet ./... && go test ./internal/convert/... -v`
Expected: PASS, including the new test above and every pre-existing test unchanged.

- [ ] **Step 8: Commit**

```bash
git add internal/convert/merge.go internal/convert/merge_test.go
git commit -m "feat(convert): apply mask: pattern masking to unmatched.txt, wire mask into matching"
```

---

### Task 4: `schema/rules.schema.json` — structural schema for `mask:`

**Files:**
- Modify: `schema/rules.schema.json`

**Interfaces:**
- None (JSON, not consumed by Go code — validated by editors via the `$schema` hint and, if present, a schema test).

- [ ] **Step 1: Check for an existing schema-validation test to extend**

Run: `rg -rn "rules.schema.json" internal/ cmd/ --type go`
If a Go test loads and validates `schema/rules.schema.json` against example YAML (e.g. as part of `internal/schema`), note its file/pattern to add a `mask:`-bearing case in Step 3. If none exists, skip straight to Step 2 (this repo may only lint the schema via the editor's `$schema` hint, per `README.md`'s "Editor integration" section).

- [ ] **Step 2: Add the `mask:` schema**

In `schema/rules.schema.json`, add `"mask": { "$ref": "#/$defs/mask..." }` style referencing — concretely, change the root `properties` (currently):

```json
  "properties": {
    "rules": {
      "type": "array",
      "minItems": 1,
      "items": { "$ref": "#/$defs/rule" }
    },
    "compression": { "$ref": "#/$defs/compression" },
    "row_group": { "$ref": "#/$defs/row_group" }
  },
```

to:

```json
  "properties": {
    "rules": {
      "type": "array",
      "minItems": 1,
      "items": { "$ref": "#/$defs/rule" }
    },
    "mask": {
      "type": "array",
      "items": { "$ref": "#/$defs/maskRule" }
    },
    "compression": { "$ref": "#/$defs/compression" },
    "row_group": { "$ref": "#/$defs/row_group" }
  },
```

Then add a `maskRule` definition to `$defs` (insert it next to `replaceOrNormalizeRule`, i.e. right after the closing `}` of `replaceOrNormalizeRule` and before `"field": {`):

```json
    "maskRule": {
      "type": "object",
      "required": ["type", "pattern", "action"],
      "properties": {
        "type": { "type": "string", "enum": ["key", "pattern"] },
        "pattern": { "type": "string" },
        "action": { "type": "string", "enum": ["redact", "hash"] },
        "value": { "type": "string" },
        "length": { "type": "integer", "minimum": 1, "maximum": 64 }
      },
      "additionalProperties": false,
      "oneOf": [
        {
          "properties": { "action": { "const": "redact" } },
          "required": ["value"],
          "not": { "required": ["length"] }
        },
        {
          "properties": { "action": { "const": "hash" } },
          "required": ["length"],
          "not": { "required": ["value"] }
        }
      ]
    },
```

- [ ] **Step 3: Validate the schema is well-formed JSON and self-consistent**

Run: `python3 -c "import json; json.load(open('schema/rules.schema.json'))"` (or `jq . schema/rules.schema.json >/dev/null`)
Expected: no error (valid JSON).

If Step 1 found an existing Go-based schema validation test, add one case there using the design's example `mask:` block (from `docs/superpowers/specs/2026-08-12-sensitive-data-masking-design.md` §1) and confirm it validates successfully; run that test package's `go test` and expect PASS.

- [ ] **Step 4: Commit**

```bash
git add schema/rules.schema.json
git commit -m "docs(schema): add mask: to rules.yaml JSON Schema"
```

---

### Task 5: Documentation — `README.md` and `docs/reference.md`

**Files:**
- Modify: `README.md:71`
- Modify: `docs/reference.md` (Contents list, new section)

**Interfaces:** None (documentation only).

- [ ] **Step 1: Update `README.md`'s feature-list line**

Change (currently `README.md:71`):

```markdown
See the [reference](docs/reference.md) for the full `rules.yaml` format: presets, timestamp formats, field value transforms, multi-line entries, embedded structured data, compression, and more.
```

to:

```markdown
See the [reference](docs/reference.md) for the full `rules.yaml` format: presets, timestamp formats, field value transforms, multi-line entries, embedded structured data, compression, sensitive data masking, and more.
```

- [ ] **Step 2: Add a Contents entry to `docs/reference.md`**

Change (currently `docs/reference.md:12-13`):

```markdown
- [Partial structured-data parsing (`structured:` / `key:` / `extra:`)](#partial-structured-data-parsing-structured--key--extra)
- [Source location metadata (`meta:`)](#source-location-metadata-meta)
```

to:

```markdown
- [Partial structured-data parsing (`structured:` / `key:` / `extra:`)](#partial-structured-data-parsing-structured--key--extra)
- [Sensitive data masking (`mask:`)](#sensitive-data-masking-mask)
- [Source location metadata (`meta:`)](#source-location-metadata-meta)
```

- [ ] **Step 3: Add the `mask:` section body to `docs/reference.md`**

Insert the new section immediately before `## Source location metadata (`meta:`)` (currently `docs/reference.md:320`), i.e. right after the `#### `structured.format` as a preset name` subsection ends (currently lines 316-318):

```markdown
- The names usable in `key:` are the field names listed in that preset's own `fields:` definition (`apache_clf`: `remote_addr`/`remote_user`/`time`/`method`/`path`/`proto`/`status`/`bytes`; `apache_combined`: the same 8 plus `referer`/`user_agent`, 10 total; `syslog_rfc3164`: `time`/`host`/`tag`/`pid`/`message`; `syslog_rfc5424`: `pri`/`version`/`time`/`host`/`app`/`procid`/`msgid`/`sd`/`message`). As with ordinary `structured:` usage, you can pick any subset of keys and give them whatever field name/type you like (the example above receives `time` under the name `access_time`).
- If the preset's fixed pattern doesn't match the text captured by `structured.source`, it's treated the same as an ordinary structured-data parse failure and written to `unmatched.txt`.
- This is independent of the rule-level `preset:` shortcut (which replaces the whole `pattern`/`fields`); there's no special interaction between the two.

## Sensitive data masking (`mask:`)

`mask:` is a `rules.yaml`-wide, top-level list (like `compression:`/`row_group:`, not nested under any one rule) that redacts or deterministically hashes sensitive values before they reach Parquet or `unmatched.txt`:

```yaml
mask:
  - type: key
    pattern: '(?i)^(password|pwd|secret|api[_-]?key|token)$'
    action: redact
    value: '[MASKED]'
  - type: key
    pattern: '(?i)^(email|user_email)$'
    action: hash
    length: 8
  - type: pattern
    pattern: '[\w.+-]+@[\w.-]+\.\w+'
    action: hash
    length: 8
```

- **`type: key`** matches a **key name** in a rule's `structured:`-parsed JSON/LTSV/logfmt data and replaces that key's *entire value*. For JSON, this recurses into nested objects and arrays at any depth (a `password` key three levels deep is masked just like a top-level one); LTSV/logfmt have no nesting, so only their top-level keys are checked. It fires for both `key:`-mapped fields and whatever lands in an `extra:` column, and does nothing on a rule with no `structured:` block. It does **not** apply to preset structured formats (`structured.format: apache_clf` etc.) — presets have a small, fixed set of key names, so masking them by regex isn't useful the way it is for free-form JSON/LTSV/logfmt.
- **`type: pattern`** matches a substring inside a **`type: string` field's value** (mapped fields and `extra:` alike, since `extra:` is always a JSON string) and replaces just the matched part — the same "keep the rest of the value" idea as `replace:`. It also applies to raw lines written to `unmatched.txt`. It is never applied to `int`/`float`/`timestamp` fields, to avoid turning a valid number into unparsable masked text.
- **`action: redact`** replaces the match with the fixed `value:` string (`value: ''` deletes it, same as `replace:`'s `value: ''`). **`action: hash`** replaces it with the first `length` (1-64) hex characters of its SHA-256 digest — no secret key, so the same input always hashes to the same short value. That's deliberate: values stay hidden, but rows sharing the same original value (e.g. the same user's email) still hash identically, so you can still correlate/group by them.
- Multiple `mask:` entries matching the same key (for `type: key`) or the same value (for `type: pattern`) chain in declaration order — each rule's output feeds the next.
- `mask:` has no per-rule override; it's one global list applied identically everywhere it can fire.

## Source location metadata (`meta:`)
```

(The last line above, `## Source location metadata (`meta:`)`, is the pre-existing heading — it is repeated here only to show the exact insertion point; do not duplicate it.)

- [ ] **Step 4: Spot-check the rendered Markdown**

Run: `rg -n "^## " docs/reference.md` and confirm `## Sensitive data masking (`mask:`)` appears once, in the right place, and the Contents anchor link (`#sensitive-data-masking-mask`) matches GitHub's heading-to-anchor slug rules (lowercase, spaces→hyphens, backticks/parens/colons stripped) — compare against the existing `#partial-structured-data-parsing-structured--key--extra` entry's pattern for consistency.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/reference.md
git commit -m "docs: document mask: sensitive data masking in the rules.yaml reference"
```

---

### Task 6: Final regression pass

**Files:** None (verification only).

- [ ] **Step 1: Format and vet**

Run: `gofmt -l .` — expected: empty output (no files need formatting; if any are listed, run `gofmt -w` on them and re-check).
Run: `go vet ./...` — expected: no output.

- [ ] **Step 2: Lint**

Run: `golangci-lint run ./...`
Expected: no new findings introduced by this feature (pre-existing findings, if any, are out of scope).

- [ ] **Step 3: Full test suite**

Run: `go test ./... -count=1`
Expected: PASS, all packages, including every pre-existing test (unmodified assertions — the mask: unset behavior regression guarantee from Global Constraints) and every new test from Tasks 1-3.

- [ ] **Step 4: Manual smoke test with a real rules.yaml + log file**

```bash
dir=$(mktemp -d)
cat > "$dir/rules.yaml" <<'EOF'
mask:
  - type: key
    pattern: '(?i)^(password|pwd)$'
    action: redact
    value: '[MASKED]'
  - type: pattern
    pattern: '[\w.+-]+@[\w.-]+\.\w+'
    action: hash
    length: 8

rules:
  - name: app_log
    pattern: '^(?P<time>\S+) (?P<json>\{.*\})$'
    structured:
      source: json
      format: json
    fields:
      time:
        type: timestamp
        format: iso8601
      user:
        type: string
        key: user
      password:
        type: string
        key: password
      extra:
        type: string
        extra: true
EOF
cat > "$dir/in.log" <<'EOF'
2026-08-06T12:00:00Z {"user":"alice","password":"hunter2","email":"alice@example.com"}
this line matches nothing, contact bob@example.com
EOF
go run ./cmd/logidx import --rules "$dir/rules.yaml" --out "$dir/out" "$dir/in.log"
go run ./cmd/logidx dump "$dir/out/app_log.parquet" -
cat "$dir/out/unmatched.txt"
```

Expected: the dumped Parquet row shows `"password":"[MASKED]"` and an `extra` column containing a hashed (not plaintext) `email`; `unmatched.txt`'s line has `bob@example.com` replaced by its hash, not left in plaintext.

- [ ] **Step 5: Report**

Summarize (no commit needed — this task is verification-only): confirm all four checks above passed, and paste the smoke test's Parquet dump + `unmatched.txt` output showing masking took effect.

---

## Summary of files touched

- `internal/rules/rules.go`, `internal/rules/validate.go` — `MaskRule`, `Config.Mask`, load-time compile, validation (Task 1)
- `internal/parse/mask.go` (new), `internal/parse/structured.go`, `internal/parse/match.go`, `internal/parse/convertvalue.go` — masking primitives and data-flow wiring (Task 2)
- `internal/convert/merge.go` — `fileCursor` wiring, `unmatched.txt` masking (Task 3)
- `schema/rules.schema.json` — structural schema for `mask:` (Task 4)
- `README.md`, `docs/reference.md` — documentation (Task 5)
- Test files: `internal/rules/rules_test.go`, `internal/rules/validate_test.go`, `internal/parse/mask_test.go` (new), `internal/parse/structured_test.go`, `internal/parse/match_test.go`, `internal/parse/convertvalue_test.go`, `internal/parse/presets_test.go`, `internal/convert/merge_test.go`
