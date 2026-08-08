# importの圧縮済み入力ファイル対応 設計

## 概要

現在`logidx import`は入力ファイルを常に無圧縮テキストとして`bufio.Scanner`で読む(`internal/convert.newFileCursor`)。実運用のログはgzip等で圧縮されて保存されることが多く、事前に手動で解凍する手間なしにそのまま渡せるようにしたい。

Goライブラリ(標準ライブラリ+純Go実装の第三者ライブラリ)で解凍を行い、外部コマンド(`gzip -dc`等)への依存は持たない。単一バイナリで配布できるという既存の設計(Parquet圧縮も`parquet-go`自身が処理し外部コマンドを使わない)と一貫させる。

## 目的

- `logidx import`に`.gz`/`.xz`/`.bz2`/`.zst`拡張子の入力ファイルをそのまま渡せるようにする
- 拡張子から自動判定し、ユーザーが明示的にフォーマットを指定する必要をなくす
- 既存のストリーミング処理(1ファイル1回のシーケンシャル読み込み)の設計をそのまま維持する

## Non-goals

- 標準入力(`-`)経由の圧縮入力に対応すること(常に無圧縮として扱う。圧縮されたデータを標準入力から渡したい場合は、呼び出し側で先に解凍してパイプする)
- 拡張子以外の判定方法(マジックバイトによるスニッフィングなど)
- 圧縮フォーマットを明示指定するCLIフラグ
- `dump`/`restore`/`copy`/`info`コマンドへの対応(これらはParquetファイルを直接読み書きするコマンドで、今回の対象=ログ入力ファイルとは無関係)
- tar等のアーカイブ形式への対応(圧縮のみを対象とし、複数ファイルをまとめたアーカイブは対象外)

## 対応フォーマット

| 拡張子 | フォーマット | 使用ライブラリ | 備考 |
|---|---|---|---|
| `.gz` | gzip | 標準ライブラリ `compress/gzip` | `Close()`が必要 |
| `.xz` | xz | `github.com/ulikunitz/xz`(新規依存) | 純Go実装。`Close()`不要 |
| `.bz2` | bzip2 | 標準ライブラリ `compress/bzip2` | デコードのみ。`Close()`不要 |
| `.zst` | zstd | `github.com/klauspost/compress/zstd`(既存の間接依存を直接依存化) | `Close()`が必要 |

拡張子の比較は大文字小文字を無視する(`.GZ`も`.gz`として扱う)。上記以外の拡張子(拡張子なしも含む)は無圧縮として扱う。

## アーキテクチャ

新規パッケージ`internal/decompress`を1つ追加する。

```go
// Wrap inspects path's extension and, if it names a supported compression
// format, wraps r in the matching decompressing reader. If the extension is
// unrecognized (including no extension), r is returned unchanged along with
// a nil Closer. The returned io.Closer releases any resources the
// decompressor itself holds beyond r (currently only gzip and zstd need
// this); callers must call it (if non-nil) in addition to closing the
// original file.
func Wrap(path string, r io.Reader) (io.Reader, io.Closer, error)
```

統合ポイントは`internal/convert/merge.go`の`newFileCursor`一箇所のみ。既存コードは以下のようにファイルを開いて`io.Reader`を`bufio.Scanner`に渡している:

```go
f, err = os.Open(inputPath)
...
in = f
```

これを、`inputPath != "-"`の場合のみ`decompress.Wrap`を通すように変更する:

```go
f, err = os.Open(inputPath)
...
in, decompressCloser, err = decompress.Wrap(inputPath, f)
```

`fileCursor`構造体に`decompressCloser io.Closer`フィールドを追加し、既存の`close()`メソッド(現在は`c.file.Close()`のみ)で、`decompressCloser`が非nilなら併せて呼ぶ。`inputPath == "-"`(標準入力)の場合は`decompress.Wrap`を呼ばず、常に無圧縮の`os.Stdin`をそのまま使う(Non-goals参照)。

### 検討した他の構造(却下)

- **`newFileCursor`に拡張子判定を直接書く**: 実装量は最小だが、`merge.go`は複数行ログエントリのマージ機能で既にこのプロジェクトで最大のファイルになっており、フォーマット判定ロジックまで足すと責務が混ざり単体テストもしにくくなる。
- **`compression.Settings`のような設定構造体を用意する**: 既存の`compression`/`row_group`はユーザーが明示的に選ぶ出力側の設定なので構造体化する意味があるが、今回は「拡張子を見て自動的に解凍する」だけでユーザーが選ぶ設定項目自体が発生しないため過剰設計。

## エラーハンドリング

- **未対応/認識できない拡張子**: `decompress.Wrap`はエラーを返さず`r`をそのまま返す。紛らわしい拡張子(`.gz2`等)も「非対応」として無圧縮扱いになり、解凍を試みない。結果として文字化けした行を読むことになった場合は、既存の「パターンにマッチしない行は`unmatched.txt`へ」という処理がそのままセーフティネットになる。
- **拡張子はgzip/zstdだが中身が壊れている/対応フォーマットでない**: `gzip.NewReader`/`zstd.NewReader`は`NewReader`呼び出し時点でヘッダ検証を行うため、`decompress.Wrap`はこの時点でエラーを返す。`newFileCursor`はこれを既存の`open input: %w`エラーと同様に扱い、そのファイルだけを`mergeFiles`のエラー集約に載せて処理を継続する(他ファイルの処理は止めない)。
- **xz/bzip2で中身が壊れている**: これらはストリーミング検証(最初のブロックを読むまでヘッダエラーが出ない)のため、最初の`scanner.Scan()`で失敗し、既存の`scanner.Err()`経由の`read input: %w`エラー(同じ集約先)に自然に乗る。
- **ファイルの途中で壊れている(打ち切り等)**: どのフォーマットでも`scanner.Err()`が拾い、既存の`read input: %w`パスに乗る。新しいエラー分類は増やさない。

## 影響範囲

- `internal/decompress/decompress.go`(新規): `Wrap`関数
- `internal/convert/merge.go`: `newFileCursor`が`decompress.Wrap`を呼ぶように変更、`fileCursor`に`decompressCloser`フィールド追加、`close()`で併せてクローズ
- `go.mod`: `github.com/ulikunitz/xz`を新規依存として追加、`github.com/klauspost/compress`を間接依存から直接依存に変更
- `README.md`: 圧縮済み入力ファイルの自動解凍について追記

## テスト方針

- `internal/decompress`: 各フォーマット(gzip/xz/bzip2/zstd)について、圧縮済みバイト列を`Wrap`に通して正しく解凍されること、拡張子の大文字小文字を無視すること、未対応拡張子は無変更で返ること、破損データではエラーになることを確認。gzip/zstdは`Close()`が実際に呼べる(nilでないCloserが返る)こと、bzip2/xzは`Closer`がnilであることも確認。
- `internal/convert`: `newFileCursor`/`fileCursor.advance()`レベルで、gzip圧縮した入力ファイル1つを渡して中身が正しくパースされることを確認する回帰テスト。壊れたgzipファイルを渡した場合に、そのファイルだけがエラーとして扱われ他ファイルの処理は継続されることも確認(既存の`TestFiles_ContinuesPastAFailedInputAndStillMergesTheRest`と同じ形)。
- `cmd/logidx`: `import`に`.gz`拡張子のサンプルログを渡して`dump`で中身を確認するEnd-to-endテスト(拡張子判定がCLIパイプライン全体で機能することの証明)。
- README: 対応フォーマット・拡張子判定・stdinの扱いを追記。
