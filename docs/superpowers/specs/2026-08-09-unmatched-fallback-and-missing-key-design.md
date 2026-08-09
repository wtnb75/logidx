# unmatched判定のフォールバック統一とmissing-key検出

## 背景・目的

現状、行がルールの正規表現にマッチした後、以下のケースは**フォールバックなしで即unmatchedに落ちる**:

- フィールドの型変換失敗（`int`/`float`/`timestamp`のパースエラー）
- `structured:`データ自体のパース失敗（不正なJSON/LTSV/logfmt）
- `structured:`データに`field.Key`で指定したキーが存在しない場合 — これは現状**エラーとして検出すらされていない**。`internal/parse/match.go`の`Convert`が`structuredValues[field.Key]`をmapの通常参照で読んでおり、キーが無ければGoのゼロ値`""`が黙って使われる。フィールドが`string`型ならそのまま空文字列として変換成功してしまい、意図しないマッチが成立する。

`internal/parse/match.go`の`Match`関数のdocコメントが明言する通り、これは意図された設計だった:

> If that rule's pattern matches but any field fails type conversion, the line is treated as unmatched (ok=false) — there is no fallthrough to subsequent rules, since "first match" refers to the regex match, not to conversion success.

この設計を変更し、**エラーは直接unmatchedに行くのではなく次の候補ルールを試す。試す候補が尽きた行だけがunmatchedに落ちる**ようにする。あわせて、structuredのmissing-keyもエラーとして検出する。

## スコープ

- 対象は**単一行ルール（`continuation`未設定）のみ**。
- `continuation`（複数行）ルールは対象外、現状維持とする。理由: `continuation`ルールは先頭行の正規表現マッチ時点でストリーミングスキャナが以降の行をそのルールの継続パターンで消費し始めており、変換失敗が判明する時点（エントリのfinalize時）では既に複数行を消費し終えている。ここから「次のルールを試す」には消費済みの行を巻き戻して別ルールの継続パターンで再解釈する必要があり、`fileCursor`の前方向スキャン構造と相性が悪い。この拡張は将来の別課題とする。

## A. missing-keyの検出

`internal/parse/match.go`の`Convert`内、`field.Key != ""`の分岐（現状67行目）をcomma-okで書き換える:

```go
case field.Key != "":
    v, ok := structuredValues[field.Key]
    if !ok {
        return nil, fmt.Errorf("structured data missing key %q", field.Key)
    }
    rawValue = v
```

これにより「structuredにキーがない」は型変換失敗や`structured:`パース失敗と同じ「`Convert`のerror」に統一される。B節のフォールバック機構に特別扱いなく乗る。

## B. マッチング処理の再構成

`internal/parse/match.go`に新関数`MatchAndConvert`を追加する。単一行ルールに限り「マッチ→即`Convert`→失敗したら次の候補ルールへ」という1つのループにまとめる。

```go
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
func MatchAndConvert(ruleList []rules.Rule, line string, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool)
```

戻り値の意味:

- `ok=true, values!=nil`: 単一行ルールが変換まで成功。呼び出し側はそのまま書き込める。
- `ok=true, values==nil`: continuationルールがマッチ（先頭行のcaptureのみ、`raw`に格納）。呼び出し側が継続行を蓄積し、従来通り`finalizeEntry`で`Convert`する（失敗時はフォールバックなしでunmatched、現状維持）。
- `ok=false`: どの候補も成立せず。`attempts`に「正規表現はマッチしたが変換に失敗した単一行ルール」の一覧（ルール名+エラー）が入る（C節で使用）。

既存の`MatchRaw`（正規表現マッチのみ）は`MatchAndConvert`の内部実装に吸収するか、regexマッチ単体のヘルパーとして残すかは実装時に判断する。外部から見える契約は`MatchAndConvert`に一本化する。

既存の`Match`関数（`MatchRaw`+`Convert`の薄いラッパー、本体コードでは未使用でテストのみ使用）は`MatchAndConvert`と役割が重複するため削除する。

## C. デバッグログ

`parse`パッケージはロガーを持たない現在の設計を維持する（純粋関数のまま）。`MatchAndConvert`が`ok=false`を返した際、呼び出し側（`internal/convert/merge.go`）が`attempts`の各要素をDebugログに出してから、既存の「どのルールにもマッチしなかった」ログを出す。

途中で候補ルールが変換に失敗して次に進んだが最終的にはどこかのルールで成功したケースでは、ログは出さない。最終的にunmatchedになったときだけ、「どの候補が何のエラーで落ちたか」をまとめて出す。

## D. 既存コードへの影響

### `internal/convert/merge.go`

- `finalizeEntry`の「変換成功後の書き込み処理」（マージキー判定・`WriteMatched`・candidate生成の部分）を`writeConverted(name string, values map[string]any, startLine int) (*candidate, error)`として切り出す。`finalizeEntry`はこれを呼ぶ薄いラッパーのまま、continuationエントリ用（`open`がfinalizeされる時点でConvertし、失敗時は現状通りフォールバックなしでunmatched split）に残す。
- `advance()`の単一行マッチ部分を`parse.MatchAndConvert`呼び出しに置き換える。`values != nil`（単一行ルールが変換まで成功）なら二重変換を避けて直接`writeConverted`を呼ぶ。`values == nil`（continuationルールがマッチ）なら現状通り`c.open`に積む。`ok == false`なら`attempts`をDebugログに出してunmatched書き込み。

### `internal/parse/match.go`

- `Convert`にmissing-keyチェックを追加（A節）。
- `MatchAndConvert`を新設（B節）。
- `Match`関数を削除。

### テスト

- `internal/parse/match_test.go`: `Match`呼び出し3箇所（型変換成功、無マッチ、型変換失敗のケース）を`MatchAndConvert`呼び出しに書き換える。加えて新規に以下のケースを追加する:
  - 1つ目のルールが型変換失敗、2つ目のルールがマッチして成功する
  - structuredキーがなくフォールバックが働く
  - 全候補が失敗してunmatchedになる（`attempts`の内容も検証）
- `internal/convert/merge_test.go`: 既存のcontinuationエントリの型変換失敗テスト（フォールバックなしでunmatched split）はそのまま現状維持を確認するテストとして残す。新たに単一行版の「1つ目のルールが変換失敗し2つ目のルールが採用される」ケースを追加する。

## 非対象・将来課題

- continuationルールへのフォールバック拡張（前述の通り、行の巻き戻しが必要で構造的に大きな変更になるため今回は対象外）。
