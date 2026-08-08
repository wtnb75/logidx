# プリセットを`structured.format`として使う設計

## 概要

`2026-08-08-rule-format-presets-design.md`で定義するプリセット(`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`)は、ログ行全体をそのプリセットのパターンに置き換える機能。しかしsyslog転送されたコンテナログの末尾だけがCLFアクセスログになっている、といった「行の一部がプリセット形式」のケースもある。例:

```
2026-01-01T11:19:03.727584+09:00 wtnb4 container/apprise/209c6867d22d[1019] 172.20.0.20 - - [01/Jan/2026:11:19:03 +0900] "POST /notify/ HTTP/1.1" 200 113 "-" "Deno/2.2.4"
```

`2026-08-08-structured-log-field-extraction-design.md`の`structured:`は、ログ行の一部(1キャプチャグループ)をJSON/LTSV/logfmtとして別途パースし、`key:`/`extra:`でフィールドにマッピングする機能。そのパース結果(名前付きキャプチャグループ→文字列のmap)は、プリセットの正規表現を`FindStringSubmatch`した結果と同じ形になる。そこで、`structured.format`にjson/ltsv/logfmtに加えてプリセット名も指定できるようにし、上記のような「行の一部がプリセット形式」のケースを`key:`/`extra:`の枠組みでそのまま扱えるようにする。

## 目的

- ログ行の一部がプリセット形式(CLF、syslogなど)であるケースを、`structured:`の`key:`/`extra:`によるフィールドマッピングの枠組みでそのまま扱えるようにする
- プリセットの固定フィールドのうち必要なものだけを、好きなフィールド名・型で選んで取り出せるようにする(既存の`structured:`のjson/ltsv/logfmtと同じ使い勝手)

## Non-goals

- プリセット自体の追加・変更・パターン定義 — `2026-08-08-rule-format-presets-design.md`のスコープ。本docはそこで定義済みのプリセットレジストリを参照するだけ。
- `structured:`の基本機能(json/ltsv/logfmt、`key:`/`extra:`によるフィールドマッピングの仕組みそのもの)の変更 — `2026-08-08-structured-log-field-extraction-design.md`のスコープ。本docは`Structured.Format`が受け付ける値を1種類増やすだけで、`key:`/`extra:`側のロジックには一切手を加えない。
- ルールレベルの`preset:`ショートカット(行全体をプリセットに置き換える機能)との統合・共存の特別扱い — 両者は独立した機能であり、干渉しない。

## 依存関係・実装順序

本docは次の2つに依存するため、**両方の実装が完了した後**に着手する:

1. `2026-08-08-structured-log-field-extraction-design.md`の基本部分(json/ltsv/logfmtの`structured:`、`key:`/`extra:`によるフィールドマッピング) — 本docはその上に`PresetRegexp`という分岐を1本追加するだけなので、先に土台が要る。
2. `2026-08-08-rule-format-presets-design.md`のプリセットレジストリ(`internal/rules/presets.go`) — プリセット名からパターン文字列を引けないと、`format:`に指定しても解決できない。

上記2つの間には依存関係がなく、どちらを先に実装してもよい(並行可)。

## 1. rules.yaml設定

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

## 2. 実装

- `StructuredConfig.Format`の許容値を「`json`/`ltsv`/`logfmt`のいずれか」から「`json`/`ltsv`/`logfmt`、またはプリセットレジストリに登録された名前」に拡張する。
- `rules.Load()`: `Structured.Format`がjson/ltsv/logfmtのいずれでもない場合、プリセットレジストリから引いて`regexp.Compile`し、`StructuredConfig.PresetRegexp *regexp.Regexp`(新規、yaml `-`)にキャッシュする(`Rule.Regexp`と同じ「一度だけコンパイルする」方針)。
- `internal/parse/structured.go`に`ParsePreset(re *regexp.Regexp, raw string) (map[string]string, error)`を追加する。`re.FindStringSubmatch(raw)`を実行し、名前付きキャプチャグループを`map[string]string`に詰める。マッチしなければエラーを返す。
- `parse.Convert()`: `rule.Structured.PresetRegexp != nil`なら`ParsePreset`を、そうでなければ既存の`ParseStructured`を呼ぶよう分岐する。それ以外(`key:`/`extra:`によるマッピング、型変換)は既存ロジックのまま変更しない。
- バリデーション: `Structured.Format`のチェックを上記の許容値に拡張するだけで、`key:`/`extra:`関連の既存チェックは変更不要。

## エラーハンドリング

- プリセットのパターンが`raw`(アンカーのキャプチャ内容)にマッチしない場合: 既存の「構造化データのパース失敗」(壊れたJSON等)と全く同じ扱いでunmatchedになる。新しいエラー分類は追加しない。
- それ以外(キー不在、型変換失敗など)は既存の`structured:`のエラーハンドリングをそのまま流用する。

## 影響範囲

- `internal/rules/rules.go`: `StructuredConfig.PresetRegexp`追加、`Load()`でのプリセット解決処理追加。
- `internal/rules/validate.go`: `Structured.Format`の許容値拡張。
- `internal/parse/structured.go`: `ParsePreset`追加。
- `internal/parse/match.go`: `Convert`に`ParsePreset`への分岐追加。
- `README.md`: `structured.format`にプリセット名を指定するケースの書き方・実例を追記。

## テスト方針

- `internal/parse`: `ParsePreset`の単体テスト(マッチ成功、マッチ失敗時のエラー)。
- `internal/rules`: `Structured.Format`にプリセット名を指定した場合のロード(`PresetRegexp`が正しくコンパイルされる)・バリデーション(未知のプリセット名がエラーになる)テスト。
- `cmd/logidx`: 概要に挙げた実例(syslog転送されたコンテナログの一部がCLFになっている行)を使ったEnd-to-endの回帰テスト。
- README: 本docの書き方・実例を追記。
