# continuationエントリへのフォールバック拡張

## 背景・目的

`docs/superpowers/specs/2026-08-09-unmatched-fallback-and-missing-key-design.md`で、行がルールにマッチした後の型変換失敗(型変換エラー・`structured:`パース失敗・`key:`のキー不在)を、即unmatchedにせず次の候補ルールを試すフォールバックを導入した。ただしそのときのスコープは**単一行ルール(`continuation`未設定)のみ**に限定していた。理由(同designより引用):

> `continuation`ルールは先頭行の正規表現マッチ時点でストリーミングスキャナが以降の行をそのルールの継続パターンで消費し始めており、変換失敗が判明する時点(エントリのfinalize時)では既に複数行を消費し終えている。ここから「次のルールを試す」には消費済みの行を巻き戻して別ルールの継続パターンで再解釈する必要があり、`fileCursor`の前方向スキャン構造と相性が悪い。

この制限を取り払い、`continuation`ルールのエントリが変換に失敗した場合も次の候補ルールへのフォールバックを行えるようにする。

## スコープ

- `continuation`エントリのfinalize失敗時のフォールバックを追加する。
- フォールバック時の挙動: **先頭行だけを次の候補ルールで再試行し、2行目以降は完全に白紙へ戻して1行ずつ独立に再マッチする**(候補ルールの継続パターンで「同じ行数を消費し直す」リプレイは行わない)。結果として、失敗したエントリの2行目以降が別のルールにマッチしたり、別々のunmatchedレコードになったりし得る。単一行ルールのフォールバックと同じ「次の候補を試す」という考え方を、continuationエントリの先頭行にもそのまま適用する形。

## A. `internal/parse`: `MatchAndConvertFrom`の追加

既存の`MatchAndConvert`のロジックを、ルール一覧を走査し始めるindexを指定できる形に一般化する。

```go
// MatchAndConvertFrom is MatchAndConvert generalized to start scanning
// ruleList at startIndex instead of always at 0. ruleIndex reports the
// index (within ruleList) of the rule that ultimately matched - callers
// that later need to retry from "the next candidate after this one" (see
// internal/convert.fileCursor.finalizeEntry) pass ruleIndex+1 back in as
// startIndex. ruleIndex is meaningless when ok is false.
func MatchAndConvertFrom(ruleList []rules.Rule, startIndex int, line string, now time.Time) (rule *rules.Rule, ruleIndex int, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	for i := startIndex; i < len(ruleList); i++ {
		r := &ruleList[i]
		captured, matched := matchRule(r, line)
		if !matched {
			continue
		}

		if r.ContinuationRegexp != nil {
			return r, i, captured, nil, attempts, true
		}

		v, err := Convert(*r, captured, now)
		if err != nil {
			attempts = append(attempts, MatchAttempt{RuleName: r.Name, Err: err})
			continue
		}
		return r, i, captured, v, attempts, true
	}
	return nil, -1, nil, nil, attempts, false
}

// MatchAndConvert tries every rule in ruleList, in declaration order - the
// startIndex=0 case of MatchAndConvertFrom.
func MatchAndConvert(ruleList []rules.Rule, line string, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	rule, _, raw, values, attempts, ok = MatchAndConvertFrom(ruleList, 0, line, now)
	return
}
```

`MatchAndConvert`の公開シグネチャ・挙動は変わらないため、既存の呼び出し元・テストへの影響はない。

## B. `internal/convert`: `pending`のキュー化と`ruleIndex`の記録

### `openEntry`

```go
type openEntry struct {
	rule     *rules.Rule
	ruleIndex int // index within cfg.Rules that rule matched at - see finalizeEntry
	raw      map[string]string
	rawLines []scannedLine
}
```

### `fileCursor.pending`

`*scannedLine`から`[]scannedLine`に変更する。`nextLine()`はキューの先頭から取り出す:

```go
func (c *fileCursor) nextLine() (line scannedLine, ok bool, err error) {
	if len(c.pending) > 0 {
		line = c.pending[0]
		c.pending = c.pending[1:]
		return line, true, nil
	}
	if !c.scanner.Scan() {
		...
	}
	...
}
```

新しく`pushPending`を追加する。既存のpendingより時系列で前の行を戻すので、末尾追加ではなく先頭挿入にする:

```go
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

## C. `internal/convert`: `tryCandidates`によるマッチ処理の統一

「白紙の1行を先頭からマッチさせる」処理と「finalize失敗後に1行を次の候補からリトライする」処理を、探索開始indexだけが違う同じ処理として1つの関数にまとめる:

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
	rule, ruleIndex, raw, values, attempts, matched := parse.MatchAndConvertFrom(c.cfg.Rules, startIndex, line.text, c.now)
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

## D. `internal/convert`: `finalizeEntry`の再構成

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
	values, convErr := parse.Convert(*entry.rule, entry.raw, c.now)
	if convErr == nil {
		return c.writeConverted(entry.rule.Name, values, entry.rawLines[0].lineNum)
	}

	c.logger.Debug("entry failed type conversion, trying next candidate rule", "file", c.inputPath, "rule", entry.rule.Name, "start_line", entry.rawLines[0].lineNum, "error", convErr)
	c.pushPending(entry.rawLines[1:])
	return c.tryCandidates(entry.rawLines[0], entry.ruleIndex+1)
}
```

## E. `internal/convert`: `advance()`の再構成

トップレベルの1行マッチも`tryCandidates(line, 0)`を使うようにして重複を排除する。EOF分岐は、`finalizeEntry`のリトライが新しい`c.open`を作り直す可能性に対応するため、`cand == nil`なら`return`せず`continue`する(ループが自然にもう一度EOF分岐へ入り、新しいエントリを即finalizeする。`ruleIndex`が単調増加するため必ず停止する):

```go
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

## 既存コードへの影響

### `internal/parse/match.go`
- `MatchAndConvertFrom`を追加(A節)。`MatchAndConvert`はそのラッパーに変更。

### `internal/convert/merge.go`
- `openEntry`に`ruleIndex`を追加。
- `fileCursor.pending`を`[]scannedLine`に変更、`pushPending`を追加。
- `tryCandidates`を新設(C節)。
- `finalizeEntry`を再構成(D節)。
- `advance()`を再構成(E節)。

### `README.md`
- 236行目「複数行エントリの型変換失敗は従来通りフォールバックなしでunmatched」という記述を、新しい挙動(先頭行のみ次候補にフォールバック、2行目以降は独立して再マッチ)に更新する。

### テスト
- `internal/parse/match_test.go`: `MatchAndConvertFrom`が`startIndex`未満の候補を無視することの単体テスト。
- `internal/convert/merge_test.go`:
  - 「continuationエントリの型変換失敗はフォールバックなしでunmatched split」という既存テスト(2026-08-09の設計で追加)を、新しい挙動(先頭行だけ次候補にフォールバック)に合わせて書き換える。
  - continuationエントリが変換失敗 → 次の候補(非continuationルール)にフォールバックして成功するケース。
  - continuationエントリが変換失敗 → 次の候補も別のcontinuationルールで、2行目以降が正しく新エントリに引き継がれるケース。
  - 全候補が尽きて先頭行のみunmatchedになり、2行目以降が独立して別ルールにマッチする/unmatchedになるケース。
  - EOFちょうどでの多段リトライ(2行目以降が存在しない状態で複数回finalizeEntryが再試行されるケース)。

## 非対象・将来課題

- 候補ルール探索の計算量は最悪`O(ルール数)`/エントリ(単一行ルールのフォールバックと同じオーダー)。極端にルール数が多い設定でのパフォーマンスは対象外。
