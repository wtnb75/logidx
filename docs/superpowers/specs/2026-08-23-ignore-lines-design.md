# 行単位の無視ルール(`ignore:`) 設計

## 概要

`logidx import`の入力には、壊れたバイナリの混入・異常に長い行・コメント行・空行など、どのルールにもマッチさせる意味がない「ノイズ行」が混ざることがある。これらを`pattern:`によるルールマッチングより前の、生の行の段階で無視できるようにする。

対象は**行全体**に対する判定のみ(フィールド抽出後の値による無視は対象外、Non-goals参照)。以下4種類の条件に対応する:

- 正規表現パターンにマッチする行
- 指定バイト長を超える行
- 有効なUTF-8としてデコードできない行
- 空行(前後の空白を除いて空文字になる行)

## 目的

- ログに混入するノイズ行(コメント、区切り線、破損データ等)を、パターンマッチや型変換の失敗という形ではなく、明示的な設定で除外できるようにする
- 異常に長い行(壊れたバイナリの混入等)によるメモリ使用量の増大や処理コストを、パターンマッチを試みる前に防ぐ
- 不正なUTF-8バイト列を含む行がParquetの`string`カラムにそのまま流れ込む問題を、importの入口で解消する
- 無視された行が「なぜ無視されたか」を`unmatched.txt`から追跡できるようにする

## Non-goals

- フィールド抽出・型変換後の値による無視(例: 特定フィールドの値が条件に合致したらレコードごと捨てる) — 判定タイミングが異なり(`structured:`/`key:`/`extra:`のパース結果を必要とする)、設計・実装ともに規模が大きくなるため別機能として扱う。
- ルールごとの個別`ignore:`定義 — `mask:`/`compression:`/`row_group:`と同じく、全ルール共通のグローバル設定のみとする。
- 無視条件同士のチェイン適用や優先度のユーザー指定 — 4条件は独立した真偽判定であり、`mask:`のように値を変換しながら連鎖する概念ではない。判定順序は実装が固定で持つ(後述)。
- `unmatched.txt`以外への出力(例: `ignored.txt`という別ファイル) — 「無視された行を記録する」という点で既存の`unmatched.txt`の役割と地続きであり、理由列で区別できれば十分と判断した。
- 空行判定のカスタマイズ(改行のみ/タブのみを区別する等) — `strings.TrimSpace`後に空文字列になるかどうかの単純な判定のみ。

## 1. rules.yaml設定

```yaml
ignore:
  patterns:
    - '^#'
    - '^\s*--'
  max_length: 100000
  invalid_utf8: true
  empty: true

rules:
  - name: access_log
    # ...(既存通り)
```

- `ignore:`はrules.yamlのトップレベルに1つ、全ルール共通のグローバル設定として置く。`compression:`/`row_group:`と同じく単一オブジェクト形式(リストではない)。`rules.Config`に`Ignore IgnoreConfig`(yaml `ignore`)を追加する。省略時はゼロ値(すべての条件が無効)になり、既存の動作を完全に維持する。
- 各フィールド:
  - `patterns`(`[]string`, 省略可): 正規表現のリスト。行全体がこのうち**いずれか一つにでも**マッチすれば無視する(`regexp.MatchString`、部分一致)。
  - `max_length`(int, 省略可, デフォルト0=無制限): 行のバイト長(`len(line)`)がこの値を**超えたら**無視する。0以下は「無制限」を意味する。
  - `invalid_utf8`(bool, 省略可, デフォルトfalse): `true`のとき、`utf8.ValidString(line)`が`false`を返す行(=有効なUTF-8としてデコードできない行)を無視する。
  - `empty`(bool, 省略可, デフォルトfalse): `true`のとき、`strings.TrimSpace(line) == ""`となる行(空行・空白のみの行)を無視する。
- 4条件は独立したOR判定であり、複数設定した場合はいずれか一つでも該当すれば無視される。

## 2. 実装への組み込み

### `internal/rules`(設定)

```go
type IgnoreConfig struct {
    Patterns    []string         `yaml:"patterns"`
    PatternsRe  []*regexp.Regexp `yaml:"-"`
    MaxLength   int              `yaml:"max_length"`
    InvalidUTF8 bool             `yaml:"invalid_utf8"`
    Empty       bool             `yaml:"empty"`
}
```

- `Config.Ignore IgnoreConfig`(yaml `ignore`)を追加する。
- `loadConfig`で、既存の`Mask[i].Pattern`コンパイルと同じ要領で`cfg.Ignore.Patterns`の各要素をコンパイルし`PatternsRe`に格納する。コンパイル失敗は既存の`pattern`/`replace`/`normalize`と同じく起動時エラー。
- `Config.Validate()`に`MaxLength < 0`を起動時エラーとするチェックを追加する(0は「無制限」として許容、負数のみ拒否)。

### `internal/rules`(新規 `ignore.go`)

```go
// Reason returns "" if line should not be ignored, otherwise one of
// "empty" / "invalid_utf8" / "max_length" / "pattern" - the first
// matching condition, checked in that fixed order regardless of
// declaration order in rules.yaml.
func (ic *IgnoreConfig) Reason(line string) string
```

- 判定順序を`empty → invalid_utf8 → max_length → patterns`に固定する(安価な判定から先に行う実装上の都合であり、ユーザー設定では変更できない)。最初に真になった条件の理由文字列を返す。どれにも該当しなければ空文字列を返す。

### 組み込み位置(`internal/convert/merge.go`)

`fileCursor`に`ignoreCfg *rules.IgnoreConfig`(または値渡し)を保持させ、`nextLine()`を次のように変更する:

```go
func (c *fileCursor) nextLine() (line scannedLine, ok bool, err error) {
    if len(c.pending) > 0 {
        line = c.pending[0]
        c.pending = c.pending[1:]
        return line, true, nil
    }
    for {
        if !c.scanner.Scan() {
            if err := c.scanner.Err(); err != nil {
                return scannedLine{}, false, fmt.Errorf("read input: %w", err)
            }
            return scannedLine{}, false, nil
        }
        c.lineNum++
        text := c.scanner.Text()
        if reason := c.ignoreCfg.Reason(text); reason != "" {
            if err := c.writeUnmatchedLine(scannedLine{text: text, lineNum: c.lineNum}, "ignored:"+reason); err != nil {
                return scannedLine{}, false, err
            }
            c.ignored++
            continue
        }
        return scannedLine{text: text, lineNum: c.lineNum}, true, nil
    }
}
```

- `pending`(継続行処理で読み戻された行、`pushPending`参照)は無視判定を再度行わない。すでに一度`nextLine()`を通過して初めて`pending`に入る行なので、無視されるべき行がそこに紛れ込むことはない。
- ループの中で無視行を消費して継続することで、呼び出し元(`advance()`、継続行処理)から見ると無視行は完全に透過になる。継続中のエントリ(`continuation`)がある状態で無視行を挟んでも、エントリは閉じずに開いたままになる。
- `source_line`(`meta: source_line`)用の行番号は無視された行も含めて連番でカウントする(物理行番号としての一貫性を保つため)。

### `writeUnmatchedLine`のシグネチャ変更

```go
func (c *fileCursor) writeUnmatchedLine(line scannedLine, reason string) error {
    text := parse.ApplyPatternMask(line.text, c.patternMaskRules)
    if err := c.set.WriteUnmatched(c.inputPath, line.lineNum, reason, text); err != nil {
        return fmt.Errorf("write unmatched line %d: %w", line.lineNum, err)
    }
    c.unmatched++
    return nil
}
```

- 既存の「どのルールにもマッチしなかった」呼び出し箇所は`reason: "unmatched"`を渡すように変更する。
- 無視行のマスク処理(`ApplyPatternMask`)は既存の呼び出し経路をそのまま流用する(無視理由が`invalid_utf8`の場合、パターンが元々マッチしない可能性はあるが、それ自体はエラーではない — Goの文字列は任意のバイト列を保持できる)。
- `c.ignored int`フィールドを`fileCursor`に追加し、`logFileProcessed`で`unmatched`の隣に`ignored`として出力する。

### `internal/writer`(`WriteUnmatched`)

```go
func (s *Set) WriteUnmatched(source string, lineNum int, reason, raw string) error {
    ...
    fmt.Fprintf(s.unmatchedFile, "%s\t%d\t%s\t%s\n", source, lineNum, reason, raw)
    ...
}
```

## 3. `unmatched.txt`フォーマット変更(破壊的変更)

- **現状**: `<source>\t<lineNum>\t<raw>\n`(3列)
- **変更後**: `<source>\t<lineNum>\t<reason>\t<raw>\n`(4列)。`reason`は次のいずれか: `unmatched`(既存の「どのルールにもマッチしなかった」)、`ignored:pattern`、`ignored:max_length`、`ignored:invalid_utf8`、`ignored:empty`。
- これは`docs/reference.md`が明記しているファイル形式の破壊的変更であり、`awk -F'\t'`等で3列を前提にパースしている既存の外部スクリプトは列がずれて壊れる。`docs/reference.md`の該当箇所(rules.yaml構造の節、`mask:`の節の注記)を更新し、CHANGELOG相当の記載(READMEのリリースノートがあれば)にも明記する。

## 4. エラーハンドリング

- **`ignore.patterns`の正規表現がコンパイルできない**: 起動時エラー(既存の`pattern`/`mask`と同じ扱い)。
- **`max_length`が負数**: `Config.Validate()`で起動時エラー。
- **無視条件に一つも該当しない行**: 既存通りパターンマッチ・継続行判定に進む。無視機能追加による既存動作への影響はない(`ignore:`未設定時は`IgnoreConfig`がゼロ値になり、`Reason()`は常に空文字列を返す)。

## 5. 影響範囲

- `internal/rules/rules.go`: `IgnoreConfig`型、`Config.Ignore`追加、YAMLデコード、`Load()`でのパターンコンパイル
- `internal/rules/ignore.go`(新規): `IgnoreConfig.Reason()`
- `internal/rules/validate.go`: `max_length`のバリデーション追加
- `internal/convert/merge.go`: `fileCursor.nextLine()`のループ化、`writeUnmatchedLine`のシグネチャ変更(`reason`引数追加)、`fileCursor.ignored`カウンタ追加、`logFileProcessed`への`ignored`出力追加
- `internal/writer/writer.go`: `WriteUnmatched`のシグネチャ変更(`reason`引数追加)、書き込みフォーマットを4列に変更
- `schema/rules.schema.json`: `ignore:`のJSON Schema追加
- `README.md`/`docs/reference.md`: `ignore:`の書き方・意味論・具体例の追記、`unmatched.txt`フォーマット変更(3列→4列)の明記

## 6. テスト方針

- `internal/rules`: `ignore:`のYAMLデコード・パターンコンパイル。不正な正規表現、負数の`max_length`が起動時エラーになることを確認。`IgnoreConfig.Reason()`の単体テスト — 各条件が単独で発火すること、複数条件が同時に該当する行での優先順位(`empty → invalid_utf8 → max_length → patterns`)、どの条件にも該当しない行で空文字列を返すこと、`ignore:`未設定(ゼロ値)で常に空文字列を返すこと。
- `internal/convert`: `fileCursor.nextLine()`拡張のテスト — 無視対象行が`advance()`に渡らず`unmatched.txt`に理由付きで書かれること、継続行(`continuation`)がオープンな状態で無視行を挟んでもエントリが閉じないこと、`source_line`メタが無視行も含めて正しくカウントされること、`pending`経由の行(継続行処理で読み戻された行)が無視判定を再度受けないこと。
- `internal/writer`: `WriteUnmatched`が4列フォーマットで書き込むことの回帰テスト(既存テストの期待値更新を含む)。
- 回帰: `ignore:`未設定のrules.yamlで、`unmatched.txt`の`reason`列が常に`unmatched`になり、他の列・既存の挙動(パース結果、Parquet出力)が変わらないこと。
- README/docs: `ignore:`の書き方、4条件それぞれの意味、`unmatched.txt`フォーマット変更(3列→4列、既存スクリプトへの影響)を追記。
