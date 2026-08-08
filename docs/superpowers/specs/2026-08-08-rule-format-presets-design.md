# よく使われるログ形式のプリセット機能 設計

## 概要

Apache/nginxのCommon Log Format(CLF)・Combined Log Format、BSD syslog(RFC 3164)、syslog protocol(RFC 5424)のような、形式が固定されていてよく見かけるログ形式については、ユーザーが毎回`pattern`(正規表現)と`fields`を手書きしなくても、`preset:`の1行だけで使えるようにする。

## 目的

- `apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`の4形式を、`preset: <名前>`の1行で使えるようにする
- プリセットの内容(`pattern`/`fields`)は固定とし、実装・仕様をシンプルに保つ

## Non-goals

- プリセットの部分カスタマイズ(特定フィールドの`format`だけ上書きする、など)。カスタマイズしたい場合は、プリセットの内容(本仕様書に記載)をそのまま自分の`pattern`/`fields`としてコピーして書き換える運用とする。
- CLF/Combined/syslog以外の形式のプリセット(W3C Extended Log Format、AWS ALB/ELBアクセスログ、CEF/LEEFなど)。将来必要になれば別途追加を検討する。
- RFC 5424のSTRUCTURED-DATA部分(`[id key="value"]`)の中身までのパース。プリセットでは生テキストのまま1カラムに格納する(json/ltsv/logfmt向けの既存の`structured:`機能はRFC5424独自のSD構文には対応しない)。
- ログ行内の構造化データ(JSON/LTSV/logfmt)のキー抽出機能(`structured:`/`key:`/`extra:`) — これは別の設計(`2026-08-08-structured-log-field-extraction-design.md`)で扱う、独立した機能。両者は併用可能(例: `syslog_rfc3164`プリセット+`continuation:`の追加、など)だが、プリセット自体はこの機能に依存しない。

## 1. rules.yaml設定とバリデーション

```yaml
rules:
  - name: access_log
    preset: apache_clf
  - name: syslog
    preset: syslog_rfc3164
```

- `rules.Rule`に`Preset string`(yaml `preset`、任意)を追加。
- `Load()`時、`Preset != ""`ならプリセットレジストリ(後述)から`Pattern`/`Fields`を取り出して設定し、以降は通常のルールと全く同じ経路(`regexp.Compile`、`Config.Validate()`)でコンパイル・検証される。
- `Config.Validate()`に追加するチェック:
  - `preset:`と`pattern:`/`fields:`を同時に指定した場合はエラー(`rule %q: preset and pattern/fields are mutually exclusive`)。完全に置き換える方針のため混在は許可しない。
  - 存在しないプリセット名を指定した場合はエラー(`rule %q: unknown preset %q`)。
- `continuation:`/`structured:`/`compression:`など他のルール・グローバル設定とは独立して併用できる(プリセットは`pattern`/`fields`だけを置き換えるため)。

## 2. 各プリセットの内容

### `apache_clf`(Apache/nginx Common Log Format)

既存のREADME例(`nginx_access`)と同じ定義を流用する。

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

### `apache_combined`

CLFにreferer/user-agentを追加した形式。

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

### `syslog_rfc3164`(BSD syslog)

`tag[pid]:`の`[pid]`は多くのデーモンで省略されることがあるため、`pid`は`string`型にして未指定時は空文字にする(`int`型だと空文字の型変換に失敗し、pidなしの行がすべてunmatchedになってしまうため)。

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

### `syslog_rfc5424`

`procid`/`msgid`/STRUCTURED-DATAはRFC上「値なし」を表す`-`が入りうるため`string`型にする。

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

`sd`(STRUCTURED-DATA)は生テキストのまま1カラムに入れるだけで、中身まではパースしない(Non-goals参照)。

## 3. 実装配置

新規ファイル`internal/rules/presets.go`に、プリセット名から`{Pattern string, Fields []Field}`へのマップ(またはそれを返す関数)を定義する。`rules.Load()`はこのマップを参照するだけで、既存のコンパイル・検証パスには一切手を加えない。

## エラーハンドリング

- **`preset:`と`pattern:`/`fields:`の同時指定**: 起動時バリデーションエラー。
- **未知のプリセット名**: 起動時バリデーションエラー。
- **プリセットのパターンに実際のログ行がマッチしない**(例: BSD syslogの亜種で書式が微妙に違う場合): 既存の「パターン不一致」と全く同じ扱いで`unmatched.txt`へ。プリセット専用の特別なエラー処理は追加しない — カスタムパターンで書いたルールと区別なく扱われる。
- **プリセットの`pattern`/`fields`自体のコンパイル・検証**: 内部的に用意した固定の文字列なので本来コンパイルエラーになることはない前提だが、通常のルールと同じ検証パス(`regexp.Compile`失敗、フィールドとキャプチャグループの不一致など)をそのまま通す(プリセット定義自体の正しさはテストで担保する)。

## 影響範囲

- `internal/rules/rules.go`: `Rule.Preset`追加、`Load()`でのプリセット展開。
- `internal/rules/validate.go`: `preset`と`pattern`/`fields`の排他チェック、未知のプリセット名チェック追加。
- `internal/rules/presets.go`(新規): プリセットレジストリ。
- `README.md`: 4つのプリセット名と展開内容、カスタマイズしたい場合の運用方針を追記。

## テスト方針

- `internal/rules`: 各プリセット(`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`)について、`preset:`だけを書いたルールが正しく`Pattern`/`Fields`に展開されコンパイルされることを確認。`preset:`と`pattern:`/`fields:`の同時指定、未知のプリセット名がそれぞれ起動時エラーになることを確認。
- `internal/parse`または`internal/convert`: 各プリセットについて、実際のログ行サンプル(CLF/Combined各1行、BSD syslog(pidあり・なし各1パターン)、RFC5424(structured-dataあり・`-`各1パターン))を用意し、`Match`/`Convert`で期待通りの値が取れることを確認する回帰的なテーブルテスト。
- README: 4つのプリセット名と、それぞれが展開される`pattern`/`fields`の内容(本仕様書の内容と同じ表)を追記。「プリセットは固定内容で、部分的なカスタマイズはできない。カスタマイズしたい場合は同じ内容をコピーして自分の`pattern`/`fields`として書き換える」という運用方針も明記する。
