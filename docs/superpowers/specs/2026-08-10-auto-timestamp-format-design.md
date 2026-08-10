# タイムスタンプ`format: auto` 設計

## 概要

`rules.yaml`のtimestampフィールド`format`に`auto`という特別な値を追加する。`auto`を指定すると、定義済みのレイアウト系プリセット(`iso8601`/`rfc2822`/`rfc822`/`clf`/`syslog`/`pylog`)を決められた順序で順に試し、最初にエラーなくパースできたものを採用する。書式が事前にわからない、または複数の書式が混在するログを扱うユーザーの手間を減らすのが目的。

本設計は[timestamp-format-presets-design](2026-08-07-timestamp-format-presets-design.md)で導入したプリセット機構の上に構築する。

## 目的

- タイムスタンプの正確な書式を事前に調べたり指定したりする手間を省く
- 複数の書式が混在するログ(例: 一部の行だけ書式が違う、複数ソースをマージした結果)でも`auto`一つで対応できるようにする

## Non-goals

- epoch系プリセット(`unix`/`unix_ms`/`unix_us`/`unix_ns`)を`auto`の候補に含めること。数値文字列はどの epoch 単位でもパース自体は成功してしまう(値の桁数だけでは単位を一意に判定できない)ため、layout系プリセットと混在させると誤判定のリスクが高い。epoch形式を使いたい場合は明示的にプリセット名を指定する
- strptime記法・生のGoレイアウト文字列を`auto`の候補に含めること。候補はレイアウト系プリセット6種に固定する
- 複数の候補が同時にマッチしうる場合の曖昧性解決。各プリセットは区切り文字・曜日名・月名の有無などで構造が大きく異なり、実用上は最初にマッチしたものを採用すれば十分とみなす

## `auto`の候補と優先順序

`format: auto`は、以下の6種のレイアウト系プリセットを**この順序**で試す(`iso8601`と`rfc3339`は同一レイアウトのためどちらか一方のみを候補に含める):

| 順序 | プリセット名 | Goレイアウト |
|---|---|---|
| 1 | `iso8601`(=`rfc3339`) | `2006-01-02T15:04:05.999999999Z07:00` |
| 2 | `rfc2822` | `Mon, 02 Jan 2006 15:04:05 -0700` |
| 3 | `rfc822` | `02 Jan 06 15:04 -0700` |
| 4 | `clf` | `02/Jan/2006:15:04:05 -0700` |
| 5 | `syslog` | `Jan _2 15:04:05` |
| 6 | `pylog` | `2006-01-02 15:04:05,999999999` |

年なしプリセット(`syslog`)がマッチした場合も、既存の年補完ロジック(`parseTimestampLayout`)がそのまま適用される。

## アーキテクチャ

### `ResolveFormat`での特別扱い

`internal/rules/timeformat.go`の`ResolveFormat`に、既知プリセット名・strptime・生レイアウトの判定より先に`format == "auto"`のチェックを追加する。`TimeFormat`構造体を拡張する:

```go
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
	// goroutines) - see convert package. nil for every non-auto format.
	// Exported (like Layout/EpochUnit) so internal/parse, a different
	// package, can read and update it.
	LastGood *int
}
```

`ResolveFormat("auto")`は、上表のレイアウトを順序通り格納した`Candidates`と、新規に確保した`LastGood`(初期値0を指すポインタ)を持つ`TimeFormat`を返す。`rules.Load()`がtimestampフィールドごとに`ResolveFormat`を呼ぶ既存の仕組みにより、フィールドごとに独立した`LastGood`が割り当てられる。

### パース時の候補探索

`internal/parse/timestamp.go`の`parseTimestamp`に、`Candidates`が設定されている場合の分岐を追加する:

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
```

`parseTimestampAuto`は、まず`*tf.LastGood`が指す候補を試し、成功すればそれを返す。失敗した場合は残りの候補を元の順序で(前回成功したものを除いて)順に試し、成功した時点で`*tf.LastGood`をそのインデックスに更新して返す。全候補が失敗した場合はエラーを返す(個々の失敗理由は列挙せず、「どのフォーマットにもマッチしなかった」という趣旨のメッセージにする — 既存のパースエラーメッセージの簡潔さに合わせる)。

### 並行処理との関係

現状、ログ行の変換処理(`internal/convert`)はgoroutineを使わずシングルスレッドで行われている(複数入力ファイルのマージはヒープによる逐次処理)。`LastGood`はポインタ経由で共有される可変状態だが、排他制御なしで安全なのはこの前提があるため。将来この処理が並行化される場合は、`LastGood`の更新をアトミックにするか、フィールドごとにロックを設ける必要がある — コード内にコメントで明記する。

## バリデーション(起動時)

既存の「timestamp型なのに`format`が空はエラー」チェックはそのまま維持する(`"auto"`は空文字ではないので通過する)。`auto`自体の妥当性は`ResolveFormat`が常に成功する(候補リストは固定・ハードコードされているため失敗しようがない)ので、追加のバリデーションは不要。

## 後方互換性

- `auto`は新しい予約語として扱われるが、既存の`presetLayouts`/`presetEpochUnits`のどちらにも`"auto"`というキーは存在しないため、既存の`rules.yaml`の挙動に影響しない
- `auto`は判定ルールの一番手前でチェックするが、他の判定(プリセット名/strptime/生レイアウト)より優先されるのは`"auto"`という文字列そのものだけであり、既存のプリセット名や生のGoレイアウト文字列が誤って`auto`扱いされることはない

## テスト方針

- **`internal/rules`**: `ResolveFormat("auto")`が期待する`Candidates`(6要素、順序通り)と非nilな`LastGood`を返すことをテーブル駆動テストで検証
- **`internal/parse`**:
  - `auto`指定のフィールドが、6種類のプリセット形式それぞれの値を正しくパースできること
  - 一度成功した書式が次回以降優先して試されること(例: 最初に`clf`形式の値でパースし、次に別の`clf`形式の値を渡したとき、他の候補を試さず一発で成功する — カウンタやモックで呼び出し回数を検証するのではなく、キャッシュ済みインデックスが正しく先頭で使われることを、意図的に他候補と誤マッチしうる値を混ぜて確認する形で検証)
  - どのプリセットにもマッチしない値でエラーになること
  - 年なしプリセット(`syslog`)が`auto`経由でマッチした場合も年補完ロジックが機能すること
- **README**: `format: auto`の説明・候補プリセットと優先順序・epoch系が対象外である理由を追記する
