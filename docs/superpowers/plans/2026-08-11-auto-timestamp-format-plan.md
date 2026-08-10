# タイムスタンプ`format: auto` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `format: auto` value for `timestamp` fields that tries the 6 layout-preset formats (`iso8601`/`rfc2822`/`rfc822`/`clf`/`syslog`/`pylog`) in a fixed order and uses whichever parses successfully first, caching the last-successful candidate per field for speed.

**Architecture:** `internal/rules/timeformat.go`'s `ResolveFormat` gets an `"auto"` special case that returns a `TimeFormat` carrying an ordered `Candidates []string` slice of Go layouts plus a `LastGood *int` pointer (one allocation per field, shared across all calls for that field since `internal/convert` is single-threaded). `internal/parse/timestamp.go`'s `parseTimestamp` gets a new branch: when `Candidates` is non-empty, `parseTimestampAuto` tries `Candidates[*LastGood]` first, then falls through the rest of `Candidates` in order, updating `*LastGood` on success. Design doc: `docs/superpowers/specs/2026-08-10-auto-timestamp-format-design.md`.

**Tech Stack:** Go 1.x, standard `time` package, table-driven tests (existing style in `internal/rules/timeformat_test.go` and `internal/parse/timestamp_test.go`).

## Global Constraints

- `auto`'s candidate set is exactly these 6 layout-preset layouts, in this fixed order (from the spec table): `iso8601` (`2006-01-02T15:04:05.999999999Z07:00`), `rfc2822` (`Mon, 02 Jan 2006 15:04:05 -0700`), `rfc822` (`02 Jan 06 15:04 -0700`), `clf` (`02/Jan/2006:15:04:05 -0700`), `syslog` (`Jan _2 15:04:05`), `pylog` (`2006-01-02 15:04:05,999999999`).
- Epoch presets (`unix`/`unix_ms`/`unix_us`/`unix_ns`) and strptime/raw-layout formats are never candidates for `auto` — out of scope entirely (Non-goals in the spec).
- No ambiguity resolution beyond "first candidate (starting from the cached index) that parses without error wins" — do not try to disambiguate multiple simultaneous matches.
- `LastGood` is a `*int`, shared by pointer across copies of the same field's `TimeFormat` (the struct is copied by value on each call), and updated without synchronization — safe only because `internal/convert` runs single-threaded today. This must be called out in a code comment, not just this plan.
- Year-less candidate (`syslog`) reached via `auto` must still go through the existing year-completion logic in `parseTimestampLayout`.
- All-candidates-failed must return a single concise error (not a list of every candidate's failure reason), matching the terseness of existing parse error messages.
- `ResolveFormat("auto")` must always succeed (the candidate list is hardcoded) — no new validation path is needed in `internal/rules/validate.go`.

---

## File Structure

- `internal/rules/timeformat.go` — add `Candidates`/`LastGood` fields to `TimeFormat`, add an `autoCandidateLayouts` ordered slice, add the `"auto"` branch in `ResolveFormat`.
- `internal/rules/timeformat_test.go` — add `TestResolveFormat_Auto` covering `Candidates` order/content and a non-nil, independently-allocated `LastGood`.
- `internal/parse/timestamp.go` — add `parseTimestampAuto`, add the `Candidates`-branch in `parseTimestamp`.
- `internal/parse/timestamp_test.go` — add tests for: each of the 6 candidate formats parsing via `auto`; `LastGood` caching behavior; all-candidates-fail error; year-less candidate (`syslog`) year completion via `auto`.
- `README.md` — document `format: auto` in the "タイムスタンプの`format`指定" section (around line 130-172).

---

## Task 1: `TimeFormat.Candidates`/`LastGood` and `ResolveFormat("auto")`

**Files:**
- Modify: `internal/rules/timeformat.go`
- Test: `internal/rules/timeformat_test.go`

**Interfaces:**
- Produces: `rules.TimeFormat` struct gains two exported fields:
  - `Candidates []string` — ordered Go reference-time layouts to try; empty (nil) for every non-`auto` format.
  - `LastGood *int` — pointer to the index into `Candidates` to try first; nil for every non-`auto` format; freshly allocated (pointing at `0`) each time `ResolveFormat("auto")` is called.
  - `ResolveFormat("auto")` returns `(TimeFormat{Candidates: autoCandidateLayouts, LastGood: new(int)}, nil)` where `autoCandidateLayouts` is a package-level `[]string` var holding the 6 layouts in spec order.
- Consumes: nothing new — this task only touches `internal/rules`.

- [ ] **Step 1: Write the failing test**

Add to `internal/rules/timeformat_test.go`:

```go
func TestResolveFormat_Auto(t *testing.T) {
	wantCandidates := []string{
		"2006-01-02T15:04:05.999999999Z07:00", // iso8601 / rfc3339
		"Mon, 02 Jan 2006 15:04:05 -0700",      // rfc2822
		"02 Jan 06 15:04 -0700",                // rfc822
		"02/Jan/2006:15:04:05 -0700",           // clf
		"Jan _2 15:04:05",                      // syslog
		"2006-01-02 15:04:05,999999999",        // pylog
	}

	got, err := ResolveFormat("auto")
	if err != nil {
		t.Fatalf(`ResolveFormat("auto"): %v`, err)
	}
	if got.Layout != "" {
		t.Errorf("Layout = %q, want empty for auto", got.Layout)
	}
	if got.EpochUnit != 0 {
		t.Errorf("EpochUnit = %v, want 0 for auto", got.EpochUnit)
	}
	if len(got.Candidates) != len(wantCandidates) {
		t.Fatalf("Candidates = %v, want %v", got.Candidates, wantCandidates)
	}
	for i, want := range wantCandidates {
		if got.Candidates[i] != want {
			t.Errorf("Candidates[%d] = %q, want %q", i, got.Candidates[i], want)
		}
	}
	if got.LastGood == nil {
		t.Fatal("LastGood = nil, want a non-nil pointer")
	}
	if *got.LastGood != 0 {
		t.Errorf("*LastGood = %d, want 0", *got.LastGood)
	}
}

func TestResolveFormat_Auto_EachCallGetsIndependentLastGood(t *testing.T) {
	first, err := ResolveFormat("auto")
	if err != nil {
		t.Fatalf(`ResolveFormat("auto"): %v`, err)
	}
	second, err := ResolveFormat("auto")
	if err != nil {
		t.Fatalf(`ResolveFormat("auto"): %v`, err)
	}

	*first.LastGood = 3
	if *second.LastGood != 0 {
		t.Errorf("mutating first.LastGood affected second.LastGood (got %d) - LastGood must not be shared across ResolveFormat calls", *second.LastGood)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rules/... -run TestResolveFormat_Auto -v`
Expected: FAIL (compile error or wrong `Candidates`/`LastGood` since neither the fields nor the `"auto"` branch exist yet).

- [ ] **Step 3: Write minimal implementation**

In `internal/rules/timeformat.go`, extend the `TimeFormat` doc comment and struct:

```go
// TimeFormat is a timestamp Field's Format string, resolved once (at
// rules.Load time, via ResolveFormat) into an efficient parsing strategy -
// mirroring how Rule.Pattern is compiled once into Rule.Regexp rather than
// re-interpreted on every log line.
//
// EpochUnit == 0 means "not an epoch format": parse with Layout via
// time.ParseInLocation. EpochUnit != 0 means "epoch format": parse as a
// number of EpochUnit ticks since the Unix epoch (Layout is unused).
//
// Candidates != nil means "format: auto": Layout and EpochUnit are unused,
// and internal/parse tries each layout in Candidates (see LastGood) instead.
type TimeFormat struct {
	Layout    string
	EpochUnit time.Duration

	// Candidates holds the ordered layout strings to try when this
	// TimeFormat came from format: "auto". Empty for every other format.
	Candidates []string

	// LastGood indexes into Candidates: the position that parsed
	// successfully last time, tried first on the next call. Shared via
	// pointer across every copy of this TimeFormat (Field is copied by
	// value on each call), since input processing is single-threaded (no
	// goroutines) - see internal/convert. If that ever changes, updates to
	// *LastGood need to become atomic or field-scoped locking needs to be
	// added. nil for every non-auto format. Exported (like Layout/
	// EpochUnit) so internal/parse, a different package, can read and
	// update it.
	LastGood *int
}

// autoCandidateLayouts is the fixed, ordered list of layout-preset layouts
// that format: "auto" tries. Order matters: it is the order candidates are
// attempted in when no LastGood cache hit is available. Epoch presets
// (unix/unix_ms/unix_us/unix_ns) and strptime/raw layouts are deliberately
// excluded - see the Non-goals section of
// docs/superpowers/specs/2026-08-10-auto-timestamp-format-design.md.
var autoCandidateLayouts = []string{
	presetLayouts["iso8601"],
	presetLayouts["rfc2822"],
	presetLayouts["rfc822"],
	presetLayouts["clf"],
	presetLayouts["syslog"],
	presetLayouts["pylog"],
}
```

Then add the `"auto"` check to `ResolveFormat`, before the existing preset-name lookup:

```go
func ResolveFormat(format string) (TimeFormat, error) {
	if format == "auto" {
		lastGood := 0
		return TimeFormat{Candidates: autoCandidateLayouts, LastGood: &lastGood}, nil
	}
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
```

Also update `ResolveFormat`'s doc comment to mention the `"auto"` special case as a fourth style, ahead of the numbered list of three (it's checked before all of them):

```go
// ResolveFormat interprets a timestamp Field's Format string, auto-detecting
// which of four styles it is:
//
//  0. The literal string "auto": returns a TimeFormat whose Candidates
//     holds the fixed, ordered auto-detection layout list (see
//     autoCandidateLayouts) and whose LastGood is a freshly allocated
//     pointer, independent from every other ResolveFormat call.
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/rules/... -run TestResolveFormat_Auto -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/rules/...`
Expected: PASS (all existing tests, e.g. `TestResolveFormat_Presets`, still pass unchanged)

- [ ] **Step 6: Commit**

```bash
git add internal/rules/timeformat.go internal/rules/timeformat_test.go
git commit -m "feat(rules): resolve format: auto to an ordered candidate list"
```

---

## Task 2: `parseTimestampAuto` candidate search in `internal/parse`

**Files:**
- Modify: `internal/parse/timestamp.go`
- Test: `internal/parse/timestamp_test.go`

**Interfaces:**
- Consumes: `rules.TimeFormat.Candidates []string` and `rules.TimeFormat.LastGood *int` from Task 1. `parseTimestampLayout(raw, layout string, now time.Time) (time.Time, error)` (already exists, unchanged signature).
- Produces: `parseTimestampAuto(raw string, tf rules.TimeFormat, now time.Time) (time.Time, error)` — tried by `parseTimestamp` whenever `len(tf.Candidates) > 0`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/parse/timestamp_test.go`:

```go
func TestParseTimestamp_Auto_MatchesEachCandidateFormat(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		raw         string
		wantYear    int
		wantMonth   time.Month
		wantDay     int
	}{
		{"iso8601", "2026-08-06T12:00:00Z", 2026, time.August, 6},
		{"rfc2822", "Thu, 06 Aug 2026 12:00:00 +0000", 2026, time.August, 6},
		{"rfc822", "06 Aug 26 12:00 +0000", 2026, time.August, 6},
		{"clf", "06/Aug/2026:12:00:00 +0000", 2026, time.August, 6},
		{"syslog", "Aug  6 12:00:00", 2026, time.August, 6},
		{"pylog", "2026-08-06 12:00:00,000000000", 2026, time.August, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tf, err := rules.ResolveFormat("auto")
			if err != nil {
				t.Fatalf(`ResolveFormat("auto"): %v`, err)
			}
			got, err := parseTimestamp(tt.raw, tf, now)
			if err != nil {
				t.Fatalf("parseTimestamp(%q): unexpected error: %v", tt.raw, err)
			}
			if got.Year() != tt.wantYear || got.Month() != tt.wantMonth || got.Day() != tt.wantDay {
				t.Errorf("got %v, want year=%d month=%v day=%d", got, tt.wantYear, tt.wantMonth, tt.wantDay)
			}
		})
	}
}

func TestParseTimestamp_Auto_NoCandidateMatchesIsError(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	tf, err := rules.ResolveFormat("auto")
	if err != nil {
		t.Fatalf(`ResolveFormat("auto"): %v`, err)
	}
	_, err = parseTimestamp("not-a-timestamp-at-all", tf, now)
	if err == nil {
		t.Fatal("expected error when no auto candidate matches")
	}
}

func TestParseTimestamp_Auto_YearlessCandidateStillFillsInYear(t *testing.T) {
	// "now" is Jan 2; the syslog-shaped line "Dec 31" has no year and must
	// resolve to the previous year, exactly like the non-auto syslog path
	// (TestParseTimestamp_NoYear_FallsBackToPreviousYearWhenFuture) - this
	// exercises that parseTimestampAuto still routes through
	// parseTimestampLayout's year-completion logic for the syslog candidate.
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	tf, err := rules.ResolveFormat("auto")
	if err != nil {
		t.Fatalf(`ResolveFormat("auto"): %v`, err)
	}
	got, err := parseTimestamp("Dec 31 23:59:59", tf, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2025 || got.Month() != time.December || got.Day() != 31 {
		t.Errorf("got %v, want 2025-12-31", got)
	}
}

func TestParseTimestamp_Auto_CachesLastSuccessfulCandidate(t *testing.T) {
	// clf ("02/Jan/2006:15:04:05 -0700") and rfc822 ("02 Jan 06 15:04
	// -0700") are structurally close but clf comes first in
	// autoCandidateLayouts and has a colon after the day where rfc822 has
	// a space; a clf-shaped value cannot accidentally match rfc822 or vice
	// versa, so this exercises genuine index caching rather than a
	// coincidental cross-match.
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	tf, err := rules.ResolveFormat("auto")
	if err != nil {
		t.Fatalf(`ResolveFormat("auto"): %v`, err)
	}

	if _, err := parseTimestamp("06/Aug/2026:12:00:00 +0000", tf, now); err != nil {
		t.Fatalf("first (clf) parse: unexpected error: %v", err)
	}
	if *tf.LastGood != 3 { // index of clf in autoCandidateLayouts
		t.Fatalf("*LastGood = %d after first clf match, want 3 (clf's index)", *tf.LastGood)
	}

	got, err := parseTimestamp("07/Aug/2026:13:00:00 +0000", tf, now)
	if err != nil {
		t.Fatalf("second (clf) parse: unexpected error: %v", err)
	}
	if got.Day() != 7 || got.Hour() != 13 {
		t.Errorf("got %v, want 2026-08-07 13:00:00", got)
	}
	if *tf.LastGood != 3 {
		t.Errorf("*LastGood = %d after second clf match, want unchanged 3", *tf.LastGood)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parse/... -run TestParseTimestamp_Auto -v`
Expected: FAIL (`parseTimestamp` doesn't branch on `Candidates` yet, so every case falls through to `parseTimestampLayout(raw, "", now)` and fails to parse).

- [ ] **Step 3: Write minimal implementation**

In `internal/parse/timestamp.go`, update `parseTimestamp`:

```go
func parseTimestamp(raw string, tf rules.TimeFormat, now time.Time) (time.Time, error) {
	if tf.EpochUnit != 0 {
		return parseTimestampEpoch(raw, tf.EpochUnit)
	}
	if len(tf.Candidates) > 0 {
		return parseTimestampAuto(raw, tf, now)
	}
	return parseTimestampLayout(raw, tf.Layout, now)
}

// parseTimestampAuto tries tf.Candidates in order, starting from the
// index tf.LastGood points at (the candidate that matched last time, on
// the assumption that a field's format doesn't change line to line), then
// falling through the remaining candidates in their original order. On
// success it updates *tf.LastGood to the matching index, so the next call
// on the same field (LastGood is shared by pointer, see TimeFormat's doc
// comment) tries that candidate first. If no candidate matches, it returns
// a single terse error rather than every candidate's individual failure
// reason, matching this package's existing error style.
func parseTimestampAuto(raw string, tf rules.TimeFormat, now time.Time) (time.Time, error) {
	first := *tf.LastGood
	if t, err := parseTimestampLayout(raw, tf.Candidates[first], now); err == nil {
		return t, nil
	}

	for i, layout := range tf.Candidates {
		if i == first {
			continue
		}
		if t, err := parseTimestampLayout(raw, layout, now); err == nil {
			*tf.LastGood = i
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("parse timestamp %q: no auto format matched", raw)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/parse/... -run TestParseTimestamp_Auto -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/parse/... ./internal/rules/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/parse/timestamp.go internal/parse/timestamp_test.go
git commit -m "feat(parse): try format: auto candidates in order, caching the last match"
```

---

## Task 3: End-to-end test via `rules.Load` + README documentation

**Files:**
- Test: `internal/rules/rules_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `rules.Load(path string) (*Config, error)` (existing, `internal/rules/rules.go:190`), `fieldByName(fields []Field, name string) (Field, bool)` and `writeTempRules(t *testing.T, content string) string` test helpers already defined at the top of `internal/rules/rules_test.go`, and `Field.ResolvedFormat rules.TimeFormat` (existing field, already exercised by `TestLoad_ResolvesTimestampFormat` at `internal/rules/rules_test.go:127`).
- Produces: nothing new — this task is verification + docs only.

- [ ] **Step 1: Write the failing test**

Add to `internal/rules/rules_test.go`, right after `TestLoad_ResolvesTimestampFormat` (which ends at line 144):

```go
func TestLoad_ResolvesTimestampFormatAuto(t *testing.T) {
	rulesYAML := `
rules:
  - name: mixed
    pattern: '^(?P<ts>\S+.*)$'
    fields:
      ts:
        type: timestamp
        format: "auto"
`
	path := writeTempRules(t, rulesYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	tsField, ok := fieldByName(cfg.Rules[0].Fields, "ts")
	if !ok {
		t.Fatal("expected ts field")
	}
	if len(tsField.ResolvedFormat.Candidates) != 6 {
		t.Errorf("ResolvedFormat.Candidates length = %d, want 6", len(tsField.ResolvedFormat.Candidates))
	}
	if tsField.ResolvedFormat.LastGood == nil {
		t.Error("ResolvedFormat.LastGood = nil, want non-nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rules/... -run TestLoad_ResolvesTimestampFormatAuto -v`
Expected: FAIL only if `auto` isn't wired into `rules.Load` correctly — since Task 1 already made `ResolveFormat("auto")` work and `rules.Load` calls `ResolveFormat(field.Format)` unconditionally for every timestamp field (`internal/rules/rules.go:264`), this test is expected to pass immediately after being written, with no production-code changes in this task. If it unexpectedly fails, that indicates `rules.Load` has additional format validation/dispatch logic beyond calling `ResolveFormat` that also needs to accept `"auto"` — investigate `internal/rules/rules.go` and `internal/rules/validate.go` around the `format == ""` check (`internal/rules/validate.go:63`) before changing anything.

- [ ] **Step 3: Run test to verify it passes**

Run: `go test ./internal/rules/... -run TestLoad_ResolvesTimestampFormatAuto -v`
Expected: PASS

- [ ] **Step 4: Update README.md**

In the "タイムスタンプの`format`指定" section (`README.md:130-172`):

Change line 132 from:

```
`timestamp`型フィールドの`format`は、以下の3通りのいずれかで書ける。値の見た目で自動判別するため、書き方を明示する追加のキーは不要:
```

to:

```
`timestamp`型フィールドの`format`は、以下の4通りのいずれかで書ける。値の見た目で自動判別するため、書き方を明示する追加のキーは不要:
```

Change the numbered list (lines 134-136) to prepend the `auto` special case:

```
0. **`auto`**(下記参照。書式を決め打ちせず複数候補を順に試す)
1. **プリセット名**(下表)
2. **strptime記法**(`%`で始まる文字列。下表のディレクティブのみ対応)
3. **生のGoレイアウト文字列**(上記のどちらにも該当しない場合、そのままGoの`time.Parse`レイアウトとして使う。既存の`rules.yaml`はこの扱いのまま変わらない)
```

After the strptime conversion table and its trailing note (after line 171, before the section ends), add a new subsection:

```markdown

### `format: auto`(書式の自動判定)

書式が事前にわからない、または複数の書式が混在するログの場合、`format: auto`を指定すると以下6種のレイアウト系プリセットを**この順序**で試し、最初にエラーなくパースできたものを採用する:

| 順序 | プリセット名 |
|---|---|
| 1 | `iso8601`(=`rfc3339`) |
| 2 | `rfc2822` |
| 3 | `rfc822` |
| 4 | `clf` |
| 5 | `syslog` |
| 6 | `pylog` |

一度成功した書式は次回以降そのフィールドで優先して試されるため、実運用でログの書式が行ごとに変わらない限りオーバーヘッドは実質1回だけになる。

`unix`/`unix_ms`/`unix_us`/`unix_ns`(epoch系)は`auto`の候補に含まれない。数値文字列はどのepoch単位でもパース自体は成功してしまい、値の桁数だけでは単位を一意に判定できないため、layout系プリセットと混在させると誤判定のリスクが高い。epoch形式を使いたい場合は明示的にプリセット名を指定すること。strptime記法・生のGoレイアウト文字列も`auto`の候補には含まれない。

年なしプリセット(`syslog`)が`auto`経由でマッチした場合も、既存の年補完ロジック(実行時刻を基準に、未来にならない直近の年を採用)がそのまま適用される。
```

- [ ] **Step 5: Proofread the README diff**

Run: `git diff README.md`
Expected: the new numbered item 0 and new subsection read naturally in context; no leftover references to "3通り" elsewhere in the section.

- [ ] **Step 6: Commit**

```bash
git add internal/rules/*_test.go README.md
git commit -m "docs(readme): document format: auto and its candidate order"
```

---

## Final Verification

- [ ] **Step 1: Run the full test suite**

Run: `go test ./...`
Expected: PASS, no regressions.

- [ ] **Step 2: Run linters if configured**

Run: `golangci-lint run ./...` (skip if not configured in this repo — check for `.golangci.yml`/`.golangci.yaml` first)
Expected: no new findings introduced by this change.

- [ ] **Step 3: Confirm no epoch/strptime/raw-layout candidates leaked into `auto`**

Run: `git diff internal/rules/timeformat.go` and manually confirm `autoCandidateLayouts` contains exactly the 6 layout-preset layouts from the Global Constraints section, in that exact order, and nothing else.
