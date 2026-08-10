# unmatched判定のフォールバック統一とmissing-key検出 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 単一行ルールについて、正規表現マッチ後の型変換失敗（missing structured key含む）を即unmatchedにせず、次の候補ルールを試すようフォールバックさせる。あわせて`structured:`のmissing-keyを検出可能なエラーにする。

**Architecture:** `internal/parse/match.go`に「マッチ→即Convert→失敗したら次候補」という1ループの新関数`MatchAndConvert`を追加し、既存の`MatchRaw`+`Convert`の薄いラッパーだった`Match`を削除する。`internal/convert/merge.go`の`fileCursor.advance()`が単一行マッチ部分で`parse.MatchRaw`の代わりに`parse.MatchAndConvert`を呼ぶよう置き換え、変換成功後の共通処理（マージキー判定・書き込み・candidate生成）を`finalizeEntry`から`writeConverted`として切り出して両方から使う。continuation（複数行）ルールの挙動は完全に現状維持。

**Tech Stack:** Go, 標準の`testing`パッケージ（テーブル駆動ではなく個別関数形式、既存コードのスタイルに合わせる）。

## Global Constraints

- 対象は**単一行ルール（`continuation`未設定）のみ**。continuationルールの型変換失敗は現状通りフォールバックなしでunmatched split。
- `parse`パッケージは引き続きロガーを持たない純粋関数のまま（ログ出力は呼び出し側`internal/convert/merge.go`が担当）。
- 既存のエクスポート契約のうち`Match`関数だけを削除する。`MatchRaw`と`Convert`はそのまま残す。

---

### Task 1: `Convert`にmissing-key検出を追加

**Files:**
- Modify: `internal/parse/match.go:71-73`
- Test: `internal/parse/match_test.go`

**Interfaces:**
- Consumes: 既存の`Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error)`のシグネチャは変更しない。
- Produces: `Convert`は`field.Key`が`structuredValues`に存在しない場合、`fmt.Errorf("structured data missing key %q", field.Key)`を返す。Task 3・4がこのエラーを`MatchAndConvert`のフォールバック経路で利用する。

- [ ] **Step 1: 失敗するテストを書く**

`internal/parse/match_test.go`の末尾に追加:

```go
func TestConvert_MissingStructuredKeyReturnsError(t *testing.T) {
	rule := rules.Rule{
		Name:       "container_log",
		Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
		Fields: []rules.Field{
			{Name: "level", Type: "string", Key: "level"},
		},
	}
	now := time.Now()

	_, err := Convert(rule, map[string]string{
		"json": `{"msg":"no level field here"}`,
	}, now)
	if err == nil {
		t.Fatal("expected an error when structured data has no value for field.Key")
	}
}
```

- [ ] **Step 2: テストが失敗することを確認**

Run: `go test ./internal/parse/... -run TestConvert_MissingStructuredKeyReturnsError -v`
Expected: FAIL — 現状は`structuredValues["level"]`がゼロ値`""`を返し、`string`型フィールドとしてそのまま変換成功してしまうため、`err == nil`でテストが落ちる。

- [ ] **Step 3: `Convert`のKeyフィールド分岐をcomma-okに書き換える**

`internal/parse/match.go`の`converted := make(...)`ループ内、現状の:

```go
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
```

を次のように変更する（`field.Key != ""`のcaseだけがcomma-okのif文に変わる。ループ本体の残りはそのまま）:

```go
	converted := make(map[string]any, len(rule.Fields))
	for _, field := range rule.Fields {
		rawValue := raw[field.Name]
		switch {
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
```

- [ ] **Step 4: テストが通ることを確認**

Run: `go test ./internal/parse/... -run TestConvert_MissingStructuredKeyReturnsError -v`
Expected: PASS

- [ ] **Step 5: パッケージ全体のテストも流し、既存の`Convert`テストが壊れていないことを確認**

Run: `go test ./internal/parse/... -v`
Expected: 既存の`TestConvert_*`は全てPASS（既存のキー存在ケースは影響を受けない）。

- [ ] **Step 6: コミット**

```bash
git add internal/parse/match.go internal/parse/match_test.go
git commit -m "fix(parse): detect missing structured key as a conversion error"
```

---

### Task 2: `MatchAndConvert`を新設し、`Match`を削除する

**Files:**
- Modify: `internal/parse/match.go`
- Test: `internal/parse/match_test.go`

**Interfaces:**
- Consumes: Task 1で変更済みの`Convert`（missing-keyがエラーになる）。既存の`MatchRaw`のキャプチャ抽出ロジック（正規表現名前付きグループをmapへ詰める処理）。
- Produces:
  ```go
  type MatchAttempt struct {
      RuleName string
      Err      error
  }

  func MatchAndConvert(ruleList []rules.Rule, line string, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool)
  ```
  戻り値の意味:
  - `ok=true, values!=nil`: 単一行ルールが変換まで成功。
  - `ok=true, values==nil`: continuationルールがマッチ（`raw`は先頭行のcapture）。呼び出し側が継続行を蓄積し、`finalizeEntry`で`Convert`する。
  - `ok=false`: どの候補も成立せず。`attempts`に「正規表現はマッチしたが変換に失敗した単一行ルール」の一覧が入る。

  Task 4（`internal/convert/merge.go`）がこの関数を呼び出す。

- [ ] **Step 1: 失敗するテストを書く（フォールバック成立ケース）**

`internal/parse/match_test.go`に追加。まず既存の`TestMatch_TypeConversionFailureIsUnmatched_NoFallthrough`を**削除**し、代わりに新しい期待値（フォールバックが効く）のテストに置き換える:

```go
func TestMatchAndConvert_TypeConversionFailureFallsThroughToNextRule(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		// First rule's pattern matches but "status" won't parse as int.
		mustRule(t, "strict", `^(?P<status>\S+)$`, []rules.Field{
			{Name: "status", Type: "int"},
		}),
		// Second rule matches the same line and succeeds.
		mustRule(t, "loose", `^(?P<status>\S+)$`, []rules.Field{
			{Name: "status", Type: "string"},
		}),
	}

	rule, _, values, attempts, ok := MatchAndConvert(ruleList, "not-a-number", now)
	if !ok {
		t.Fatal("expected the second rule to succeed after the first fails conversion")
	}
	if rule.Name != "loose" {
		t.Errorf("rule.Name = %q, want loose", rule.Name)
	}
	if values["status"] != "not-a-number" {
		t.Errorf("values[status] = %v, want %q", values["status"], "not-a-number")
	}
	if len(attempts) != 1 || attempts[0].RuleName != "strict" || attempts[0].Err == nil {
		t.Errorf("attempts = %+v, want one failed attempt for rule %q", attempts, "strict")
	}
}

func TestMatchAndConvert_FirstMatchingRuleWins(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "time", Type: "string"},
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
		}),
	}

	rule, _, values, _, ok := MatchAndConvert(ruleList, "2026-08-06T12:00:01+09:00 [INFO] user logged in", now)
	if !ok {
		t.Fatal("expected match, got none")
	}
	if rule.Name != "app_log" {
		t.Errorf("expected rule name app_log, got %q", rule.Name)
	}
	if values["level"] != "INFO" || values["message"] != "user logged in" {
		t.Errorf("unexpected values: %+v", values)
	}
}

func TestMatchAndConvert_NoRuleMatches(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		mustRule(t, "app_log", `^\[(?P<level>\w+)\] (?P<message>.*)$`, []rules.Field{
			{Name: "level", Type: "string"},
			{Name: "message", Type: "string"},
		}),
	}

	_, _, _, attempts, ok := MatchAndConvert(ruleList, "this line matches nothing", now)
	if ok {
		t.Error("expected no match")
	}
	if len(attempts) != 0 {
		t.Errorf("expected no attempts when no rule's pattern even matches, got %+v", attempts)
	}
}

func TestMatchAndConvert_MissingStructuredKeyFallsThroughToNextRule(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		{
			Name:       "container_log",
			Regexp:     regexp.MustCompile(`^(?P<json>\{.*\})$`),
			Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
			Fields: []rules.Field{
				{Name: "json", Type: "string"},
				{Name: "level", Type: "string", Key: "level"},
			},
		},
		{
			Name:   "raw_line",
			Regexp: regexp.MustCompile(`^(?P<line>.*)$`),
			Fields: []rules.Field{
				{Name: "line", Type: "string"},
			},
		},
	}

	line := `{"msg":"no level field here"}`
	rule, _, values, attempts, ok := MatchAndConvert(ruleList, line, now)
	if !ok {
		t.Fatal("expected the fallback rule to match after the structured rule's missing key fails")
	}
	if rule.Name != "raw_line" {
		t.Errorf("rule.Name = %q, want raw_line", rule.Name)
	}
	if values["line"] != line {
		t.Errorf("values[line] = %v, want %q", values["line"], line)
	}
	if len(attempts) != 1 || attempts[0].RuleName != "container_log" || attempts[0].Err == nil {
		t.Errorf("attempts = %+v, want one failed attempt for rule %q", attempts, "container_log")
	}
}

func TestMatchAndConvert_AllCandidatesFailBecomesUnmatchedWithAttempts(t *testing.T) {
	now := time.Now()
	ruleList := []rules.Rule{
		{
			Name:       "container_log",
			Regexp:     regexp.MustCompile(`^(?P<json>\{.*\})$`),
			Structured: &rules.StructuredConfig{Source: "json", Format: "json"},
			Fields: []rules.Field{
				{Name: "json", Type: "string"},
				{Name: "level", Type: "string", Key: "level"},
			},
		},
		mustRule(t, "strict", `^(?P<status>\{.*\})$`, []rules.Field{
			{Name: "status", Type: "int"},
		}),
	}

	line := `{"msg":"no level field here"}`
	rule, raw, values, attempts, ok := MatchAndConvert(ruleList, line, now)
	if ok {
		t.Fatalf("expected no candidate to succeed, got rule=%v raw=%v values=%v", rule, raw, values)
	}
	if rule != nil {
		t.Errorf("rule = %v, want nil", rule)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %+v, want 2 entries", attempts)
	}
	if attempts[0].RuleName != "container_log" || attempts[0].Err == nil {
		t.Errorf("attempts[0] = %+v, want failed attempt for container_log", attempts[0])
	}
	if attempts[1].RuleName != "strict" || attempts[1].Err == nil {
		t.Errorf("attempts[1] = %+v, want failed attempt for strict", attempts[1])
	}
}
```

Add `"regexp"` to `internal/parse/match_test.go`'s import block if not already present (it already is, used by `TestConvert_PresetFormatTakesKeyFieldFromPresetMatch`).

Also delete the now-obsolete `TestMatch_FirstMatchingRuleWins` and `TestMatch_NoRuleMatches` (superseded by the `MatchAndConvert` versions above — keeping both would just duplicate coverage of `Match`, which Step 4 removes).

- [ ] **Step 2: テストが失敗することを確認（コンパイルエラー含む）**

Run: `go test ./internal/parse/... -v`
Expected: FAIL — `MatchAndConvert`と`MatchAttempt`が未定義でビルドエラーになる。

- [ ] **Step 3: `MatchAndConvert`を実装する**

`internal/parse/match.go`の`MatchRaw`を、キャプチャ抽出を共有ヘルパーへ切り出した上で書き換える。ファイル全体を以下の形に変更する（`Convert`本体はTask 1の変更を維持、`marshalUnconsumed`はそのまま）:

```go
// matchRule tries r's pattern against line and, if it matches, returns its
// named captures.
func matchRule(r *rules.Rule, line string) (raw map[string]string, matched bool) {
	m := r.Regexp.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	captured := map[string]string{}
	for j, groupName := range r.Regexp.SubexpNames() {
		if j == 0 || groupName == "" {
			continue
		}
		captured[groupName] = m[j]
	}
	return captured, true
}

// MatchRaw tries each rule's pattern against line and returns the first
// match's rule and raw (un-type-converted) captured field values, keyed by
// field name. No type conversion happens here - see Convert.
func MatchRaw(ruleList []rules.Rule, line string) (rule *rules.Rule, raw map[string]string, ok bool) {
	for i := range ruleList {
		r := &ruleList[i]
		if captured, matched := matchRule(r, line); matched {
			return r, captured, true
		}
	}
	return nil, nil, false
}
```

Keep `Convert` and `marshalUnconsumed` unchanged from Task 1. Then, in place of the old `Match` function (currently at the bottom of the file, lines 108-125), add:

```go
// MatchAttempt records one single-line rule whose pattern matched line but
// whose field conversion failed - see MatchAndConvert.
type MatchAttempt struct {
	RuleName string
	Err      error
}

// MatchAndConvert tries each rule's pattern against line in order. A
// non-continuation rule whose pattern matches is converted immediately; if
// conversion fails, that rule is treated as a non-match and the next
// candidate rule is tried - conversion failure no longer ends the search.
// A continuation rule whose pattern matches is returned right away without
// conversion (values == nil): its entry accumulates further lines and is
// converted later by the caller, and a conversion failure there still has
// no fallback, since by that point earlier lines were already consumed
// under this rule's continuation pattern and can't be replayed against a
// different rule.
func MatchAndConvert(ruleList []rules.Rule, line string, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	for i := range ruleList {
		r := &ruleList[i]
		captured, matched := matchRule(r, line)
		if !matched {
			continue
		}

		if r.ContinuationRegexp != nil {
			return r, captured, nil, attempts, true
		}

		v, err := Convert(*r, captured, now)
		if err != nil {
			attempts = append(attempts, MatchAttempt{RuleName: r.Name, Err: err})
			continue
		}
		return r, captured, v, attempts, true
	}
	return nil, nil, nil, attempts, false
}
```

Finally, delete the old `Match` function entirely (its doc comment and body).

- [ ] **Step 4: テストが通ることを確認**

Run: `go test ./internal/parse/... -v`
Expected: PASS — 全テストが通る。`go build ./...`もエラーなし（`Match`削除後、`internal/convert`側はまだ`MatchRaw`を呼んでいるのでこの時点ではビルドは通る）。

- [ ] **Step 5: コミット**

```bash
git add internal/parse/match.go internal/parse/match_test.go
git commit -m "feat(parse): add MatchAndConvert with per-rule fallback, remove unused Match"
```

---

### Task 3: `internal/convert/merge.go`を`MatchAndConvert`に接続する

**Files:**
- Modify: `internal/convert/merge.go:214-333` (`finalizeEntry`, `advance`)
- Test: `internal/convert/merge_test.go`

**Interfaces:**
- Consumes: Task 2の`parse.MatchAndConvert(ruleList []rules.Rule, line string, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []parse.MatchAttempt, ok bool)`。
- Produces: `(c *fileCursor) writeConverted(name string, values map[string]any, startLine int) (*candidate, error)` — Task 3内で`finalizeEntry`から切り出し、`advance()`の単一行成功パスからも直接呼ぶ。

- [ ] **Step 1: 失敗するテストを書く（単一行フォールバック）**

`internal/convert/merge_test.go`に追加（`TestFileCursor_Advance_ConversionFailureSplitsIntoIndividualUnmatchedRecords`の直後あたりに置く）:

```go
func TestFileCursor_Advance_SingleLineFallsThroughToNextRuleOnConversionFailure(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: strict
    pattern: '^(?P<status>\S+)$'
    fields:
      status: int
  - name: loose
    pattern: '^(?P<status>\S+)$'
    fields:
      status: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "not-a-number\n")

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

	// "strict" matches the pattern but "not-a-number" fails int conversion;
	// the line falls through to "loose", which has no merge key, so it's
	// written immediately rather than returned as a candidate.
	_, ok, err := cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false: loose has no merge key and is written immediately")
	}
	if cursor.unmatched != 0 {
		t.Errorf("unmatched = %d, want 0: the line matched loose after strict failed conversion", cursor.unmatched)
	}
	if cursor.counts["loose"] != 1 {
		t.Errorf("counts[loose] = %d, want 1", cursor.counts["loose"])
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["loose"] != 1 {
		t.Errorf("expected 1 loose row written, got %d", summary.Counts["loose"])
	}
	if summary.Counts["strict"] != 0 {
		t.Errorf("expected 0 strict rows written, got %d", summary.Counts["strict"])
	}
}
```

- [ ] **Step 2: テストが失敗することを確認**

Run: `go test ./internal/convert/... -run TestFileCursor_Advance_SingleLineFallsThroughToNextRuleOnConversionFailure -v`
Expected: FAIL — 現状の`advance()`は`parse.MatchRaw`＋即`finalizeEntry`の`Convert`のみで、`strict`の変換失敗が即unmatchedになるため、`cursor.unmatched`が1、`counts["loose"]`が0になり両アサーションが落ちる。

- [ ] **Step 3: `finalizeEntry`から`writeConverted`を切り出す**

`internal/convert/merge.go`の`finalizeEntry`(214-260行目付近)を次のように変更する。既存のdocコメントとロジックのうち、変換成功後の処理を新メソッド`writeConverted`に切り出し、`finalizeEntry`はConvert呼び出しと失敗時のunmatched splitだけを残す:

```go
// writeConverted disposes of a rule's already-converted values: written
// immediately if name's rule has no merge key (see mergeKeyField), or
// returned as a candidate for the caller to hand to mergeFiles otherwise.
// The returned error is only non-nil for a genuine write/I-O failure.
func (c *fileCursor) writeConverted(name string, values map[string]any, startLine int) (*candidate, error) {
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

// finalizeEntry converts entry's accumulated raw values and disposes of
// the result. A type-conversion failure splits the entry back into its
// original per-line unmatched.txt records instead of writing one record
// with embedded newlines, preserving unmatched.txt's one-record-per-line
// format. A successfully converted row is disposed of by writeConverted.
func (c *fileCursor) finalizeEntry(entry *openEntry) (*candidate, error) {
	values, convErr := parse.Convert(*entry.rule, entry.raw, c.now)
	if convErr != nil {
		c.logger.Debug("entry failed type conversion", "file", c.inputPath, "rule", entry.rule.Name, "start_line", entry.rawLines[0].lineNum, "error", convErr)
		for _, rl := range entry.rawLines {
			if err := c.writeUnmatchedLine(rl); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}

	return c.writeConverted(entry.rule.Name, values, entry.rawLines[0].lineNum)
}
```

This is a pure refactor of `finalizeEntry` — behavior for continuation entries (the only caller of `finalizeEntry`, so far) is identical to before.

- [ ] **Step 4: `advance()`の単一行マッチ部分を`MatchAndConvert`に置き換える**

`internal/convert/merge.go`の`advance()`(272-333行目付近)の末尾ブロック、現状:

```go
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
```

を次に置き換える:

```go
		rule, raw, values, attempts, matched := parse.MatchAndConvert(c.cfg.Rules, line.text, c.now)
		if !matched {
			for _, a := range attempts {
				c.logger.Debug("candidate rule matched but failed conversion", "file", c.inputPath, "line", line.lineNum, "rule", a.RuleName, "error", a.Err)
			}
			c.logger.Debug("line did not match any rule", "file", c.inputPath, "line", line.lineNum)
			if err := c.writeUnmatchedLine(line); err != nil {
				return nil, false, err
			}
			continue
		}

		if values == nil {
			c.open = &openEntry{rule: rule, raw: raw, rawLines: []scannedLine{line}}
			continue
		}

		cand, err := c.writeConverted(rule.Name, values, line.lineNum)
		if err != nil {
			return nil, false, err
		}
		if cand != nil {
			return cand, true, nil
		}
	}
}
```

Note the fallback log lines are only emitted on the final `!matched` branch (final unmatched), never when a later candidate rule goes on to succeed — matching design doc section C.

- [ ] **Step 5: テストが通ることを確認**

Run: `go test ./internal/convert/... -v`
Expected: PASS — 新規テストに加え、`TestFileCursor_Advance_ConversionFailureSplitsIntoIndividualUnmatchedRecords`（continuationの変換失敗は現状維持）を含む既存の全テストが通る。

- [ ] **Step 6: リポジトリ全体のビルドとテストを確認**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: 全てPASS。`gofmt -l .`が空を返すことも確認する。

- [ ] **Step 7: コミット**

```bash
git add internal/convert/merge.go internal/convert/merge_test.go
git commit -m "feat(convert): fall back to the next candidate rule on conversion failure"
```

---

### Task 4: 最終確認と全体テスト

**Files:**
- No file changes — verification-only task.

**Interfaces:**
- Consumes: Task 1〜3の完了状態。

- [ ] **Step 1: 設計ドキュメントのスコープと実装の突き合わせ**

`docs/superpowers/specs/2026-08-09-unmatched-fallback-and-missing-key-design.md`のA〜D節を読み直し、以下を確認する:
- A（missing-key検出）: Task 1で実装済み。
- B（`MatchAndConvert`新設、`Match`削除）: Task 2で実装済み。
- C（デバッグログ、途中失敗は最終的に成功すればログなし）: Task 3 Step 4の実装で、`attempts`のログは`!matched`（最終的にunmatched）の場合のみ出力される — 途中で失敗しても最終的に成功したケースはログが出ないことをコードレビューで確認する。
- D（`merge.go`/`match.go`への影響、テスト）: Task 1〜3で実装済み。

- [ ] **Step 2: 全パッケージのテストとlintを通す**

Run: `go build ./... && go vet ./... && go test ./... && gofmt -l .`
Expected: 全てPASS、`gofmt -l .`は空出力。

Run (if `golangci-lint` is available per project convention): `golangci-lint run ./...`
Expected: PASS（既存のlint設定に新規warningが出ないこと）。

- [ ] **Step 3: `git log`で4コミットが積まれていることを確認**

Run: `git log --oneline -5`
Expected: Task 1〜3の3コミットがmainブランチの先頭に積まれている。
