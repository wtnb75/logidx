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

### タイムスタンプの`format`指定

`timestamp`型フィールドの`format`は、以下の3通りのいずれかで書ける。値の見た目で自動判別するため、書き方を明示する追加のキーは不要:

1. **プリセット名**(下表)
2. **strptime記法**(`%`で始まる文字列。下表のディレクティブのみ対応)
3. **生のGoレイアウト文字列**(上記のどちらにも該当しない場合、そのままGoの`time.Parse`レイアウトとして使う。既存の`rules.yaml`はこの扱いのまま変わらない)

プリセット一覧:

| プリセット名 | 意味 | 備考 |
|---|---|---|
| `iso8601` / `rfc3339` | `2006-01-02T15:04:05.999999999Z07:00` | 小数秒はあってもなくても可(エイリアス) |
| `rfc822` | `02 Jan 06 15:04 -0700` | 数値タイムゾーンオフセット版 |
| `rfc2822` | `Mon, 02 Jan 2006 15:04:05 -0700` | メールヘッダ`Date:`相当 |
| `clf` | `02/Jan/2006:15:04:05 -0700` | Apache/nginxのCommon Log Format |
| `syslog` | `Jan _2 15:04:05` | 年なし。伝統的なBSD syslog形式(`Aug  7`のようにスペース埋め) |
| `pylog` | `2006-01-02 15:04:05,999999999` | Pythonロガーの`%(asctime)s`デフォルト形式(カンマ区切り小数秒) |
| `unix` | epoch秒(整数/小数) | |
| `unix_ms` / `unix_us` / `unix_ns` | epochミリ/マイクロ/ナノ秒(整数) | |

strptime変換表:

| ディレクティブ | 意味 | Goトークン |
|---|---|---|
| `%Y` / `%y` | 年(4桁/2桁) | `2006` / `06` |
| `%m` | 月(2桁) | `01` |
| `%d` | 日(2桁) | `02` |
| `%H` / `%I` | 時(24h/12h) | `15` / `03` |
| `%M` | 分 | `04` |
| `%S` | 秒 | `05` |
| `%f` | 小数秒(可変桁数、`.`/`,`どちらの区切りも受理) | `999999999` |
| `%z` | UTCオフセット | `-0700` |
| `%Z` | タイムゾーン名 | `MST` |
| `%a` / `%A` | 曜日(省略形/フル) | `Mon` / `Monday` |
| `%b` / `%B` | 月名(省略形/フル) | `Jan` / `January` |
| `%p` | AM/PM | `PM` |
| `%%` | リテラルの`%` | `%` |

表にないディレクティブ(`%j`、`%U`など)は起動時エラーになる。`%`で始まらない文字列はプリセット名としても解釈されない場合、生のGoレイアウトとして扱われる(検証は行われず、実際の値をパースするまでエラーが分からない点は既存動作のまま)。

年なしのプリセット・strptime(`syslog`など)は、既存の年補完ロジック(実行時刻を基準に、未来にならない直近の年を採用)がそのまま適用される。

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
