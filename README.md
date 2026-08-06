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

出力ファイル名は `<入力ファイルのbasename>.<ルールname>.parquet`。
どのルールにもマッチしなかった行は `<basename>.unmatched.txt` に行番号付きで保存される。

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

## Development

    task test   # go test ./...
    task lint   # golangci-lint run ./...
    task fmt    # gofmt -l -w .
