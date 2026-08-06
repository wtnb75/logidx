# テキストログ → Parquet変換ツール 設計

## 概要

複数種別が混在するテキストログを、種別ごとに定義した正規表現ルールでパターンマッチして構造化し、DuckDBなどで分析可能なParquetファイルへ変換するバッチCLIツール。

## 目的

- テキストログをパターンマッチで構造化フィールドに分解する
- 構造化結果を種別(ルール名)ごとにParquetファイルとして出力する
- 出力Parquetは後工程でDuckDB等から直接クエリできる状態にする

## Non-goals

- ストリーミング/tail追従での常時稼働(バッチCLIのみを対象とする)
- 自動的なログテンプレート抽出(drain3等の機械学習的パターン発見は対象外。ルールは手動定義)
- 複数の入力ファイル・複数typeを横断した単一スキーマへの強制統合(typeごとにスキーマが異なることを許容する)

## アーキテクチャ

```
入力ログファイル(複数, 種別混在)
        │
        ▼
  [ルール設定 rules.yaml] ──読み込み・検証──▶ [ルールエンジン]
                                                    │
  入力ファイルを1行ずつ読む ──────────────────────▶│
                                                    │ 全ルールを順番に試す(最初のマッチを採用)
                                    ┌───────────────┼───────────────┐
                                    ▼               ▼               ▼
                              マッチ(name=A)   マッチ(name=B)   型変換失敗 or 無マッチ
                                    │               │               │
                                    ▼               ▼               ▼
                          parquet-go Writer  parquet-go Writer   raw fallback
                          (out/file.A.parquet) (out/file.B.parquet) (out/file.unmatched.txt)
```

処理は完全にバッチ実行で、入力ファイルを1つずつ最後まで処理し、出力Parquetをクローズしてから次の入力ファイルへ進む。

## コンポーネント(パッケージ構成)

- **`cmd/logidx`**: CLIエントリポイント。flag解析、複数入力ファイルのループ、終了コード制御
- **`internal/rules`**: YAML形式のルール定義の読み込みと起動時検証(後述)。正規表現のコンパイルもここで行う
- **`internal/schema`**: ルールの`fields`定義からParquetスキーマ(列名・列型)を導出する
- **`internal/parse`**: 1行を受け取り、ルールを順番に試して最初にマッチしたルール名+型変換済みの抽出値を返す。マッチなし/型変換失敗の場合はunmatchedとして返す
- **`internal/writer`**: `name`別のparquet-go Writerと、unmatched用のプレーンテキストWriterをラップする。1入力ファイルにつき、実際に使われた`name`の数だけParquetファイルを作る(未使用の`name`はファイルを作らない)
- **`internal/convert`**: 1入力ファイルの処理オーケストレーション(オープン→行ループ→Writer振り分け→クローズ→サマリ生成)
- **`internal/logging`**: `log/slog`のセットアップ(ハンドラ切替、レベル制御)

## ルール設定ファイル(rules.yaml)

```yaml
rules:
  - name: nginx_access
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: "02/Jan/2006:15:04:05 -0700"
      method: string
      path: string
      proto: string
      status: int
      bytes: int

  - name: app_log
    pattern: '^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      level: string
      message: string
```

### ルールの意味

- ルールは配列の**上から順に**マッチを試行し、最初にマッチしたものを採用する(grok的な動作)
- `pattern`はGo標準`regexp`(RE2構文)。名前付きキャプチャグループ`(?P<name>...)`が列の元になる
- `fields`で各キャプチャグループの型(`string`/`int`/`float`/`timestamp`)を明示指定する。`timestamp`型は`format`にGoの参照時刻フォーマットを書く
- **複数の異なる`pattern`が同じ`name`を持てる**。同じ`name`を持つルール同士は、`fields`(列名+型)が完全一致していなければならない(不一致は起動時エラー)。一致していれば同じ出力Parquetファイルに書き込まれる。これにより、フォーマットが変化した同一種別のログ(新旧フォーマット違い等)を1つの出力にまとめられる

### 起動時検証(fail-fast)

1. 各ルールについて、`fields`の各キーに対応する名前付きキャプチャグループが`pattern`に存在するか(なければエラー)
2. `pattern`側にのみ存在する名前付きグループは無視してよい(スキーマに含めない。可読性のための補助グループを許容するため)
3. 同じ`name`を持つルール同士は、`fields`(列名+型)が完全一致しているか(不一致ならエラー)
4. `type`に許容外の値(`string`/`int`/`float`/`timestamp`以外)が指定されていないか
5. `timestamp`型のフィールドに`format`が指定されているか
6. 正規表現自体がコンパイル可能か(RE2構文エラーの検出)

いずれかに違反があれば、どの入力ファイルも処理せずに即座にエラー終了する(exit code 1)。

## データフロー具体例

入力ファイル `access.log`:
```
192.168.1.1 - - [06/Aug/2026:12:00:00 +0900] "GET /index.html HTTP/1.1" 200 512
2026-08-06T12:00:01+09:00 [INFO] user logged in
this is a garbled line that matches nothing
192.168.1.2 - - [06/Aug/2026:12:00:02 +0900] "GET /api HTTP/1.1" 200 128
```

処理結果:
- `out/access.nginx_access.parquet` … 2行(remote_addr, remote_user, time, method, path, proto, status, bytesの8列)
- `out/access.app_log.parquet` … 1行(time, level, messageの3列)
- `out/access.unmatched.txt` … 1行、`3\tthis is a garbled line that matches nothing` のように行番号+元テキストで記録

出力ファイル名規則: `<入力ファイル名から拡張子を除いたbasename>.<ルールname>.parquet`(例: `access.log` → basename `access`)。unmatchedファイルは `<basename>.unmatched.txt`。どちらも、該当する行が1件もなければファイル自体を作成しない。

異なるディレクトリの入力ファイルが同じbasenameを持つ場合(例: `2026-08-01/access.log` と `2026-08-02/access.log`)、同じ`--out`ディレクトリに出力すると出力ファイル名が衝突し上書きされる。衝突を避けるのは呼び出し側(cron設定など)の責務とし、ツール側での自動回避は行わない。

## typeごとの共通列について

typeごとにスキーマが異なっても、全type共通で強制する列(timestampやsource_file等)は設けない。列構成は各ルールの`fields`定義に完全に委ねる(ルール作成者の裁量)。

## 実装方式

Parquet書き出しは純Go製ライブラリ`parquet-go`を使用し、直接`.parquet`ファイルを生成する。cgo非依存で静的バイナリにしやすい構成を優先する。スキーマは`internal/schema`で各ルールの`fields`定義から動的に構築する。

## ログ出力(slog)

このツール自身の動作ログ(処理対象のログデータとは別)は全て`log/slog`経由で出力する。

- 出力先: 標準エラー(stderr)。標準出力は使わない(結果はファイル出力のため)
- ハンドラはフラグで切替: `--log-format text`(デフォルト、`slog.NewTextHandler`) / `--log-format json`(`slog.NewJSONHandler`)
- ログレベル: デフォルト`Info`。`-v`/`--verbose`指定で`Debug`に引き上げ、行単位の型変換失敗などの詳細もこのレベルで出す
- 主なイベントとレベル:
  - 起動時検証エラー → `Error`で詳細を出力してexit
  - 入力ファイルオープン失敗 → `Error`(該当ファイルはskip、処理継続)
  - 出力Writer初期化失敗 → `Error`(該当ファイル処理を中断)
  - 行単位の型変換失敗(unmatched化) → `Debug`(`-v`時のみ)
  - ファイル処理完了サマリ → `Info`、構造化属性でtype別件数・unmatched件数を持たせる

サマリのログ例(テキスト形式):
```
time=2026-08-06T12:34:56+09:00 level=INFO msg="file processed" file=access.log nginx_access=1823 app_log=402 unmatched=3
```

JSON形式:
```json
{"time":"2026-08-06T12:34:56+09:00","level":"INFO","msg":"file processed","file":"access.log","nginx_access":1823,"app_log":402,"unmatched":3}
```

## CLIインターフェース

- `--rules <path>`(必須): ルール定義YAMLファイルのパス
- `--out <dir>`(デフォルト `./out`): 出力先ディレクトリ
- `--log-format text|json`(デフォルト `text`)
- `-v, --verbose`: ログレベルをDebugまで引き上げる
- 位置引数: 入力ログファイル(複数指定可)

## エラーハンドリング(実行時)

- **入力ファイル単位**: 指定ファイルが開けない場合はそのファイルをスキップしてエラーを記録し、他の入力ファイルの処理は継続する。最終的に1件でも失敗があれば全体のexit codeを非0にする
- **行単位**: どのルールにもマッチしない、またはマッチしたが型変換に失敗した行はunmatchedとして記録する(処理は継続、`-v`時のみDebugログに詳細を出す)
- **書き込みエラー**: 出力先への書き込み権限がない等、Writer初期化に失敗した場合はそのファイルの処理を中断してエラー記録する(行単位エラーとは異なり、データ整合性に関わるため握りつぶさない)

## テスト方針

- **`internal/rules`**: YAML読み込み・起動時検証6項目それぞれの正常系/異常系テスト。特に「同名ルールのfields不一致」「fields宣言はあるがキャプチャ名がない」「キャプチャ名はあるがfields宣言なし(無視されること)」を個別ケースでカバー
- **`internal/parse`**: ルール順序どおりに最初のマッチが採用されること、型変換(string/int/float/timestamp)の正常系・失敗系(→unmatched化)をテーブル駆動テストで網羅
- **`internal/schema`**: フィールド型定義からParquetスキーマが正しく導出されること
- **`internal/writer`**: 同名ルールが同じParquetファイルに書き込まれること、typeごとに使われた分だけファイルが作られること(未使用typeのファイルが作られないこと)
- **統合テスト(`cmd/logidx`)**: 複数種別+unmatched行を含むfixtureログファイルでCLIを実行し、出力Parquetをparquet-goで読み戻して行数・値を検証、unmatchedファイルの内容も検証
- **Lint/フォーマット**: `gofmt`, `golangci-lint`、CIでの`go test ./...`
