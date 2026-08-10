# rules.yamlのscaffoldコマンドとJSON Schema

## 背景・目的

`rules.yaml`は`name`/`pattern`/`fields`のほか、`structured`/`continuation`/`preset`/`replace`/`normalize`/`key`/`extra`など機能が増えてきており、ゼロから書き始めるユーザーが迷いやすい。

- 最小構成の雛形を一発で出力できる`scaffold`コマンドを追加する。
- エディタ(VSCodeのyaml-language-server等)が補完・型チェックに使えるJSON Schemaを提供する。

## スコープ

- `scaffold`は最小構成(1ルール分の`name`/`pattern`/`fields`)の雛形のみを出力する。機能網羅サンプルや入力ログからの推定機能は対象外。
- 出力先は標準出力のみ(`logidx scaffold > rules.yaml`)。ファイルへの直接書き込みオプションは対象外。
- JSON Schemaは手書きの静的ファイルとして作成する。`rules.yaml`の実際のデコードロジック(`Field`のmap+shorthand形式、`structured`の手動パースなど)は`Field`/`Rule`のGo構造体タグから単純に反射生成できないため。
- JSON Schemaは構文・型レベルの支援に留める。フィールド間の意味的な整合性(例: `field`名がpatternの名前付きキャプチャグループと対応しているか、`key`/`extra`使用時に`structured`が設定されているか)は対象外 — これらは`rules.Validate`が担う。
- JSON Schemaと`rules.Validate`のドリフトを自動検知する仕組みは追加しない。バリデーションルールを追加・変更する際は`schema/rules.schema.json`も手動で見直す運用とする。

## A. `logidx scaffold`コマンド

既存の`import`/`info`/`cat`/`dump`/`restore`と同じフラットなサブコマンドとして追加する。引数・フラグは持たない。

```
logidx scaffold > rules.yaml
```

### テンプレートの持ち方

新設する`internal/scaffold`パッケージに、実ファイル`internal/scaffold/template.yaml`を`//go:embed`で埋め込む(Go文字列リテラルではなく実際のYAMLファイルとして持つことで、`rules.Load`にそのまま通せることをテストで保証できる)。

```go
package scaffold

import _ "embed"

//go:embed template.yaml
var Template string
```

### テンプレート内容

1ルール分の最小構成に、各キーの意味を説明するコメントを添える。`structured`/`continuation`/`preset`/`replace`/`normalize`/`key`/`extra`などの高度な機能は本文に含めず、コメントでREADME参照に留める。

```yaml
# logidx rules.yaml
#
# Each rule matches one log line (or one continuation-joined entry) against a
# regexp pattern, and defines how each captured value is typed/converted.
# See README.md for the full set of available options.
rules:
  - name: example           # matched lines go to <name>.parquet
    pattern: '^(?P<time>\S+) (?P<message>.*)$'  # named capture groups map to fields
    fields:
      time:
        type: timestamp      # string / int / float / timestamp
        format: iso8601       # required when type is timestamp
      message: string         # type-only shorthand: "field_name: type"
```

### テスト

`internal/scaffold`に、埋め込んだ`Template`を一時ファイルに書き出し`rules.Load`が成功することを確認するテストを追加する(構文が壊れていないことの保証。コメントの正確さまでは検証しない)。

## B. JSON Schema

### 配置

- `schema/rules.schema.json`(JSON Schema 2020-12)を新規追加。
- CLIからも取得できるよう`logidx schema`コマンドを追加し、`go:embed`で同じファイルを埋め込んで標準出力に書く(ファイル本体とCLI出力が同一内容であることを`go:embed`の性質上保証できる)。

### スキーマの構造

トップレベル(`rules.Config`に対応):

- `rules`(必須・配列・1件以上)
- `compression`(任意・オブジェクト)
  - `codec`: enum `uncompressed`/`snappy`/`gzip`/`brotli`/`zstd`/`lz4`
  - `level`: 任意の整数
- `row_group`(任意・オブジェクト)
  - `max_rows`: 任意の正整数

`rules[]`の各要素:

- `name`(必須・文字列)
- `preset`のみを使う形と、`pattern`+`fields`を使う形を`oneOf`で表現(`rules.Validate`の「presetとpattern/fieldsは相互排他」に対応)。
- `structured`(任意・オブジェクト): `source`(必須)/`format`(必須・文字列。json/ltsv/logfmt/preset名のいずれもあり得るためenumにはせず自由文字列とする)。
- `continuation`(任意・文字列)
- `fields`(オブジェクト・map。キーがフィールド名): 各値は`oneOf`
  - shorthand: 文字列、enum `string`/`int`/`float`/`timestamp`
  - フル形式: オブジェクト。`type`(必須・同enum)、`format`(任意・文字列)、`key`(任意・文字列)、`extra`(任意・真偽値)、`replace`/`normalize`(任意・配列)
    - `replace[]`/`normalize[]`の要素: `pattern`(必須・文字列)、`value`(必須・文字列)

### README更新

エディタ連携のため、`rules.yaml`先頭に付けるyaml-language-server用コメントの書き方を追記する:

```yaml
# yaml-language-server: $schema=./schema/rules.schema.json
```

## C. 既存コードへの影響

### `cmd/logidx/main.go`

- `newScaffoldCmd(stdout, stderr) *cobra.Command`を追加し、`root.AddCommand`に登録。
- `newSchemaCmd(stdout, stderr) *cobra.Command`を追加し、`root.AddCommand`に登録。

### 新設パッケージ

- `internal/scaffold`: `template.yaml` + `//go:embed`。
- JSON Schemaの埋め込みは新規パッケージを作らず、`cmd/logidx`パッケージ内で`schema/rules.schema.json`を直接`//go:embed`する(`newSchemaCmd`からのみ使う1ファイルのために別パッケージを起こす必要はない。既存の`internal/schema`パッケージ(Parquetスキーマ生成用)とは名前が紛らわしいため区別する: JSON Schemaファイルは常にフルパス`schema/rules.schema.json`または`rules.schema.json`のように参照し、`internal/schema`は触らない)。

### README.md

- `scaffold`/`schema`コマンドの使い方セクションを追加。
- yaml-language-server用の`$schema`コメントの書き方を追記。

## D. テスト

- `internal/scaffold`: テンプレートが`rules.Load`で読み込めることのテスト(A節)。
- `schema/rules.schema.json`: JSON構文として妥当であることの最低限のテスト(`encoding/json`でUnmarshalできることを確認する程度。JSON Schemaとしての妥当性検証・ドリフト検知はスコープ外)。
- `cmd/logidx/main_test.go`: `scaffold`/`schema`サブコマンドが標準出力に非空の内容を書くことのテスト(既存の他コマンドのテストと同水準)。

## 非対象・将来課題

- `scaffold`の出力先ファイル指定(`--out`)、既存ファイル上書き保護。
- 機能網羅サンプルの雛形、入力ログからの推定生成。
- JSON Schemaと`rules.Validate`のドリフト自動検知(schema.jsonでtestdataをバリデートするテストなど)。
- フィールド間の意味的整合性をJSON Schemaで表現すること(構造的に困難なため)。
