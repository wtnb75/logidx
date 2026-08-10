# rules.yamlのpreset展開・圧縮サブコマンド(`expand`/`collapse`)設計

## 概要

`2026-08-08-rule-format-presets-design.md`で追加した`preset:`ショートカット(`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`)は、行全体のパターンを`preset: <名前>`の1行に置き換える機能。しかし以下のようなニーズがある:

- プリセットの内容を確認・部分カスタマイズしたい(プリセットは全体一致のみでNon-goalsとして部分上書きは対象外 — `preset:`を手書きの`pattern:`/`fields:`に展開してから編集する運用になる)
- 逆に、手書きの`pattern:`/`fields:`がたまたまプリセットの定義と一致している場合、`preset:`の1行に圧縮して読みやすくしたい

この2方向の変換を行うユーティリティサブコマンド`logidx expand`/`logidx collapse`を追加する。

## 目的

- `logidx expand <src.yaml> <dst.yaml>`: ルールの`preset:`を、そのプリセットが展開する`pattern:`/`fields:`に書き換える。
- `logidx collapse <src.yaml> <dst.yaml>`: ルールの`pattern:`/`fields:`が(正規化後に)プリセットの定義と完全一致する場合、`preset: <名前>`に書き換える。
- どちらも変換対象以外のYAML(コメント、キー順、インデント、他のルール)はそのまま保持する。

## Non-goals

- 最上位の`Rule.Preset`以外 — `structured.format:`にプリセット名を指定するケース(`2026-08-08-preset-as-structured-format-design.md`)は対象外。こちらはプリセットの一部フィールドだけを`key:`/`extra:`で選んで取り出す機能であり、「行全体の置き換え」である`Rule.Preset`とは変換の意味が異なるため、実装をシンプルに保つ目的で今回は扱わない。
- 正規表現の真の意味的等価性判定(オートマトンの最小化・比較など)。「意味的に同じパターン」の判定は、`regexp.Compile`してから`Regexp.String()`で得られる正規化済み文字列同士の比較で近似する(下記3節)。
- プリセットの部分一致・部分カスタマイズの検出(一部フィールドだけ違う、など)。既存の`preset:`機能自体がAll-or-Nothing方針のため、collapseも完全一致のみを対象とする。
- インプレース編集用の`--in-place`/`-w`フラグ。`<src> <dst>`の位置引数(同一パスを指定すればインプレースと同等)のみとする。

## 1. コマンドインターフェース

```
logidx expand   [--log-format text|json] [-v|--verbose] <src.yaml> <dst.yaml>
logidx collapse [--log-format text|json] [-v|--verbose] <src.yaml> <dst.yaml>
```

- `src`/`dst`は`dump`/`restore`と同じ規約: `src`に`-`で標準入力から読み、`dst`に`-`で標準出力に書く。
- `--log-format`/`-v|--verbose`は`import`と同じ意味・実装(`logging.New(stderr, logFormat, verbose)`でロガーを作る)。
- 完了時、`logger.Info("expanded rules", "count", n)` / `logger.Info("collapsed rules", "count", n)`を出す。対象0件でも同じログを出して正常終了(exit 0) — dump/restoreと同様、「対象なし」は異常ではない。
- 引数の個数が2個でない場合は使用方法をstderrに出し`exitCodeError{2}`。

## 2. 内部実装アーキテクチャ

新規ファイル`internal/rules/convert.go`(既存の`internal/rules`パッケージ内に置く。`presetRegistry`/`Field`など非公開の型に直接アクセスするため、新パッケージには分離しない)に以下を実装する:

```go
// Expand rewrites every rule's `preset:` into the pattern/fields it names,
// leaving everything else in data byte-for-byte where possible (comments,
// key order, indentation, non-preset rules). Returns the rewritten YAML and
// the number of rules it expanded.
func Expand(data []byte) (out []byte, count int, err error)

// Collapse rewrites every rule whose pattern/fields exactly match a
// registered preset (after normalization, see normalizePattern) into
// `preset: <name>`, leaving everything else untouched. Returns the
// rewritten YAML and the number of rules it collapsed.
func Collapse(data []byte) (out []byte, count int, err error)
```

- 入出力は生YAMLバイト列。`cmd/logidx`側はファイル/`-`の読み書きだけを担当し、変換ロジックには一切関与しない。
- 両関数とも`yaml.Node`としてパースし(コメント・整形を保持するため)、対象ルールのマッピングノードだけを書き換える。
- 既存の`Load(path string) (*Config, error)`は「ファイル読み込み→パース→プリセット展開→コンパイル→検証」を1関数でやっている。この処理本体を`loadConfig(data []byte) (*Config, error)`に切り出し、`Load`はファイルを読んで`loadConfig`を呼ぶだけのラッパーにする(挙動は変えない、責務分割のみ)。
- `Expand`/`Collapse`は、変換後のYAMLバイト列を最後に`loadConfig`に通し、`Validate`まで含めて壊れていないことを確認してから呼び出し元に返す(既存コードの「ロード時に全部検証する」文化に合わせるsafety net。失敗した場合は実装バグを意味するので、そのままエラーとして返す)。

## 3. expandの変換ロジック

`rules:`シーケンスの各ルールのマッピングノードを走査し、`preset`キーを持つものだけ処理する:

1. `preset`の値(スカラーノード)からプリセット名を取得し、`presetRegistry[name]`を引く。未知のプリセット名なら即エラーで打ち切る(部分的に書き換えた不完全なYAMLを出力しない) — エラーメッセージは`Validate`と同じ形式`rule %q: unknown preset %q`を再利用する。
2. マッピングノードの`Content`から`preset`キー/値ペアを削除し、同じ位置に`pattern`(単一引用符スタイルのスカラー)と`fields`(マッピングノード)を挿入する。
3. `fields`の各エントリのエンコード方針(`Field`の全属性に対応する汎用ロジック。現状のプリセット定義は`Type`/`Format`のみ使うが、将来プリセットが増えても流用できるようにする):
   - `Format`/`Key`/`Extra`/`Replace`/`Normalize`が全て未設定(ゼロ値) → `name: type`の省略形(スカラー)。
   - それ以外 → `type:`/`format:`/`key:`/`extra:`/`replace:`/`normalize:`のうち値が設定されているキーだけを持つフルのマッピング。
4. `preset`キーに`HeadComment`/`LineComment`が付いていれば、新しい`pattern`キーに引き継ぐ。それ以外のコメント・他のキーの順序・インデントは元のノードをそのまま使うため自動的に保持される。
5. 変換した件数を`count`としてカウントする。

## 4. collapseの変換ロジック

1. `loadConfig(data)`で一度フルにロードする(既存の`Load`と同じパス。パターンのコンパイル・プリセット展開まで完了した`*Config`が手に入り、`Rule.Regexp`も使える)。
2. 各ルールについて、`Preset == ""`かつ元YAMLで`pattern`/`fields`を明示していた(すでに`preset:`を使っているルールではない)ものを候補とする。
3. 候補ルールの`Regexp.String()`(コンパイル後の正規化済み正規表現文字列)と、各プリセット定義の`Pattern`を同様に`regexp.Compile`してから`String()`した結果を比較する(`normalizePattern(pattern string) (string, error)`ヘルパーを両者に使う)。一致し、かつ`Fields`が`Name`/`Type`/`Format`/`Key`/`Extra`/`Replace`/`Normalize`の全属性・全要素で完全一致(順序含む)するプリセットがあれば、そのルールを対象とする。
   - プリセット名はソートした順に走査し、最初に見つかった一致を採用する(複数一致は想定しないが、決定的な結果にするため)。
4. 対象ルールについて、yaml.Nodeツリー上で`pattern`キー/値ペアがあった位置に`preset: <name>`(スカラー)を挿入し、`pattern`/`fields`の2つのキー/値ペアを削除する。`pattern`キーに付いていた`HeadComment`/`LineComment`は新しい`preset`キーに引き継ぐ。
5. 変換した件数を`count`としてカウントする。

## エラーハンドリング

- **expand**: 未知のプリセット名を指定したルールがあれば、その時点でエラーとして処理を打ち切り、部分的に変換したYAMLは返さない。
- **collapse**: 通常はエラーにならない(一致しなければスキップするだけ)。
- **両方共通**:
  - 入力YAML自体が不正(`loadConfig`の事前パースが失敗)なら、その時点でエラーとして打ち切る。
  - 変換後の出力を`loadConfig`に通した結果が失敗する場合は実装バグを意味するので、そのままエラーとして返す(通常のユーザー向けメッセージとは書き分けない)。
- CLI層(`cmd/logidx`)は既存コマンドと同じ`SilenceUsage`/`SilenceErrors`。引数個数不正は`exitCodeError{2}`、それ以外の失敗は`exitCodeError{1}`。

## 影響範囲

- `internal/rules/rules.go`: `Load`の本体を`loadConfig(data []byte) (*Config, error)`に切り出し、`Load`はラッパー化。
- `internal/rules/convert.go`(新規): `Expand`/`Collapse`、`normalizePattern`、フィールドのエンコードヘルパー。
- `cmd/logidx/main.go`: `newExpandCmd`/`newCollapseCmd`を追加し、`root.AddCommand`に登録。`src`/`dst`のファイル・stdin/stdout・ロガーまわりは`newImportCmd`/`newDumpCmd`/`newRestoreCmd`と同じパターンを踏襲。
- `README.md`: `expand`/`collapse`の使い方セクションを追加。

## テスト方針

- `internal/rules`(`convert_test.go`新規):
  - expand: 4プリセットそれぞれについて、`preset:`だけのルールが正しい`pattern`/`fields`に展開されること。未知プリセット名がエラーになること。`preset:`を使っていないルール・他のキー(`structured`/`continuation`など)・コメントが変化しないこと。
  - collapse: プリセットと完全一致する`pattern`/`fields`が`preset:`に置き換わること。正規表現の些細な表記揺れ(不要なエスケープなど、正規化後には一致するケース)でも置き換わること。1フィールドでも異なれば(型違い・順序違い・余分な`normalize`など)置き換わらないこと。すでに`preset:`のルールがno-opであること。
  - expand→collapseの往復で元の`preset:`形式に戻ることを確認する回帰テスト。
- `cmd/logidx/main_test.go`: 既存の`import`/`dump`と同水準で、`expand`/`collapse`サブコマンドの基本動作(`-`でのstdin/stdout、ログ出力、引数個数エラー)をテスト。
- README: `expand`/`collapse`の使い方・実例を追記。
