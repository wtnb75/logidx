# 複数ファイルのタイムスタンプ順マージ / row group分割制御 設計

## 概要

`logidx import`(`internal/convert.Files`)は現在、複数の入力ファイルを渡された場合、各ファイルを最初から最後まで順番に丸ごと処理してから次のファイルに進む。各入力ログファイル自体はほぼ時系列順に書かれているが、ファイルをまたいだ全体では時系列順が保証されない(ファイルA全体→ファイルB全体、という連結順になる)。

また、出力Parquetファイルのrow group分割は`parquet-go`のデフォルト(無制限、実質1ファイル=1 row group)のままで、明示的な制御手段がない。

この2つを改善する:

1. 複数入力ファイルを、ルールのtimestampフィールド値でグローバルに時系列順マージしてから書き込む
2. row groupの行数上限(`MaxRowsPerRowGroup`)をCLIフラグ/configファイルで指定可能にする

## 目的

- 複数ログファイルをマージする運用で、出力Parquetのtimestamp列がグローバルに単調増加に近づき、圧縮率(RLE/辞書エンコーディングの効き)とクエリ性能(row group min/maxによるpredicate pushdown)が改善する
- row group単位を利用側で調整できるようにし、読み出し側の並列度やpruning粒度をチューニングできるようにする

## Non-goals

- メモリに乗らない規模の入力に対する外部ソート(スピル)。入力ファイルは既にほぼソート済みという前提のため、ストリーミングマージで十分とする
- row groupの目標バイトサイズを直接指定する機能。`parquet-go`は行数のみを制御手段として提供しており(圧縮後サイズは事前に予測できないため)、バイトサイズはユーザー側で行数から逆算する運用とする
- timestamp型以外のフィールドをマージキーにすること(型が異なるルール間の比較が曖昧になるため対象外)
- プロセスのオープンファイル記述子数上限を超えないための対策。N個のファイルをマージするにはN個のファイル記述子を同時にオープンし続ける必要があり、この制約に対する緩和策(バッチ処理・記述子プーリング等)は設けない

## 1. タイムスタンプ順マージ

### マージキーの決定方法

設定は不要。各ルールについて、`fields:`宣言順で最初に現れる`Type == "timestamp"`のフィールドを、自動的にそのルールのマージキーとする。timestamp型フィールドを1つも持たないルールは、マージ対象外とし、**現状通りファイル到達順のまま**書き込む(フォールバック)。

マージキーを持つルールが1つもない場合、または入力ファイルが1つしかない場合は、後述のアーキテクチャが自然に現在の動作(ファイルを順番に処理)へ縮退するため、動作を切り替えるフラグや分岐は設けない。

### アーキテクチャ: ファイルカーソル + 最小ヒープによるストリーミングk-wayマージ

`internal/convert.Files`内の「ファイルを順番に`processInput`する」ループを、以下に置き換える。

- **`mergeKeyField(rules []rules.Rule) map[string]string`**
  ルール名 → マージキーに使うフィールド名、のmapを1回だけ構築する(`rules.Load()`時にRegexpをコンパイルしてキャッシュする既存の設計と同じ考え方)。

- **`fileCursor`**
  入力ファイル1つにつき1つ生成する。`*bufio.Scanner`と現在の読み取り位置を保持する。

  - `advance() (cand *candidate, ok bool, err error)`
    次の行から読み進め、
    - マッチしない行 → 即`WriteUnmatched`し、読み進めを継続
    - マッチしたがマージキーを持たないルール → 即`WriteMatched`し、読み進めを継続
    - マッチしてマージキーを持つルール → その行を`candidate`(ルール名・値・`sortValue time.Time`)として返し、読み進めを停止する

    ファイル終端に達したら`ok=false`を返す。

  - `close() error`

- **`mergeFiles(inputPaths []string, cfg, set, logger, now) error`**
  各`inputPath`から`fileCursor`を作り、それぞれ`advance()`を1回呼んで得られた`candidate`を最小ヒープ(`container/heap`、キーは`sortValue`、同値時は入力ファイルの引数順をタイブレークに使い出力を安定させる)に積む。以降、
  1. ヒープから最小の`candidate`を取り出す
  2. `set.WriteMatched(cand.name, cand.values)`
  3. 取り出した候補の`cursor`に対して再度`advance()`を呼び、`ok`ならヒープに積み直す。`ok=false`ならそのカーソルを`close()`する
  4. ヒープが空になるまで繰り返す

  この設計により、各入力ファイルは1回シーケンシャルに読むだけで済み、メモリ上に保持するのは各ファイルの「現在の候補行」(最大で入力ファイル数ぶん)のみとなる。マージキーを持つルールが存在しない場合、ヒープには何も積まれず、全行が`advance()`内の即時書き込みで処理されるため、現在の「ファイルを順番に処理する」動作と同じ結果になる。

### エラーハンドリング

- 1ファイルの`open`/`scan`/`advance`エラーは、そのカーソルをヒープから除外し(以後そのファイルは進めない)、`close()`した上で他のファイルの処理を継続する。エラーは`errors.Join`で`Files()`の返り値に積まれる(現行の`processInput`と同じ「1入力の失敗で全体を止めない」方針を維持)。
- `set.Close()`は`Files()`の`defer`で従来通り必ず呼ばれ、一部ファイルが失敗しても書き込み済み分はflushされる。

### 影響範囲

- `internal/convert/convert.go`: `processInput`を`fileCursor.advance()`ベースの実装に置き換え、`Files()`のループを`mergeFiles`呼び出しに変更する。単一ファイル入力時の出力(行の内容・順序)は変わらない。

## 2. row group分割の制御

### 設計

`internal/compression`と対になる新規パッケージ`internal/rowgroup`を追加する。

```go
package rowgroup

type Settings struct {
    MaxRows *int64 `yaml:"max_rows" json:"max_rows,omitempty"`
}

func Resolve(cli, file Settings) Settings
func (s Settings) Validate() error
func (s Settings) Option() (parquet.WriterOption, bool)
```

- `Resolve`: `compression.Resolve`と同じくCLI > configファイル > デフォルトの優先度。ただし**デフォルトは`MaxRows == nil`(無制限)のまま**とする(既存出力ファイルの構造を暗黙に変えないため)。
- `Validate`: 指定時は`> 0`のみ許可。
- `Option`: `MaxRows`が`nil`なら`(nil, false)`を返す。`compression.Settings.WriterOption`と異なり常に有効な値を返せるわけではないため、呼び出し側(`writer.Set`)で戻り値の`bool`を見て`WriterOption`のスライスに条件付きで追加する。

### 設定・CLI連携

- `internal/rules.Config`に`RowGroup rowgroup.Settings \`yaml:"row_group"\``を追加。rules.yaml側:
  ```yaml
  row_group:
    max_rows: 500000
  ```
- `cmd/logidx/main.go`の`import`サブコマンドに、`--compression`/`--compression-level`と同じパターンで`--max-rows-per-row-group`フラグを追加する(`0`はunset扱い)。`rowgroup.Resolve`で解決した`Settings`を`writer.NewSet`に渡す。

### 影響範囲

- `internal/writer/writer.go`: `NewSet`のシグネチャに`rowgroup.Settings`を追加し、`writerFor`内で`parquet.NewGenericWriter`に渡す`WriterOption`のスライスに、設定されていれば`rowgroup.Option()`の結果を追加する。

## テスト方針

- `fileCursor.advance()`の単体テスト: マージキーありルール/なしルール/unmatched行が混在する入力で、保留候補と即時書き込みの振り分けが正しいこと
- `mergeFiles`のヒープ挙動:
  - 複数ファイル(時間帯が重なる場合・重ならない場合)を渡し、出力がグローバルにtimestamp昇順になること
  - マージキーを持たないルールの行は各ファイル内の出現順が保たれること
  - 1ファイルのみ・入力ファイル数0のケースで既存動作と出力が変わらないこと(回帰)
  - 1ファイルの読み込みエラーが他ファイルの処理を止めないこと
- `rowgroup.Resolve`/`Validate`/`Option`の単体テスト(`compression`の対応するテストと同じパターン)
- End-to-end(`cmd/logidx/main_test.go`): 複数ファイル入力 + `--max-rows-per-row-group`を組み合わせ、`pqinfo`で出力Parquetの`NumRowGroups`とtimestamp列の並び順が期待通りであることを確認する
- README: `row_group.max_rows`設定と複数ファイルマージの挙動(マージキーの自動決定ルール、フォールバック条件)を追記する
