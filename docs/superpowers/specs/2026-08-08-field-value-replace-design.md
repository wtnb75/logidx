# フィールド値の部分文字列置換(`replace:`)設計

## 概要

標準出力をsyslog経由で収集しているようなログには、制御文字の8進エスケープ表記(例: `#015` = `\r`)やANSIカラーエスケープシーケンス(例: `\x1b[31m`)のような、値の一部にだけ混入するノイズが含まれることがある。行末に固定で出るならパターンの正規表現で除去できるが、値の途中や複数箇所に不定回数出現する場合、`pattern`(1本の正規表現)だけで書くのは煩雑になる。

既存の`normalize:`は「値全体がパターンにマッチしたら固定の値に置き換える」機能(例: `WARN`/`WARNING` → `WARN`)であり、値の一部だけを置換して残りを保持する用途には使えない。フィールド定義に、値の一部を正規表現で置換する新しい`replace:`を追加する。

## 目的

- フィールドの値からノイズ(制御文字エスケープ表記、ANSIエスケープシーケンス等)を、値の残り部分を保持したまま除去・置換できるようにする
- 既存の`normalize:`(値全体の正規化)とは独立した、別の概念として提供する
- 正規表現キャプチャ由来の値だけでなく、将来の拡張(構造化データの`key:`由来の値など)でも同じように効くようにする

## Non-goals

- ログ行全体(パターンマッチング前)に対する置換 — `replace:`はフィールド単位の値変換であり、`pattern`によるマッチング自体には影響しない。
- ANSIエスケープ除去などの名前付きプリセット(例: `sanitize: ansi`) — 汎用的な正規表現ベースの`replace:`で任意のパターンに対応できるため、専用プリセットは用意しない。
- `normalize:`の統合・置き換え — 「値の一部を置換」(`replace`)と「値全体を正規化」(`normalize`)は別概念のまま独立して提供する。

## 1. rules.yaml設定

```yaml
fields:
  message:
    type: string
    replace:
      - pattern: '#\d{3}'
        value: ''
      - pattern: '\x1b\[[0-9;]*m'
        value: ''
    normalize:
      - pattern: '(?i)^warn(ing)?$'
        value: WARN
```

- `rules.Field`に`Replace []ReplaceRule`(yaml `replace:`)を追加する。
- `ReplaceRule`は既存の`NormalizeRule`と同じ形で定義する: `ReplaceRule{Pattern string (yaml `pattern`), Replacement string (yaml `value`), Regexp *regexp.Regexp (yaml `-`)}`。YAML上のキー名を`normalize:`と揃えるため、Goのフィールド名は`Replacement`だがyamlタグは`value`のままにする。
- `replace`ルールは宣言順に適用され、前のルールの出力が次のルールの入力になる(チェイン)。`value: ''`を指定すればマッチした部分文字列を削除できる。`regexp.ReplaceAllString`はGo標準の機能であり、`$1`のようなキャプチャグループ後方参照も追加実装なしで使える。
- `replace`→`normalize`の順で適用する(ノイズを取り除いたクリーンなテキストに対して`normalize`のパターンマッチングを行うため)。

## 2. `convertValue`への組み込み

`internal/parse/convertvalue.go`の`convertValue`を拡張する:

```go
func convertValue(raw string, field rules.Field, now time.Time) (any, error) {
	replaced := raw
	for _, r := range field.Replace {
		replaced = r.Regexp.ReplaceAllString(replaced, r.Replacement)
	}

	normalized := replaced
	if len(field.Normalize) > 0 {
		normalized = applyNormalize(replaced, field.Normalize)
	}

	switch field.Type {
	// ...既存の型変換ロジック(normalizedを使う点は変更なし)
	}
}
```

この変更は`convertValue`という1点に閉じているため、正規表現キャプチャ由来の値でも、将来実装する構造化データ(`key:`)由来の値(別設計`2026-08-08-structured-log-field-extraction-design.md`)でも同じように効く — `convertValue`はどちらの経路でも最終的に呼ばれる共通の型変換関数であるため、統合作業は不要。

`rules.Load()`では、既存の`Normalize`パターンのコンパイルと同じループに`Replace`のコンパイルを追加する:

```go
for j := range field.Replace {
	rre, err := regexp.Compile(field.Replace[j].Pattern)
	if err != nil {
		return nil, fmt.Errorf("rule %q field %q: compile replace pattern: %w", cfg.Rules[i].Name, field.Name, err)
	}
	field.Replace[j].Regexp = rre
}
```

## エラーハンドリング・バリデーション

- **`replace`のパターンがコンパイルできない**: 起動時エラー(既存の`pattern`/`normalize`のパターンコンパイル失敗と同じ扱い)。
- **`replace`適用後の値が型変換に失敗する**(例: 置換で数字部分を消してしまい`int`型のパースに失敗): 既存の型変換失敗ロジックがそのまま働き、その行はunmatchedになる。`replace`専用の新しいエラー分類は追加しない。
- **`Config.Validate()`への追加は不要**: `replace`はフィールド単位の値変換であり、パターンの妥当性はコンパイル時点(`Load()`)で保証される。`normalize`が現在バリデーション項目を持たないのと同じ扱い。

## 影響範囲

- `internal/rules/rules.go`: `Field.Replace`/`ReplaceRule`追加、`Load()`でのコンパイル。
- `internal/parse/convertvalue.go`: `convertValue`の拡張(`replace`→`normalize`の順で適用)。
- `README.md`: `replace:`の書き方、`normalize:`との違いと適用順序、ANSIエスケープ除去の実例を追記。

## テスト方針

- `internal/parse`: `convertValue`の拡張部分の単体テスト — 単一`replace`ルールでの部分文字列削除、複数`replace`ルールが宣言順にチェインして適用されること、キャプチャグループ後方参照(`$1`)を使った置換、`replace`→`normalize`の適用順序(replaceの結果がnormalizeの入力になること)、`replace`未設定フィールドの既存挙動が変わらないこと(回帰)。
- `internal/rules`: `replace:`のYAMLデコード・コンパイル、不正なパターンが起動時エラーになることを確認。
- `cmd/logidx`または`internal/convert`: ANSIカラーエスケープシーケンスや`#015`のような制御文字エスケープ表記を含むサンプルログ行を使い、`replace:`で除去された値がParquetに書き込まれることを確認するEnd-to-endテスト。
- README: `replace:`の書き方、`normalize:`との違いと適用順序、ANSIエスケープ除去の実例を追記。
