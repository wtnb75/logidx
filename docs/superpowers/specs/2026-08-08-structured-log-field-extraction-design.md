# ログ行内の構造化データ(JSON/LTSV/logfmt)フィールド抽出 設計

## 概要

ログ行の一部が構造化データ(JSON、LTSV、logfmt)になっているケースがある。例:

```
2026-08-04T23:26:39.247486+09:00 wtnb4 container/clc/137272bf8941[874] {"time":"2026-08-04T14:26:39.229216178Z","level":"INFO","msg":"caught signal","signal":15}
2026-08-04T23:26:47.661639+09:00 wtnb4 container/clc/131568006cb0[874] {"time":"2026-08-04T14:26:47.661294297Z","level":"INFO","msg":"server starting","listen":{"IP":"::","Port":3000,"Zone":""},"pid":1}
```

このJSON部分はキーの出現順が行ごとに異なりうる(`msg`の後に`signal`が来る行もあれば`listen`が来る行もある)ため、現在の`pattern`(1本の正規表現、名前付きキャプチャグループの位置で値を取る)では書きにくい。ログ行の一部を構造化データとして別途パースし、キー名でフィールドにマッピングできるようにする。マッピングされなかったキーは、まとめて1つの「残りカラム」に格納する。

## 目的

- 構造化データ部分の生テキストを、`pattern`の名前付きキャプチャグループでこれまで通り切り出す(既存の正規表現中心の設計を変えない)
- キーの出現順に依存せず、キー名でフィールドにマッピングできるようにする
- フィールド名と構造化データ側のキー名が異なっていてもマッピングできるようにする(例: 行先頭のタイムスタンプと、JSON内の`time`キーは別物)
- マッピングされなかったキーを1つの列にまとめて保持し、情報を失わないようにする
- JSON・LTSV・logfmtの3フォーマットに対応する

## Non-goals

- Apache CLFなど「よく使われる形式のプリセット」機能(`preset: apache_clf`のような短縮記法)をこのdoc自身が定義すること — 構造化データの部分パースとは別のトピックとして切り分ける。現状の`pattern`(1本の正規表現)だけで十分書ける形式であり、今回のスコープには含めない。ただし、既存のプリセット(`2026-08-08-rule-format-presets-design.md`で定義)を`structured.format`から参照できるようにする拡張は、この対象外には当たらない(5節で扱う)。
- 構造化データのネストしたキーパス指定(例: `key: listen.Port`のような入れ子アクセス) — v1では常にトップレベルのキーのみを対象とする。ネストしたオブジェクト/配列は、その部分をまるごとJSON文字列として1つの値にする。
- 残りカラムの型情報保持(数値・真偽値をJSONの元の型のまま残りカラムに入れること) — 残りカラムは常に「キー→文字列」のJSONになる(後述)。
- 複数の構造化データ領域を1ルール内に複数持つこと(`structured:`は1ルールにつき最大1個)。
- 標準入力・出力フォーマットの変更、既存の`pattern`のみのルールの挙動変更(`structured:`未設定のルールは完全に従来通り)。

## 1. rules.yaml設定

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

- `rules.Rule`に`Structured *StructuredConfig`(yamlキー`structured`、任意)を追加。`StructuredConfig`は`Source string`(yaml `source`)と`Format string`(yaml `format`、`json`/`ltsv`/`logfmt`のいずれか。5節でプリセット名も追加で指定できるよう拡張される)を持つ。
- `rules.Field`に`Key string`(yaml `key`、任意)と`Extra bool`(yaml `extra`、任意)を追加。
  - `key:`が設定されたフィールドは、構造化データの当該キーの値を使う。フィールド名とキー名が一致していなくてよい(上記例の`event_time`はキー名`time`から取る)。
  - `extra: true`のフィールドは、`key:`で消費されなかった構造化データのキーをすべて集めてJSON文字列として格納する。1ルールにつき最大1個。
  - どちらも設定しないフィールドは従来通り`pattern`の同名キャプチャグループから値を取る(既存ルールは無変更で動作する)。
- `Structured.Source`で指定したキャプチャグループ自体を`fields:`に列挙する必要はない。列挙したい場合(生の構造化データテキストをそのまま1列としても残したい場合)は、`key:`なしの通常フィールドとして追加すればよい(両立可能)。

## 2. パース処理(`internal/parse`)

新しい関数を追加する:

```go
// ParseStructured parses raw (the captured substring named by a rule's
// Structured.Source) according to format ("json", "ltsv", or "logfmt") into
// a flat map of key to string value. Nested JSON objects/arrays are
// re-encoded as their own compact JSON string; JSON numbers keep their
// original textual digits (via json.Number, avoiding float64 formatting
// artifacts); JSON null becomes an empty string. LTSV/logfmt values are
// already flat strings and pass through unchanged. Returns an error if raw
// isn't valid for the given format.
func ParseStructured(format, raw string) (map[string]string, error)
```

- **json**: `encoding/json`の`Decoder`に`UseNumber()`を設定してデコードし、各値を文字列化する(文字列はそのまま、数値は`json.Number`の元の桁そのまま、真偽値は`true`/`false`、nullは空文字列、オブジェクト/配列はその部分を`json.Marshal`で再エンコードしたコンパクトなJSON文字列)。トップレベルがオブジェクトでない場合(配列や単純値)はエラー。同一キーが複数回出現した場合は後勝ち(`encoding/json`のmapデコードの自然な挙動をそのまま使う)。
- **ltsv**: タブ区切りで分割し、各要素を最初の`:`で`key`/`value`に分割する(値に`:`が含まれてもよいように最初の1つだけで区切る)。
- **logfmt**: スペース区切りの`key=value`。値がダブルクォートで囲まれている場合は、中の空白・エスケープされた`"`を許容する(Go標準のlogfmt出力と互換)。サードパーティ依存は追加せず、手書きの小さなパーサーで実装する。

`parse.Convert`(既存のシグネチャ`Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error)`は変更しない)を拡張する:

```go
func Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error) {
	var structuredValues map[string]string
	if rule.Structured != nil {
		structuredValues, err = ParseStructured(rule.Structured.Format, raw[rule.Structured.Source])
		if err != nil {
			return nil, fmt.Errorf("parse structured data: %w", err)
		}
	}

	var extraJSON string
	if structuredValues != nil {
		extraJSON, err = marshalUnconsumed(rule.Fields, structuredValues)
		if err != nil {
			return nil, fmt.Errorf("encode extra field: %w", err)
		}
	}

	converted := make(map[string]any, len(rule.Fields))
	for _, field := range rule.Fields {
		rawValue := raw[field.Name]
		switch {
		case field.Extra:
			rawValue = extraJSON
		case field.Key != "":
			rawValue = structuredValues[field.Key]
		}
		v, err := convertValue(rawValue, field, now)
		if err != nil {
			return nil, err
		}
		converted[field.Name] = v
	}
	return converted, nil
}
```

`marshalUnconsumed(fields []rules.Field, structuredValues map[string]string) (string, error)`は、`fields`から`Key != ""`のものを集めて消費済みキー集合を作り、`structuredValues`からそれらを除いた残りを`map[string]string`として`encoding/json.Marshal`する。Goの`json.Marshal`はmapキーを常にソートして出力するため、結果は決定的(実行のたびに同じ順序)になる。

**残りカラムの値は常に文字列型になる点に注意**: `structuredValues`はどのフォーマット(json/ltsv/logfmt)でも「キー→文字列」に平坦化された状態で保持されるため、残りカラムをJSON化した際、元がJSONの数値・真偽値であっても文字列としてクォートされる(例: 元の構造化データが`{"signal":15}`でも、`signal`が未マッピングだった場合の残りカラムは`{"signal":"15"}`になる)。JSON/LTSV/logfmtを同じ土俵で一貫して扱うためのトレードオフとして採用する。

## 3. バリデーション(`Config.Validate()`への追加)

- `rule.Structured`が設定されている場合: `Format`が`json`/`ltsv`/`logfmt`のいずれかであること(5節でプリセット名も許容するよう拡張される)、`Source`が`rule.Regexp`の名前付きキャプチャグループ名のいずれかと一致すること(既存の「フィールド名がキャプチャグループ名と一致するか」のチェックと同じロジックを流用)。
- `field.Key != ""`または`field.Extra == true`のフィールドは、`rule.Structured != nil`が必須(構造化データの定義がないルールでこれらを使うのは起動時エラー)。
- `field.Key != ""`と`field.Extra == true`は同時に設定不可(起動時エラー)。
- `field.Extra == true`のフィールドは1ルールにつき最大1個まで(2個以上は起動時エラー)。
- 既存の「フィールド名に一致する名前付きキャプチャグループが必要」というチェックは、`Key != ""`または`Extra == true`のフィールドには適用しない(これらは`pattern`ではなく構造化データ側から値を取るため)。

## 4. エラーハンドリング

- **構造化データのパース失敗**(壊れたJSON、トップレベルがオブジェクトでないJSON等): `ParseStructured`がエラーを返し、`Convert`もエラーを返す。呼び出し元(`fileCursor`)から見ると既存の「型変換失敗」と完全に同じ扱いになり、その行(複数行エントリの場合は蓄積していた各行)が`unmatched.txt`に書かれる。構造化パース専用の新しいエラー分類は増やさない。
- **構造化データのキーが存在しない**(`key:`で指定したキーが実際のログ行になかった場合): `structuredValues[key]`はGoのmapのゼロ値である空文字列になる。既存の「キャプチャグループが空文字にマッチした場合」と同じ扱いで、`type: string`ならそのまま空文字、`type: int`/`timestamp`なら既存の型変換失敗ロジックでunmatchedになる。新しい「キー不在」エラーは作らない。
- **`Structured.Source`のキャプチャグループ自体が空文字**: 空文字を`json`/`ltsv`/`logfmt`としてパースしようとしてエラーになり、型変換失敗と同じ扱いでunmatchedになる。
- **`extra`列のJSON化失敗**: `structuredValues`の値は既に文字列化済みなので、`map[string]string`の`json.Marshal`が失敗することは実質的にない。万が一失敗しても同じエラー経路でunmatchedになる。

## 5. プリセットを`format`として使う(ログ行の一部がプリセット形式のケース)

`2026-08-08-rule-format-presets-design.md`で定義するプリセット(`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`)は、ログ行全体をそのプリセットのパターンに置き換える機能。しかしsyslog転送されたコンテナログの末尾だけがCLFアクセスログになっている、といった「行の一部がプリセット形式」のケースもある。例:

```
2026-01-01T11:19:03.727584+09:00 wtnb4 container/apprise/209c6867d22d[1019] 172.20.0.20 - - [01/Jan/2026:11:19:03 +0900] "POST /notify/ HTTP/1.1" 200 113 "-" "Deno/2.2.4"
```

このケースは、`structured.format`にプリセット名を指定することで対応する(json/ltsv/logfmtに加えて、プリセットレジストリに登録された名前も`format:`に指定できるようにする)。パース結果(名前付きキャプチャグループ→文字列のmap)の形が既存のjson/ltsv/logfmtと同じなので、`key:`/`extra:`によるフィールドマッピングは無変更で流用できる。

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

`key:`で参照する名前は、プリセット定義(`2026-08-08-rule-format-presets-design.md`の各プリセットの`fields:`)に列挙されているフィールド名(`remote_addr`/`remote_user`/`time`/`method`/`path`/`proto`/`status`/`bytes`など)。既存の`structured:`と同じく、必要なキーだけ選んで好きなフィールド名・型で受け取れる(この例では`time`を`access_time`という名前で受けている)。

### 実装

- `StructuredConfig.Format`の許容値を「`json`/`ltsv`/`logfmt`のいずれか」から「`json`/`ltsv`/`logfmt`、またはプリセットレジストリに登録された名前」に拡張する。
- `rules.Load()`: `Structured.Format`がjson/ltsv/logfmtのいずれでもない場合、プリセットレジストリから引いて`regexp.Compile`し、`StructuredConfig.PresetRegexp *regexp.Regexp`(新規、yaml `-`)にキャッシュする(`Rule.Regexp`と同じ「一度だけコンパイルする」方針)。
- `internal/parse/structured.go`に`ParsePreset(re *regexp.Regexp, raw string) (map[string]string, error)`を追加する。`re.FindStringSubmatch(raw)`を実行し、名前付きキャプチャグループを`map[string]string`に詰める。マッチしなければエラーを返す。
- `parse.Convert()`: `rule.Structured.PresetRegexp != nil`なら`ParsePreset`を、そうでなければ既存の`ParseStructured`を呼ぶよう分岐する。それ以外(`key:`/`extra:`によるマッピング、型変換)は既存ロジックのまま変更しない。
- バリデーション: `Structured.Format`のチェックを上記の許容値に拡張するだけで、`key:`/`extra:`関連の既存チェックは変更不要。
- エラーハンドリング: プリセットのパターンが`raw`にマッチしない場合は、既存の「構造化データのパース失敗」(壊れたJSON等)と全く同じ扱いでunmatchedになる(4節)。新しいエラー分類は追加しない。

### 実装順序

この拡張は次の2つに依存するため、**両方の実装が完了した後**に着手する:

1. 本設計の基本部分(json/ltsv/logfmtの`structured:`、`key:`/`extra:`によるフィールドマッピング) — この拡張はその上に`PresetRegexp`という分岐を1本追加するだけなので、先に土台が要る。
2. `2026-08-08-rule-format-presets-design.md`のプリセットレジストリ(`internal/rules/presets.go`) — プリセット名からパターン文字列を引けないと、`format:`に指定しても解決できない。

上記2つの間には依存関係がなく、どちらを先に実装してもよい(並行可)。

## 影響範囲

- `internal/rules/rules.go`: `Rule.Structured`/`StructuredConfig`、`Field.Key`/`Field.Extra`追加、YAMLデコード対応。5節分の`StructuredConfig.PresetRegexp`追加も含む。
- `internal/rules/validate.go`: 上記バリデーション追加(5節分の`Format`許容値拡張を含む)。
- `internal/parse/structured.go`(新規): `ParseStructured`とjson/ltsv/logfmt各パーサー。5節分の`ParsePreset`も同ファイルに追加する。
- `internal/parse/match.go`: `Convert`の拡張(`marshalUnconsumed`含む。5節分の`ParsePreset`への分岐も含む)。
- `README.md`: `structured:`/`key:`/`extra:`の書き方と挙動、5節のプリセットを`format`に使うケースを追記。

## テスト方針

- `internal/parse`: `ParseStructured`の単体テスト — JSON(ネストしたオブジェクト/配列の再エンコード、`json.Number`による整数保持、同一キー後勝ち、トップレベルが配列/スカラーの場合のエラー)、LTSV(タブ区切り、値に`:`を含むケース)、logfmt(スペース区切り、ダブルクォート値、クォート内のエスケープ)、不正な入力でのエラーをそれぞれ確認。
- `internal/parse`: `Convert`の拡張部分の単体テスト — `key:`によるマッピング、`extra:`による残りキーの集約(順序が決定的であること)、構造化データパース失敗がエラーになること、`Structured`未設定ルールの既存挙動が変わらないこと(回帰)。
- `internal/rules`: `Structured`のYAMLデコード・コンパイル、バリデーション(`Source`がキャプチャグループ名と不一致、`key`/`extra`が`Structured`未設定で使われている、`key`と`extra`の同時指定、`extra`が2個以上)がそれぞれ起動時エラーになることを確認。
- `cmd/logidx`: 概要に挙げたサンプルログ(container_logの例)を使ったEnd-to-endテストで、`level`/`message`等のマッピング済みフィールドと、マッピングされなかったキー(`signal`、`args`、`count`、`files`、`archives`、`listen`、`pid`など)が`extra`列にJSON文字列として入ることを確認する。
- README: `structured:`の書き方、`key:`/`extra:`の挙動、対応フォーマット(json/ltsv/logfmt)、残りカラムが常に文字列型のJSONになる点を追記。
- (5節、実装順序に従いjson/ltsv/logfmt側の実装完了後) `internal/parse`: `ParsePreset`の単体テスト(マッチ成功、マッチ失敗時のエラー)。`internal/rules`: `Structured.Format`にプリセット名を指定した場合のロード(`PresetRegexp`が正しくコンパイルされる)・バリデーション(未知のプリセット名がエラーになる)テスト。`cmd/logidx`: 5節の実例(syslog転送されたコンテナログの一部がCLFになっている行)を使ったEnd-to-endの回帰テスト。README: 5節の書き方・実例を追記。
