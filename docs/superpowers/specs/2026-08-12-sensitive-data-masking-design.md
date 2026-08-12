# import時の機微情報マスク 設計

## 概要

`logidx import`で生成したParquetを配布・共有する際、ログに含まれる機微情報(パスワード・APIキー・メールアドレス等)をそのまま渡したくない。以下2種類のマスクに対応する:

- JSON/LTSV/logfmtとして構造化パースされたデータの、特定のキーの値をマスクする
- テキスト(構造化データの内容も含む)中の特定パターンにマッチする部分文字列をマスクする

## 目的

- 配布・共有前に、機微情報を機械的に除去・秘匿できるようにする
- キー名ベースのマスクは、`extra:`列に入る未マッピングのキーや、ネストしたJSONオブジェクト内のキーも対象にする(表記ゆれのあるキー名を正規表現で一括して捕まえられるようにする)
- パターンベースのマスクは、マッピング済みフィールド・`extra:`列・`unmatched.txt`のいずれにも同じ設定で効くようにする
- 固定文字列への置換(`redact`)と、決定的なハッシュ化(`hash`、衝突を許容し秘密鍵は使わない)の両方に対応する。同じ入力が常に同じマスク結果になることで、値そのものは隠しつつ行間の同一性相関やk-匿名化的なグルーピングを可能にする

## Non-goals

- 秘密鍵付きHMACによるハッシュ化 — 今回は「同じ入力は同じ出力になる」ことが目的であり、辞書攻撃への耐性(鍵管理の手間)は要件外。単純な`sha256`の切り詰めで十分とする。
- 部分マスク(値の一部を残して伏字にする、クレジットカード下4桁のみ残す等) — `redact`(固定文字列で完全置換)と`hash`(決定的ハッシュ化)の2方式で十分とし、部分マスクは追加しない。
- ルール単位での個別`mask:`定義 — 全ルール共通のグローバル設定のみとし、ルールごとの再定義には対応しない。
- ネストしたJSONオブジェクト内へのキーパス指定(`key: listen.Port`のような入れ子アクセス) — マスク対象はキー名の正規表現マッチのみで、パス指定はしない(同名のキーは深さを問わず一律マスクされる)。
- `logidx import`以外の独立した前処理コマンド(`logidx mask`等)としての提供 — マスクはimportパイプラインに統合する。
- メールアドレス・クレジットカード番号などの組み込みプリセットパターン — `replace:`が汎用正規表現のみでプリセットを持たない(`2026-08-08-field-value-replace-design.md`のNon-goals参照)のと同じ方針で、`type: pattern`も任意の正規表現をユーザーが書く形のみ提供する。

## 1. rules.yaml設定

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

rules:
  - name: container_log
    pattern: '^(?P<time>\S+) (?P<host>\S+) (?P<tag>\S+) (?P<json>\{.*\})$'
    structured:
      source: json
      format: json
    fields:
      # ...(既存通り)
```

- `mask:`はrules.yamlのトップレベルに1つ、全ルール共通のグローバル設定として置く。`rules.Config`に`Mask []MaskRule`(yaml `mask`)を追加する。
- 各エントリの構造:
  - `type`(string, 必須): `key` または `pattern`。
  - `pattern`(string, 必須): 正規表現。`type: key`ならキー名に、`type: pattern`なら値の内容にマッチさせる。
  - `action`(string, 必須): `redact` または `hash`。
  - `value`(string, `action: redact`のとき使用): マッチ箇所を置き換える固定文字列。空文字列も許容する(マッチ箇所を削除する意味になり、`replace:`の`value: ''`と同じ扱い)。
  - `length`(int, `action: hash`のとき必須): SHA-256の16進ダイジェストを先頭何文字に切り詰めるか(1〜64)。
- `MaskRule`のGo定義: `replace:`/`normalize:`と同じパターンで、`Pattern string`(yaml `pattern`)、`Regexp *regexp.Regexp`(yaml `-`、`Load()`でコンパイル)を持つ。
- **`type: key`**: 構造化データ(`structured:`でパースされたJSON/LTSV/logfmt)の**キー名**に`pattern`をマッチさせる。マッチしたキーの値をまるごと`action`の結果に置き換える。JSONはネスト・配列内オブジェクトも深さを問わず再帰的に対象になる。LTSV/logfmtはフラットな形式のため、トップレベルのキーのみが対象になる。`structured:`が設定されていないルールでは(構造化データ自体が存在しないため)発火しない。
- **`type: pattern`**: 最終的な文字列値の**内容**に`pattern`をマッチさせ、マッチした部分文字列だけを`action`で置き換える(`replace:`と同じ「値の一部を保持したまま置換」の考え方)。適用対象は次の3箇所すべて:
  - `type: string`のフィールド値(通常フィールド・`extra:`フィールドの両方。`extra:`は常に文字列型のJSONなので自然に対象になる)
  - `unmatched.txt`に書き込まれる、どのルールにもマッチしなかった生ログ行
  - (`type: string`以外のフィールド — int/float/timestamp — には適用しない。意図しない型変換失敗を避けるため)
- **`action: redact`**: マッチ箇所を固定文字列`value`で完全に置き換える。
- **`action: hash`**: マッチ箇所(`type: key`なら値全体、`type: pattern`ならマッチした部分文字列)をSHA-256でハッシュ化し、16進ダイジェストの先頭`length`文字に置き換える。秘密鍵は使わない — 同じ入力は常に同じ短いハッシュ値になるため、元の値は分からないまま、行間の同一性(同じユーザーかどうか等)や大まかなグルーピングは維持できる。
- `mask:`のエントリは宣言順にすべて適用される(チェイン)。複数の`type: key`ルールが同じキーにマッチする場合、複数の`type: pattern`ルールが同じ値にマッチする場合は、どちらも宣言順に前段の出力が次段の入力になる。

## 2. 実装への組み込み

### `internal/rules`(設定)

```go
type MaskRule struct {
    Type    string         `yaml:"type"`
    Pattern string         `yaml:"pattern"`
    Regexp  *regexp.Regexp `yaml:"-"`
    Action  string         `yaml:"action"`
    Value   string         `yaml:"value,omitempty"`
    Length  int            `yaml:"length,omitempty"`
}
```

`Config.Mask []MaskRule`(yaml `mask`)を追加する。`Load()`で既存の`replace`/`normalize`のパターンコンパイルと同じループで`Mask[i].Pattern`をコンパイルする。`Config.Validate()`で以下をチェックする(いずれも起動時エラー):

- `Type`が`key`/`pattern`のいずれかであること
- `Action`が`redact`/`hash`のいずれかであること
- `Action == "hash"`のとき`1 <= Length <= 64`であること

### `internal/parse`(新規 `mask.go`)

- `applyKeyMaskJSON(tree any, keyRules []rules.MaskRule) any` — JSONデコード後の`any`木(`map[string]any`/`[]any`/`json.Number`/`string`/`bool`/`nil`)を深さを問わず再帰的に歩く。`map[string]any`のキーが`keyRules`のいずれかの`Pattern`にマッチしたら、そのキーの値を`action`の結果(常に文字列)に差し替える。マッチする`keyRules`が複数あれば宣言順にチェイン適用する。
- `applyKeyMaskFlat(m map[string]string, keyRules []rules.MaskRule)` — LTSV/logfmt用。トップレベルキーのみを対象に同じロジックをin-placeで適用する。
- `applyPatternMask(s string, patternRules []rules.MaskRule) string` — `patternRules`を宣言順にチェイン適用する。`action: redact`は`Regexp.ReplaceAllString(s, rule.Value)`。`action: hash`は`Regexp.ReplaceAllStringFunc`でマッチした部分文字列ごとに`hashTrunc`を適用する。
- `hashTrunc(s string, length int) string` — `sha256.Sum256([]byte(s))`の16進ダイジェストを先頭`length`文字に切り詰めて返す。

### `ParseStructured`の変更(`internal/parse/structured.go`)

シグネチャに`keyRules []rules.MaskRule`を追加する(呼び出し元が`Config.Mask`から`Type == "key"`のものだけ抽出して渡す)。

- **json**: 現状は「トップレベルのキーだけ見て、値がオブジェクト/配列ならそのままre-marshalする」実装だが、これを「`json.Decoder`(`UseNumber()`)で`any`にフルデコード → `applyKeyMaskJSON`でマスク(in-place) → トップレベルの各キーを文字列化(オブジェクト/配列は`json.Marshal`、プリミティブは既存通りの文字列化)」という流れに変更する。これにより、`key:`マッピングされるフィールドも`extra:`列(`marshalUnconsumed`)も、マスク済みの値を使うことになる。
- **ltsv/logfmt**: 既存のパース直後に`applyKeyMaskFlat`を適用するだけ(元々フラットなため木を辿る変更は不要)。

### `Convert`/`convertValue`の変更(`internal/parse/match.go`, `convertvalue.go`)

`Convert`のシグネチャに`patternRules []rules.MaskRule`を追加する(呼び出し元が`Config.Mask`から`Type == "pattern"`のものだけ抽出して渡す)。`convertValue`内で、`replace`→`normalize`の適用後・型変換の前に、**`field.Type == "string"`のときだけ**`applyPatternMask(normalized, patternRules)`を適用する。

### `unmatched.txt`書き込み(`internal/convert/merge.go`の`writeUnmatchedLine`)

`fileCursor`が`Config.Mask`から抽出した`patternRules`を保持し、`writeUnmatchedLine`で行を書き込む直前に`applyPatternMask`を通す。

## 3. エラーハンドリング

- **`mask:`のパターンがコンパイルできない**: 起動時エラー(既存の`pattern`/`replace`/`normalize`のパターンコンパイル失敗と同じ扱い)。
- **`type`/`action`の不正値、`hash`の`length`範囲外**: `Config.Validate()`で起動時エラー。
- **マスク後の値が型変換に失敗する**(例: `type: key`マスクで本来`int`型にマッピングされるキーの値をhash文字列に置換してしまった): 既存の型変換失敗ロジックがそのまま働き、その行はunmatchedになる。マスク専用の新しいエラー分類は追加しない(`replace:`/`structured:`と同じ方針)。
- **構造化データ自体のパース失敗**: 既存通り(マスク処理より前の段階のため無関係)。

## 4. 影響範囲

- `internal/rules/rules.go`: `MaskRule`型、`Config.Mask`追加、YAMLデコード、`Load()`でのパターンコンパイル
- `internal/rules/validate.go`: `type`/`action`/`length`のバリデーション追加
- `internal/parse/mask.go`(新規): `applyKeyMaskJSON`/`applyKeyMaskFlat`/`applyPatternMask`/`hashTrunc`
- `internal/parse/structured.go`: `ParseStructured`のシグネチャ変更、JSON処理を全木デコード+再帰マスク方式に変更
- `internal/parse/match.go`: `Convert`のシグネチャ変更(`patternRules`追加)
- `internal/parse/convertvalue.go`: `convertValue`へのpatternマスク組み込み(`type: string`フィールドのみ)
- `internal/convert/merge.go`: `writeUnmatchedLine`へのpatternマスク組み込み、`fileCursor`への`patternRules`配線
- `schema/rules.schema.json`: `mask:`のJSON Schema追加
- `README.md`/`docs/reference.md`: `mask:`の書き方・意味論・具体例の追記

## 5. テスト方針

- `internal/rules`: `mask:`のYAMLデコード・コンパイル。`type`/`action`の不正値、`hash`アクションでの`length`範囲外(0、65以上)がそれぞれ起動時エラーになることを確認。
- `internal/parse`:
  - `applyKeyMaskJSON`: トップレベルキーのマッチ、ネストしたオブジェクト内のキーのマッチ、配列内オブジェクトのキーのマッチ、複数`type: key`ルールがチェイン適用されること
  - `applyKeyMaskFlat`: LTSV/logfmtのトップレベルキーへのマッチ
  - `applyPatternMask`: `redact`/`hash`それぞれの部分文字列置換、複数`type: pattern`ルールのチェイン適用
  - `hashTrunc`: 同一入力が同一出力になること(決定性)、`length`で指定した文字数になること
  - `ParseStructured`拡張: `type: key`マスクが`key:`マッピング済みフィールドと`extra:`列の両方に効くこと、ネストしたJSON内のキーにも効くこと
  - `Convert`/`convertValue`拡張: `type: pattern`マスクが`type: string`フィールドと`extra:`列に効き、`int`/`timestamp`等の非stringフィールドには適用されないこと
- `internal/convert`: `unmatched.txt`書き込みで`type: pattern`マスクが効くことを確認するEnd-to-endテスト
- 回帰: `mask:`未設定のrules.yamlでの既存挙動(パース結果・Parquet出力・unmatched.txt)が変わらないこと
- README/docs: `mask:`の書き方、`type: key`/`type: pattern`の違い、`redact`/`hash`の挙動、具体例(パスワード除去・メールアドレスのハッシュ化等)を追記。
