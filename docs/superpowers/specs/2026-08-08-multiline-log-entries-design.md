# 複数行ログエントリのマージ機能 設計

## 概要

現在`logidx import`(`internal/convert.fileCursor.advance()`)は入力を1行ずつ読み、各行を独立に`internal/parse.Match`へ渡して1行=1エントリとしてマッチさせている。しかし実際のログには、1つの論理エントリが複数行にまたがる形式がある(例: macOSのsyslogの`Configuration Notice:`に続くインデントされた説明行)。

```
Aug  8 00:30:05 WatanabenoMacBook-Pro syslogd[149]: Configuration Notice:
        ASL Module "com.apple.cdscheduler" claims selected messages.
        Those messages may not appear in standard system log files or in the ASL database.
```

ルールごとに「継続行」を検出する正規表現を設定できるようにし、継続行の内容を該当フィールドへ改行区切りで追記してから、1つの行(1つのParquet行)として書き込めるようにする。

## 目的

- インデントなどで表される複数行のログエントリを、1つの構造化された行として取り込めるようにする
- 検出方法・追記先フィールドをルールごとに柔軟に設定できるようにする(ログ形式は多様なため)
- 既存のストリーミング処理(1ファイル1回のシーケンシャル読み込み、k-wayマージ)の設計をそのまま維持する — メモリに保持するのは「現在オープン中の1エントリ」のみ

## Non-goals

- `continuation`未設定のルールの挙動を変えること(後方互換を維持し、1行=1エントリのまま)
- 継続行かどうかをインデント等から自動推測すること(明示的な正規表現設定のみをサポートする)
- 継続行の区切り文字(連結時のセパレータ)を設定可能にすること(改行`\n`固定)
- 複数のルールにまたがる継続(あるルールのエントリが別ルールの継続行パターンで継続される、といった動作)

## 1. rules.yaml設定とルール読み込み

```yaml
rules:
  - name: syslog
    pattern: '^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<process>\S+): (?P<message>.*)$'
    continuation: '^\s+(?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "Jan _2 15:04:05"
      host: string
      process: string
      message: string
```

- `rules.Rule`に`Continuation string`(`yaml:"continuation"`、任意項目)を追加。
- `rules.Load()`で、`Pattern`と同様に`Continuation`が空でなければ`regexp.Compile`し、`Rule.ContinuationRegexp *regexp.Regexp`にキャッシュする。
- `continuation`の名前付きキャプチャグループは、その1つ1つが「マッチした内容をどのフィールドに追記するか」を表す。1つの継続行パターンに複数の名前付きキャプチャがあってもよく、その場合は同じ継続行で複数フィールドへ同時に追記する。名前付きキャプチャが0個の継続行パターンも有効: その行はエントリの継続として認識され(エントリは確定されない)、どのフィールドにも追記されない(装飾的な区切り行を読み飛ばしたい場合などに使える)。
- `Config.Validate()`にバリデーションを追加: `continuation`が設定されているルールについて、その正規表現の名前付きキャプチャグループ名が、すべて同じルールの`fields:`に宣言済みのフィールド名と一致することを起動時に検証する。一致しないキャプチャ名があれば起動時エラーとする。
- 追記先フィールドの型に関する制限は設けない。型変換(`Convert`)は最終的に連結済みの生文字列に対して行われるため、パースできない値であれば既存の「型変換失敗=unmatched」ロジックがそのまま働く。
- `continuation`が未設定のルールは、従来通り1行=1エントリとして即座に確定する。

## 2. `internal/parse`の分割

現在の`Match(ruleList, line, now) (name string, values map[string]any, ok bool)`は「正規表現マッチ」と「型変換」を1関数で行っている。これを2段階に分割する。

```go
// MatchRaw tries each rule's pattern against line and returns the first
// match's rule and raw (un-type-converted) captured field values, keyed by
// field name. No type conversion happens here - see Convert.
func MatchRaw(ruleList []rules.Rule, line string) (rule *rules.Rule, raw map[string]string, ok bool)

// Convert type-converts raw's captured values according to rule's field
// definitions. Returns an error if any field fails conversion - callers
// treat that the same way a failed match is treated (write to unmatched).
func Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error)
```

- 既存の`Match`は`MatchRaw`+`Convert`を内部で呼ぶ薄いラッパーとして残し、シグネチャ・挙動は変更しない(呼び出し元は今後`fileCursor`のみになるが、テストのしやすさのため関数自体は残す)。
- `now`は`Convert`(年なしタイムスタンプの補完に使う)でのみ必要になるため、`MatchRaw`のシグネチャには含めない。

## 3. `fileCursor`の状態遷移

```go
// openEntry accumulates one in-progress multi-line log entry: a matched
// rule plus its raw (un-converted) field captures, updated in place as
// continuation lines are folded in.
type openEntry struct {
    rule    *rules.Rule
    raw     map[string]string
    lineNum int // the entry's starting line, for diagnostics
}

type fileCursor struct {
    // ...既存フィールド(inputPath, fileIndex, file, scanner, cfg, mergeKey, set, logger, now, counts, unmatched)...
    open    *openEntry
    pending *scannedLine // 1行分の読み戻しバッファ
}

type scannedLine struct {
    text    string
    lineNum int
}
```

- `nextLine() (line scannedLine, ok bool, err error)`ヘルパーを追加: `pending`が非nilならそれを返して`pending = nil`にする(消費)。`pending`がnilなら`scanner.Scan()`する。
- `advance()`の本体を、行ごとに以下の分岐で処理する(擬似コード):

```
loop:
    line, ok, err := nextLine()
    if err != nil { return error }
    if !ok {
        // EOF
        if open != nil {
            cand, err := finalize(open); open = nil
            if err is unmatched-style conversion failure {
                write each accumulated raw line to unmatched.txt individually
            } else if err != nil { return error }
            if cand != nil (has merge key) { return cand, true, nil }
        }
        return nil, false, nil
    }

    if open != nil {
        if raw, matched := open.rule.ContinuationRegexp matches line.text {
            append raw's captures into open.raw (each field: "\n"+capture if already present, else capture)
            goto loop
        }
        // not a continuation of the open entry: close it, replay this line
        cand, err := finalize(open); open = nil
        pending = line // don't lose this line - it hasn't been matched yet
        if conversion failed { write each accumulated raw line to unmatched.txt individually; goto loop }
        if err != nil { return error }
        if cand != nil (has merge key) { return cand, true, nil }
        goto loop // immediate-write rule: already written by finalize, reprocess pending line next iteration
    }

    // no open entry: try matching against all rules
    rule, raw, matched := parse.MatchRaw(cfg.Rules, line.text)
    if !matched {
        WriteUnmatched(line); goto loop
    }
    if rule.ContinuationRegexp != nil {
        open = &openEntry{rule: rule, raw: raw, lineNum: line.lineNum}
        goto loop
    }
    // no continuation configured: finalize immediately (existing single-line behavior)
    values, err := parse.Convert(*rule, raw, now)
    if err != nil { WriteUnmatched(line); goto loop }
    if rule has merge key { return candidate{...}, true, nil }
    WriteMatched(rule.Name, values); counts[rule.Name]++; goto loop
```

- 孤立継続行(まだどのエントリも開いていない状態で継続行パターンにマッチする行)は、`open == nil`のときは継続行判定自体を行わないため、自然に「全ルールに対する`MatchRaw`」に流れ、マッチしなければ`WriteUnmatched`される(特別扱い不要)。

## 4. エラーハンドリング

- **型変換失敗時のunmatched書き込み**: `unmatched.txt`は`source\tlineNum\traw\n`の1行1レコード形式。複数行エントリの生テキストをそのまま1レコードに書くと改行が混入してフォーマットが壊れるため、型変換に失敗した複数行エントリは、蓄積していた元の行それぞれを個別のレコードとして(各行本来の行番号で)書き出す。1エントリとしてのまとまりは`unmatched.txt`上では失われるが、フォーマットの不変条件を守り、どの行が原因か追跡できる。
- **マージキーとの関係**: エントリが確定した時点でメインパターンの`timestamp`フィールドは(継続行を含む)最終的な値になっているため、マージキー付きルールはそのまま`candidate`としてk-wayマージに乗る。追加の変更は不要。
- **I/Oエラー**: 既存の`scanner.Err()`と同じ扱い(ファイル単位のエラーとして`mergeFiles`に伝播、他ファイルの処理は継続)。

## 影響範囲

- `internal/rules/rules.go`: `Rule.Continuation`/`ContinuationRegexp`追加、`Load()`でのコンパイル。
- `internal/rules/validate.go`: 継続行キャプチャ名とフィールド宣言の整合性チェック追加。
- `internal/parse/match.go`: `MatchRaw`/`Convert`への分割、`Match`はラッパー化。
- `internal/convert/merge.go`: `fileCursor`に`open`/`pending`状態を追加し、`advance()`を上記の状態遷移に置き換える。

## テスト方針

- `internal/rules`: `continuation`のコンパイル、継続行キャプチャ名がフィールド未宣言の場合の起動時バリデーションエラー
- `internal/parse`: `MatchRaw`/`Convert`分割後の単体テスト(既存`Match`の挙動が両者の組み合わせで再現されること)
- `internal/convert`:
  - 単一行エントリと複数行エントリが混在する入力で正しく1エントリに集約されること(mac syslogの例に相当するケース)
  - 孤立継続行がunmatchedになること
  - 複数行エントリがファイル末尾(EOF)で終わる場合に正しく確定されること
  - 複数行エントリの型変換失敗時に、各行が個別のunmatchedレコードになること
  - マージキー付きルールで複数行エントリがcandidateとして正しく返ること(タイムスタンプがメイン行のものになっていること)
  - `continuation`未設定のルールの既存挙動が変わらないこと(回帰)
- `cmd/logidx`: mac syslogのサンプルログを使ったEnd-to-endテスト(`import`→`dump`で複数行`message`フィールドが1レコードに収まっていることを確認)
- README: `continuation`設定の書き方と挙動(検出・追記先・区切り文字・孤立継続行の扱い)を追記
