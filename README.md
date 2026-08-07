# logidx

複数種別が混在するテキストログを、YAMLで定義した正規表現ルールでパターンマッチして構造化し、
ルール名(type)ごとにParquetファイルへ変換するバッチCLIツール。

## Build

    task build
    # または
    go build -o bin/logidx ./cmd/logidx

## Usage

    logidx import --rules rules.yaml --out ./out access.log app.log

- `--rules <path>` (必須): ルール定義YAMLファイル
- `--out <dir>` (デフォルト `./out`): 出力先ディレクトリ
- `--log-format text|json` (デフォルト `text`)
- `-v` / `--verbose`: Debugレベルまでログを出す
- `--compression <codec>`: Parquet圧縮コーデック(`uncompressed`/`snappy`/`gzip`/`brotli`/`zstd`/`lz4`、デフォルト `zstd`)
- `--compression-level <n>`: コーデック別の圧縮レベル(下表参照)

入力ログファイルには `-` を指定でき、標準入力から読む。

複数の入力ファイルを渡した場合、すべて1つの出力にマージされる。出力ファイル名は入力ファイル名によらず `<ルールname>.parquet`(`--out`ディレクトリ内)。どのルールにもマッチしなかった行は、入力ファイルによらず共通の `unmatched.txt` に `<入力ファイル>\t<行番号>\t<内容>` の形式で保存される(複数ファイル分がマージされるため、行番号だけでは元のファイルが分からなくなるのを防ぐ)。

出力Parquetファイルのカラム順は、rules.yamlの`fields:`に書いた順番になる(アルファベット順ではない)。同じルール名を複数のルールで使う場合、フィールドの名前・型に加えて順番も完全に一致している必要がある。

各出力Parquetファイルについて、書き込み後に行数・圧縮/非圧縮バイト数・圧縮率をログ出力する(`msg="output parquet file"`)。

ルール定義の書き方は `docs/superpowers/specs/2026-08-06-log-to-parquet-converter-design.md` を参照。

### 圧縮設定

圧縮コーデック・レベルは以下の優先順位で決まる: **CLI引数 > rules.yamlの`compression` > デフォルト(zstd)**。

rules.yamlで指定する場合:

```yaml
compression:
  codec: gzip
  level: 9

rules:
  - name: app_log
    ...
```

コーデックごとのレベル範囲:

| codec | level範囲 | 備考 |
|---|---|---|
| `zstd` (デフォルト) | 1-4 | 1=最速 .. 4=最高圧縮 |
| `gzip` | -2-9 | -2=Huffman-only, -1=デフォルト, 0=無圧縮, 9=最高圧縮 |
| `brotli` | 0-11 | 数値が大きいほど高圧縮・低速 |
| `lz4` | 0-9 | 0=fast, 1-9=高圧縮 |
| `snappy` / `uncompressed` | 指定不可 | levelを指定するとエラー |

`--compression`のみ指定して`--compression-level`を省略した場合、rules.yaml側にlevelがあればそれを引き継ぐ(コーデックが変わるとlevelの意味が変わるため、範囲外ならエラーになる)。

### info: Parquetファイルの中身を見る

    logidx info [--format text|json] file1.parquet file2.parquet ...

スキーマ(列名・型・repetition)、列ごとの圧縮コーデックと圧縮/非圧縮バイト数、行数・行グループ数・Parquetバージョンなどを表示する。複数ファイルを渡すと順に出力する(`--format json`時はJSON配列)。読み込みに失敗したファイルはエラーを表示してスキップし、残りの処理は続行する。

### copy: Parquetファイルを圧縮方式を変えて複製する

    logidx copy [--compression <codec>] [--compression-level <n>] src.parquet dst.parquet

`src.parquet`と同一スキーマ・同一データの`dst.parquet`を作成する。圧縮コーデック/レベルを変えたい場合に使う。

- `--compression`を省略した場合、srcファイル自体の圧縮コーデックを引き継ぐ(`import`の`--compression`省略時のデフォルトがzstdなのとは異なる)
- `--compression-level`を省略した場合、コーデックのデフォルトレベルを使う
- `src`と`dst`に同じパスは指定できない

完了後、コピーした行数と圧縮後/圧縮前バイト数・圧縮率を標準出力に表示する。

### dump / restore: Parquetファイルをテキスト形式で書き出す・復元する

    logidx dump src.parquet dst.txt
    logidx restore [--compression <codec>] [--compression-level <n>] dst.txt restored.parquet

`dump`はParquetファイルをテキスト(JSON Lines)形式に変換する。1行目がスキーマ・圧縮設定を記録したヘッダー、2行目以降が1行1レコードのJSONオブジェクト:

```jsonl
{"columns":[{"name":"level","type":"string"},{"name":"message","type":"string"},{"name":"ts","type":"timestamp"}],"compression":{"codec":"gzip"}}
{"level":"INFO","message":"hello world","ts":"2026-08-07T03:34:56Z"}
{"level":"WARN","message":"careful now","ts":"2026-08-07T03:35:10Z"}
```

- `type`はrules.yamlのフィールド型と同じ語彙(`string`/`int`/`float`/`timestamp`)
- `timestamp`列はRFC3339Nano(UTC)の文字列で出力する。内部的にはマイクロ秒精度のint64で保持しているため、精度は失われない(復元時に完全に同じ値へ戻る)
- ヘッダー行は常に1行目固定(予約キーでの判定はしない)

`dst.txt`に`-`を指定すると標準出力に書く。この場合、`dumped N rows: ...`という完了メッセージはstdoutではなくstderrに出す(stdoutは`jq`や`logidx restore -`にそのままパイプできる、dumpの中身だけにするため)。

`restore`はdumpファイルからヘッダーのスキーマ情報を使ってParquetファイルを再構築する。行数・圧縮後/圧縮前バイト数・圧縮率を標準出力に表示する。

- `--compression`を省略した場合、ヘッダーに記録された圧縮コーデックを引き継ぐ(`copy`のデフォルト挙動と同様)
- `--compression-level`を省略した場合、コーデックのデフォルトレベルを使う
- `dump.txt`に`-`を指定すると標準入力から読む(例: `logidx dump src.parquet - | logidx restore - dst.parquet`)

Parquetファイル自体(`src.parquet`/`dst.parquet`など)は常に実ファイルパスを指定する(標準入出力は非対応)。標準入出力に対応しているのは、ログ・dumpのテキスト入出力のみ(`import`の入力ログファイル、`dump`の出力先、`restore`の入力)。

## Development

    task test   # go test ./...
    task lint   # golangci-lint run ./...
    task fmt    # gofmt -l -w .
