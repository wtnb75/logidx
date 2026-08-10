# マッチ行にソースファイル名・行番号を保存する `meta` フィールド

## 背景・目的

`logidx import`は複数の入力ファイルを1つのParquet出力にマージするが、マッチした行(Parquet出力)には、その行がどの入力ファイルの何行目に由来するかという情報が残らない。`unmatched.txt`側は既に`<source>\t<lineNum>\t<raw>\n`形式でこれを保存している(`internal/writer.Set.WriteUnmatched`)が、マッチしてParquetに書かれる行にはこの情報を持たせる手段がない。

インポート時に元ファイル名・行番号をParquetのカラムとして保存できるようにする。

## スコープ

- 対象はマッチした行(Parquet出力)のみ。`unmatched.txt`は現状のまま変更しない。
- ルールごとのオプトイン方式とする。既存の`structured:` + `Field.Key`/`Field.Extra`(値の取得元をフィールド単位で宣言的に切り替える仕組み)と同じパターンで拡張する。全ルール自動付与にはしない — 不要なルールにまで固定カラムが増えるのを避け、既存の出力スキーマにも影響を与えない。
- カラム名はユーザーが`fields:`のキー名で自由に決める(例のような`log_file`/`log_line`に限らない)。

## A. `rules.yaml`のAPI: `Field.Meta`

`Field`に新しい文字列属性`meta`を追加する。値は`source_file`または`source_line`のいずれか。

```yaml
rules:
  - name: access
    pattern: '^(?P<remote>\S+) (?P<msg>.*)$'
    fields:
      remote: string
      msg: string
      log_file:
        type: string
        meta: source_file
      log_line:
        type: int
        meta: source_line
```

- `meta: source_file` → `type: string`必須。値は入力パス(`-`はstdinのまま、`unmatched.txt`と同じ表記)。
- `meta: source_line` → `type: int`必須。値はその行の1始まりの行番号(継続行を束ねるルールの場合はエントリの先頭物理行番号)。
- `replace`/`normalize`は`meta`フィールドにもそのまま適用できる(例: フルパスからファイル名だけ取り出す正規表現置換)。既存の値解決パイプライン(`convertValue`)をそのまま通すため、特別扱いする必要がない。
- `internal/rules.Field`に定数を追加: `FieldMetaSourceFile = "source_file"`, `FieldMetaSourceLine = "source_line"`。

## B. `parse.SourceMeta`とシグネチャ拡張

`internal/parse`パッケージに新しい型を追加する:

```go
// SourceMeta carries the input file's path and the current line number, for
// fields declared with meta: source_file / meta: source_line.
type SourceMeta struct {
    File string
    Line int
}
```

`Convert`/`MatchAndConvert`のシグネチャに`source SourceMeta`パラメータを追加する:

```go
func Convert(rule rules.Rule, raw map[string]string, source SourceMeta, now time.Time) (values map[string]any, err error)
func MatchAndConvert(ruleList []rules.Rule, line string, source SourceMeta, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool)
```

`Convert`内、フィールドごとの値解決スイッチ(`field.Extra`/`field.Key`の分岐と同列)に追加する:

```go
switch {
case field.Meta == rules.FieldMetaSourceFile:
    rawValue = source.File
case field.Meta == rules.FieldMetaSourceLine:
    rawValue = strconv.Itoa(source.Line)
case field.Extra:
    ...
case field.Key != "":
    ...
}
```

その後は既存の`convertValue(rawValue, field, now)`にそのまま流れ、型変換(`int`ならここで`strconv.ParseInt`)・replace・normalizeが適用される。

## C. バリデーション(`internal/rules/validate.go`)

既存の`usesStructured := field.Key != "" || field.Extra`の判定に`field.Meta != ""`を合流させ(`usesDerived`のような名前にリネーム)、キャプチャグループ必須チェックをスキップする対象に加える。加えて以下を追加する:

- `field.Meta`が空文字列/`source_file`/`source_line`以外ならエラー("unsupported meta value")。
- `meta: source_file`なのに`type != "string"`、`meta: source_line`なのに`type != "int"`ならエラー。
- `field.Meta != ""`と`field.Key != ""`または`field.Extra`の併用はエラー(値の取得元は1つだけ)。
- continuationパターンが`meta`フィールドと同名のキャプチャグループを持つ場合はエラー(既存の`Key`/`Extra`と同じ理由・同じチェック箇所に`field.Meta != ""`を追加)。

## D. 既存コードへの影響

### `internal/rules/rules.go`

- `Field`に`Meta string \`yaml:"meta"\``を追加。
- `FieldMetaSourceFile`/`FieldMetaSourceLine`定数を追加。

### `internal/rules/validate.go`

- C節のチェックを追加。

### `internal/parse/match.go`

- `SourceMeta`型を追加。
- `Convert`/`MatchAndConvert`に`source SourceMeta`引数を追加し、B節のswitch分岐を追加。

### `internal/convert/merge.go`

- `finalizeEntry`の`parse.Convert`呼び出しに`parse.SourceMeta{File: c.inputPath, Line: entry.rawLines[0].lineNum}`を渡す(継続エントリの先頭行を採用)。
- `advance()`の`parse.MatchAndConvert`呼び出しに`parse.SourceMeta{File: c.inputPath, Line: line.lineNum}`を渡す。

### `README.md`

- `meta`フィールドの説明とサンプルを、`key`/`extra`の説明の近くに追記する。

## E. テスト

- `internal/rules/validate_test.go`: C節の各バリデーションルールの単体テスト(型不一致、key/extraとの併用、continuationとの衝突)。
- `internal/parse/match_test.go`: `SourceMeta`を渡した`Convert`/`MatchAndConvert`が`meta: source_file`/`meta: source_line`のフィールドに正しい値(ファイルパス・行番号)を詰めることのテスト。既存の全呼び出し箇所は新しい引数を渡す形に更新する。
- `internal/convert/merge_test.go`: 複数入力ファイル・継続行ありのケースで、出力Parquetの`meta`カラムが実際の入力ファイルパス・正しい行番号(継続エントリでは先頭行番号)になっていることを検証する結合テストを追加する。

## 非対象・将来課題

- `unmatched.txt`のフォーマット変更は対象外(既にファイル名・行番号を持っている)。
- 全ルール自動付与のオプトアウト方式は対象外(スコープ節参照)。
- `meta`の値の種類は`source_file`/`source_line`の2つのみ。将来的にルール名や更新日時などを追加する余地はあるが、今回は最小構成とする。
