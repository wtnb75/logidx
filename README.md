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

### よく使われるログ形式のプリセット(`preset:`)

Apache/nginx Common Log Format・Combined Log Format、BSD syslog(RFC 3164)、syslog protocol(RFC 5424)は、`pattern:`/`fields:`を手書きせず`preset:`の1行で使える。

```yaml
rules:
  - name: access_log
    preset: apache_clf
  - name: syslog
    preset: syslog_rfc3164
```

- `preset:`と`pattern:`/`fields:`は同時に指定できない(起動時エラー)。存在しないプリセット名を指定した場合も起動時エラーになる。
- プリセットの内容は完全固定で、部分的なカスタマイズ(特定フィールドの`format`だけ上書きする等)はできない。カスタマイズしたい場合は、下表の`pattern`/`fields`をそのまま自分の`pattern:`/`fields:`としてコピーし、書き換えて使う。
- `continuation:`/`compression:`など他の設定とは独立して併用できる。ただし`structured:`はプリセットとは組み合わせられない — `structured:`を活かすには`key:`/`extra:`を設定したフィールドが必要だが、`preset:`使用時は`fields:`自体を宣言できないため(前項参照)、そのようなフィールドを作れない。ログ行の一部がプリセット形式であるケースへの対応は`docs/superpowers/specs/2026-08-08-preset-as-structured-format-design.md`を参照。

利用可能なプリセット一覧:

| プリセット名 | 形式 |
|---|---|
| `apache_clf` | Apache/nginx Common Log Format |
| `apache_combined` | Apache/nginx Combined Log Format(CLF + referer/user-agent) |
| `syslog_rfc3164` | BSD syslog(RFC 3164) |
| `syslog_rfc5424` | syslog protocol(RFC 5424) |

#### `apache_clf`

```yaml
pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
fields:
  remote_addr: string
  remote_user: string
  time:
    type: timestamp
    format: clf
  method: string
  path: string
  proto: string
  status: int
  bytes: int
```

#### `apache_combined`

```yaml
pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+) "(?P<referer>[^"]*)" "(?P<user_agent>[^"]*)"$'
fields:
  remote_addr: string
  remote_user: string
  time:
    type: timestamp
    format: clf
  method: string
  path: string
  proto: string
  status: int
  bytes: int
  referer: string
  user_agent: string
```

#### `syslog_rfc3164`

`tag[pid]:`の`[pid]`は多くのデーモンで省略されることがあるため、`pid`は`string`型(未指定時は空文字)。

```yaml
pattern: '^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<tag>[^:\[\s]+)(?:\[(?P<pid>\d+)\])?: (?P<message>.*)$'
fields:
  time:
    type: timestamp
    format: syslog
  host: string
  tag: string
  pid: string
  message: string
```

#### `syslog_rfc5424`

`procid`/`msgid`/STRUCTURED-DATA(`sd`)はRFC上「値なし」を表す`-`が入りうるため`string`型。`sd`は中身をパースせず、生テキストのまま1カラムに格納する(構造化データのキー抽出をしたい場合は`docs/superpowers/specs/2026-08-08-preset-as-structured-format-design.md`を参照)。

```yaml
pattern: '^<(?P<pri>\d+)>(?P<version>\d+) (?P<time>\S+) (?P<host>\S+) (?P<app>\S+) (?P<procid>\S+) (?P<msgid>\S+) (?P<sd>-|(?:\[[^\]]*\])+) (?P<message>.*)$'
fields:
  pri: int
  version: int
  time:
    type: timestamp
    format: iso8601
  host: string
  app: string
  procid: string
  msgid: string
  sd: string
  message: string
```

### タイムスタンプの`format`指定

`timestamp`型フィールドの`format`は、以下の4通りのいずれかで書ける。値の見た目で自動判別するため、書き方を明示する追加のキーは不要:

0. **`auto`**(下記参照。書式を決め打ちせず複数候補を順に試す)
1. **プリセット名**(下表)
2. **strptime記法**(`%`で始まる文字列。下表のディレクティブのみ対応)
3. **生のGoレイアウト文字列**(上記のいずれにも該当しない場合、そのままGoの`time.Parse`レイアウトとして使う。既存の`rules.yaml`はこの扱いのまま変わらない)

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

### `format: auto`(書式の自動判定)

書式が事前にわからない、または複数の書式が混在するログの場合、`format: auto`を指定すると以下6種のレイアウト系プリセットを**この順序**で試し、最初にエラーなくパースできたものを採用する:

| 順序 | プリセット名 |
|---|---|
| 1 | `iso8601`(=`rfc3339`) |
| 2 | `rfc2822` |
| 3 | `rfc822` |
| 4 | `clf` |
| 5 | `syslog` |
| 6 | `pylog` |

一度成功した書式は次回以降そのフィールドで優先して試されるため、実運用でログの書式が行ごとに変わらない限りオーバーヘッドは実質1回だけになる。

`unix`/`unix_ms`/`unix_us`/`unix_ns`(epoch系)は`auto`の候補に含まれない。数値文字列はどのepoch単位でもパース自体は成功してしまい、値の桁数だけでは単位を一意に判定できないため、layout系プリセットと混在させると誤判定のリスクが高い。epoch形式を使いたい場合は明示的にプリセット名を指定すること。strptime記法・生のGoレイアウト文字列も`auto`の候補には含まれない。

年なしプリセット(`syslog`)が`auto`経由でマッチした場合も、既存の年補完ロジック(実行時刻を基準に、未来にならない直近の年を採用)がそのまま適用される。

### フィールド値の変換(`normalize:`/`replace:`)

`fields:`の各フィールドには、値を変換する`replace:`と`normalize:`をそれぞれ設定できる。両者は別の概念であり、常に次の順序で適用される: `replace` → `normalize`。

- `replace:`は、値の一部を正規表現で置換する。宣言順に適用され、前のルールの出力が次のルールの入力になる(チェイン)。`value: ''`を指定すればマッチした部分文字列を削除できる。`$1`のようなキャプチャグループ後方参照も使える(Go標準の`regexp.ReplaceAllString`の機能で、追加実装は不要)。`value:`に文字通りの`$`を書きたい場合は`$$`とエスケープすること(`regexp.ReplaceAllString`はエスケープなしの`$`を常にキャプチャグループ展開として解釈するため、例えば`value: '$USD'`は存在しない`USD`という名前のグループを展開しようとして空文字列になってしまう)。制御文字の8進エスケープ表記(`#015`など)やANSIカラーエスケープシーケンス(`\x1b[31m`など)のような、値の一部にだけ混入するノイズの除去に向く。
- `normalize:`は、値の**どこかにパターンがマッチした時点で**(部分一致、`regexp.MatchString`)、最初にマッチしたルールの固定値で値**全体**を置き換える。値の一部だけを保持したまま変換することはできない。例(`(?i)^warn(ing)?$` → `WARN`)が「値全体が一致した場合のみ」に見えるのはパターン自体に`^`/`$`で全体アンカーを付けているためで、`normalize:`自体の性質ではない。パターンの一部にだけマッチさせたい場合は、`^`/`$`で明示的にアンカーすること(アンカーなしだと、値の一部に偶然パターンが出現しただけで全体が置き換わってしまう)。

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

上の例では、`message`の値からまず`#\d{3}`(制御文字の8進エスケープ表記)とANSIカラーエスケープシーケンスを`replace`で除去し、そのクリーンな値に対して`normalize`のパターンマッチングを行う。

### 構造化データの部分パース(`structured:`/`key:`/`extra:`)

ログ行の一部がJSON/LTSV/logfmtになっているケース(例: 行末尾がJSONのコンテナログ)向けに、`pattern`の名前付きキャプチャグループで切り出した生テキストをさらにキー名でパースし、フィールドにマッピングできる。

```yaml
rules:
  - name: container_log
    pattern: '^(?P<time>\S+) (?P<host>\S+) (?P<tag>\S+) (?P<json>\{.*\})$'
    structured:
      source: json
      format: json
    fields:
      time:
        type: timestamp
        format: iso8601
      host: string
      tag: string
      level:
        type: string
        key: level
      event_time:
        type: timestamp
        format: iso8601
        key: time
      message:
        type: string
        key: msg
      extra:
        type: string
        extra: true
```

- `structured.source`は、構造化データを含む名前付きキャプチャグループの名前(上記例では`json`)。`structured.format`は`json`/`ltsv`/`logfmt`、またはプリセット名(後述の「`structured.format`にプリセット名を指定する」を参照)。1ルールにつき`structured:`は最大1個。
- `fields:`の各フィールドに`key:`を設定すると、構造化データの当該キーの値を使う。フィールド名とキー名が一致していなくてよい(上記例の`event_time`は、行先頭のタイムスタンプとは別物であるJSON側の`time`キーから値を取る)。
- `extra: true`を設定したフィールドは、`key:`で消費されなかった構造化データのキーをすべて集めてJSON文字列として格納する。1ルールにつき最大1個。
- `key:`/`extra:`のどちらも設定しないフィールドは、従来通り`pattern`の同名キャプチャグループから値を取る(既存ルールは無変更で動作する)。
- `structured.source`で指定したキャプチャグループ自体を`fields:`に列挙する必要はない。生の構造化データテキストをそのまま1列として残したい場合は、`key:`なしの通常フィールドとして追加すればよい(両立可能。上記例の`json`のように)。
- **残りカラム(`extra:`)の値は、`structured.format`が`json`のとき元のJSONの型をそのまま保つ**: 未マッピングの`signal`キーが元は`15`という数値なら残りカラムでも`{"signal":15}`(文字列化されない)。ネストしたオブジェクト・配列もネストしたJSONのまま残る(例: `{"listen":{"IP":"::","Port":3000}}`。壊れたJSON文字列として2重エンコードされることはない)。`ltsv`/`logfmt`/プリセット形式はもともと値がすべて文字列なので、残りカラムでも文字列のまま(例: `{"status":"200"}`)。
- `key:`で個別に取り出した値は、これまで通りフィールドの`type:`に従って型変換される(`type: string`ならJSONの数値・真偽値・ネストしたオブジェクト/配列もすべて文字列として格納され、ネストは丸ごとコンパクトなJSON文字列になる。ネストしたキーパスの個別指定は非対応)。
- 構造化データのパース失敗(壊れたJSON、トップレベルがオブジェクトでないJSON、空文字など)は、既存の「型変換失敗」と同じ扱いになる。`key:`で指定したキーが実際のログ行に存在しない場合も同様に「型変換失敗」として扱われる(フィールドの型を問わない。以前はGoのゼロ値`""`が使われ、`string`型フィールドでは黙って空文字列に変換成功していたが、現在はキーが無いこと自体がエラーとして検出される)。
- ルールの`pattern`がマッチしても、その後のフィールド変換(上記の型変換失敗・構造化データのパース失敗・`key:`のキー不在を含む)が失敗した場合、そのルールは即座にunmatched扱いにはならず、`rules:`の次の候補ルールが宣言順に試される。すべての候補ルールで変換に失敗した行だけが最終的に`unmatched.txt`に書かれる。このフォールバックは単一行ルールのみが対象で、`continuation`ルール(後述の「複数行ログエントリのマージ」を参照)では継続行パターンが既に後続行を消費してしまっているため対象外であり、複数行エントリの型変換失敗は従来通りフォールバックなしでunmatchedになる。

#### `structured.format`にプリセット名を指定する

`structured.format`には`json`/`ltsv`/`logfmt`に加えて、`preset:`(前述)で使えるプリセット名(`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`)も指定できる。ログ行全体ではなく、一部だけがプリセット形式になっているケース(例: syslog転送されたコンテナログの末尾がCLFアクセスログ)向け。

```yaml
rules:
  - name: docker_apprise_access
    pattern: '^(?P<ts>\S+) (?P<host>\S+) (?P<tag>[^\[]+)\[(?P<pid>\d+)\] (?P<access>.*)$'
    structured:
      source: access
      format: apache_clf
    fields:
      ts:
        type: timestamp
        format: iso8601
      host: string
      tag: string
      pid: string
      remote_addr:
        type: string
        key: remote_addr
      method:
        type: string
        key: method
      path:
        type: string
        key: path
      status:
        type: int
        key: status
      access_time:
        type: timestamp
        format: clf
        key: time
      extra:
        type: string
        extra: true
```

- `key:`で参照する名前は、そのプリセット定義の`fields:`に列挙されているフィールド名(`apache_clf`なら`remote_addr`/`remote_user`/`time`/`method`/`path`/`proto`/`status`/`bytes`、`apache_combined`なら`apache_clf`と同じ8個に加えて`referer`/`user_agent`の計10個、`syslog_rfc3164`なら`time`/`host`/`tag`/`pid`/`message`、`syslog_rfc5424`なら`pri`/`version`/`time`/`host`/`app`/`procid`/`msgid`/`sd`/`message`)。既存の`structured:`と同じく、必要なキーだけ選んで好きなフィールド名・型で受け取れる(上記例では`time`を`access_time`という名前で受けている)。
- プリセットの固定パターンが`structured.source`のキャプチャ内容にマッチしない場合は、既存の「構造化データのパース失敗」と同じ扱いで`unmatched.txt`に書かれる。
- ルールレベルの`preset:`ショートカット(行全体をプリセットに置き換える機能)とは独立した機能で、組み合わせや特別な連携はない。

### マッチ行の入力元情報を保存する(`meta:`)

`logidx import`は複数の入力ファイルを1つのParquet出力にマージするため、通常はマッチした行がどの入力ファイルの何行目に由来するかという情報が出力に残らない(`unmatched.txt`側は元々`<source>\t<lineNum>\t<raw>\n`形式でこれを持っている)。フィールドに`meta:`を設定すると、その情報をカラムとして保存できる。

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

- `meta: source_file`は`type: string`必須。値はその行が由来する入力パス(`-`はstdinのまま、`unmatched.txt`と同じ表記)。
- `meta: source_line`は`type: int`必須。値はその行の1始まりの行番号。`continuation:`で複数行を1エントリに束ねるルールの場合は、エントリの先頭物理行番号になる(継続行自体の行番号ではない)。
- カラム名は`fields:`のキー名で自由に決められる(`log_file`/`log_line`という名前に限らない)。
- `replace:`/`normalize:`は`meta`フィールドにもそのまま適用できる(例: フルパスからファイル名だけ取り出す正規表現置換)。
- `meta:`はルールごとのオプトインで、全ルールに自動付与されることはない。既存のルールは無変更で動作する。
- `meta:`と`key:`/`extra:`は同じフィールドに同時設定できない(値の取得元は1つだけ)。

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

### row group分割設定

Parquetのrow group行数上限は以下の優先順位で決まる: **CLI引数(`--max-rows-per-row-group`) > rules.yamlの`row_group.max_rows` > デフォルト(無制限)**。

rules.yamlで指定する場合:

```yaml
row_group:
  max_rows: 500000

rules:
  - name: app_log
    ...
```

行数は圧縮後のバイトサイズを直接指定する代わりの代理指標(parquet-go自体がバイトサイズを直接制御する手段を提供していないため)。目的のファイルサイズに近づけたい場合は、対象ルールの1行あたりの平均サイズから逆算して行数を決める。

### 複数ファイルのマージ順

`logidx import`に複数の入力ファイルを渡した場合、ルールに`type: timestamp`のフィールドが1つでもあれば、そのルールの宣言順で最初のtimestampフィールドの値を使って、全入力ファイルをまたいでタイムスタンプ昇順にマージしてから書き込む(設定不要、自動検出)。

timestampフィールドを持たないルール(マージキーなし)の行は、**そのルールの行を出力した各ファイル自身の出現順**は保たれるが、設定内のいずれかのルールにマージキーがある場合、ファイルAの行とファイルBの行が交互に書き込まれることがある — 「ファイルAの行を全て書いてからファイルBの行」という従来の並び順は保証されなくなる点に注意。`unmatched.txt`の行も同様で、各行の元ファイルごとの行番号昇順(その行が書かれた元ファイル内での相対順序)は保たれるが、異なる入力ファイル由来の行同士が交互に並ぶことがある(従来は常にファイル単位でまとまって出力されていた)。

各入力ファイル自体が既に時系列順であることを前提にしたストリーミングマージなので、ファイル内が時系列順に並んでいないログを渡した場合の出力順は保証されない。

マージは全入力ファイルを同時にオープンした状態で行う(k-wayマージの性質上、比較のため全ファイルのカーソルを同時に保持する必要があるため)。そのため入力ファイル数はプロセスのオープンファイル数上限(`ulimit -n`。多くの環境で256〜1024)に制限される。上限を超えると、超過分のファイルごとに特別なメッセージではなく通常の「open input」エラーが発生し、そのファイルはマージ対象から除外される。

### 複数行ログエントリのマージ(`continuation`)

1つの論理ログエントリが複数行にまたがる場合(macOSのsyslogの`Configuration Notice:`に続くインデント行など)、ルールに`continuation`(継続行を検出する正規表現)を設定すると、継続行の内容を該当フィールドへ改行(`\n`)区切りで追記してから1つのParquet行として書き込む。

```yaml
rules:
  - name: syslog
    pattern: '^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<process>\S+): (?P<message>.*)$'
    continuation: '^\s+(?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: syslog
      host: string
      process: string
      message: string
```

- `continuation`の名前付きキャプチャグループが、追記先のフィールドを表す(`fields:`に宣言済みのフィールド名と一致している必要があり、一致しない場合は起動時エラーになる)。1つの継続行パターンに複数の名前付きキャプチャを持たせて、同じ継続行で複数フィールドへ同時に追記することもできる。
- 名前付きキャプチャが0個の継続行パターンも書ける。その行はエントリの継続として認識される(エントリはまだ確定しない)が、どのフィールドにも追記されない — 装飾的な区切り行を読み飛ばしたい場合に使える。
- 連結時の区切り文字は改行(`\n`)固定(設定不可)。
- 継続行パターンにマッチしない行・新しいエントリの開始行・ファイル末尾のいずれかに到達した時点でエントリが確定し、型変換される。ただし、ある行が継続行パターンと新エントリの開始パターンの両方にマッチする場合は継続行として扱われる(継続行判定が優先)。継続行パターンは、新しいエントリの開始行にマッチしないよう十分に限定して書くこと — 緩すぎる継続行パターンは、後続のエントリを次々に飲み込んで1行に潰してしまう(エラーや警告は出ない)。
- 継続行パターンにマッチし続ける限りエントリは開いたまま蓄積されるため、通常は1行ずつ処理するストリーミング設計のメモリ使用量が、そのエントリの行数に比例して増える。上記の「緩すぎる継続行パターン」はこの面でも問題になりうる。
- `replace`/`normalize`は、複数行が改行で連結された最終的な値に対して1回だけ適用される(行ごとには適用されない)。正規表現に`^`/`$`を使う場合、埋め込まれた改行を含む文字列に対してマッチさせることになる点に注意。
- まだどのエントリも開いていない状態で継続行パターンにのみマッチする行(孤立継続行)は、通常の未マッチ行と同様に`unmatched.txt`へ書かれる。
- 確定に失敗した(型変換エラーになった)複数行エントリは、`unmatched.txt`の1行1レコード形式を保つため、蓄積していた元の行それぞれを個別のレコードとして(各行本来の行番号で)書き出す。
- `continuation`を設定しないルールの挙動は従来通り(1行=1エントリ)。

### 圧縮済み入力ファイルの自動解凍

`logidx import`の入力ファイルは、拡張子から自動判定して透過的に解凍される。外部コマンド(`gzip`等)は不要で、Goライブラリのみで完結する。

| 拡張子 | フォーマット |
|---|---|
| `.gz` | gzip |
| `.xz` | xz |
| `.bz2` | bzip2 |
| `.zst` | zstd |

- 拡張子の大文字小文字は区別しない(`.GZ`も`.gz`として扱われる)。
- 上記以外の拡張子(拡張子なしも含む)は無圧縮として扱われる — フォーマットを明示指定するCLIフラグはない。
- 標準入力(`-`)は常に無圧縮として扱われる。圧縮データを標準入力から渡したい場合は、呼び出し側で先に解凍してパイプすること(例: `gzip -dc access.log.gz | logidx import --rules rules.yaml -`)。
- gzip/xz/zstdは、ファイルを開いた時点でヘッダが検証される。フォーマットに合わない・壊れたデータの場合はその場でエラーになり、そのファイルだけがマージ対象から除外される(他の入力ファイルの処理は継続する)。bzip2はストリーミング検証のため、壊れている場合は読み込み中にエラーになる。

### info: Parquetファイルの中身を見る

    logidx info [--format text|json] file1.parquet file2.parquet ...

スキーマ(列名・型・repetition)、列ごとの圧縮コーデックと圧縮/非圧縮バイト数、行数・行グループ数・Parquetバージョンなどを表示する。複数ファイルを渡すと順に出力する(`--format json`時はJSON配列)。読み込みに失敗したファイルはエラーを表示してスキップし、残りの処理は続行する。

### cat: 複数のParquetファイルを結合する

    logidx cat --output <dst.parquet> [--compression <codec>] [--compression-level <n>] [--max-rows-per-row-group <n>] <src.parquet>...

同一スキーマ(列名・型・順番が完全一致)の`src.parquet`を1つ以上結合し、`dst.parquet`を作成する。1ファイルだけを指定した場合は、旧`copy`コマンド相当(圧縮方式を変えた複製)になる。

- スキーマが1つでも一致しない場合は起動時エラーになる(自動変換・カラムのリマップはしない)。エラーメッセージに不一致のファイル名と列位置を含む。
- 結合対象のスキーマに`type: timestamp`の列が1つでもあれば、その最初の列(宣言順)の値で全入力ファイルをまたいで昇順にマージしてから書き込む(`import`の複数ファイルマージと同じ自動検出、設定不要)。timestamp型の列が無ければ、指定した順番のまま単純に連結する。
- `--compression`を省略した場合、1つ目の入力ファイルの圧縮コーデックを引き継ぐ(`import`の`--compression`省略時のデフォルトがzstdなのとは異なる)
- `--compression-level`を省略した場合、コーデックのデフォルトレベルを使う
- `--max-rows-per-row-group`を省略した場合、無制限(`import`と同じ既定)
- `--output`と入力ファイルに同じパスは指定できない

完了後、結合したファイル数・行数・入出力ファイル名・圧縮後/圧縮前バイト数・圧縮率を標準出力に表示する:

```
concatenated 3 files, 12345 rows: a.parquet,b.parquet,c.parquet -> out.parquet (zstd), 4096/16384 bytes (25.0%, 4.00x)
```

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

- `--compression`を省略した場合、ヘッダーに記録された圧縮コーデックを引き継ぐ(`cat`のデフォルト挙動と同様)
- `--compression-level`を省略した場合、コーデックのデフォルトレベルを使う
- `dump.txt`に`-`を指定すると標準入力から読む(例: `logidx dump src.parquet - | logidx restore - dst.parquet`)

Parquetファイル自体(`src.parquet`/`dst.parquet`など)は常に実ファイルパスを指定する(標準入出力は非対応)。標準入出力に対応しているのは、ログ・dumpのテキスト入出力のみ(`import`の入力ログファイル、`dump`の出力先、`restore`の入力)。

### expand / collapse: `preset:`とpattern/fieldsを相互変換する

    logidx expand   [--log-format text|json] [-v|--verbose] <src.yaml> <dst.yaml>
    logidx collapse [--log-format text|json] [-v|--verbose] <src.yaml> <dst.yaml>

`expand`はルールの`preset: <名前>`を、そのプリセットが展開する`pattern:`/`fields:`に書き換える。プリセットの内容を確認したり部分カスタマイズしたい場合に使う(プリセットは全体一致のみでNon-goalsとして部分上書きは対象外 - `expand`してから手で編集する運用になる)。

`collapse`はルールの`pattern:`/`fields:`が(正規表現の表記揺れを正規化した上で)プリセットの定義と完全一致する場合、`preset: <名前>`の1行に書き換える。手書きのパターンがプリセットとたまたま一致している場合に、読みやすく圧縮する用途。

- どちらも変換対象以外のYAML(コメント、キー順、インデント、他のルール)はそのまま保持する
- `src`/`dst`は`dump`/`restore`と同じ規約: `src`に`-`を指定すると標準入力から読み、`dst`に`-`を指定すると標準出力に書く
- 完了後、変換したルール数をログに出す(`expanded rules count=N` / `collapsed rules count=N`)。対象0件でも正常終了する
- `expand`で未知のプリセット名を指定したルールがあるとエラーで打ち切る
- `collapse`は一致しなければそのルールをスキップするだけで、通常はエラーにならない
- インプレース編集用のフラグは無い(`<src> <dst>`に同じパスを指定すればインプレースと同等)

## Development

    task test   # go test ./...
    task lint   # golangci-lint run ./...
    task fmt    # gofmt -l -w .
