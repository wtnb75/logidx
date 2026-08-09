# 同一スキーマの複数Parquetファイルを結合する `cat` サブコマンド 設計

## 概要

同一スキーマを持つ複数のParquetファイルを1つのParquetファイルへ結合する `logidx cat` サブコマンドを追加する。

```
logidx cat --output <dst.parquet> [--compression <codec>] [--compression-level <n>] [--max-rows-per-row-group <n>] <src.parquet>...
```

既存の `logidx copy`(1入力ファイルを、圧縮コーデックを変えつつ複製するコマンド)は、入力ファイルが1つの `cat` と機能的に等価になるため、`copy` サブコマンドを廃止し `cat` に統合する。

## 目的

- 日次/時間帯別などに分かれて出力された同一ルール由来のParquetファイル群を、後から1ファイルへまとめる運用をサポートする
- 結合と同時に圧縮コーデック・row group行数上限を変更できるようにし、アーカイブ用途での再圧縮・再チャンク分割を一度の操作で済ませる
- `copy`・`cat`という機能重複したサブコマンドを持たないようにし、CLIサーフェスと内部実装(`internal/pqcopy`)を一本化する

## Non-goals

- スキーマの自動変換・カラムのリマップ(型変換、列名の読み替え等)。スキーマは完全一致必須とし、不一致は起動時エラーとする
- 入力ファイル間の行の並べ替え(タイムスタンプマージ等)。`import`の複数ファイルマージ(`docs/superpowers/specs/2026-08-08-timestamp-merge-and-row-group-design.md`相当の機能)とは異なり、`cat`は引数の順番どおりに単純連結する
- 同時オープンするファイル記述子数の上限対策(既存の`import`の複数ファイルマージと同じ方針で対象外とする)

## CLI仕様

```
logidx cat --output <dst.parquet> [--compression <codec>] [--compression-level <n>] [--max-rows-per-row-group <n>] <src.parquet>...
```

- `--output`(必須): 出力先ファイルパス。未指定または位置引数が0個の場合はusageエラー(exit code 2)
- 位置引数: 結合するParquetファイルを1つ以上、任意の順で指定する(1つだけの指定も許可し、旧`copy`相当の動作になる)
- `--compression`/`--compression-level`: 省略時は**1つ目に指定した入力ファイルのコーデック**を引き継ぐ(旧`copy`の「未指定時はソースのコーデックを維持する」という既定動作を踏襲)。値が指定された場合は`compression.Settings.Validate()`で検証する
- `--max-rows-per-row-group`: 省略時は無制限(`import`と同じ既定)。指定時は`rowgroup.Settings.Validate()`で検証する
- `--output`に指定したパスが、いずれかの入力ファイルのパスと一致する場合はエラー(旧`copy`の同一パスチェックの一般化)

**成功時のサマリ**(stdout、他コマンドと同じ形式):

```
concatenated 3 files, 12345 rows: a.parquet,b.parquet,c.parquet -> out.parquet (zstd), 4096/16384 bytes (25.0%, 4.00x)
```

## パッケージ設計

### `internal/pqcat`(新規、`internal/pqcopy`を置き換え)

既存の「1サブコマンド = 1内部パッケージ」の構成(`pqcopy`/`pqdump`/`pqinfo`)に揃え、`cat`サブコマンド用に`internal/pqcat`を新設し、`internal/pqcopy`を削除する。

```go
package pqcat

// SourceCodec は旧 pqcopy.SourceCodec と同じ実装(ファイル先頭列の圧縮コーデック名を返す)。
func SourceCodec(path string) (string, error)

// Cat は srcPaths の各ファイルの行を、指定した順番のまま dstPath へ連結して書き込む。
// 全ファイルのスキーマ(列名・型・順番)が一致しない場合は、dstPath を作成せずにエラーを返す。
func Cat(srcPaths []string, dstPath string, comp compression.Settings, rg rowgroup.Settings) (rows int64, err error)
```

**`Cat`の処理手順:**

1. `dstPath`が`srcPaths`のいずれかと同一パスでないことを確認する(`filepath.Clean`で比較。旧`pqcopy.Copy`の同一パスチェックの一般化)
2. `srcPaths`を先頭から順にオープンし、`parquet.OpenFile`でフッタをパースして`*parquet.Schema`を取得する
3. 1つ目のファイルのスキーマを正とし、2つ目以降のスキーマと`schema.Equal`(後述)で比較する。1件でも不一致があれば、その時点で全ファイルをクローズしエラーを返す(`dstPath`は作成しない)
4. 正としたスキーマに対し`schema.ForceCompression`(後述)で`comp.CodecInstance()`を強制した書き込み用スキーマを構築する
5. `dstPath`を作成し、`rg.Option()`が有効なら`parquet.WriterOption`として追加した`GenericWriter[map[string]any]`を用意する
6. `srcPaths`を順番に、1000行ずつのバッチで`GenericReader`から読み`GenericWriter`へ書く(旧`pqcopy.Copy`のバッチループをファイル数分繰り返す形に一般化)。各ファイルの読み込みが終わったら次のファイルへ進む
7. 全ファイル処理後に`writer.Close()`し、書き込んだ総行数を返す

### `internal/schema`への追加

- **`ForceCompression(node parquet.Node, codec compress.Codec) parquet.Node`**
  現在`internal/pqcopy/pqcopy.go`にある非公開の`forceCompression`をそのまま`schema`パッケージへ移設・公開する。`pqcat`はこれをそのまま使う。

- **`Equal(a, b *parquet.Schema) error`**
  2つのスキーマの葉ノード(列)を先頭から順に比較し、列数・列名・型・repetitionが完全に一致すればnilを返す。最初に見つかった不一致について、列位置・両者の列名/型を含むエラーを返す(下記フォーマット)。`pqinfo`と同様、フラットな(ネストしない)スキーマのみを対象とする。

  ```go
  func Equal(a, b *parquet.Schema) error
  ```

  比較に使う情報は`pqinfo.ColumnInfo`が使っているのと同じ`field.Name()` / `field.Type().String()` / repetition(`Optional()`/`Repeated()`/どちらでもない)。

### `pqcat.Cat`のスキーマ不一致エラーメッセージ

`schema.Equal`が返すエラーに、比較対象のファイルパス情報を`pqcat.Cat`側で付加する。例:

```
schema mismatch: c.parquet does not match a.parquet (canonical): column 3: name "code" (c.parquet) vs "status" (a.parquet)
```

列数が異なる場合:

```
schema mismatch: c.parquet does not match a.parquet (canonical): column count 5 (c.parquet) vs 6 (a.parquet)
```

## CLI側の変更(`cmd/logidx/main.go`)

- `newCopyCmd`を削除する
- `newCatCmd(stdout, stderr io.Writer) *cobra.Command`を追加する。`newCopyCmd`と同じ「`--compression`未指定時はソースのコーデックを引き継ぐ」パターンを踏襲しつつ、ソースは`pqcat.SourceCodec(srcPaths[0])`から取得する
- `root.AddCommand(newCopyCmd(...))`を`root.AddCommand(newCatCmd(...))`に置き換える

## エラーハンドリング

- 入力ファイルが開けない/フッタがパースできない → そのファイルパスを含むエラーメッセージで即終了(exit code 1)、出力ファイルは作成しない
- スキーマ不一致 → 上記フォーマットのエラーで終了(exit code 1)、出力ファイルは作成しない
- `--output`が入力ファイルのいずれかと同一パス → 起動時エラー(exit code 1)
- 圧縮設定・row group設定のバリデーションエラー → usageエラー(exit code 2)、`import`/旧`copy`と同じ扱い

## 移行・削除対象

- `internal/pqcopy`パッケージ(`pqcopy.go`・`doc.go`・`pqcopy_test.go`)を削除
- `cmd/logidx/main.go`: `newCopyCmd`を削除、`newCatCmd`を追加
- `cmd/logidx/main_test.go`: `TestRun_Copy*`系のテストを`TestRun_Cat*`として書き換え、複数ファイル・スキーマ不一致・同一パスエラーのケースを追加
- `README.md`: `copy`コマンドの説明を`cat`コマンドの説明に置き換える

## テスト方針

- `internal/pqcat`の単体テスト(`internal/pqcopy/pqcopy_test.go`の既存ケースを移設した上で拡張):
  - 単一ファイル入力で行数・列順が保持されること(旧`copy`相当の回帰確認)
  - 複数ファイル入力で、結合順が引数順であること
  - 圧縮コーデックの変更、および未指定時に1つ目のファイルのコーデックを引き継ぐこと
  - 複数バッチにまたがる行数での動作
  - スキーマ不一致の検出: 列名違い・型違い・列数違いのそれぞれのケースでエラーメッセージにファイル名と列位置が含まれること
  - `--output`と入力ファイルが同一パスの場合にエラーになること
  - 存在しない入力ファイルを渡した場合にエラーになること
  - `SourceCodec`の単体テスト(旧`pqcopy`のテストを移設)
- `internal/schema`の単体テスト: `Equal`が一致/列名不一致/型不一致/列数不一致を正しく判定すること、`ForceCompression`が既存の`pqcopy`テストで検証していた内容(列順の保持を含む)を引き続きカバーすること
- `cmd/logidx`のend-to-endテスト(`TestRun_Cat*`): フラグ検証・usageエラー・実ファイルでの単一/複数ファイル結合・圧縮変更・スキーマ不一致時のエラーをカバー
