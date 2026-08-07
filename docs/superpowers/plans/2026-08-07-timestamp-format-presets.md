# Timestamp Format Presets & strptime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `rules.yaml` timestamp fields specify `format:` as a named preset (`iso8601`, `unix`, `clf`, ...), a strptime-style pattern (`%Y-%m-%d %H:%M:%S`), or (as today) a raw Go reference-time layout string, auto-detected from the value with zero YAML schema changes.

**Architecture:** `internal/rules` gains `ResolveFormat(format string) (TimeFormat, error)`, which resolves a `Field.Format` string into either a Go layout string or an epoch unit, exactly once at `rules.Load()` time (mirroring how `Rule.Pattern` is compiled once into `Rule.Regexp`). `internal/parse`'s `parseTimestamp` stops interpreting the raw `Format` string per log line and instead consumes the pre-resolved `rules.TimeFormat`, branching to either the existing Go-layout parser or a new epoch parser.

**Tech Stack:** Go 1.25, stdlib `time`/`strconv`/`strings` only (no new dependencies).

## Global Constraints

- Design source: `docs/superpowers/specs/2026-08-07-timestamp-format-presets-design.md` — resolve any ambiguity in this plan against that spec.
- No new third-party dependencies (`go.mod` stays limited to what's already there).
- Preset names are matched case-sensitively; strptime detection is "starts with `%`"; anything else is a raw Go layout — this exact 3-way order must not change (see spec's "自動判別ルール").
- Every commit must pass `gofmt -l .` (no output), `go build ./...`, `go vet ./...`, `go test ./...`, and `golangci-lint run ./...` (0 issues) before moving to the next task.

---

### Task 1: `rules.ResolveFormat` — presets, strptime translation, raw layout passthrough

**Files:**
- Create: `internal/rules/timeformat.go`
- Create: `internal/rules/timeformat_test.go`

**Interfaces:**
- Consumes: nothing from other tasks (pure, self-contained).
- Produces: `type TimeFormat struct { Layout string; EpochUnit time.Duration }` and `func ResolveFormat(format string) (TimeFormat, error)`, both exported from package `rules`. Task 2 calls `ResolveFormat` from `rules.Load()`. Task 3 consumes the `TimeFormat` type in `internal/parse`.

- [ ] **Step 1: Write `internal/rules/timeformat_test.go`**

```go
package rules

import (
	"strings"
	"testing"
	"time"
)

func TestResolveFormat_Presets(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		wantLayout string
	}{
		{"iso8601", "iso8601", "2006-01-02T15:04:05.999999999Z07:00"},
		{"rfc3339 alias", "rfc3339", "2006-01-02T15:04:05.999999999Z07:00"},
		{"rfc822", "rfc822", "02 Jan 06 15:04 -0700"},
		{"rfc2822", "rfc2822", "Mon, 02 Jan 2006 15:04:05 -0700"},
		{"clf", "clf", "02/Jan/2006:15:04:05 -0700"},
		{"syslog", "syslog", "Jan _2 15:04:05"},
		{"pylog", "pylog", "2006-01-02 15:04:05,999999999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveFormat(tt.format)
			if err != nil {
				t.Fatalf("ResolveFormat(%q): %v", tt.format, err)
			}
			if got.Layout != tt.wantLayout {
				t.Errorf("Layout = %q, want %q", got.Layout, tt.wantLayout)
			}
			if got.EpochUnit != 0 {
				t.Errorf("EpochUnit = %v, want 0", got.EpochUnit)
			}
		})
	}
}

func TestResolveFormat_EpochPresets(t *testing.T) {
	tests := []struct {
		format string
		want   time.Duration
	}{
		{"unix", time.Second},
		{"unix_ms", time.Millisecond},
		{"unix_us", time.Microsecond},
		{"unix_ns", time.Nanosecond},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			got, err := ResolveFormat(tt.format)
			if err != nil {
				t.Fatalf("ResolveFormat(%q): %v", tt.format, err)
			}
			if got.EpochUnit != tt.want {
				t.Errorf("EpochUnit = %v, want %v", got.EpochUnit, tt.want)
			}
			if got.Layout != "" {
				t.Errorf("Layout = %q, want empty for an epoch format", got.Layout)
			}
		})
	}
}

func TestResolveFormat_Strptime(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		wantLayout string
	}{
		{"date and time", "%Y-%m-%d %H:%M:%S", "2006-01-02 15:04:05"},
		{"with numeric offset", "%Y-%m-%dT%H:%M:%S%z", "2006-01-02T15:04:05-0700"},
		{"comma fractional seconds", "%Y-%m-%d %H:%M:%S,%f", "2006-01-02 15:04:05,999999999"},
		{"rfc2822-equivalent", "%a, %d %b %Y %H:%M:%S %z", "Mon, 02 Jan 2006 15:04:05 -0700"},
		{"12-hour clock", "%I:%M %p", "03:04 PM"},
		{"literal percent", "%Y%%", "2006%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveFormat(tt.format)
			if err != nil {
				t.Fatalf("ResolveFormat(%q): %v", tt.format, err)
			}
			if got.Layout != tt.wantLayout {
				t.Errorf("Layout = %q, want %q", got.Layout, tt.wantLayout)
			}
			if got.EpochUnit != 0 {
				t.Errorf("EpochUnit = %v, want 0", got.EpochUnit)
			}
		})
	}
}

func TestResolveFormat_StrptimeUnsupportedDirectiveIsError(t *testing.T) {
	_, err := ResolveFormat("%j")
	if err == nil {
		t.Fatal("expected error for unsupported directive %j")
	}
	if !strings.Contains(err.Error(), "%j") {
		t.Errorf("expected error to mention %%j, got: %v", err)
	}
}

func TestResolveFormat_StrptimeTrailingPercentIsError(t *testing.T) {
	_, err := ResolveFormat("%Y-%")
	if err == nil {
		t.Fatal("expected error for trailing %")
	}
}

func TestResolveFormat_RawGoLayoutPassesThroughUnchanged(t *testing.T) {
	format := "02/Jan/2006:15:04:05 -0700"
	got, err := ResolveFormat(format)
	if err != nil {
		t.Fatalf("ResolveFormat(%q): %v", format, err)
	}
	if got.Layout != format {
		t.Errorf("Layout = %q, want %q (unchanged)", got.Layout, format)
	}
	if got.EpochUnit != 0 {
		t.Errorf("EpochUnit = %v, want 0", got.EpochUnit)
	}
}

func TestResolveFormat_EmptyStringIsNotAnError(t *testing.T) {
	got, err := ResolveFormat("")
	if err != nil {
		t.Fatalf(`ResolveFormat(""): %v`, err)
	}
	if got.Layout != "" || got.EpochUnit != 0 {
		t.Errorf("got %+v, want zero value", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails to compile (ResolveFormat doesn't exist yet)**

Run: `go test ./internal/rules/... -run TestResolveFormat -v`
Expected: FAIL — `undefined: ResolveFormat` (and `TimeFormat`)

- [ ] **Step 3: Write `internal/rules/timeformat.go`**

```go
package rules

import (
	"fmt"
	"strings"
	"time"
)

// TimeFormat is a timestamp Field's Format string, resolved once (at
// rules.Load time, via ResolveFormat) into an efficient parsing strategy -
// mirroring how Rule.Pattern is compiled once into Rule.Regexp rather than
// re-interpreted on every log line.
//
// EpochUnit == 0 means "not an epoch format": parse with Layout via
// time.ParseInLocation. EpochUnit != 0 means "epoch format": parse as a
// number of EpochUnit ticks since the Unix epoch (Layout is unused).
type TimeFormat struct {
	Layout    string
	EpochUnit time.Duration
}

// presetLayouts maps a format preset name to the Go reference-time layout
// it resolves to.
var presetLayouts = map[string]string{
	"iso8601": "2006-01-02T15:04:05.999999999Z07:00",
	"rfc3339": "2006-01-02T15:04:05.999999999Z07:00",
	"rfc822":  "02 Jan 06 15:04 -0700",
	"rfc2822": "Mon, 02 Jan 2006 15:04:05 -0700",
	"clf":     "02/Jan/2006:15:04:05 -0700",
	"syslog":  "Jan _2 15:04:05",
	"pylog":   "2006-01-02 15:04:05,999999999",
}

// presetEpochUnits maps a format preset name to the unit its numeric value
// counts since the Unix epoch.
var presetEpochUnits = map[string]time.Duration{
	"unix":    time.Second,
	"unix_ms": time.Millisecond,
	"unix_us": time.Microsecond,
	"unix_ns": time.Nanosecond,
}

// strptimeDirectives maps a strptime %-directive (without the %) to the Go
// reference-time layout token it translates to. Directives not listed here
// have no direct Go layout equivalent (e.g. %j day-of-year, %U/%W week
// number) and are rejected by strptimeToLayout with a clear error instead
// of being silently mishandled.
var strptimeDirectives = map[rune]string{
	'Y': "2006",
	'y': "06",
	'm': "01",
	'd': "02",
	'H': "15",
	'I': "03",
	'M': "04",
	'S': "05",
	'f': "999999999",
	'z': "-0700",
	'Z': "MST",
	'a': "Mon",
	'A': "Monday",
	'b': "Jan",
	'B': "January",
	'p': "PM",
	'%': "%",
}

// ResolveFormat interprets a timestamp Field's Format string, auto-detecting
// which of three styles it is:
//
//  1. A known preset name (presetLayouts/presetEpochUnits), matched
//     case-sensitively and exactly.
//  2. A strptime pattern, if it starts with '%' (see strptimeToLayout).
//  3. Otherwise, a raw Go reference-time layout string, used as-is - this
//     is what every Format string meant before presets/strptime existed,
//     so existing rules.yaml files keep working unchanged.
//
// format == "" resolves successfully to a zero-value TimeFormat; whether a
// timestamp field requires a non-empty Format is rules.Validate's concern,
// not ResolveFormat's.
func ResolveFormat(format string) (TimeFormat, error) {
	if layout, ok := presetLayouts[format]; ok {
		return TimeFormat{Layout: layout}, nil
	}
	if unit, ok := presetEpochUnits[format]; ok {
		return TimeFormat{EpochUnit: unit}, nil
	}
	if strings.HasPrefix(format, "%") {
		layout, err := strptimeToLayout(format)
		if err != nil {
			return TimeFormat{}, err
		}
		return TimeFormat{Layout: layout}, nil
	}
	return TimeFormat{Layout: format}, nil
}

// strptimeToLayout translates a strptime-style format string (%-prefixed
// directives, see strptimeDirectives) into a Go reference-time layout
// string. Characters other than a recognized %-directive are copied
// through unchanged, so literal separators (spaces, colons, commas, ...)
// need no special handling.
func strptimeToLayout(format string) (string, error) {
	runes := []rune(format)
	var b strings.Builder
	for i := 0; i < len(runes); i++ {
		if runes[i] != '%' {
			b.WriteRune(runes[i])
			continue
		}
		i++
		if i >= len(runes) {
			return "", fmt.Errorf("strptime format %q: trailing %%", format)
		}
		token, ok := strptimeDirectives[runes[i]]
		if !ok {
			return "", fmt.Errorf("strptime format %q: unsupported directive %%%c (supported: %%Y %%y %%m %%d %%H %%I %%M %%S %%f %%z %%Z %%a %%A %%b %%B %%p %%%%)", format, runes[i])
		}
		b.WriteString(token)
	}
	return b.String(), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/rules/... -run TestResolveFormat -v`
Expected: PASS (every subtest)

- [ ] **Step 5: Run the full verification suite**

Run: `gofmt -l internal/rules/ && go build ./... && go vet ./... && go test ./internal/rules/... && golangci-lint run ./internal/rules/...`
Expected: no gofmt output, all green, "0 issues"

- [ ] **Step 6: Commit**

```bash
git add internal/rules/timeformat.go internal/rules/timeformat_test.go
git commit -m "$(cat <<'EOF'
Add rules.ResolveFormat: timestamp format presets and strptime support

Resolves a Field.Format string into either a Go reference-time layout
or an epoch unit, auto-detecting whether it's a known preset name
(iso8601, unix, clf, syslog, pylog, ...), a strptime pattern (starts
with %), or (as before) a raw Go layout string used as-is.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Wire `ResolveFormat` into `rules.Field` and `rules.Load`

**Files:**
- Modify: `internal/rules/rules.go:20-28` (Field struct), `internal/rules/rules.go:121-138` (Load's field loop)
- Modify: `internal/rules/rules_test.go` (add `"strings"` import, add 2 new tests)

**Interfaces:**
- Consumes: `rules.TimeFormat`, `rules.ResolveFormat` from Task 1.
- Produces: `Field.ResolvedFormat TimeFormat` — Task 3/4 read this field instead of `Field.Format` when parsing.

- [ ] **Step 1: Add the two new tests to `internal/rules/rules_test.go`**

Add `"strings"` to the existing `import` block (currently `"os"`, `"path/filepath"`, `"testing"`), then append:

```go
func TestLoad_ResolvesTimestampFormat(t *testing.T) {
	path := writeTempRules(t, sampleRulesYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	nginx := cfg.Rules[0]
	timeField, ok := fieldByName(nginx.Fields, "time")
	if !ok {
		t.Fatal("expected time field")
	}
	wantLayout := "02/Jan/2006:15:04:05 -0700"
	if timeField.ResolvedFormat.Layout != wantLayout {
		t.Errorf("ResolvedFormat.Layout = %q, want %q", timeField.ResolvedFormat.Layout, wantLayout)
	}
}

func TestLoad_InvalidStrptimeFormatIsError(t *testing.T) {
	rulesYAML := `
rules:
  - name: bad
    pattern: '^(?P<ts>\S+)$'
    fields:
      ts:
        type: timestamp
        format: "%j"
`
	path := writeTempRules(t, rulesYAML)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unsupported strptime directive")
	}
	if !strings.Contains(err.Error(), "%j") {
		t.Errorf("expected error to mention %%j, got: %v", err)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/rules/... -run 'TestLoad_ResolvesTimestampFormat|TestLoad_InvalidStrptimeFormatIsError' -v`
Expected: `TestLoad_ResolvesTimestampFormat` FAILs (`ResolvedFormat.Layout = ""`, want the CLF layout); `TestLoad_InvalidStrptimeFormatIsError` FAILs ("expected error ... got nil")

- [ ] **Step 3: Add `ResolvedFormat` to `Field` in `internal/rules/rules.go`**

Change (around line 23-28):

```go
type Field struct {
	Name      string          `yaml:"-"`
	Type      string          `yaml:"type"`
	Format    string          `yaml:"format"`
	Normalize []NormalizeRule `yaml:"normalize"`
}
```

to:

```go
type Field struct {
	Name      string          `yaml:"-"`
	Type      string          `yaml:"type"`
	Format    string          `yaml:"format"`
	Normalize []NormalizeRule `yaml:"normalize"`

	// ResolvedFormat is Format resolved once by ResolveFormat, at Load
	// time - see TimeFormat. Only meaningful when Type == "timestamp".
	ResolvedFormat TimeFormat `yaml:"-"`
}
```

- [ ] **Step 4: Resolve the format inside `Load`'s field loop in `internal/rules/rules.go`**

Change the field loop (around line 128-137):

```go
		for fi := range cfg.Rules[i].Fields {
			field := &cfg.Rules[i].Fields[fi]
			for j := range field.Normalize {
				nre, err := regexp.Compile(field.Normalize[j].Pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: compile normalize pattern: %w", cfg.Rules[i].Name, field.Name, err)
				}
				field.Normalize[j].Regexp = nre
			}
		}
```

to:

```go
		for fi := range cfg.Rules[i].Fields {
			field := &cfg.Rules[i].Fields[fi]
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

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/rules/... -v`
Expected: PASS (every test in the package, including the two new ones and everything from before)

- [ ] **Step 6: Run the full verification suite**

Run: `gofmt -l internal/rules/ && go build ./... && go vet ./... && go test ./internal/rules/... && golangci-lint run ./internal/rules/...`
Expected: no gofmt output, all green, "0 issues"

- [ ] **Step 7: Commit**

```bash
git add internal/rules/rules.go internal/rules/rules_test.go
git commit -m "$(cat <<'EOF'
Resolve timestamp Format once at rules.Load time

Field gains ResolvedFormat, computed via ResolveFormat alongside the
existing Pattern/Normalize regex compilation in Load - a bad strptime
directive now fails fast at startup instead of surfacing later.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Teach `parseTimestamp` to consume `rules.TimeFormat` and parse epoch formats

**Files:**
- Modify: `internal/parse/timestamp.go` (full rewrite)
- Modify: `internal/parse/timestamp_test.go` (update existing call sites + add epoch tests)
- Modify: `internal/parse/convertvalue.go:36-41` (timestamp case)

**Interfaces:**
- Consumes: `rules.TimeFormat` from Task 1, `Field.ResolvedFormat` from Task 2.
- Produces: `parseTimestamp(raw string, tf rules.TimeFormat, now time.Time) (time.Time, error)` — replaces the old `parseTimestamp(raw, format string, now time.Time)`. `convertvalue.go` (this task) and any future caller use this new signature.

This task changes `parseTimestamp`'s signature, which breaks every existing call site at once (there's no way to add the new epoch capability without it - epoch formats have no "layout string" to parse with `time.ParseInLocation` at all). Steps 1-2 write the *new* epoch-focused tests against the *new* signature first (so they fail to compile, proving they aren't accidentally passing); Step 3 is the single implementation change that both adds epoch parsing and updates every existing caller to the new signature; Step 4 verifies everything compiles and passes together.

- [ ] **Step 1: Replace `internal/parse/timestamp_test.go` with the new-signature version plus new epoch tests**

```go
package parse

import (
	"fmt"
	"testing"
	"time"

	"logidx/internal/rules"
)

func TestParseTimestamp_FormatWithYear(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("06/Aug/2026:12:00:00 +0900", rules.TimeFormat{Layout: "02/Jan/2006:15:04:05 -0700"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2026 || got.Month() != time.August || got.Day() != 6 {
		t.Errorf("unexpected parsed time: %v", got)
	}
}

func TestParseTimestamp_NoYear_UsesCurrentYearWhenNotFuture(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("Aug  1 09:00:00", rules.TimeFormat{Layout: "Jan _2 15:04:05"}, now)
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
	got, err := parseTimestamp("Dec 31 23:59:59", rules.TimeFormat{Layout: "Jan _2 15:04:05"}, now)
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

func TestParseTimestamp_NoYear_NonUTCLocation_InterpretsWallClockInLocalZone(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	// now is Aug 7 02:00 JST; the log line is 2 hours earlier the same day.
	// The old bug (time.Parse, which always produces UTC for a zone-less
	// format) would read "Aug 7 00:00:00" as UTC, i.e. equivalent to
	// Aug 7 09:00 JST wall-clock - which is *after* a JST "now" of
	// Aug 7 02:00, so the buggy code would wrongly decrement the year to
	// 2025. Assert the fixed code interprets the wall-clock in now's (JST)
	// location and keeps the correct (current) year.
	now := time.Date(2026, 8, 7, 2, 0, 0, 0, jst)
	got, err := parseTimestamp("Aug  7 00:00:00", rules.TimeFormat{Layout: "Jan _2 15:04:05"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2026 {
		t.Errorf("expected year 2026 (not decremented), got %d", got.Year())
	}
	if got.Month() != time.August || got.Day() != 7 || got.Hour() != 0 {
		t.Errorf("unexpected parsed time: %v", got)
	}
	if _, offset := got.Zone(); offset != 9*60*60 {
		t.Errorf("expected result in JST (+9h offset), got offset %d", offset)
	}
}

func TestParseTimestamp_NoYear_NonUTCLocation_FutureWallClockFallsBackToPreviousYear(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	// now is Jan 2 00:30 JST; the log line "Jan 2 01:00" has no year and,
	// interpreted in JST, is a half hour in the future relative to now - so
	// it must resolve to the previous year, exercising the future-check
	// comparison in the same (JST) zone as now.
	now := time.Date(2026, 1, 2, 0, 30, 0, 0, jst)
	got, err := parseTimestamp("Jan  2 01:00:00", rules.TimeFormat{Layout: "Jan _2 15:04:05"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2025 {
		t.Errorf("expected year 2025 (previous year, since wall-clock is future in JST), got %d", got.Year())
	}
}

func TestParseTimestamp_FormatWithYearNoZone_UsesNowsLocation(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, jst)
	got, err := parseTimestamp("2026-08-06 09:00:00", rules.TimeFormat{Layout: "2006-01-02 15:04:05"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2026 || got.Month() != time.August || got.Day() != 6 || got.Hour() != 9 {
		t.Errorf("unexpected parsed time: %v", got)
	}
	if _, offset := got.Zone(); offset != 9*60*60 {
		t.Errorf("expected result interpreted in JST (+9h offset) rather than UTC, got offset %d", offset)
	}
}

func TestParseTimestamp_InvalidInputReturnsError(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, err := parseTimestamp("not-a-timestamp", rules.TimeFormat{Layout: "Jan _2 15:04:05"}, now)
	if err == nil {
		t.Fatal("expected error for unparsable timestamp")
	}
}

func TestParseTimestamp_EpochSeconds(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("1754557200", rules.TimeFormat{EpochUnit: time.Second}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Unix(1754557200, 0).UTC()
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTimestamp_EpochMillis(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("1754557200500", rules.TimeFormat{EpochUnit: time.Millisecond}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.UnixMilli(1754557200500).UTC()
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTimestamp_EpochNanos_LargeValuePreservesExactPrecision(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	// Large enough that a naive float64 conversion would round this away:
	// float64 has ~53 bits of exact integer precision (~9.007e15), and
	// this value is ~1.75e18.
	const nanos = int64(1754557200123456789)
	got, err := parseTimestamp(fmt.Sprintf("%d", nanos), rules.TimeFormat{EpochUnit: time.Nanosecond}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Unix(0, nanos).UTC()
	if !got.Equal(want) {
		t.Errorf("got %v, want %v (nanosecond mismatch: got=%d, want=%d)", got, want, got.Nanosecond(), want.Nanosecond())
	}
}

func TestParseTimestamp_EpochFractionalSeconds(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseTimestamp("1754557200.5", rules.TimeFormat{EpochUnit: time.Second}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Unix(1754557200, 500000000).UTC()
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTimestamp_EpochInvalidInputIsError(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, err := parseTimestamp("not-a-number", rules.TimeFormat{EpochUnit: time.Second}, now)
	if err == nil {
		t.Fatal("expected error for unparsable epoch value")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail to compile**

Run: `go test ./internal/parse/... -run TestParseTimestamp -v`
Expected: FAIL to build with a type-mismatch compile error (something like `cannot use rules.TimeFormat{...} (value of struct type rules.TimeFormat) as string value in argument to parseTimestamp`) - the old `parseTimestamp`'s second parameter is still a plain `string` at this point, and the new tests pass a `rules.TimeFormat`

- [ ] **Step 3: Replace `internal/parse/timestamp.go`**

```go
package parse

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"logidx/internal/rules"
)

// parseTimestamp parses raw according to tf, which was resolved once from
// a Field's Format string by rules.ResolveFormat (see
// internal/rules/timeformat.go) rather than re-interpreted here on every
// call. now is used both as the timezone reference for layout-based
// parsing and as the reference instant for filling in a missing year (see
// parseTimestampLayout); epoch-based formats ignore it, since an epoch
// value is always absolute and has no ambiguity to resolve against now.
func parseTimestamp(raw string, tf rules.TimeFormat, now time.Time) (time.Time, error) {
	if tf.EpochUnit != 0 {
		return parseTimestampEpoch(raw, tf.EpochUnit)
	}
	return parseTimestampLayout(raw, tf.Layout, now)
}

// parseTimestampLayout parses raw using a Go reference-time layout. If the
// layout contains no year token ("2006"), the parsed year is resolved to
// the nearest year that is not in the future relative to now: try
// now.Year(), and if that combined with the parsed month/day/time would be
// after now, use the previous year instead.
func parseTimestampLayout(raw, layout string, now time.Time) (time.Time, error) {
	t, err := time.ParseInLocation(layout, raw, now.Location())
	if err != nil {
		return time.Time{}, err
	}

	if strings.Contains(layout, "2006") {
		return t, nil
	}

	candidate := time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	if candidate.After(now) {
		candidate = candidate.AddDate(-1, 0, 0)
	}
	return candidate, nil
}

// parseTimestampEpoch parses raw as a number of unit ticks since the Unix
// epoch (e.g. unit=time.Millisecond for milliseconds-since-epoch). Integer
// input is parsed exactly via int64 arithmetic; only input containing a
// decimal point falls back to float64, which is adequate for fractional
// seconds but would lose precision on very large integer nanosecond values
// (float64 has ~53 bits of exact integer precision, and nanosecond epoch
// values today are already around 1.7e18).
func parseTimestampEpoch(raw string, unit time.Duration) (time.Time, error) {
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(0, n*int64(unit)), nil
	}

	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse epoch value: %w", err)
	}
	return time.Unix(0, int64(f*float64(unit))), nil
}
```

- [ ] **Step 4: Update the timestamp case in `internal/parse/convertvalue.go`**

Change (around line 36-41):

```go
	case "timestamp":
		v, err := parseTimestamp(normalized, field.Format, now)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp: %w", err)
		}
		return v, nil
```

to:

```go
	case "timestamp":
		v, err := parseTimestamp(normalized, field.ResolvedFormat, now)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp: %w", err)
		}
		return v, nil
```

- [ ] **Step 5: Run the package tests to verify they pass**

Run: `go test ./internal/parse/... -v 2>&1 | tail -80`
Expected: `TestParseTimestamp_*` all PASS. `TestConvertValue_Timestamp` in `convertvalue_test.go` still FAILs at this point: it constructs a `rules.Field{..., Format: "..."}` literal directly without a `ResolvedFormat`, so `convertValue` now parses against a zero-value `TimeFormat{}` (empty layout) instead of the intended one, and a non-empty input can't match an empty layout. `TestConvertValue_TimestampInvalidIsError` happens to still PASS even though it has the same missing-`ResolvedFormat` problem - it only asserts that parsing fails, and parsing against an empty layout fails too, just not for the reason the test intends. Task 4 fixes both properly.

- [ ] **Step 6: Run build and vet (test failures from convertvalue_test.go are expected and addressed in Task 4)**

Run: `gofmt -l internal/parse/ internal/rules/ && go build ./... && go vet ./...`
Expected: no gofmt output, build and vet clean (compilation succeeds even though the two convertvalue_test.go cases fail at runtime)

- [ ] **Step 7: Commit**

```bash
git add internal/parse/timestamp.go internal/parse/timestamp_test.go internal/parse/convertvalue.go
git commit -m "$(cat <<'EOF'
Parse timestamps from a resolved rules.TimeFormat, add epoch parsing

parseTimestamp now takes the TimeFormat rules.Load already resolved
(Task 2) instead of re-interpreting a raw format string per line, and
gains a second code path for epoch-since-Unix-epoch formats (unix,
unix_ms, unix_us, unix_ns), parsed via exact int64 arithmetic where
possible to avoid float64 precision loss on large nanosecond values.

Note: internal/parse/convertvalue_test.go's two timestamp tests are
expected to fail after this commit until Task 4 updates them.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Fix `convertvalue_test.go`'s direct `Field` literals, add preset/strptime/epoch coverage through `convertValue`

**Files:**
- Modify: `internal/parse/convertvalue_test.go`

**Interfaces:**
- Consumes: `rules.ResolveFormat` (Task 1), `field.ResolvedFormat`-consuming `parseTimestamp` (Task 3).
- Produces: nothing new for later tasks — this is the integration-level test coverage for the whole `Field.Format` string → `convertValue`'s returned `time.Time` pipeline.

- [ ] **Step 1: Replace the two existing timestamp tests and add three new ones in `internal/parse/convertvalue_test.go`**

Replace (around line 65-88):

```go
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

with:

```go
func timestampField(t *testing.T, format string) rules.Field {
	t.Helper()
	tf, err := rules.ResolveFormat(format)
	if err != nil {
		t.Fatalf("rules.ResolveFormat(%q): %v", format, err)
	}
	return rules.Field{Type: "timestamp", Format: format, ResolvedFormat: tf}
}

func TestConvertValue_Timestamp(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	field := timestampField(t, "2006-01-02T15:04:05Z07:00")
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
	field := timestampField(t, "2006-01-02T15:04:05Z07:00")
	_, err := convertValue("not-a-timestamp", field, now)
	if err == nil {
		t.Fatal("expected error for invalid timestamp")
	}
}

func TestConvertValue_TimestampPreset(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	field := timestampField(t, "iso8601")
	v, err := convertValue("2026-08-06T12:00:01Z", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tm, ok := v.(time.Time)
	if !ok {
		t.Fatalf("got %T, want time.Time", v)
	}
	if !tm.Equal(time.Date(2026, 8, 6, 12, 0, 1, 0, time.UTC)) {
		t.Errorf("unexpected parsed time: %v", tm)
	}
}

func TestConvertValue_TimestampStrptimeWithCommaFractionalSeconds(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	field := timestampField(t, "%Y-%m-%d %H:%M:%S,%f")
	v, err := convertValue("2026-08-06 12:00:01,500", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tm, ok := v.(time.Time)
	if !ok {
		t.Fatalf("got %T, want time.Time", v)
	}
	if tm.Nanosecond() != 500000000 {
		t.Errorf("expected 500ms fractional seconds, got nanosecond=%d (%v)", tm.Nanosecond(), tm)
	}
}

func TestConvertValue_TimestampEpoch(t *testing.T) {
	now := time.Now()
	field := timestampField(t, "unix")
	v, err := convertValue("1754557200", field, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tm, ok := v.(time.Time)
	if !ok {
		t.Fatalf("got %T, want time.Time", v)
	}
	if !tm.Equal(time.Unix(1754557200, 0)) {
		t.Errorf("unexpected parsed time: %v", tm)
	}
}
```

- [ ] **Step 2: Run the full package test suite to verify everything passes**

Run: `go test ./internal/parse/... -v 2>&1 | tail -100`
Expected: every test PASSes, including the `TestParseTimestamp_*` tests from Task 3 that were already green and the `TestConvertValue_*` tests including the three new ones

- [ ] **Step 3: Run the full verification suite**

Run: `gofmt -l internal/parse/ && go build ./... && go vet ./... && go test ./... && golangci-lint run ./...`
Expected: no gofmt output, all packages build/vet/test clean, "0 issues"

- [ ] **Step 4: Commit**

```bash
git add internal/parse/convertvalue_test.go
git commit -m "$(cat <<'EOF'
Fix convertValue timestamp tests for ResolvedFormat, add format coverage

The two existing timestamp tests constructed a rules.Field literal
directly (bypassing rules.Load), so they need to compute
ResolvedFormat themselves now that convertValue reads it instead of
the raw Format string. Also adds convertValue-level coverage for a
preset (iso8601), strptime with comma-decimal fractional seconds, and
an epoch (unix) format, proving the full Format-string-to-time.Time
pipeline works end-to-end for each style, not just its pieces in
isolation.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: End-to-end CLI test (epoch + strptime through `import`/`dump`)

**Files:**
- Modify: `cmd/logidx/main_test.go` (add `"fmt"` and `"time"` imports, add 1 new test)

**Interfaces:**
- Consumes: `logidx import` (existing), `logidx dump` (existing) — no new production code in this task, pure test coverage proving Tasks 1-4 compose correctly through the real CLI.
- Produces: nothing for later tasks.

This test deliberately avoids year-less formats (e.g. `syslog`) - those already have thorough, injectable-`now` unit coverage in `internal/parse/timestamp_test.go` (Task 3), and the real CLI's `now` comes from `time.Now()` with no way to inject a fixed instant, which would make asserting an exact year non-deterministic near year boundaries. It also gives the strptime pattern an explicit `%z` offset so the asserted instant doesn't depend on the test machine's local timezone (`parseTimestampLayout` parses in `now.Location()`, and `now` here is the real wall clock).

- [ ] **Step 1: Add `"fmt"` and `"time"` to the import block in `cmd/logidx/main_test.go`**

The existing block is:

```go
import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"logidx/internal/pqinfo"
)
```

Change to:

```go
import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logidx/internal/pqinfo"
)
```

- [ ] **Step 2: Append the new test to `cmd/logidx/main_test.go`**

```go
func TestRun_ImportSupportsTimestampPresetsAndStrptime(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: epoch_event
    pattern: '^(?P<ts>\d+) (?P<message>.*)$'
    fields:
      ts:
        type: timestamp
        format: unix
      message: string
  - name: py_event
    pattern: '^(?P<ts>\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d+[+-]\d{4}) (?P<message>.*)$'
    fields:
      ts:
        type: timestamp
        format: "%Y-%m-%d %H:%M:%S,%f%z"
      message: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)

	epochTime := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	pyTime := time.Date(2026, 8, 7, 9, 15, 30, 500000000, time.UTC)

	logContent := fmt.Sprintf("%d unix epoch message\n2026-08-07 09:15:30,500+0000 python style message\n", epochTime.Unix())
	logPath := writeFile(t, dir, "app.log", logContent)
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("import: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	dumpPath := filepath.Join(dir, "dump.jsonl")

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"dump", filepath.Join(outDir, "epoch_event.parquet"), dumpPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("dump epoch_event: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	epochDump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	wantEpoch := epochTime.Format(time.RFC3339Nano)
	if !strings.Contains(string(epochDump), wantEpoch) {
		t.Errorf("epoch_event dump missing expected timestamp %q, got: %s", wantEpoch, epochDump)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"dump", filepath.Join(outDir, "py_event.parquet"), dumpPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("dump py_event: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	pyDump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	wantPy := pyTime.Format(time.RFC3339Nano)
	if !strings.Contains(string(pyDump), wantPy) {
		t.Errorf("py_event dump missing expected timestamp %q, got: %s", wantPy, pyDump)
	}
}
```

- [ ] **Step 3: Run the new test to verify it passes**

Run: `go test ./cmd/logidx/... -run TestRun_ImportSupportsTimestampPresetsAndStrptime -v`
Expected: PASS

- [ ] **Step 4: Run the full verification suite**

Run: `gofmt -l . && go build ./... && go vet ./... && go test ./... && golangci-lint run ./...`
Expected: no gofmt output, all green, "0 issues"

- [ ] **Step 5: Manual smoke test with the real binary**

```bash
go build -o bin/logidx ./cmd/logidx
mkdir -p /tmp/ts-smoke
cat > /tmp/ts-smoke/rules.yaml <<'EOF'
rules:
  - name: app_log
    pattern: '^\[(?P<level>\w+)\] (?P<ts>\S+ \S+) (?P<message>.*)$'
    fields:
      level: string
      ts:
        type: timestamp
        format: pylog
      message: string
EOF
printf '[INFO] 2026-08-07 09:15:30,500 hello from pylog format\n' > /tmp/ts-smoke/app.log
./bin/logidx import --rules /tmp/ts-smoke/rules.yaml --out /tmp/ts-smoke/out /tmp/ts-smoke/app.log
./bin/logidx dump /tmp/ts-smoke/out/app_log.parquet -
```

Expected: the dumped JSON row shows `"ts":"2026-08-07T09:15:30.5Z"` (or equivalent RFC3339Nano rendering of the same instant).

- [ ] **Step 6: Commit**

```bash
git add cmd/logidx/main_test.go
git commit -m "$(cat <<'EOF'
Add end-to-end test for timestamp presets/strptime through import+dump

Covers the unix epoch preset and a strptime pattern with comma
fractional seconds (the pylog use case) through the real CLI, proving
rules.Load's format resolution, parseTimestamp, and the writer/dump
pipeline compose correctly together - not just in isolation.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: README documentation

**Files:**
- Modify: `README.md` (insert a new section after line 31, before `### 圧縮設定`)

**Interfaces:**
- Consumes: nothing (documentation only).
- Produces: nothing for later tasks.

- [ ] **Step 1: Insert the new section into `README.md`**

After the line `ルール定義の書き方は \`docs/superpowers/specs/2026-08-06-log-to-parquet-converter-design.md\` を参照。` (line 31) and before `### 圧縮設定` (line 33), insert:

```markdown
### タイムスタンプの`format`指定

`timestamp`型フィールドの`format`は、以下の3通りのいずれかで書ける。値の見た目で自動判別するため、書き方を明示する追加のキーは不要:

1. **プリセット名**(下表)
2. **strptime記法**(`%`で始まる文字列。下表のディレクティブのみ対応)
3. **生のGoレイアウト文字列**(上記のどちらにも該当しない場合、そのままGoの`time.Parse`レイアウトとして使う。既存の`rules.yaml`はこの扱いのまま変わらない)

プリセット一覧:

| プリセット名 | 意味 | 備考 |
|---|---|---|
| `iso8601` / `rfc3339` | `2006-01-02T15:04:05.999999999Z07:00` | 小数秒はあってもなくても可(エイリアス) |
| `rfc822` | `02 Jan 06 15:04 -0700` | 数値タイムゾーンオフセット版 |
| `rfc2822` | `Mon, 02 Jan 2006 15:04:05 -0700` | メールヘッダ`Date:`相当 |
| `clf` | `02/Jan/2006:15:04:05 -0700` | Apache/nginxのCommon Log Format |
| `syslog` | `Jan _2 15:04:05` | 年なし。伝統的なBSD syslog形式(`Aug  7`のようにスペース埋め) |
| `pylog` | `2006-01-02 15:04:05,999999999` | Pythonロガーの`%(asctime)s`デフォルト形式(カンマ区切り小数秒) |
| `unix` | epoch秒(整数/小数) | |
| `unix_ms` / `unix_us` / `unix_ns` | epochミリ/マイクロ/ナノ秒(整数) | |

strptime変換表:

| ディレクティブ | 意味 | Goトークン |
|---|---|---|
| `%Y` / `%y` | 年(4桁/2桁) | `2006` / `06` |
| `%m` | 月(2桁) | `01` |
| `%d` | 日(2桁) | `02` |
| `%H` / `%I` | 時(24h/12h) | `15` / `03` |
| `%M` | 分 | `04` |
| `%S` | 秒 | `05` |
| `%f` | 小数秒(可変桁数、`.`/`,`どちらの区切りも受理) | `999999999` |
| `%z` | UTCオフセット | `-0700` |
| `%Z` | タイムゾーン名 | `MST` |
| `%a` / `%A` | 曜日(省略形/フル) | `Mon` / `Monday` |
| `%b` / `%B` | 月名(省略形/フル) | `Jan` / `January` |
| `%p` | AM/PM | `PM` |
| `%%` | リテラルの`%` | `%` |

表にないディレクティブ(`%j`、`%U`など)は起動時エラーになる。`%`で始まらない文字列はプリセット名としても解釈されない場合、生のGoレイアウトとして扱われる(検証は行われず、実際の値をパースするまでエラーが分からない点は既存動作のまま)。

年なしのプリセット・strptime(`syslog`など)は、既存の年補完ロジック(実行時刻を基準に、未来にならない直近の年を採用)がそのまま適用される。
```

- [ ] **Step 2: Verify the section renders correctly and doesn't break surrounding structure**

Run: `grep -n '^##' README.md`
Expected: heading hierarchy unchanged (`## Build`, `## Usage`, `### タイムスタンプの` (new), `### 圧縮設定`, `### info: ...`, `### copy: ...`, `### dump / restore: ...`, `## Development`, in that order)

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
Document timestamp format presets and strptime support

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```
