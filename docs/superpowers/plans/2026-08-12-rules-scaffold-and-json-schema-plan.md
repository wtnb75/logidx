# rules.yamlのscaffoldコマンドとJSON Schema Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `logidx scaffold`(最小構成のrules.yaml雛形を標準出力に書く)と`logidx schema`(rules.yaml用JSON Schemaを標準出力に書く)の2つのサブコマンドを追加し、`schema/rules.schema.json`をリポジトリに新規追加する。

**Architecture:** どちらのコマンドも既存の`version`コマンドと同じ最小形(引数なし・`go:embed`した固定コンテンツをそのまま標準出力に書くだけ)。テンプレートYAMLは新設`internal/scaffold`パッケージに、JSON SchemaはGitHub raw URLをファイルパスそのままで公開できるようリポジトリルート直下`schema/`ディレクトリに置き、そこに新設する小さな`jsonschema`パッケージ(`schema/embed.go`)が同じディレクトリの`rules.schema.json`を`go:embed`して`cmd/logidx`へ供給する。

**Tech Stack:** Go 1.25, `embed`(標準ライブラリ)、`github.com/spf13/cobra`(既存)。新規の外部依存は追加しない。

## Global Constraints

- モジュールパスは`github.com/wtnb75/logidx`(go.mod)。全ての新規importはこのプレフィックス。
- JSON Schemaのdraftは`https://json-schema.org/draft/2020-12/schema`(design doc通り)。
- JSON Schemaの`$id`と、README/scaffold等で示す`# yaml-language-server: $schema=...`コメント例は、**`main`ブランチ固定のGitHub raw URL** `https://raw.githubusercontent.com/wtnb75/logidx/main/schema/rules.schema.json` を使う(ユーザー確認済み — バージョンタグ固定ではなく常に最新を指す)。
- **設計doc(`docs/superpowers/specs/2026-08-10-rules-scaffold-and-json-schema-design.md`)からの訂正が1点ある**: 同docのC節は「JSON Schemaの埋め込みは新規パッケージを作らず、`cmd/logidx`パッケージ内で`schema/rules.schema.json`を直接`//go:embed`する」としているが、Goの`//go:embed`パターンは埋め込み対象ファイルが**そのソースファイル自身のディレクトリ以下**にある必要があり、`..`や他ディレクトリへの絶対参照は使えない(`invalid pattern syntax`でコンパイルエラーになることを確認済み)。`cmd/logidx/main.go`からリポジトリルート直下の`schema/`を直接embedすることはできない。
  - 対応: `schema/rules.schema.json`はそのまま(raw URLをきれいに保つため)リポジトリルート直下に置き、同じ`schema/`ディレクトリに`embed.go`(`package jsonschema`)を新設してそこで`//go:embed rules.schema.json`する。`cmd/logidx`は`jsonschema.RulesSchema`をimportして使う。ファイル本体は1つのまま(`go:embed`が同一性を保証する design docの意図はそのまま満たす)。パッケージ名を`jsonschema`にする理由: 既存の`internal/schema`(Parquetスキーマ生成用)と紛らわしい名前を避けるため(design doc C節が触れている懸念と同じ)。
  - 設計docの該当箇所(B節「配置」の`logidx schema`コマンドの説明、およびC節「新設パッケージ」)は、Task 2の最初のステップで正確な内容に修正する(このリポジトリでは design doc の事後訂正は独立コミットで行う慣習がある — 例: commit `1968a9e`)。
- `internal/scaffold`・`schema`ともに、既存コマンド(`version`など)と同じく引数を取らない(`cobra.NoArgs`)。
- 新規サブコマンドは他の全コマンドと同じく`SilenceUsage: true, SilenceErrors: true`を設定する。
- `rules.Field`が現在サポートするキーは`type`/`format`/`key`/`extra`/`meta`/`replace`/`normalize`(`internal/rules/rules.go`・`internal/rules/validate.go`で確認済み)。design doc(2026-08-10時点)の書き方には`meta`が含まれていないが、これは design doc の後に追加された機能(`2026-08-11-source-location-fields-design.md`)なので、JSON Schemaには`meta`(enum: `source_file`/`source_line`)を含める。
- `compression.codec`の有効値は`uncompressed`/`snappy`/`gzip`/`brotli`/`zstd`/`lz4`(`internal/compression/compression.go`で確認済み)。
- JSON Schemaはフィールド間の意味的整合性(`rules.Validate`が担う分)を表現しない — design docのNon-goals通り。

---

## File Structure

- Create: `internal/scaffold/template.yaml` — 最小構成rules.yamlの雛形(コメント付き)。
- Create: `internal/scaffold/scaffold.go` — `template.yaml`を`go:embed`した`Template`定数。
- Test: `internal/scaffold/scaffold_test.go` — `Template`が`rules.Load`で読み込めることの検証。
- Create: `schema/rules.schema.json` — rules.yaml用JSON Schema(2020-12)。
- Create: `schema/embed.go` — `rules.schema.json`を`go:embed`した`jsonschema.RulesSchema`定数。
- Test: `schema/embed_test.go` — `RulesSchema`が妥当なJSONであることの検証。
- Modify: `cmd/logidx/main.go` — `newScaffoldCmd`・`newSchemaCmd`を追加し`root.AddCommand`に登録。
- Modify: `cmd/logidx/main_test.go` — `scaffold`/`schema`サブコマンドのCLIテストを追加。
- Modify: `docs/superpowers/specs/2026-08-10-rules-scaffold-and-json-schema-design.md` — go:embedレイアウトと`$schema`URL例の訂正(Task 2 Step 1)。
- Modify: `README.md` — `scaffold`/`schema`コマンドをCommandsテーブルに追加、エディタ連携(`$schema`コメント)のセクションを追加。

---

## Task 1: `internal/scaffold`パッケージと`logidx scaffold`コマンド

**Files:**
- Create: `internal/scaffold/template.yaml`
- Create: `internal/scaffold/scaffold.go`
- Test: `internal/scaffold/scaffold_test.go`
- Modify: `cmd/logidx/main.go`
- Modify: `cmd/logidx/main_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `rules.Load(path string) (*rules.Config, error)`(既存、`internal/rules/rules.go`)。
- Produces: `scaffold.Template string`(このタスクで新規定義) — Task 2のCLI配線では使わないが、後続の`newSchemaCmd`と対になる`newScaffoldCmd`が本タスクの成果物。

- [ ] **Step 1: 失敗するテストを書く — `internal/scaffold`**

`internal/scaffold/scaffold_test.go`:

```go
package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wtnb75/logidx/internal/rules"
)

func TestTemplateLoadsAsValidRulesConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(Template), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	cfg, err := rules.Load(path)
	if err != nil {
		t.Fatalf("rules.Load(scaffold template): %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(cfg.Rules))
	}
	if cfg.Rules[0].Name != "example" {
		t.Errorf("rule name = %q, want %q", cfg.Rules[0].Name, "example")
	}
	if len(cfg.Rules[0].Fields) != 2 {
		t.Fatalf("got %d fields, want 2", len(cfg.Rules[0].Fields))
	}
}

func TestTemplateIsNonEmpty(t *testing.T) {
	if Template == "" {
		t.Fatal("Template is empty")
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/scaffold/... -v`
Expected: FAIL — `internal/scaffold`パッケージが存在しない(`no Go files in ...`または`undefined: Template`)

- [ ] **Step 3: テンプレートと`Template`を実装する**

`internal/scaffold/template.yaml`:

```yaml
# logidx rules.yaml
#
# Each rule matches one log line (or one continuation-joined entry) against a
# regexp pattern, and defines how each captured value is typed/converted.
# See README.md for the full set of available options.
rules:
  - name: example           # matched lines go to <name>.parquet
    # named capture groups map to fields; ">-" folds these lines into one
    # pattern string, so each capture group can stay on its own line
    pattern: >-
      ^(?P<time>\S+)
      (?P<message>.*)$
    fields:
      time:
        type: timestamp      # string / int / float / timestamp
        format: iso8601       # required when type is timestamp
      message: string         # type-only shorthand: "field_name: type"
```

`internal/scaffold/scaffold.go`:

```go
// Package scaffold holds the fixed-content template that `logidx scaffold`
// prints: a minimal, commented rules.yaml a new user can start editing
// immediately. Embedding the real YAML file (instead of a Go string
// literal) lets scaffold_test.go run it through rules.Load and prove the
// template itself is never broken.
package scaffold

import _ "embed"

//go:embed template.yaml
var Template string
```

- [ ] **Step 4: テストを実行して通ることを確認する**

Run: `go test ./internal/scaffold/... -v`
Expected: PASS(両テスト)

- [ ] **Step 5: 失敗するテストを書く — `logidx scaffold`コマンド**

`cmd/logidx/main_test.go`の末尾に追加(先頭のimportブロックに`"github.com/wtnb75/logidx/internal/scaffold"`を追加する必要がある):

```go
func TestScaffoldCmd_WritesTemplateToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"scaffold"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != scaffold.Template {
		t.Errorf("stdout = %q, want scaffold.Template", stdout.String())
	}
}

func TestScaffoldCmd_RejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"scaffold", "extra"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2, stderr = %s", code, stderr.String())
	}
}
```

- [ ] **Step 6: テストを実行して失敗を確認する**

Run: `go test ./cmd/logidx/... -run TestScaffoldCmd -v`
Expected: FAIL — `unknown command "scaffold" for "logidx"`(exit code 2ではなく、`code != 0`のFatalで落ちる)

- [ ] **Step 7: `newScaffoldCmd`を実装し登録する**

`cmd/logidx/main.go`の先頭importブロックに追加:

```go
	"github.com/wtnb75/logidx/internal/scaffold"
```

`run`関数内、`root.AddCommand(newVersionCmd(stdout))`の直前に追加:

```go
	root.AddCommand(newScaffoldCmd(stdout))
```

ファイル末尾(`writeDstFile`の後)に追加:

```go
// newScaffoldCmd prints scaffold.Template unchanged - a minimal, commented
// rules.yaml a new user can copy and start editing, mirroring how
// newVersionCmd writes a single fixed value with no flags/args.
func newScaffoldCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:           "scaffold",
		Short:         "Print a minimal rules.yaml template to start from",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprint(stdout, scaffold.Template)
			return err
		},
	}
}
```

- [ ] **Step 8: テストを実行して通ることを確認する**

Run: `go test ./cmd/logidx/... -run TestScaffoldCmd -v`
Expected: PASS(両テスト)

- [ ] **Step 9: READMEに`scaffold`コマンドを追記する**

`README.md`のCommandsテーブル(`| logidx collapse ... |`の行の直後)に追加:

```markdown
| `logidx scaffold` | Print a minimal rules.yaml template to start from |
```

- [ ] **Step 10: リポジトリ全体をビルド・テストする**

Run: `go build ./... && go test ./...`
Expected: ビルド成功、全テストPASS

- [ ] **Step 11: コミット**

```bash
git add internal/scaffold/ cmd/logidx/main.go cmd/logidx/main_test.go README.md
git commit -m "feat(scaffold): add internal/scaffold template and logidx scaffold command"
```

---

## Task 2: `schema/rules.schema.json`と`logidx schema`コマンド

**Files:**
- Modify: `docs/superpowers/specs/2026-08-10-rules-scaffold-and-json-schema-design.md`
- Create: `schema/rules.schema.json`
- Create: `schema/embed.go`
- Test: `schema/embed_test.go`
- Modify: `cmd/logidx/main.go`
- Modify: `cmd/logidx/main_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: なし(Task 1とは独立)。
- Produces: `jsonschema.RulesSchema string`(パッケージ`github.com/wtnb75/logidx/schema`) — `newSchemaCmd`が使う。

- [ ] **Step 1: 設計docを訂正する**

`docs/superpowers/specs/2026-08-10-rules-scaffold-and-json-schema-design.md`の該当2箇所を修正する。

1箇所目、B節「配置」(既存の2行目、`logidx schema`コマンドの説明)を置き換え:

- 旧: `CLIからも取得できるよう\`logidx schema\`コマンドを追加し、\`go:embed\`で同じファイルを埋め込んで標準出力に書く(ファイル本体とCLI出力が同一内容であることを\`go:embed\`の性質上保証できる)。`
- 新: `CLIからも取得できるよう\`logidx schema\`コマンドを追加し、\`go:embed\`で同じファイルを埋め込んで標準出力に書く(ファイル本体とCLI出力が同一内容であることを\`go:embed\`の性質上保証できる)。ただし\`go:embed\`は埋め込み元ファイルがソースファイル自身のディレクトリ以下にある必要があるため、\`cmd/logidx\`から\`schema/rules.schema.json\`を直接embedすることはできない。埋め込みは\`schema/rules.schema.json\`と同じディレクトリに置く\`schema/embed.go\`(パッケージ名\`jsonschema\`)が担う。`

2箇所目、README更新のyaml-language-server例を置き換え:

- 旧:
```
エディタ連携のため、`rules.yaml`先頭に付けるyaml-language-server用コメントの書き方を追記する:

\`\`\`yaml
# yaml-language-server: $schema=./schema/rules.schema.json
\`\`\`
```
- 新:
```
エディタ連携のため、`rules.yaml`先頭に付けるyaml-language-server用コメントの書き方を追記する。ローカルチェックアウトのファイルパスではなく、GitHub raw URL(mainブランチ固定)を使う — rules.yamlはlogidxのリポジトリとは別の場所に置かれるのが通常のため:

\`\`\`yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/wtnb75/logidx/main/schema/rules.schema.json
\`\`\`
```

3箇所目、C節「新設パッケージ」の該当文を置き換え:

- 旧: `JSON Schemaの埋め込みは新規パッケージを作らず、\`cmd/logidx\`パッケージ内で\`schema/rules.schema.json\`を直接\`//go:embed\`する(\`newSchemaCmd\`からのみ使う1ファイルのために別パッケージを起こす必要はない。既存の\`internal/schema\`パッケージ(Parquetスキーマ生成用)とは名前が紛らわしいため区別する: JSON Schemaファイルは常にフルパス\`schema/rules.schema.json\`または\`rules.schema.json\`のように参照し、\`internal/schema\`は触らない)。`
- 新: `\`go:embed\`はソースファイル自身のディレクトリ以下しか埋め込めないため、\`cmd/logidx\`から直接embedすることはできない。\`schema/rules.schema.json\`と同じ\`schema/\`ディレクトリに\`embed.go\`(パッケージ名\`jsonschema\` — 既存の\`internal/schema\`(Parquetスキーマ生成用)と紛らわしい名前を避けるため)を置き、\`//go:embed rules.schema.json\`で埋め込む。\`cmd/logidx\`は\`jsonschema.RulesSchema\`をimportして使う。ファイル本体は1つのまま。`

- [ ] **Step 2: 失敗するテストを書く — `schema`パッケージ**

`schema/embed_test.go`:

```go
package jsonschema

import (
	"encoding/json"
	"testing"
)

func TestRulesSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(RulesSchema), &v); err != nil {
		t.Fatalf("RulesSchema is not valid JSON: %v", err)
	}
}

func TestRulesSchemaIsNonEmpty(t *testing.T) {
	if RulesSchema == "" {
		t.Fatal("RulesSchema is empty")
	}
}
```

- [ ] **Step 3: テストを実行して失敗を確認する**

Run: `go test ./schema/... -v`
Expected: FAIL — `schema`ディレクトリにGoファイルがない、または`undefined: RulesSchema`

- [ ] **Step 4: `rules.schema.json`と`embed.go`を実装する**

`schema/rules.schema.json`:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://raw.githubusercontent.com/wtnb75/logidx/main/schema/rules.schema.json",
  "$comment": "Hand-maintained: keep in sync with internal/rules.Validate by hand when adding/changing validation rules (see the design doc's Non-goals). Structural/type checks only - semantic checks (e.g. field names matching pattern capture groups) are rules.Validate's job, not this schema's.",
  "title": "logidx rules.yaml",
  "type": "object",
  "required": ["rules"],
  "properties": {
    "rules": {
      "type": "array",
      "minItems": 1,
      "items": { "$ref": "#/$defs/rule" }
    },
    "compression": { "$ref": "#/$defs/compression" },
    "row_group": { "$ref": "#/$defs/row_group" }
  },
  "additionalProperties": false,
  "$defs": {
    "fieldType": {
      "type": "string",
      "enum": ["string", "int", "float", "timestamp"]
    },
    "replaceOrNormalizeRule": {
      "type": "object",
      "required": ["pattern", "value"],
      "properties": {
        "pattern": { "type": "string" },
        "value": { "type": "string" }
      },
      "additionalProperties": false
    },
    "field": {
      "oneOf": [
        { "$ref": "#/$defs/fieldType" },
        {
          "type": "object",
          "required": ["type"],
          "properties": {
            "type": { "$ref": "#/$defs/fieldType" },
            "format": { "type": "string" },
            "key": { "type": "string" },
            "extra": { "type": "boolean" },
            "meta": { "type": "string", "enum": ["source_file", "source_line"] },
            "replace": {
              "type": "array",
              "items": { "$ref": "#/$defs/replaceOrNormalizeRule" }
            },
            "normalize": {
              "type": "array",
              "items": { "$ref": "#/$defs/replaceOrNormalizeRule" }
            }
          },
          "additionalProperties": false
        }
      ]
    },
    "structured": {
      "type": "object",
      "required": ["source", "format"],
      "properties": {
        "source": { "type": "string" },
        "format": { "type": "string" }
      },
      "additionalProperties": false
    },
    "rule": {
      "type": "object",
      "required": ["name"],
      "properties": {
        "name": { "type": "string" },
        "preset": { "type": "string" },
        "pattern": { "type": "string" },
        "fields": {
          "type": "object",
          "minProperties": 1,
          "additionalProperties": { "$ref": "#/$defs/field" }
        },
        "structured": { "$ref": "#/$defs/structured" },
        "continuation": { "type": "string" }
      },
      "additionalProperties": false,
      "oneOf": [
        {
          "required": ["preset"],
          "not": { "anyOf": [{ "required": ["pattern"] }, { "required": ["fields"] }] }
        },
        {
          "required": ["pattern", "fields"],
          "not": { "required": ["preset"] }
        }
      ]
    },
    "compression": {
      "type": "object",
      "properties": {
        "codec": {
          "type": "string",
          "enum": ["uncompressed", "snappy", "gzip", "brotli", "zstd", "lz4"]
        },
        "level": { "type": "integer" }
      },
      "additionalProperties": false
    },
    "row_group": {
      "type": "object",
      "properties": {
        "max_rows": { "type": "integer", "exclusiveMinimum": 0 }
      },
      "additionalProperties": false
    }
  }
}
```

`schema/embed.go`:

```go
// Package jsonschema embeds the JSON Schema (schema/rules.schema.json) that
// describes rules.yaml, for editor integration (yaml-language-server) and
// for `logidx schema` to print. Named jsonschema, not schema, to avoid
// confusion with internal/schema (which builds Parquet schemas, an
// unrelated concept).
package jsonschema

import _ "embed"

//go:embed rules.schema.json
var RulesSchema string
```

- [ ] **Step 5: テストを実行して通ることを確認する**

Run: `go test ./schema/... -v`
Expected: PASS(両テスト)

- [ ] **Step 6: 失敗するテストを書く — `logidx schema`コマンド**

`cmd/logidx/main_test.go`の末尾に追加(先頭のimportブロックに`jsonschema "github.com/wtnb75/logidx/schema"`を追加する必要がある):

```go
func TestSchemaCmd_WritesJSONSchemaToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"schema"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != jsonschema.RulesSchema {
		t.Errorf("stdout = %q, want jsonschema.RulesSchema", stdout.String())
	}
}

func TestSchemaCmd_RejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"schema", "extra"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2, stderr = %s", code, stderr.String())
	}
}
```

- [ ] **Step 7: テストを実行して失敗を確認する**

Run: `go test ./cmd/logidx/... -run TestSchemaCmd -v`
Expected: FAIL — `unknown command "schema" for "logidx"`

- [ ] **Step 8: `newSchemaCmd`を実装し登録する**

`cmd/logidx/main.go`の先頭importブロックに追加(`"github.com/wtnb75/logidx/internal/scaffold"`の隣):

```go
	jsonschema "github.com/wtnb75/logidx/schema"
```

`run`関数内、`root.AddCommand(newScaffoldCmd(stdout))`の直後に追加:

```go
	root.AddCommand(newSchemaCmd(stdout))
```

ファイル末尾(`newScaffoldCmd`の後)に追加:

```go
// newSchemaCmd prints jsonschema.RulesSchema unchanged, for editor
// integration (e.g. `# yaml-language-server: $schema=...` in rules.yaml) or
// for saving a local copy - see newScaffoldCmd for the mirrored rules.yaml
// template command.
func newSchemaCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:           "schema",
		Short:         "Print the JSON Schema for rules.yaml (for editor integration)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprint(stdout, jsonschema.RulesSchema)
			return err
		},
	}
}
```

- [ ] **Step 9: テストを実行して通ることを確認する**

Run: `go test ./cmd/logidx/... -run TestSchemaCmd -v`
Expected: PASS(両テスト)

- [ ] **Step 10: READMEにschemaコマンドとエディタ連携セクションを追記する**

`README.md`のCommandsテーブル、Step 9(Task 1)で追加した`scaffold`行の直後に追加:

```markdown
| `logidx schema` | Print the JSON Schema for rules.yaml (for editor integration) |
```

Commandsテーブルの直後、`Run \`logidx <command> --help\`...`の段落の後に新しいセクションを追加:

```markdown
## Editor integration

`logidx schema` prints a JSON Schema for `rules.yaml` that editors with a yaml-language-server integration (e.g. VS Code's YAML extension) can use for autocompletion and type checking. Point to it from the top of your `rules.yaml`:

\`\`\`yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/wtnb75/logidx/main/schema/rules.schema.json
\`\`\`

The schema covers syntax and types only - semantic checks (e.g. a field name matching a named capture group in `pattern`) are still only caught by `logidx import` itself.
```

- [ ] **Step 11: リポジトリ全体をビルド・テストする**

Run: `go build ./... && go test ./...`
Expected: ビルド成功、全テストPASS

- [ ] **Step 12: コミット**

分割して2回コミットする(設計doc訂正と実装は別関心事のため):

```bash
git add docs/superpowers/specs/2026-08-10-rules-scaffold-and-json-schema-design.md
git commit -m "docs: correct rules-scaffold-and-json-schema design for go:embed directory constraint and use a raw GitHub URL for \$schema"
```

```bash
git add schema/ cmd/logidx/main.go cmd/logidx/main_test.go README.md
git commit -m "feat(schema): add rules.yaml JSON Schema and logidx schema command"
```

---

## Self-Review Notes

- Spec coverage: A節(scaffoldコマンド・テンプレート・テスト)→Task 1。B節(JSON Schema配置・構造・README更新)→Task 2。C節(main.goへの登録・新設パッケージ)→Task 1 Step 7、Task 2 Step 8。D節(テスト)→各TaskのStep 1-2および5-7に相当するテストで充足。
- Non-goals(`--out`フラグ、機能網羅サンプル、ドリフト自動検知、意味的整合性チェック)はどのタスクにも含めていない。
- 設計doc記載の`go:embed`直接埋め込み方針はGoの言語仕様上不可能なため、Task 2 Step 1で設計doc自体を訂正し、実装(`schema/embed.go`+`jsonschema`パッケージ)をそれに合わせた。
- 型・シグネチャの一貫性: `scaffold.Template`(Task 1で定義)と`jsonschema.RulesSchema`(Task 2で定義)は共に`string`型で、`newScaffoldCmd`/`newSchemaCmd`ともに`fmt.Fprint(stdout, ...)`で書き込む同一パターン。両コマンドとも`newVersionCmd(stdout io.Writer)`と同じシグネチャ(`stdout`のみ、`stderr`不要)。
