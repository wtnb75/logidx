# continuationエントリへのフォールバック拡張 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `continuation`ルールのエントリがfinalize時に型変換に失敗した場合も、単一行ルールと同じ「次の候補ルールを試す」フォールバックを適用できるようにする。フォールバック時、先頭行だけが次候補で再試行され、2行目以降は白紙に戻して1行ずつ独立に再マッチする。

**Architecture:** `internal/parse.MatchAndConvert`のロジックを、走査開始indexを指定できる`MatchAndConvertFrom`に一般化する（`MatchAndConvert`はstartIndex=0のラッパーになる）。`internal/convert.fileCursor`側は、`openEntry`にマッチしたルールの`ruleIndex`を持たせ、`pending`を単一行から行のキュー（`[]scannedLine`）に変更して複数行を戻せるようにする。「白紙の1行を先頭からマッチ」と「finalize失敗後に1行を次候補からリトライ」を同じ`tryCandidates(line, startIndex)`に統一し、`finalizeEntry`は変換失敗時に2行目以降を`pending`へ戻して先頭行だけ`tryCandidates`に回す。`advance()`のEOF分岐は、リトライが`c.open`を再び開き得ることに対応して`return`ではなく`continue`する。

**Tech Stack:** Go, 標準の`testing`パッケージ（テーブル駆動ではなく個別関数形式、既存コードのスタイルに合わせる）。

## Global Constraints

- フォールバック時の挙動は「先頭行だけを次の候補ルールで再試行し、2行目以降は完全に白紙へ戻して1行ずつ独立に再マッチする」。候補ルールの継続パターンで「同じ行数を消費し直す」リプレイは行わない。
- `internal/parse`パッケージは引き続きロガーを持たない純粋関数のまま（ログ出力は呼び出し側`internal/convert/merge.go`が担当）。
- `MatchAndConvert`の公開シグネチャ・挙動は変更しない（既存呼び出し元・テストへの影響なし）。
- 候補ルール探索の計算量は最悪`O(ルール数)`/エントリ。パフォーマンス最適化は対象外。

---

### Task 1: `internal/parse`: `MatchAndConvertFrom`の追加

**Files:**
- Modify: `internal/parse/match.go:172-202`
- Test: `internal/parse/match_test.go`

**Interfaces:**
- Consumes: 既存の`matchRule`、`Convert(rule rules.Rule, raw map[string]string, source SourceMeta, now time.Time) (values map[string]any, err error)`、`MatchAttempt`。
- Produces:
  ```go
  func MatchAndConvertFrom(ruleList []rules.Rule, startIndex int, line string, source SourceMeta, now time.Time) (rule *rules.Rule, ruleIndex int, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool)
  ```
  `ruleIndex`はマッチしたルールの`ruleList`内でのindex（`ok=false`のときは`-1`で無意味）。Task 2の`internal/convert.openEntry.ruleIndex`とTask 2の`tryCandidates`/`finalizeEntry`がこれを使う。`MatchAndConvert`は`MatchAndConvertFrom(ruleList, 0, line, source, now)`のラッパーになり、シグネチャ・挙動は変わらない。

- [ ] **Step 1: 失敗するテストを書く**

`internal/parse/match_test.go`の`TestMatchAndConvert_AllCandidatesFailBecomesUnmatchedWithAttempts`の直後に追加:

```go
func TestMatchAndConvertFrom_IgnoresCandidatesBeforeStartIndex(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		// Index 0 would match and convert successfully on its own - but
		// startIndex=1 below must skip it entirely.
		mustRule(t, "would_match_but_skipped", `^(?P<status>\S+)$`, []rules.Field{
			{Name: "status", Type: "string"},
		}),
		// Index 1: pattern matches but "status" won't parse as int.
		mustRule(t, "strict", `^(?P<status>\S+)$`, []rules.Field{
			{Name: "status", Type: "int"},
		}),
		// Index 2: matches the same line and succeeds.
		mustRule(t, "loose", `^(?P<status>\S+)$`, []rules.Field{
			{Name: "status", Type: "string"},
		}),
	}

	rule, ruleIndex, _, values, attempts, ok := MatchAndConvertFrom(ruleList, 1, "not-a-number", SourceMeta{}, now)
	if !ok {
		t.Fatal("expected loose (index 2) to match after strict (index 1) fails conversion")
	}
	if rule.Name != "loose" {
		t.Errorf("rule.Name = %q, want loose", rule.Name)
	}
	if ruleIndex != 2 {
		t.Errorf("ruleIndex = %d, want 2", ruleIndex)
	}
	if values["status"] != "not-a-number" {
		t.Errorf("values[status] = %v, want %q", values["status"], "not-a-number")
	}
	if len(attempts) != 1 || attempts[0].RuleName != "strict" || attempts[0].Err == nil {
		t.Errorf("attempts = %+v, want one failed attempt for rule %q", attempts, "strict")
	}
}

func TestMatchAndConvertFrom_NoMatchReturnsRuleIndexMinusOne(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^\[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
		}),
	}

	rule, ruleIndex, _, _, _, ok := MatchAndConvertFrom(ruleList, 0, "this line matches nothing", SourceMeta{}, now)
	if ok {
		t.Error("expected no match")
	}
	if rule != nil {
		t.Errorf("rule = %v, want nil", rule)
	}
	if ruleIndex != -1 {
		t.Errorf("ruleIndex = %d, want -1", ruleIndex)
	}
}
```

- [ ] **Step 2: テストが失敗することを確認（コンパイルエラー含む）**

Run: `go test ./internal/parse/... -v`
Expected: FAIL — `MatchAndConvertFrom`が未定義でビルドエラーになる。

- [ ] **Step 3: `MatchAndConvertFrom`を実装し、`MatchAndConvert`をラッパーにする**

`internal/parse/match.go`の172-202行目（`MatchAttempt`の後、現在の`MatchAndConvert`全体）を次に置き換える:

```go
// MatchAttempt records one single-line rule whose pattern matched line but
// whose field conversion failed - see MatchAndConvert.
type MatchAttempt struct {
	RuleName string
	Err      error
}

// MatchAndConvertFrom is MatchAndConvert generalized to start scanning
// ruleList at startIndex instead of always at 0. ruleIndex reports the
// index (within ruleList) of the rule that ultimately matched - callers
// that later need to retry from "the next candidate after this one" (see
// internal/convert.fileCursor.finalizeEntry) pass ruleIndex+1 back in as
// startIndex. ruleIndex is -1 when ok is false.
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

// MatchAndConvert tries every rule in ruleList, in declaration order - the
// startIndex=0 case of MatchAndConvertFrom. A non-continuation rule whose
// pattern matches is converted immediately; if conversion fails, that rule
// is treated as a non-match and the next candidate rule is tried -
// conversion failure no longer ends the search. A continuation rule whose
// pattern matches is returned right away without conversion (values ==
// nil): its entry accumulates further lines and is converted later by the
// caller (see internal/convert.fileCursor.finalizeEntry, which can now
// also fall back via MatchAndConvertFrom on a conversion failure there).
func MatchAndConvert(ruleList []rules.Rule, line string, source SourceMeta, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	rule, _, raw, values, attempts, ok = MatchAndConvertFrom(ruleList, 0, line, source, now)
	return
}
```

- [ ] **Step 4: テストが通ることを確認**

Run: `go test ./internal/parse/... -v`
Expected: PASS — 新規テストに加え、既存の`TestMatchAndConvert_*`テストも全て通る（`MatchAndConvert`のシグネチャ・挙動は変わっていない）。

- [ ] **Step 5: コミット**

```bash
git add internal/parse/match.go internal/parse/match_test.go
git commit -m "feat(parse): add MatchAndConvertFrom to support resuming fallback search mid-list"
```

---

### Task 2: `internal/convert`: continuationエントリのfinalize失敗フォールバック

**Files:**
- Modify: `internal/convert/merge.go` (`openEntry`, `fileCursor.pending`, `nextLine`, `finalizeEntry`, `advance`; new `pushPending`, `tryCandidates`)
- Test: `internal/convert/merge_test.go`

**Interfaces:**
- Consumes: Task 1の`parse.MatchAndConvertFrom(ruleList []rules.Rule, startIndex int, line string, source parse.SourceMeta, now time.Time) (rule *rules.Rule, ruleIndex int, raw map[string]string, values map[string]any, attempts []parse.MatchAttempt, ok bool)`。既存の`(c *fileCursor) writeConverted(name string, values map[string]any, startLine int) (*candidate, error)`と`(c *fileCursor) writeUnmatchedLine(line scannedLine) error`。
- Produces:
  ```go
  func (c *fileCursor) pushPending(lines []scannedLine)
  func (c *fileCursor) tryCandidates(line scannedLine, startIndex int) (*candidate, error)
  ```
  `advance()`が両方を使う形に再構成される。`openEntry`に`ruleIndex int`が追加される。

- [ ] **Step 1: 失敗するテストを書く**

`internal/convert/merge_test.go`の既存の`TestFileCursor_Advance_ConversionFailureSplitsIntoIndividualUnmatchedRecords`（629-693行目）を、関数名ごと次の内容で**置き換える**（単一候補しか無かった旧テストは、フォールバックが実際に効くケースに書き換える）:

```go
func TestFileCursor_Advance_ContinuationConversionFailureFallsThroughToNextRule(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: counter
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    continuation: '^MORE (?P<count>\d+)$'
    fields:
      time: string
      count: int
  - name: raw_start_line
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    fields:
      time: string
      count: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// Folding two "count" captures together with a newline ("5\n6") is not
	// parseable as an int, forcing a type-conversion failure on the closed
	// multi-line entry. The first line then falls back to raw_start_line,
	// which matches the same text but converts count as a string; the
	// second line no longer belongs to any entry and is rematched on its
	// own, matching neither rule.
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
		t.Fatal("advance() ok = true, want false: neither raw_start_line nor the unmatched line has a merge key")
	}
	if cursor.counts["raw_start_line"] != 1 {
		t.Errorf("counts[raw_start_line] = %d, want 1", cursor.counts["raw_start_line"])
	}
	if cursor.counts["counter"] != 0 {
		t.Errorf("counts[counter] = %d, want 0", cursor.counts["counter"])
	}
	if cursor.unmatched != 1 {
		t.Errorf("unmatched = %d, want 1 (only the second line, MORE 6)", cursor.unmatched)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["raw_start_line"] != 1 {
		t.Errorf("expected 1 raw_start_line row written, got %d", summary.Counts["raw_start_line"])
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t2\tMORE 6\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}
```

Then add three more tests immediately after it:

```go
func TestFileCursor_Advance_ContinuationConversionFailureFallsThroughToAnotherContinuationRule(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: counter
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    continuation: '^MORE (?P<count>\d+)$'
    fields:
      time: string
      count: int
  - name: counter_loose
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    continuation: '^MORE (?P<count>\d+)$'
    fields:
      time: string
      count: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// counter's entry fails (count folds to "5\n6", not a valid int). The
	// first line falls back to counter_loose, whose continuation pattern
	// is tried fresh against the second line (MORE 6) rather than being
	// replayed from counter's already-consumed state - it folds in
	// successfully this time since counter_loose's count field is a
	// string.
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
		t.Fatal("advance() ok = true, want false: counter_loose has no merge key")
	}
	if cursor.counts["counter_loose"] != 1 {
		t.Errorf("counts[counter_loose] = %d, want 1", cursor.counts["counter_loose"])
	}
	if cursor.counts["counter"] != 0 {
		t.Errorf("counts[counter] = %d, want 0", cursor.counts["counter"])
	}
	if cursor.unmatched != 0 {
		t.Errorf("unmatched = %d, want 0: both lines ended up folded into counter_loose", cursor.unmatched)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["counter_loose"] != 1 {
		t.Errorf("expected 1 counter_loose row written, got %d", summary.Counts["counter_loose"])
	}
}

func TestFileCursor_Advance_ContinuationAllCandidatesExhaustedFirstLineUnmatchedRestRematchIndependently(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: counter
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    continuation: '^MORE (?P<count>\d+)$'
    fields:
      time: string
      count: int
  - name: more_line
    pattern: '^MORE (?P<count>\d+)$'
    fields:
      count: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// counter's entry fails (count folds to "5\n6"). Its only remaining
	// candidate, more_line, doesn't match the first line's text at all, so
	// the first line alone becomes an unmatched record. The second line,
	// rematched independently from scratch, does match more_line on its
	// own.
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
		t.Fatal("advance() ok = true, want false: more_line has no merge key")
	}
	if cursor.counts["more_line"] != 1 {
		t.Errorf("counts[more_line] = %d, want 1", cursor.counts["more_line"])
	}
	if cursor.unmatched != 1 {
		t.Errorf("unmatched = %d, want 1 (only the first line)", cursor.unmatched)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["more_line"] != 1 {
		t.Errorf("expected 1 more_line row written, got %d", summary.Counts["more_line"])
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t1\tTS 2026-08-06T12:00:00Z START 5\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}

func TestFileCursor_Advance_EOFClosedEntryCascadesThroughMultipleContinuationCandidates(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: counter_int
    pattern: '^TS (?P<time>\S+) START (?P<count>\S+)$'
    continuation: '^MORE (?P<count>\S+)$'
    fields:
      time: string
      count: int
  - name: counter_int2
    pattern: '^TS (?P<time>\S+) START (?P<count>\S+)$'
    continuation: '^MORE (?P<count>\S+)$'
    fields:
      time: string
      count: int
  - name: raw_line
    pattern: '^(?P<line>.*)$'
    fields:
      line: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// A single line with no continuation line following it, so the entry
	// closes at EOF (not by a fresh line breaking it out). count fails int
	// conversion under counter_int, then again under counter_int2 - both
	// retries happen inside advance()'s EOF branch, with no line left in
	// the file, before raw_line finally succeeds.
	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z START notanumber\n")

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
		t.Fatal("advance() ok = true, want false: raw_line has no merge key")
	}
	if cursor.counts["raw_line"] != 1 {
		t.Errorf("counts[raw_line] = %d, want 1", cursor.counts["raw_line"])
	}
	if cursor.counts["counter_int"] != 0 || cursor.counts["counter_int2"] != 0 {
		t.Errorf("counts[counter_int]=%d counts[counter_int2]=%d, want both 0", cursor.counts["counter_int"], cursor.counts["counter_int2"])
	}
	if cursor.unmatched != 0 {
		t.Errorf("unmatched = %d, want 0", cursor.unmatched)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["raw_line"] != 1 {
		t.Errorf("expected 1 raw_line row written, got %d", summary.Counts["raw_line"])
	}
}
```

- [ ] **Step 2: テストが失敗することを確認**

Run: `go test ./internal/convert/... -v`
Expected: FAIL — 現状の`finalizeEntry`は変換失敗時に即`rawLines`全体をunmatchedへ分割するだけで、次候補へのフォールバックがない。4テストとも期待するcounts/unmatchedと食い違って落ちる。

- [ ] **Step 3: `openEntry`に`ruleIndex`を追加し、`pending`をキュー化する**

`internal/convert/merge.go`の`openEntry`(65-69行目)を次に変更する:

```go
type openEntry struct {
	rule      *rules.Rule
	ruleIndex int // index within cfg.Rules that rule matched at - see finalizeEntry
	raw       map[string]string
	rawLines  []scannedLine
}
```

`fileCursor`の`pending *scannedLine`フィールド(111行目)を`pending []scannedLine`に変更する。

`nextLine`(153-167行目)を次に変更する:

```go
// nextLine returns the next physical line to process: a previously pushed
// back line, if any (see fileCursor.pending / pushPending), otherwise the
// next line from the underlying scanner. ok is false at EOF.
func (c *fileCursor) nextLine() (line scannedLine, ok bool, err error) {
	if len(c.pending) > 0 {
		line = c.pending[0]
		c.pending = c.pending[1:]
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

// pushPending prepends lines to the front of the pending queue, preserving
// their relative order, so nextLine() replays them - in original file
// order - before anything already queued (e.g. the line that closed a
// continuation entry, pushed there before finalizeEntry runs - see
// advance()).
func (c *fileCursor) pushPending(lines []scannedLine) {
	if len(lines) == 0 {
		return
	}
	c.pending = append(append([]scannedLine{}, lines...), c.pending...)
}
```

- [ ] **Step 4: `tryCandidates`を新設する**

`internal/convert/merge.go`の`writeConverted`(214-239行目)の直後、`finalizeEntry`の前に追加する:

```go
// tryCandidates searches cfg.Rules starting at startIndex for a rule that
// disposes of line: a non-continuation rule that matches and converts
// successfully, or a continuation rule whose pattern matches, which
// becomes the new open entry (c.open) instead of an immediate result. If
// every candidate from startIndex onward either doesn't match or fails
// conversion, line is written to unmatched.txt. startIndex is 0 for a
// fresh line, or one past the index of a rule whose entry already failed
// at finalize time (see finalizeEntry), so a retry never reconsiders a
// rule that already lost for this line.
func (c *fileCursor) tryCandidates(line scannedLine, startIndex int) (*candidate, error) {
	rule, ruleIndex, raw, values, attempts, matched := parse.MatchAndConvertFrom(c.cfg.Rules, startIndex, line.text, parse.SourceMeta{File: c.inputPath, Line: line.lineNum}, c.now)
	for _, a := range attempts {
		c.logger.Debug("candidate rule matched but failed conversion", "file", c.inputPath, "line", line.lineNum, "rule", a.RuleName, "error", a.Err)
	}
	if !matched {
		c.logger.Debug("line did not match any rule", "file", c.inputPath, "line", line.lineNum)
		return nil, c.writeUnmatchedLine(line)
	}

	if values == nil {
		c.open = &openEntry{rule: rule, ruleIndex: ruleIndex, raw: raw, rawLines: []scannedLine{line}}
		return nil, nil
	}

	return c.writeConverted(rule.Name, values, line.lineNum)
}
```

- [ ] **Step 5: `finalizeEntry`を再構成する**

`internal/convert/merge.go`の`finalizeEntry`(241-259行目)を次に置き換える:

```go
// finalizeEntry converts entry's accumulated raw values. A type-conversion
// failure no longer immediately splits the entry into unmatched.txt
// records: every line after the first is put back at the front of the
// pending queue (see pushPending) so it's reprocessed as an independent
// fresh line, and the first line is retried against whichever candidate
// rules after entry.rule haven't been tried yet (see tryCandidates) - the
// same declaration-order fallback single-line rules already get. Only
// once every remaining candidate for the first line also fails does that
// first line alone end up in unmatched.txt.
func (c *fileCursor) finalizeEntry(entry *openEntry) (*candidate, error) {
	values, convErr := parse.Convert(*entry.rule, entry.raw, parse.SourceMeta{File: c.inputPath, Line: entry.rawLines[0].lineNum}, c.now)
	if convErr == nil {
		return c.writeConverted(entry.rule.Name, values, entry.rawLines[0].lineNum)
	}

	c.logger.Debug("entry failed type conversion, trying next candidate rule", "file", c.inputPath, "rule", entry.rule.Name, "start_line", entry.rawLines[0].lineNum, "error", convErr)
	c.pushPending(entry.rawLines[1:])
	return c.tryCandidates(entry.rawLines[0], entry.ruleIndex+1)
}
```

- [ ] **Step 6: `advance()`を`tryCandidates`を使う形に再構成する**

`internal/convert/merge.go`の`advance()`(271-335行目)を次に置き換える:

```go
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
			if cand != nil {
				return cand, true, nil
			}
			continue // finalizeEntry's retry may have reopened c.open
		}

		if c.open != nil {
			if raw, matched := matchContinuation(c.open.rule, line.text); matched {
				appendContinuation(c.open, raw)
				c.open.rawLines = append(c.open.rawLines, line)
				continue
			}

			entry := c.open
			c.open = nil
			c.pushPending([]scannedLine{line})
			cand, err := c.finalizeEntry(entry)
			if err != nil {
				return nil, false, err
			}
			if cand != nil {
				return cand, true, nil
			}
			continue
		}

		cand, err := c.tryCandidates(line, 0)
		if err != nil {
			return nil, false, err
		}
		if cand != nil {
			return cand, true, nil
		}
	}
}
```

This removes the old direct call to `parse.MatchAndConvert` from `advance()` — `tryCandidates(line, 0)` now covers what that inline block did, unifying it with the finalize-retry path.

- [ ] **Step 7: テストが通ることを確認**

Run: `go test ./internal/convert/... -v`
Expected: PASS — 新規4テストに加え、`TestFileCursor_Advance_SingleLineFallsThroughToNextRuleOnConversionFailure`を含む既存の全テストが通る。

- [ ] **Step 8: リポジトリ全体のビルドとテストを確認**

Run: `go build ./... && go vet ./... && go test ./... && gofmt -l .`
Expected: 全てPASS。`gofmt -l .`は空出力。

- [ ] **Step 9: コミット**

```bash
git add internal/convert/merge.go internal/convert/merge_test.go
git commit -m "feat(convert): fall back to the next candidate rule when a continuation entry fails to convert"
```

---

### Task 3: `README.md`の更新

**Files:**
- Modify: `README.md:256`

**Interfaces:**
- Consumes: Task 1・2で実装済みの挙動。
- Produces: なし（ドキュメントのみ）。

- [ ] **Step 1: 該当段落を新しい挙動に合わせて書き換える**

`README.md`256行目、現状:

```
- ルールの`pattern`がマッチしても、その後のフィールド変換(上記の型変換失敗・構造化データのパース失敗・`key:`のキー不在を含む)が失敗した場合、そのルールは即座にunmatched扱いにはならず、`rules:`の次の候補ルールが宣言順に試される。すべての候補ルールで変換に失敗した行だけが最終的に`unmatched.txt`に書かれる。このフォールバックは単一行ルールのみが対象で、`continuation`ルール(後述の「複数行ログエントリのマージ」を参照)では継続行パターンが既に後続行を消費してしまっているため対象外であり、複数行エントリの型変換失敗は従来通りフォールバックなしでunmatchedになる。
```

を次に置き換える:

```
- ルールの`pattern`がマッチしても、その後のフィールド変換(上記の型変換失敗・構造化データのパース失敗・`key:`のキー不在を含む)が失敗した場合、そのルールは即座にunmatched扱いにはならず、`rules:`の次の候補ルールが宣言順に試される。すべての候補ルールで変換に失敗した行だけが最終的に`unmatched.txt`に書かれる。`continuation`ルール(後述の「複数行ログエントリのマージ」を参照)のエントリがfinalize時に型変換に失敗した場合も同様にフォールバックする: 先頭行だけが次の候補ルールで再試行され、2行目以降は完全に白紙へ戻して1行ずつ独立に再マッチする(候補ルールの継続パターンで同じ行数を消費し直すリプレイは行わない)。そのため、失敗したエントリの2行目以降が別のルールにマッチしたり、別々のunmatchedレコードになったりし得る。
```

- [ ] **Step 2: 差分を確認する**

Run: `git diff README.md`
Expected: 256行目のみが変更されている。

- [ ] **Step 3: コミット**

```bash
git add README.md
git commit -m "docs: document continuation entry fallback on type-conversion failure"
```

---

### Task 4: 最終確認と全体テスト

**Files:**
- No file changes — verification-only task.

**Interfaces:**
- Consumes: Task 1〜3の完了状態。

- [ ] **Step 1: 設計ドキュメントのスコープと実装の突き合わせ**

`docs/superpowers/specs/2026-08-10-continuation-entry-fallback-design.md`のA〜E節を読み直し、以下を確認する:
- A（`MatchAndConvertFrom`新設）: Task 1で実装済み。
- B（`openEntry.ruleIndex`、`pending`のキュー化）: Task 2 Step 3で実装済み。
- C（`tryCandidates`によるマッチ処理の統一）: Task 2 Step 4で実装済み。
- D（`finalizeEntry`の再構成）: Task 2 Step 5で実装済み。
- E（`advance()`の再構成、EOF分岐の`continue`化）: Task 2 Step 6で実装済み。
- README.mdの記述更新: Task 3で実装済み。

- [ ] **Step 2: 全パッケージのテストとlintを通す**

Run: `go build ./... && go vet ./... && go test ./... && gofmt -l .`
Expected: 全てPASS、`gofmt -l .`は空出力。

Run (golangci-lintが利用可能な場合): `golangci-lint run ./...`
Expected: PASS（既存のlint設定に新規warningが出ないこと）。

- [ ] **Step 3: `git log`で3コミットが積まれていることを確認**

Run: `git log --oneline -5`
Expected: Task 1〜3の3コミットがmainブランチの先頭に積まれている。
