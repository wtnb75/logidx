# 同一スキーマの複数Parquetファイルを結合する `cat` サブコマンド Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 同一スキーマを持つ複数のParquetファイルを1つへ結合する`logidx cat`サブコマンドを追加し、既存の`logidx copy`(単一ファイル複製)を`cat`に統合・廃止する。タイムスタンプ型の列があれば全ファイルをまたいで時系列順にマージし、無ければ引数順に連結する。

**Architecture:** `internal/pqcopy`を`internal/pqcat`へ置き換える。`internal/schema`に`Equal`(スキーマ完全一致検証)と`ForceCompression`(既存の`pqcopy`内非公開関数を公開・移設)を追加し、`pqcat.Cat`がそれらを使ってスキーマ検証と圧縮コーデック強制を行う。マージは`internal/convert/merge.go`のk-wayストリーミングマージ(`fileCursor`+最小ヒープ)と同じアーキテクチャを、ログ行ではなくParquetの行(`map[string]any`、タイムスタンプ列はマイクロ秒int64)に適用する。

**Tech Stack:** Go 1.x, `github.com/parquet-go/parquet-go`, `container/heap`(標準ライブラリ)。新規の外部依存は追加しない。

## Global Constraints

- スキーマは完全一致必須。不一致は起動時エラー(自動変換・リマップはしない) — 設計のNon-goals。
- マージキーの明示指定はできない。`import`と同様、スキーマからの自動検出のみ(最初に見つかったtimestamp型の列) — 設計のNon-goals。
- 同時オープンするファイル記述子数の上限対策はしない(既存の`import`の複数ファイルマージと同じ方針) — 設計のNon-goals。
- `parquet.GenericReader[map[string]any]`は、timestamp型の列を`time.Time`ではなく**マイクロ秒epochを表す`int64`**として返す。マージキーの取得・比較は`int64`のまま行う(`time.Time`への変換はしない)。
- `internal/pqcopy`パッケージは本プランの完了時点で削除する(`internal/pqcat`に置き換え)。
- CLIサマリの完全一致フォーマット: `concatenated %d files, %d rows: %s -> %s (%s)`(ファイルパスはカンマ区切り)、続けて既存コマンド共通の`, %d/%d bytes (%.1f%%, %.2fx)`(圧縮率が計算できる場合のみ)。
- 設計doc(`docs/superpowers/specs/2026-08-09-parquet-cat-subcommand-design.md`)のスキーマ不一致エラーメッセージ例(`schema mismatch: c.parquet does not match a.parquet (canonical): column 3: name "code" (c.parquet) vs "status" (a.parquet)`)は列位置ごとに両ファイル名を埋め込む形になっているが、`schema.Equal`のシグネチャは`Equal(a, b *parquet.Schema) error`固定でファイルパスを一切知らない。したがって値ごとの`(ファイル名)`注釈は付けない簡略形にする: `schema.Equal`は`column 3: name "code" vs "status"`のような一般形のエラーを返し、`pqcat.Cat`がそれを`schema mismatch: %s does not match %s (canonical): %w`でラップしてファイルパス2つを一度だけ付加する。設計docの「テスト方針」が実際に要求しているのは「エラーメッセージにファイル名と列位置が含まれること」であり、両方ともこの簡略形で満たされる。

---

## File Structure

- Modify: `internal/schema/schema.go` — `Equal`(新規)、`ForceCompression`(新規、`pqcopy`から移設・公開)を追加。
- Test: `internal/schema/schema_test.go` — `Equal`・`ForceCompression`の単体テスト追加。
- Modify: `internal/pqcopy/pqcopy.go` — 非公開`forceCompression`を削除し、`schema.ForceCompression`呼び出しに置き換え(Task 1でのみ。Task 4で`pqcopy`自体を削除)。
- Create: `internal/pqcat/pqcat.go` — `SourceCodec`(`pqcopy`から移設)、`Cat`(スキーマ検証付き連結、Task 2ではマージキーなし)。
- Create: `internal/pqcat/merge.go` — `rowCursor`・`mergeRows`(タイムスタンプ順k-wayマージ、Task 3)。
- Create: `internal/pqcat/doc.go` — パッケージdocコメント。
- Test: `internal/pqcat/pqcat_test.go` — `SourceCodec`・`Cat`(連結・圧縮・エラー系)の単体テスト。
- Test: `internal/pqcat/merge_test.go` — マージ順・タイブレーク・複数バッチの単体テスト(Task 3)。
- Modify: `cmd/logidx/main.go` — `newCopyCmd`を削除、`newCatCmd`を追加。
- Modify: `cmd/logidx/main_test.go` — `TestRun_Copy*`を`TestRun_Cat*`に置き換え・拡張。
- Delete: `internal/pqcopy/pqcopy.go`、`internal/pqcopy/doc.go`、`internal/pqcopy/pqcopy_test.go`(Task 4)。
- Modify: `README.md` — `copy`コマンドの説明を`cat`コマンドの説明に置き換える(Task 6)。

---

## Task 1: `internal/schema` — `Equal`と`ForceCompression`

**Files:**
- Modify: `internal/schema/schema.go`
- Modify: `internal/pqcopy/pqcopy.go`
- Test: `internal/schema/schema_test.go`

**Interfaces:**
- Consumes: `parquet.Schema`/`parquet.Node`/`parquet.Field`(既存、`github.com/parquet-go/parquet-go`)、`schema.NewOrderedGroup`(既存、`internal/schema/schema.go`)。
- Produces:
  - `func Equal(a, b *parquet.Schema) error` — 列数・列名・型・repetitionを先頭から順に比較し、全て一致すれば`nil`。最初に見つかった不一致を、列位置を含む一般形のエラー文字列で返す(ファイルパスは含まない — Global Constraints参照)。
  - `func ForceCompression(node parquet.Node, codec compress.Codec) parquet.Node` — `pqcopy`の非公開`forceCompression`と同一実装。Task 2の`pqcat.Cat`と、この後の`pqcopy.Copy`(削除までの間)の両方が使う。

- [ ] **Step 1: 失敗するテストを書く — `ForceCompression`**

`internal/schema/schema_test.go`の末尾に追加(先頭のimportに`"github.com/parquet-go/parquet-go/compress/zstd"`を追加する必要がある):

```go
func TestForceCompression_OverridesLeafCodecAndPreservesColumnOrder(t *testing.T) {
	fields := []rules.Field{
		{Name: "zzz_last", Type: "string"},
		{Name: "aaa_first", Type: "int"},
	}
	built, err := Build("row", fields)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	codec := &zstd.Codec{Level: zstd.DefaultLevel}
	forced := ForceCompression(built.Schema, codec)
	forcedSchema := parquet.NewSchema(built.Schema.Name(), forced)

	var gotOrder []string
	for _, f := range forcedSchema.Fields() {
		gotOrder = append(gotOrder, f.Name())
	}
	want := []string{"zzz_last", "aaa_first"}
	if len(gotOrder) != len(want) {
		t.Fatalf("got %d fields, want %d", len(gotOrder), len(want))
	}
	for i, name := range want {
		if gotOrder[i] != name {
			t.Errorf("field order[%d] = %q, want %q (declaration order, not alphabetical)", i, gotOrder[i], name)
		}
	}

	path := filepath.Join(t.TempDir(), "test.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := parquet.NewGenericWriter[map[string]any](f, forcedSchema)
	if _, err := w.Write([]map[string]any{{"zzz_last": "z", "aaa_first": int64(1)}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = rf.Close() }()
	fi, err := rf.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(rf, fi.Size())
	if err != nil {
		t.Fatalf("open parquet: %v", err)
	}
	if got := pf.Metadata().RowGroups[0].Columns[0].MetaData.Codec.String(); got != "ZSTD" {
		t.Errorf("on-disk codec = %q, want ZSTD", got)
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/schema/... -run TestForceCompression -v`
Expected: FAIL — `undefined: ForceCompression`

- [ ] **Step 3: `ForceCompression`を実装する**

`internal/schema/schema.go`の先頭のimportに`"github.com/parquet-go/parquet-go/compress"`を追加:

```go
import (
	"fmt"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress"

	"logidx/internal/rules"
)
```

`TypeName`関数の後(ファイル末尾)に追加:

```go
// ForceCompression rebuilds node's schema tree with every leaf column
// wrapped to report codec from Compression(), overriding whatever codec (if
// any) the original leaf reported. It preserves node.Fields()'s order via
// NewOrderedGroup - a plain parquet.Group is a map and would otherwise
// silently re-alphabetize columns, which matters here because callers pass
// in a schema whose Fields() order came from an existing on-disk file (see
// pqcat.Cat, and formerly pqcopy.Copy).
func ForceCompression(node parquet.Node, codec compress.Codec) parquet.Node {
	if node.Leaf() {
		return parquet.Compressed(node, codec)
	}

	fields := node.Fields()
	names := make([]string, len(fields))
	group := make(map[string]parquet.Node, len(fields))
	for i, f := range fields {
		names[i] = f.Name()
		group[f.Name()] = ForceCompression(f, codec)
	}

	out := NewOrderedGroup(group, names)
	switch {
	case node.Optional():
		out = parquet.Optional(out)
	case node.Repeated():
		out = parquet.Repeated(out)
	}
	return out
}
```

- [ ] **Step 4: テストを実行して通ることを確認する**

Run: `go test ./internal/schema/... -run TestForceCompression -v`
Expected: PASS

- [ ] **Step 5: `pqcopy.Copy`を`schema.ForceCompression`を使うように書き換える**

`internal/pqcopy/pqcopy.go`から非公開の`forceCompression`関数(ファイル末尾、128-156行目)を削除し、`Copy`内の呼び出し(97行目)を書き換える:

```go
	dstSchema := parquet.NewSchema(pf.Schema().Name(), schema.ForceCompression(pf.Schema(), comp.CodecInstance()))
```

`compress`パッケージのimportは`pqcopy.go`からは不要になる(`ForceCompression`の型シグネチャが`schema`パッケージ側にあるため)ので、importブロックから`"github.com/parquet-go/parquet-go/compress"`を削除する。`"logidx/internal/schema"`のimportは既存のまま残す(`schema.ForceCompression`の呼び出しに必要)。

- [ ] **Step 6: `pqcopy`の既存テストを実行して壊れていないことを確認する**

Run: `go test ./internal/pqcopy/... -v`
Expected: PASS(全テスト、特に`TestCopy_ChangesCompressionCodec`と`TestCopy_PreservesColumnOrder`が`ForceCompression`経由でも同じ結果になることを確認)

- [ ] **Step 7: 失敗するテストを書く — `Equal`**

`internal/schema/schema_test.go`の末尾に追加:

```go
func TestEqual_IdenticalSchemasReturnNil(t *testing.T) {
	fields := []rules.Field{{Name: "a", Type: "string"}, {Name: "b", Type: "int"}}
	built1, err := Build("x", fields)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	built2, err := Build("y", fields)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if err := Equal(built1.Schema, built2.Schema); err != nil {
		t.Errorf("Equal returned error for identical column layouts: %v", err)
	}
}

func TestEqual_ColumnNameMismatchIsError(t *testing.T) {
	a, err := Build("a", []rules.Field{{Name: "status", Type: "int"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := Build("b", []rules.Field{{Name: "code", Type: "int"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	err = Equal(a.Schema, b.Schema)
	if err == nil {
		t.Fatal("expected error for column name mismatch")
	}
	if !strings.Contains(err.Error(), "status") || !strings.Contains(err.Error(), "code") {
		t.Errorf("expected error to name both columns, got: %v", err)
	}
	if !strings.Contains(err.Error(), "column 0") {
		t.Errorf("expected error to name the column position, got: %v", err)
	}
}

func TestEqual_ColumnTypeMismatchIsError(t *testing.T) {
	a, err := Build("a", []rules.Field{{Name: "status", Type: "int"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := Build("b", []rules.Field{{Name: "status", Type: "string"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	err = Equal(a.Schema, b.Schema)
	if err == nil {
		t.Fatal("expected error for column type mismatch")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Errorf("expected error to name the mismatched column, got: %v", err)
	}
}

func TestEqual_ColumnCountMismatchIsError(t *testing.T) {
	a, err := Build("a", []rules.Field{{Name: "status", Type: "int"}, {Name: "path", Type: "string"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := Build("b", []rules.Field{{Name: "status", Type: "int"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	err = Equal(a.Schema, b.Schema)
	if err == nil {
		t.Fatal("expected error for column count mismatch")
	}
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "1") {
		t.Errorf("expected error to name both column counts, got: %v", err)
	}
}
```

`internal/schema/schema_test.go`の先頭importに`"strings"`が無ければ追加する(既存のimportブロックを確認する — 現状すでに`"strings"`はimport済みのはず)。

- [ ] **Step 8: テストを実行して失敗を確認する**

Run: `go test ./internal/schema/... -run TestEqual -v`
Expected: FAIL — `undefined: Equal`

- [ ] **Step 9: `Equal`を実装する**

`internal/schema/schema.go`のファイル末尾(`ForceCompression`の後)に追加:

```go
// Equal compares two flat (non-nested) Parquet schemas column by column, in
// declared order: column count, then each column's name, type, and
// repetition (optional/repeated/required - the same three-way
// classification pqinfo.ColumnInfo uses). It returns nil if every column
// matches, or an error describing the first mismatch found (by position).
// The returned error names columns/types/counts but not file paths - Equal
// only ever sees two *parquet.Schema values, not where they came from;
// callers that compare schemas from named files (see pqcat.Cat) wrap this
// error with %w to add that context. Like pqinfo, this only supports flat
// schemas, matching every file this CLI itself writes.
func Equal(a, b *parquet.Schema) error {
	af, bf := a.Fields(), b.Fields()
	if len(af) != len(bf) {
		return fmt.Errorf("column count %d vs %d", len(af), len(bf))
	}
	for i := range af {
		if af[i].Name() != bf[i].Name() {
			return fmt.Errorf("column %d: name %q vs %q", i, af[i].Name(), bf[i].Name())
		}
		if af[i].Type().String() != bf[i].Type().String() {
			return fmt.Errorf("column %d (%q): type %q vs %q", i, af[i].Name(), af[i].Type(), bf[i].Type())
		}
		if repetitionOf(af[i]) != repetitionOf(bf[i]) {
			return fmt.Errorf("column %d (%q): repetition %q vs %q", i, af[i].Name(), repetitionOf(af[i]), repetitionOf(bf[i]))
		}
	}
	return nil
}

// repetitionOf classifies field the same way pqinfo.ColumnInfo does
// (optional/repeated/required), for use in Equal's mismatch messages.
func repetitionOf(field parquet.Field) string {
	switch {
	case field.Optional():
		return "optional"
	case field.Repeated():
		return "repeated"
	default:
		return "required"
	}
}
```

- [ ] **Step 10: テストを実行して通ることを確認する**

Run: `go test ./internal/schema/... -v`
Expected: PASS(全テスト)

- [ ] **Step 11: リポジトリ全体をビルド・テストする**

Run: `go build ./... && go test ./...`
Expected: ビルド成功、全テストPASS

- [ ] **Step 12: コミット**

```bash
git add internal/schema/schema.go internal/schema/schema_test.go internal/pqcopy/pqcopy.go
git commit -m "feat(schema): add Equal and ForceCompression (moved from pqcopy), for reuse by the upcoming cat subcommand"
```

---

## Task 2: `internal/pqcat` — `SourceCodec`と`Cat`(マージキーなし連結)

**Files:**
- Create: `internal/pqcat/pqcat.go`
- Create: `internal/pqcat/doc.go`
- Test: `internal/pqcat/pqcat_test.go`

**Interfaces:**
- Consumes: `schema.Equal`・`schema.ForceCompression`(Task 1)、`compression.Settings`(既存、`internal/compression`)、`rowgroup.Settings`(既存、`internal/rowgroup`)。
- Produces:
  - `func SourceCodec(path string) (string, error)` — `pqcopy.SourceCodec`と同一実装。
  - `func Cat(srcPaths []string, dstPath string, comp compression.Settings, rg rowgroup.Settings) (rows int64, err error)` — このタスクの時点では、`srcPaths`を引数順に読み、常に単純連結する(マージキー検出・タイムスタンプ順マージはTask 3で追加)。スキーマ不一致・出力パス重複・入力ファイル欠損はこのタスクで検出・エラーになる。
  - `const batchSize = 1000`(非公開) — Task 3の`merge.go`も同じ定数を使う。

- [ ] **Step 1: パッケージdocを書く**

`internal/pqcat/doc.go`:

```go
// Package pqcat concatenates one or more Parquet files sharing the exact
// same schema into a single Parquet file, optionally applying a different
// compression codec, level, or row group size. If the shared schema has a
// timestamp-typed column, rows are merged into ascending order by that
// column across every input file instead of being concatenated in argument
// order (see merge.go).
package pqcat
```

- [ ] **Step 2: 失敗するテストを書く — 単一ファイルの行・列順保持**

`internal/pqcat/pqcat_test.go`:

```go
package pqcat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/compression"
	"logidx/internal/rowgroup"
)

// writeTestParquet writes rows (a slice of any parquet-taggable struct
// type) to path using rows[0]'s inferred schema, so both this file's
// simple fixed-schema tests and its deliberately-mismatched-schema tests
// (different struct types) can share one helper.
func writeTestParquet[T any](t *testing.T, path string, rows []T, opts ...parquet.WriterOption) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	sch := parquet.SchemaOf(rows[0])
	allOpts := append([]parquet.WriterOption{sch}, opts...)
	w := parquet.NewGenericWriter[T](f, allOpts...)
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file %s: %v", path, err)
	}
}

type testRow struct {
	Name  string `parquet:"name"`
	Count int64  `parquet:"count"`
}

// readAllRows reads dst as map[string]any, matching how pqcat.Cat itself
// reads and writes rows; reading a pqcat-produced file back with a
// struct-typed GenericReader panics (parquet-go reconstructs based on the
// schema's original Go-type hint, which Cat leaves as map-shaped).
func readAllRows(t *testing.T, path string) []testRow {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		t.Fatalf("open parquet: %v", err)
	}

	reader := parquet.NewGenericReader[map[string]any](pf, pf.Schema())
	defer func() { _ = reader.Close() }()

	var rows []testRow
	buf := make([]map[string]any, 10)
	for {
		for i := range buf {
			buf[i] = map[string]any{}
		}
		n, err := reader.Read(buf)
		for _, m := range buf[:n] {
			rows = append(rows, testRow{Name: m["name"].(string), Count: m["count"].(int64)})
		}
		if err != nil {
			break
		}
	}
	return rows
}

func TestCat_SingleFilePreservesRowsAndColumnOrder(t *testing.T) {
	dir := t.TempDir()
	want := []testRow{{Name: "a", Count: 1}, {Name: "b", Count: 2}, {Name: "c", Count: 3}}
	src := filepath.Join(dir, "src.parquet")
	writeTestParquet(t, src, want)
	dst := filepath.Join(dir, "dst.parquet")

	rows, err := Cat([]string{src}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{})
	if err != nil {
		t.Fatalf("Cat: %v", err)
	}
	if rows != int64(len(want)) {
		t.Errorf("rows = %d, want %d", rows, len(want))
	}

	got := readAllRows(t, dst)
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	f, err := os.Open(dst)
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		t.Fatalf("open parquet: %v", err)
	}
	wantCols := []string{"name", "count"}
	var gotCols []string
	for _, field := range pf.Schema().Fields() {
		gotCols = append(gotCols, field.Name())
	}
	if len(gotCols) != len(wantCols) {
		t.Fatalf("got %d columns, want %d", len(gotCols), len(wantCols))
	}
	for i, name := range wantCols {
		if gotCols[i] != name {
			t.Errorf("column order = %v, want %v (source declaration order, not alphabetical)", gotCols, wantCols)
			break
		}
	}
}
```

- [ ] **Step 3: テストを実行して失敗を確認する**

Run: `go test ./internal/pqcat/... -run TestCat_SingleFile -v`
Expected: FAIL — `undefined: Cat`

- [ ] **Step 4: `SourceCodec`と`Cat`(マージキーなし)を実装する**

`internal/pqcat/pqcat.go`:

```go
package pqcat

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/compression"
	"logidx/internal/rowgroup"
	"logidx/internal/schema"
)

// batchSize is how many rows are read from a source and written to the
// destination per Read/Write call, both for plain concatenation (this
// file) and for the timestamp-ordered merge (merge.go).
const batchSize = 1000

// SourceCodec reads the compression codec used in path's first column
// chunk and maps it to the name used in compression.Settings.Codec. It
// returns "" if the file has no rows/columns to inspect, or if it uses a
// codec compression.Settings does not support selecting explicitly (LZO or
// deprecated LZ4) - either way, callers should treat "" as "unknown" and
// fall back to their own default.
func SourceCodec(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat source: %w", err)
	}

	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		return "", fmt.Errorf("parse source parquet footer: %w", err)
	}

	meta := pf.Metadata()
	if len(meta.RowGroups) == 0 || len(meta.RowGroups[0].Columns) == 0 {
		return "", nil
	}

	name, _ := compression.NameFromFormatCodec(meta.RowGroups[0].Columns[0].MetaData.Codec)
	return name, nil
}

// Cat reads every row from each file in srcPaths and writes them to a new
// Parquet file at dstPath with comp's compression codec/level and rg's row
// group limit applied. All srcPaths must share the exact same schema
// (column names, types, order); the first path's schema is canonical, and
// every other file's schema must equal it (see schema.Equal) or Cat
// returns an error without creating dstPath. If the canonical schema has a
// timestamp-typed column, rows from every file are merged into ascending
// order by that column instead of being concatenated in argument order -
// see detectMergeKey and mergeRows in merge.go. It returns the total
// number of rows written.
func Cat(srcPaths []string, dstPath string, comp compression.Settings, rg rowgroup.Settings) (rows int64, err error) {
	for _, src := range srcPaths {
		if filepath.Clean(src) == filepath.Clean(dstPath) {
			return 0, fmt.Errorf("output path must differ from every input file: %s", src)
		}
	}

	pf := make([]*parquet.File, len(srcPaths))
	files := make([]*os.File, len(srcPaths))
	defer func() {
		for _, f := range files {
			if f != nil {
				_ = f.Close()
			}
		}
	}()

	for i, src := range srcPaths {
		f, openErr := os.Open(src)
		if openErr != nil {
			return 0, fmt.Errorf("open %s: %w", src, openErr)
		}
		files[i] = f

		fi, statErr := f.Stat()
		if statErr != nil {
			return 0, fmt.Errorf("stat %s: %w", src, statErr)
		}
		parsed, parseErr := parquet.OpenFile(f, fi.Size())
		if parseErr != nil {
			return 0, fmt.Errorf("parse %s parquet footer: %w", src, parseErr)
		}
		pf[i] = parsed
	}

	canonical := pf[0].Schema()
	for i := 1; i < len(pf); i++ {
		if eqErr := schema.Equal(canonical, pf[i].Schema()); eqErr != nil {
			return 0, fmt.Errorf("schema mismatch: %s does not match %s (canonical): %w", srcPaths[i], srcPaths[0], eqErr)
		}
	}

	out, createErr := os.Create(dstPath)
	if createErr != nil {
		return 0, fmt.Errorf("create destination: %w", createErr)
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close destination: %w", closeErr))
		}
	}()

	dstSchema := parquet.NewSchema(canonical.Name(), schema.ForceCompression(canonical, comp.CodecInstance()))
	opts := []parquet.WriterOption{dstSchema}
	if opt, ok := rg.Option(); ok {
		opts = append(opts, opt)
	}
	writer := parquet.NewGenericWriter[map[string]any](out, opts...)

	for i, parsed := range pf {
		reader := parquet.NewGenericReader[map[string]any](parsed, parsed.Schema())
		n, readErr := copyRows(reader, writer)
		rows += n
		closeErr := reader.Close()
		if readErr != nil {
			return rows, fmt.Errorf("read %s: %w", srcPaths[i], readErr)
		}
		if closeErr != nil {
			return rows, fmt.Errorf("close reader for %s: %w", srcPaths[i], closeErr)
		}
	}

	if closeErr := writer.Close(); closeErr != nil {
		return rows, fmt.Errorf("close writer: %w", closeErr)
	}
	return rows, nil
}

// copyRows reads every row from reader in batchSize batches and writes
// them to writer, unchanged. It returns the number of rows copied.
func copyRows(reader *parquet.GenericReader[map[string]any], writer *parquet.GenericWriter[map[string]any]) (rows int64, err error) {
	buf := make([]map[string]any, batchSize)
	for {
		for i := range buf {
			buf[i] = map[string]any{}
		}
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, writeErr := writer.Write(buf[:n]); writeErr != nil {
				return rows, fmt.Errorf("write rows: %w", writeErr)
			}
			rows += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return rows, nil
			}
			return rows, readErr
		}
	}
}
```

- [ ] **Step 5: テストを実行して通ることを確認する**

Run: `go test ./internal/pqcat/... -run TestCat_SingleFile -v`
Expected: PASS

- [ ] **Step 6: 残りのテストを書く — 複数ファイル連結・圧縮変更・エラー系**

`internal/pqcat/pqcat_test.go`の末尾に追加:

```go
func TestCat_MultipleFilesConcatenateInArgumentOrder(t *testing.T) {
	dir := t.TempDir()
	src1 := filepath.Join(dir, "src1.parquet")
	src2 := filepath.Join(dir, "src2.parquet")
	writeTestParquet(t, src1, []testRow{{Name: "a", Count: 1}, {Name: "b", Count: 2}})
	writeTestParquet(t, src2, []testRow{{Name: "c", Count: 3}, {Name: "d", Count: 4}})
	dst := filepath.Join(dir, "dst.parquet")

	rows, err := Cat([]string{src1, src2}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{})
	if err != nil {
		t.Fatalf("Cat: %v", err)
	}
	if rows != 4 {
		t.Errorf("rows = %d, want 4", rows)
	}

	want := []testRow{{Name: "a", Count: 1}, {Name: "b", Count: 2}, {Name: "c", Count: 3}, {Name: "d", Count: 4}}
	got := readAllRows(t, dst)
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v (argument order, no timestamp column to merge by)", i, got[i], want[i])
		}
	}
}

func TestCat_ChangesCompressionCodec(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.parquet")
	writeTestParquet(t, src, []testRow{{Name: "a", Count: 1}}, parquet.Compression(&parquet.Gzip))
	dst := filepath.Join(dir, "dst.parquet")

	if _, err := Cat([]string{src}, dst, compression.Settings{Codec: "zstd"}, rowgroup.Settings{}); err != nil {
		t.Fatalf("Cat: %v", err)
	}

	f, err := os.Open(dst)
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		t.Fatalf("open parquet: %v", err)
	}
	if got := pf.Metadata().RowGroups[0].Columns[0].MetaData.Codec.String(); got != "ZSTD" {
		t.Errorf("dst codec = %q, want ZSTD", got)
	}
}

func TestCat_ManyRowsSpanMultipleBatches(t *testing.T) {
	dir := t.TempDir()
	var want []testRow
	for i := 0; i < batchSize*2+7; i++ {
		want = append(want, testRow{Name: "row", Count: int64(i)})
	}
	src := filepath.Join(dir, "src.parquet")
	writeTestParquet(t, src, want)
	dst := filepath.Join(dir, "dst.parquet")

	rows, err := Cat([]string{src}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{})
	if err != nil {
		t.Fatalf("Cat: %v", err)
	}
	if rows != int64(len(want)) {
		t.Errorf("rows = %d, want %d", rows, len(want))
	}
	got := readAllRows(t, dst)
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
}

func TestCat_OutputPathSameAsInputIsError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.parquet")
	writeTestParquet(t, src, []testRow{{Name: "a", Count: 1}})

	if _, err := Cat([]string{src}, src, compression.Settings{Codec: "snappy"}, rowgroup.Settings{}); err == nil {
		t.Error("expected error when an input path equals the output path, got nil")
	}
}

func TestCat_MissingInputFile(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.parquet")
	if _, err := Cat([]string{"/nonexistent/src.parquet"}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{}); err == nil {
		t.Error("expected error for missing source, got nil")
	}
}

func TestCat_SchemaMismatchColumnNameNamesBothFilesAndColumnPosition(t *testing.T) {
	type renamedRow struct {
		Name string `parquet:"name"`
		Code int64  `parquet:"code"`
	}
	dir := t.TempDir()
	src1 := filepath.Join(dir, "a.parquet")
	src2 := filepath.Join(dir, "c.parquet")
	writeTestParquet(t, src1, []testRow{{Name: "a", Count: 1}})
	writeTestParquet(t, src2, []renamedRow{{Name: "x", Code: 2}})

	dst := filepath.Join(dir, "dst.parquet")
	_, err := Cat([]string{src1, src2}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{})
	if err == nil {
		t.Fatal("expected schema mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "c.parquet") || !strings.Contains(err.Error(), "a.parquet") {
		t.Errorf("expected error to name both files, got: %v", err)
	}
	if !strings.Contains(err.Error(), "column 1") {
		t.Errorf("expected error to name the mismatched column position, got: %v", err)
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("expected dst to not be created on schema mismatch")
	}
}

func TestCat_SchemaMismatchColumnType(t *testing.T) {
	type retypedRow struct {
		Name  string `parquet:"name"`
		Count string `parquet:"count"`
	}
	dir := t.TempDir()
	src1 := filepath.Join(dir, "a.parquet")
	src2 := filepath.Join(dir, "b.parquet")
	writeTestParquet(t, src1, []testRow{{Name: "a", Count: 1}})
	writeTestParquet(t, src2, []retypedRow{{Name: "x", Count: "not-a-number"}})

	dst := filepath.Join(dir, "dst.parquet")
	_, err := Cat([]string{src1, src2}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{})
	if err == nil {
		t.Fatal("expected schema mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "count") {
		t.Errorf("expected error to name the mismatched column, got: %v", err)
	}
}

func TestCat_SchemaMismatchColumnCount(t *testing.T) {
	type shortRow struct {
		Name string `parquet:"name"`
	}
	dir := t.TempDir()
	src1 := filepath.Join(dir, "a.parquet")
	src2 := filepath.Join(dir, "b.parquet")
	writeTestParquet(t, src1, []testRow{{Name: "a", Count: 1}})
	writeTestParquet(t, src2, []shortRow{{Name: "x"}})

	dst := filepath.Join(dir, "dst.parquet")
	_, err := Cat([]string{src1, src2}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{})
	if err == nil {
		t.Fatal("expected schema mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "b.parquet") || !strings.Contains(err.Error(), "a.parquet") {
		t.Errorf("expected error to name both files, got: %v", err)
	}
}

func TestSourceCodec(t *testing.T) {
	tests := []struct {
		name string
		opt  parquet.WriterOption
		want string
	}{
		{"gzip", parquet.Compression(&parquet.Gzip), "gzip"},
		{"snappy", parquet.Compression(&parquet.Snappy), "snappy"},
		{"uncompressed", parquet.Compression(&parquet.Uncompressed), "uncompressed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "src.parquet")
			writeTestParquet(t, path, []testRow{{Name: "a", Count: 1}}, tt.opt)
			got, err := SourceCodec(path)
			if err != nil {
				t.Fatalf("SourceCodec: %v", err)
			}
			if got != tt.want {
				t.Errorf("SourceCodec = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSourceCodec_MissingFile(t *testing.T) {
	if _, err := SourceCodec("/nonexistent/path.parquet"); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}
```

- [ ] **Step 7: テストを実行して通ることを確認する**

Run: `go test ./internal/pqcat/... -v`
Expected: PASS(全テスト)

- [ ] **Step 8: リポジトリ全体をビルド・テストする**

Run: `go build ./... && go test ./...`
Expected: ビルド成功、全テストPASS(`internal/pqcopy`はまだ存在し引き続きPASSする — 削除はTask 4)

- [ ] **Step 9: コミット**

```bash
git add internal/pqcat/
git commit -m "feat(pqcat): add SourceCodec and Cat (schema-validated concatenation, no timestamp merge yet)"
```

---

## Task 3: `internal/pqcat` — タイムスタンプ順マージ

**Files:**
- Create: `internal/pqcat/merge.go`
- Modify: `internal/pqcat/pqcat.go`
- Test: `internal/pqcat/merge_test.go`

**Interfaces:**
- Consumes: `schema.TypeName`(既存、`internal/schema`)、`pqcat.batchSize`(Task 2)、`Cat`の内部状態(`pf []*parquet.File`・`srcPaths []string`・`canonical *parquet.Schema`・`writer *parquet.GenericWriter[map[string]any]`、Task 2で構築済み)。
- Produces:
  - `func detectMergeKey(canonical *parquet.Schema) string` — 最初のtimestamp型列の名前、無ければ`""`(非公開)。
  - `func mergeRows(pf []*parquet.File, srcPaths []string, mergeKey string, writer *parquet.GenericWriter[map[string]any]) (rows int64, err error)` — k-wayタイムスタンプ順マージ(非公開)。
  - `Cat`は、マージキーが空なら従来通り(Task 2の)単純連結、空でなければ`mergeRows`を呼ぶよう分岐する。

- [ ] **Step 1: 失敗するテストを書く — タイムスタンプ順マージ**

`internal/pqcat/merge_test.go`:

```go
package pqcat

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/compression"
	"logidx/internal/rowgroup"
	"logidx/internal/rules"
	"logidx/internal/schema"
)

// writeRows builds a Parquet schema from fields the same way the CLI's own
// writer does (internal/schema.Build) and writes rows (already-typed
// map[string]any, e.g. timestamp columns as int64 microseconds - see
// writer.Set.WriteMatched) to path.
func writeRows(t *testing.T, path string, fields []rules.Field, rows []map[string]any) {
	t.Helper()
	built, err := schema.Build("test", fields)
	if err != nil {
		t.Fatalf("schema.Build: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	w := parquet.NewGenericWriter[map[string]any](f, built.Schema)
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file %s: %v", path, err)
	}
}

func readNameColumn(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		t.Fatalf("open parquet: %v", err)
	}
	reader := parquet.NewGenericReader[map[string]any](pf, pf.Schema())
	defer func() { _ = reader.Close() }()

	var names []string
	buf := make([]map[string]any, 10)
	for {
		for i := range buf {
			buf[i] = map[string]any{}
		}
		n, err := reader.Read(buf)
		for _, m := range buf[:n] {
			names = append(names, m["name"].(string))
		}
		if err != nil {
			break
		}
	}
	return names
}

func readSeqColumn(t *testing.T, path string) []int64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		t.Fatalf("open parquet: %v", err)
	}
	reader := parquet.NewGenericReader[map[string]any](pf, pf.Schema())
	defer func() { _ = reader.Close() }()

	var seqs []int64
	buf := make([]map[string]any, 10)
	for {
		for i := range buf {
			buf[i] = map[string]any{}
		}
		n, err := reader.Read(buf)
		for _, m := range buf[:n] {
			seqs = append(seqs, m["seq"].(int64))
		}
		if err != nil {
			break
		}
	}
	return seqs
}

func TestCat_MergesByTimestampAcrossFilesWithOverlappingRanges(t *testing.T) {
	dir := t.TempDir()
	fields := []rules.Field{{Name: "ts", Type: "timestamp"}, {Name: "name", Type: "string"}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := func(offsetSec int, name string) map[string]any {
		return map[string]any{"ts": base.Add(time.Duration(offsetSec) * time.Second).UnixMicro(), "name": name}
	}

	src1 := filepath.Join(dir, "src1.parquet")
	src2 := filepath.Join(dir, "src2.parquet")
	writeRows(t, src1, fields, []map[string]any{row(0, "a"), row(20, "c"), row(40, "e")})
	writeRows(t, src2, fields, []map[string]any{row(10, "b"), row(30, "d"), row(50, "f")})
	dst := filepath.Join(dir, "dst.parquet")

	rows, err := Cat([]string{src1, src2}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{})
	if err != nil {
		t.Fatalf("Cat: %v", err)
	}
	if rows != 6 {
		t.Errorf("rows = %d, want 6", rows)
	}

	got := readNameColumn(t, dst)
	want := []string{"a", "b", "c", "d", "e", "f"}
	if !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v (ascending timestamp order across files)", got, want)
	}
}

func TestCat_MergesByTimestampWithNonOverlappingRanges(t *testing.T) {
	dir := t.TempDir()
	fields := []rules.Field{{Name: "ts", Type: "timestamp"}, {Name: "name", Type: "string"}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := func(offsetSec int, name string) map[string]any {
		return map[string]any{"ts": base.Add(time.Duration(offsetSec) * time.Second).UnixMicro(), "name": name}
	}

	src1 := filepath.Join(dir, "src1.parquet")
	src2 := filepath.Join(dir, "src2.parquet")
	// Every src2 row is later than every src1 row - the merge should still
	// go through mergeRows (mergeKey is non-empty), even though the result
	// happens to equal plain concatenation.
	writeRows(t, src1, fields, []map[string]any{row(0, "a"), row(10, "b")})
	writeRows(t, src2, fields, []map[string]any{row(100, "c"), row(110, "d")})
	dst := filepath.Join(dir, "dst.parquet")

	if _, err := Cat([]string{src1, src2}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{}); err != nil {
		t.Fatalf("Cat: %v", err)
	}

	got := readNameColumn(t, dst)
	want := []string{"a", "b", "c", "d"}
	if !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
}

func TestCat_MergeTiesBreakByArgumentOrder(t *testing.T) {
	dir := t.TempDir()
	fields := []rules.Field{{Name: "ts", Type: "timestamp"}, {Name: "name", Type: "string"}}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro()

	src1 := filepath.Join(dir, "src1.parquet")
	src2 := filepath.Join(dir, "src2.parquet")
	writeRows(t, src1, fields, []map[string]any{{"ts": ts, "name": "from-src1"}})
	writeRows(t, src2, fields, []map[string]any{{"ts": ts, "name": "from-src2"}})
	dst := filepath.Join(dir, "dst.parquet")

	if _, err := Cat([]string{src1, src2}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{}); err != nil {
		t.Fatalf("Cat: %v", err)
	}

	got := readNameColumn(t, dst)
	want := []string{"from-src1", "from-src2"}
	if !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v (src1 first on tied timestamp, argument order tiebreak)", got, want)
	}
}

func TestCat_MergeSpansMultipleBatches(t *testing.T) {
	dir := t.TempDir()
	fields := []rules.Field{{Name: "ts", Type: "timestamp"}, {Name: "seq", Type: "int"}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	n := batchSize + 10
	var rows1, rows2 []map[string]any
	for i := 0; i < n; i++ {
		rows1 = append(rows1, map[string]any{"ts": base.Add(time.Duration(2*i) * time.Second).UnixMicro(), "seq": int64(2 * i)})
		rows2 = append(rows2, map[string]any{"ts": base.Add(time.Duration(2*i+1) * time.Second).UnixMicro(), "seq": int64(2*i + 1)})
	}
	src1 := filepath.Join(dir, "src1.parquet")
	src2 := filepath.Join(dir, "src2.parquet")
	writeRows(t, src1, fields, rows1)
	writeRows(t, src2, fields, rows2)
	dst := filepath.Join(dir, "dst.parquet")

	got, err := Cat([]string{src1, src2}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{})
	if err != nil {
		t.Fatalf("Cat: %v", err)
	}
	if got != int64(2*n) {
		t.Errorf("rows = %d, want %d", got, 2*n)
	}

	seqs := readSeqColumn(t, dst)
	if len(seqs) != 2*n {
		t.Fatalf("got %d seq values, want %d", len(seqs), 2*n)
	}
	for i, s := range seqs {
		if s != int64(i) {
			t.Fatalf("seq[%d] = %d, want %d (interleaved ascending order spanning multiple %d-row batches)", i, s, i, batchSize)
		}
	}
}

func TestCat_SingleFileWithTimestampColumnDegeneratesToFileOrder(t *testing.T) {
	dir := t.TempDir()
	fields := []rules.Field{{Name: "ts", Type: "timestamp"}, {Name: "name", Type: "string"}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := filepath.Join(dir, "src.parquet")
	writeRows(t, src, fields, []map[string]any{
		{"ts": base.UnixMicro(), "name": "a"},
		{"ts": base.Add(10 * time.Second).UnixMicro(), "name": "b"},
	})
	dst := filepath.Join(dir, "dst.parquet")

	if _, err := Cat([]string{src}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{}); err != nil {
		t.Fatalf("Cat: %v", err)
	}

	got := readNameColumn(t, dst)
	want := []string{"a", "b"}
	if !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./internal/pqcat/... -run TestCat_Merge -v`
Expected: FAIL — 現時点の`Cat`はマージキーを検出せず常に引数順連結するため、`TestCat_MergesByTimestampAcrossFilesWithOverlappingRanges`が`names = [a c e b d f]`のような連結順で失敗する

- [ ] **Step 3: `merge.go`を実装する**

`internal/pqcat/merge.go`:

```go
package pqcat

import (
	"container/heap"
	"errors"
	"fmt"
	"io"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/schema"
)

// detectMergeKey returns the name of canonical's first timestamp-typed
// column in declared order, or "" if it has none - mirrors
// internal/convert.mergeKeyField's per-rule detection, applied to a single
// already-schema-validated column set instead of a rule's Fields.
func detectMergeKey(canonical *parquet.Schema) string {
	for _, field := range canonical.Fields() {
		if name, err := schema.TypeName(field); err == nil && name == "timestamp" {
			return field.Name()
		}
	}
	return ""
}

// rowCursor streams one input file's rows in constant-memory batches, for
// use by mergeRows' k-way merge - one rowCursor per file. Mirrors
// internal/convert/merge.go's fileCursor, but over already-typed Parquet
// rows (map[string]any) instead of unconverted log lines.
type rowCursor struct {
	reader *parquet.GenericReader[map[string]any]
	buf    []map[string]any
	pos    int
	n      int
	done   bool
}

func newRowCursor(pf *parquet.File) *rowCursor {
	return &rowCursor{
		reader: parquet.NewGenericReader[map[string]any](pf, pf.Schema()),
		buf:    make([]map[string]any, batchSize),
	}
}

// next returns the cursor's next row. ok is false once the underlying file
// is exhausted. parquet-go's GenericReader.Read can return a positive
// count together with io.EOF on its final call, so done only short-circuits
// the *next* call once every already-buffered row has been served.
func (c *rowCursor) next() (row map[string]any, ok bool, err error) {
	for c.pos >= c.n {
		if c.done {
			return nil, false, nil
		}
		for i := range c.buf {
			c.buf[i] = map[string]any{}
		}
		n, readErr := c.reader.Read(c.buf)
		c.n = n
		c.pos = 0
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil, false, readErr
			}
			c.done = true
		}
	}
	row = c.buf[c.pos]
	c.pos++
	return row, true, nil
}

func (c *rowCursor) close() error {
	return c.reader.Close()
}

// mergeCandidate is one row held back from writing because mergeRows is
// still comparing it against the other open cursors' current rows.
type mergeCandidate struct {
	cursor    *rowCursor
	row       map[string]any
	sortValue int64
	fileIndex int
}

// mergeHeap is a min-heap of mergeCandidates ordered by sortValue, with the
// originating file's position among mergeRows' srcPaths as a tiebreak, so
// two candidates with the exact same merge-key value still pop in a fixed,
// repeatable order across runs - mirrors internal/convert.candidateHeap.
type mergeHeap []*mergeCandidate

func (h mergeHeap) Len() int { return len(h) }
func (h mergeHeap) Less(i, j int) bool {
	if h[i].sortValue != h[j].sortValue {
		return h[i].sortValue < h[j].sortValue
	}
	return h[i].fileIndex < h[j].fileIndex
}
func (h mergeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x any)   { *h = append(*h, x.(*mergeCandidate)) }
func (h *mergeHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// mergeKeyValue extracts row's merge-key column as the microsecond-epoch
// int64 parquet-go's GenericReader returns for timestamp columns (not
// time.Time - see the package's design doc). ok is false if the column is
// missing or not an int64, which should be unreachable once schema.Equal
// has validated every file shares mergeKey as a timestamp column, but is
// still checked defensively rather than trusting that invariant blindly.
func mergeKeyValue(row map[string]any, mergeKey string) (int64, bool) {
	v, ok := row[mergeKey].(int64)
	return v, ok
}

// mergeRows performs a k-way streaming merge of every file in pf, in
// ascending order of each row's mergeKey column, writing the merged result
// to writer in batchSize batches. pf[i] must correspond to srcPaths[i].
// Rows with equal mergeKey values are ordered by their originating file's
// position in srcPaths, so output is stable and repeatable across runs.
// With a single input file the heap never holds more than one candidate,
// so this naturally degenerates to reading that file in order - mirrors
// internal/convert.mergeFiles' own same observation about its single-file
// case.
func mergeRows(pf []*parquet.File, srcPaths []string, mergeKey string, writer *parquet.GenericWriter[map[string]any]) (rows int64, err error) {
	cursors := make([]*rowCursor, len(pf))
	h := mergeHeap{}

	closeRemaining := func() {
		for _, c := range cursors {
			if c != nil {
				_ = c.close()
			}
		}
	}

	for i, f := range pf {
		cursors[i] = newRowCursor(f)
		row, ok, nextErr := cursors[i].next()
		if nextErr != nil {
			closeRemaining()
			return rows, fmt.Errorf("read %s: %w", srcPaths[i], nextErr)
		}
		if !ok {
			if closeErr := cursors[i].close(); closeErr != nil {
				closeRemaining()
				return rows, fmt.Errorf("close %s: %w", srcPaths[i], closeErr)
			}
			continue
		}
		sortValue, isInt := mergeKeyValue(row, mergeKey)
		if !isInt {
			closeRemaining()
			return rows, fmt.Errorf("read %s: merge key %q is not a timestamp column", srcPaths[i], mergeKey)
		}
		heap.Push(&h, &mergeCandidate{cursor: cursors[i], row: row, sortValue: sortValue, fileIndex: i})
	}

	batch := make([]map[string]any, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, writeErr := writer.Write(batch); writeErr != nil {
			return fmt.Errorf("write rows: %w", writeErr)
		}
		rows += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	for h.Len() > 0 {
		cand := heap.Pop(&h).(*mergeCandidate)
		batch = append(batch, cand.row)
		if len(batch) == batchSize {
			if flushErr := flush(); flushErr != nil {
				closeRemaining()
				return rows, flushErr
			}
		}

		next, ok, nextErr := cand.cursor.next()
		if nextErr != nil {
			closeRemaining()
			return rows, fmt.Errorf("read %s: %w", srcPaths[cand.fileIndex], nextErr)
		}
		if !ok {
			if closeErr := cand.cursor.close(); closeErr != nil {
				closeRemaining()
				return rows, fmt.Errorf("close %s: %w", srcPaths[cand.fileIndex], closeErr)
			}
			continue
		}
		sortValue, isInt := mergeKeyValue(next, mergeKey)
		if !isInt {
			closeRemaining()
			return rows, fmt.Errorf("read %s: merge key %q is not a timestamp column", srcPaths[cand.fileIndex], mergeKey)
		}
		heap.Push(&h, &mergeCandidate{cursor: cand.cursor, row: next, sortValue: sortValue, fileIndex: cand.fileIndex})
	}

	if flushErr := flush(); flushErr != nil {
		return rows, flushErr
	}
	return rows, nil
}
```

- [ ] **Step 4: `Cat`をマージキー検出で分岐させる**

`internal/pqcat/pqcat.go`の`Cat`関数内、`writer := parquet.NewGenericWriter[map[string]any](out, opts...)`の直後から関数末尾までを置き換える:

```go
	mergeKey := detectMergeKey(canonical)

	if mergeKey == "" {
		for i, parsed := range pf {
			reader := parquet.NewGenericReader[map[string]any](parsed, parsed.Schema())
			n, readErr := copyRows(reader, writer)
			rows += n
			closeErr := reader.Close()
			if readErr != nil {
				return rows, fmt.Errorf("read %s: %w", srcPaths[i], readErr)
			}
			if closeErr != nil {
				return rows, fmt.Errorf("close reader for %s: %w", srcPaths[i], closeErr)
			}
		}
	} else {
		mergedRows, mergeErr := mergeRows(pf, srcPaths, mergeKey, writer)
		rows = mergedRows
		if mergeErr != nil {
			return rows, mergeErr
		}
	}

	if closeErr := writer.Close(); closeErr != nil {
		return rows, fmt.Errorf("close writer: %w", closeErr)
	}
	return rows, nil
}
```

(このタスクではTask 2の単純連結ループのロジック自体は変えず、`mergeKey == ""`の分岐に移すだけ)

- [ ] **Step 5: テストを実行して通ることを確認する**

Run: `go test ./internal/pqcat/... -v`
Expected: PASS(全テスト。Task 2のテストも引き続きPASSすること — `mergeKey == ""`の場合は従来通りの経路を通る)

- [ ] **Step 6: リポジトリ全体をビルド・テストする**

Run: `go build ./... && go test ./...`
Expected: ビルド成功、全テストPASS

- [ ] **Step 7: コミット**

```bash
git add internal/pqcat/
git commit -m "feat(pqcat): merge rows into ascending timestamp order across files when the schema has a timestamp column"
```

---

## Task 4: CLI配線 — `newCatCmd`、`pqcopy`削除、既存copyテストの移植

**Files:**
- Modify: `cmd/logidx/main.go`
- Modify: `cmd/logidx/main_test.go`
- Delete: `internal/pqcopy/pqcopy.go`
- Delete: `internal/pqcopy/doc.go`
- Delete: `internal/pqcopy/pqcopy_test.go`

**Interfaces:**
- Consumes: `pqcat.SourceCodec`・`pqcat.Cat`(Task 2/3)、`compression.Settings`/`compression.Resolve`/`compression.Settings.Validate`(既存)、`rowgroup.Settings`/`rowgroup.Settings.Validate`(既存)、`pqinfo.Read`/`pqinfo.Info.CompressionRatio`(既存)。
- Produces: `func newCatCmd(stdout, stderr io.Writer) *cobra.Command` — `run()`の`root.AddCommand(newCopyCmd(...))`を`root.AddCommand(newCatCmd(...))`に置き換える。

- [ ] **Step 1: 失敗するテストを書く — `cat`の基本フロー(旧`copy`テストの移植)**

`cmd/logidx/main_test.go`の`TestRun_CopyUsageErrorOnWrongArgCount`から`TestRun_CopyInvalidCompressionLevelReturnsUsageError`まで(既存の5関数、161-244行目)を、下記の内容にまるごと置き換える:

```go
func TestRun_CatMissingOutputFlagReturnsUsageError(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir)
	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", src}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("expected usage message on stderr, got: %s", stderr.String())
	}
}

func TestRun_CatNoSourceFilesReturnsUsageError(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", "--output", filepath.Join(dir, "dst.parquet")}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("expected usage message on stderr, got: %s", stderr.String())
	}
}

func TestRun_CatMissingSourceReturnsExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", "--output", filepath.Join(dir, "dst.parquet"), filepath.Join(dir, "missing.parquet")}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for missing source, got %d", code)
	}
}

func TestRun_CatSingleFilePreservesRowsAndDefaultsToSourceCodec(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir, "--compression", "gzip")
	dst := filepath.Join(dir, "cat.parquet")

	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", "--output", dst, src}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	srcInfo, err := pqinfo.Read(src)
	if err != nil {
		t.Fatalf("pqinfo.Read(src): %v", err)
	}
	dstInfo, err := pqinfo.Read(dst)
	if err != nil {
		t.Fatalf("pqinfo.Read(dst): %v", err)
	}
	if dstInfo.NumRows != srcInfo.NumRows {
		t.Errorf("dst NumRows = %d, want %d", dstInfo.NumRows, srcInfo.NumRows)
	}
	if len(dstInfo.Columns) == 0 || dstInfo.Columns[0].Codec != "GZIP" {
		t.Errorf("expected dst to default to source codec GZIP, got %+v", dstInfo.Columns)
	}
}

func TestRun_CatChangesCompression(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir, "--compression", "gzip")
	dst := filepath.Join(dir, "cat.parquet")

	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", "--output", dst, "--compression", "zstd", src}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	dstInfo, err := pqinfo.Read(dst)
	if err != nil {
		t.Fatalf("pqinfo.Read(dst): %v", err)
	}
	if len(dstInfo.Columns) == 0 || dstInfo.Columns[0].Codec != "ZSTD" {
		t.Errorf("expected dst codec ZSTD, got %+v", dstInfo.Columns)
	}
	if !strings.Contains(stdout.String(), "concatenated") {
		t.Errorf("expected cat summary on stdout, got: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "bytes") || !strings.Contains(stdout.String(), "%") {
		t.Errorf("expected compression ratio info on stdout, got: %s", stdout.String())
	}
}

func TestRun_CatInvalidCompressionLevelReturnsUsageError(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir)
	dst := filepath.Join(dir, "cat.parquet")

	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", "--output", dst, "--compression", "snappy", "--compression-level", "5", src}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2 for invalid compression level, got %d", code)
	}
}
```

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./cmd/logidx/... -run TestRun_Cat -v`
Expected: FAIL(コンパイルエラー — `unknown command "cat"`にはならず、`cat`サブコマンド自体が未登録なのでcobraの"unknown command"エラーがusageメッセージとして出ず、期待するexit codeと一致しない形で失敗する)

- [ ] **Step 3: `cmd/logidx/main.go`を書き換える**

先頭のimportブロックを置き換える:

```go
import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"logidx/internal/compression"
	"logidx/internal/convert"
	"logidx/internal/logging"
	"logidx/internal/pqcat"
	"logidx/internal/pqdump"
	"logidx/internal/pqinfo"
	"logidx/internal/rowgroup"
	"logidx/internal/rules"

	"github.com/spf13/cobra"
)
```

`run()`関数内の`root.AddCommand(newCopyCmd(stdout, stderr))`を置き換える:

```go
	root.AddCommand(newCatCmd(stdout, stderr))
```

`newCopyCmd`関数全体(200-259行目)を削除し、代わりに次を挿入する:

```go
func newCatCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		outputPath         string
		compressionCodec   string
		compressionLevel   int
		maxRowsPerRowGroup int64
	)

	cmd := &cobra.Command{
		Use:           "cat --output <dst.parquet> <src.parquet>...",
		Short:         "Concatenate same-schema parquet files into one, merging by timestamp if the schema has one",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputPath == "" || len(args) == 0 {
				_, _ = fmt.Fprintln(stderr, "usage: logidx cat --output <dst.parquet> [--compression <codec>] [--compression-level <n>] [--max-rows-per-row-group <n>] <src.parquet>...")
				return &exitCodeError{2}
			}

			srcCodec, err := pqcat.SourceCodec(args[0])
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "%s: %v\n", args[0], err)
				return &exitCodeError{1}
			}

			cliCompression := compression.Settings{Codec: compressionCodec}
			if cmd.Flags().Changed("compression-level") {
				level := compressionLevel
				cliCompression.Level = &level
			}
			// With no --compression flag, fall back to the first source
			// file's own codec (not the package default), matching the old
			// `copy` command's behavior for its single input file.
			comp := compression.Resolve(cliCompression, compression.Settings{Codec: srcCodec})
			if err := comp.Validate(); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return &exitCodeError{2}
			}

			cliRowGroup := rowgroup.Settings{}
			if cmd.Flags().Changed("max-rows-per-row-group") {
				maxRows := maxRowsPerRowGroup
				cliRowGroup.MaxRows = &maxRows
			}
			if err := cliRowGroup.Validate(); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return &exitCodeError{2}
			}

			rows, err := pqcat.Cat(args, outputPath, comp, cliRowGroup)
			if err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return &exitCodeError{1}
			}

			msg := fmt.Sprintf("concatenated %d files, %d rows: %s -> %s (%s)", len(args), rows, strings.Join(args, ","), outputPath, comp.Codec)
			if dstInfo, infoErr := pqinfo.Read(outputPath); infoErr == nil {
				if pct, ratio, ok := dstInfo.CompressionRatio(); ok {
					msg += fmt.Sprintf(", %d/%d bytes (%.1f%%, %.2fx)", dstInfo.CompressedBytes, dstInfo.UncompressedBytes, pct, ratio)
				}
			}
			_, _ = fmt.Fprintln(stdout, msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&outputPath, "output", "", "output parquet file path (required)")
	cmd.Flags().StringVar(&compressionCodec, "compression", "", "parquet compression codec: uncompressed, snappy, gzip, brotli, zstd, lz4; default preserves the first source file's codec")
	cmd.Flags().IntVar(&compressionLevel, "compression-level", 0, "codec-specific compression level; default uses the new codec's own default level")
	cmd.Flags().Int64Var(&maxRowsPerRowGroup, "max-rows-per-row-group", 0, "parquet row group row-count limit; unset = unlimited (default)")

	return cmd
}
```

- [ ] **Step 4: `internal/pqcopy`パッケージを削除する**

```bash
git rm -r internal/pqcopy
```

- [ ] **Step 5: テストを実行して通ることを確認する**

Run: `go test ./cmd/logidx/... -run TestRun_Cat -v`
Expected: PASS(全テスト)

- [ ] **Step 6: リポジトリ全体をビルド・テストする**

Run: `go build ./... && go test ./...`
Expected: ビルド成功、全テストPASS(`internal/pqcopy`への参照が残っていればここでビルドエラーになる)

- [ ] **Step 7: コミット**

```bash
git add cmd/logidx/main.go cmd/logidx/main_test.go
git commit -m "feat(cli): replace copy subcommand with cat (multi-file concatenation via internal/pqcat)"
```

---

## Task 5: CLI E2Eテスト — 複数ファイル連結・タイムスタンプマージ・スキーマ不一致

**Files:**
- Modify: `cmd/logidx/main_test.go`

**Interfaces:**
- Consumes: `run([]string, io.Writer, io.Writer) int`(既存)、`writeFile`/`importedParquet`(既存ヘルパー)。他タスクの新規シンボルは直接使わない — CLI経由でTask 1〜4の変更を通しで検証する。
- Produces: なし(テストのみ)。

- [ ] **Step 1: 失敗するテストを書く**

`cmd/logidx/main_test.go`の`TestRun_CatInvalidCompressionLevelReturnsUsageError`の直後に追加:

```go
func TestRun_CatConcatenatesMultipleFilesInArgumentOrderWithoutMergeKey(t *testing.T) {
	dir := t.TempDir()
	src1 := importedParquet(t, filepath.Join(dir, "run1"))
	src2 := importedParquet(t, filepath.Join(dir, "run2"))
	dst := filepath.Join(dir, "cat.parquet")

	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", "--output", dst, src1, src2}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "concatenated 2 files") {
		t.Errorf("expected 2-file summary on stdout, got: %s", stdout.String())
	}

	dstInfo, err := pqinfo.Read(dst)
	if err != nil {
		t.Fatalf("pqinfo.Read(dst): %v", err)
	}
	src1Info, err := pqinfo.Read(src1)
	if err != nil {
		t.Fatalf("pqinfo.Read(src1): %v", err)
	}
	src2Info, err := pqinfo.Read(src2)
	if err != nil {
		t.Fatalf("pqinfo.Read(src2): %v", err)
	}
	if dstInfo.NumRows != src1Info.NumRows+src2Info.NumRows {
		t.Errorf("dst NumRows = %d, want %d", dstInfo.NumRows, src1Info.NumRows+src2Info.NumRows)
	}
}

func TestRun_CatMergesMultipleFilesByTimestamp(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: ts_event
    pattern: '^TS (?P<time>\S+) (?P<msg>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      msg: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	logA := writeFile(t, dir, "a.log", "TS 2026-08-06T12:00:00Z from-a-1\nTS 2026-08-06T12:00:20Z from-a-2\n")
	logB := writeFile(t, dir, "b.log", "TS 2026-08-06T12:00:10Z from-b-1\nTS 2026-08-06T12:00:30Z from-b-2\n")

	outA := filepath.Join(dir, "outA")
	outB := filepath.Join(dir, "outB")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"import", "--rules", rulesPath, "--out", outA, logA}, &stdout, &stderr); code != 0 {
		t.Fatalf("import a failed: exit %d, stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"import", "--rules", rulesPath, "--out", outB, logB}, &stdout, &stderr); code != 0 {
		t.Fatalf("import b failed: exit %d, stderr=%s", code, stderr.String())
	}

	srcA := filepath.Join(outA, "ts_event.parquet")
	srcB := filepath.Join(outB, "ts_event.parquet")
	dst := filepath.Join(dir, "merged.parquet")

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"cat", "--output", dst, srcA, srcB}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "concatenated 2 files") {
		t.Errorf("expected cat summary on stdout, got: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", dst, "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 5 { // header + 4 rows
		t.Fatalf("expected 5 dump lines, got %d: %q", len(lines), stdout.String())
	}

	var gotMsgs []string
	for _, line := range lines[1:] {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("unmarshal dump line %q: %v", line, err)
		}
		gotMsgs = append(gotMsgs, row["msg"].(string))
	}
	want := []string{"from-a-1", "from-b-1", "from-a-2", "from-b-2"}
	if !slices.Equal(gotMsgs, want) {
		t.Errorf("merged order = %v, want %v", gotMsgs, want)
	}
}

func TestRun_CatSchemaMismatchReturnsExitCodeOneAndNamesBothFiles(t *testing.T) {
	dir := t.TempDir()
	src1 := importedParquet(t, filepath.Join(dir, "run1"))

	// Same rule name, same pattern shape, but the second field/capture group
	// is named "msg" instead of "message" - a valid rules.yaml on its own
	// (every field has a matching named capture group), producing a schema
	// whose second column name differs from src1's, which is exactly the
	// column-name mismatch this test wants to trigger.
	altRulesYAML := `
rules:
  - name: app_log
    pattern: '^\[(?P<level>\w+)\] (?P<msg>.*)$'
    fields:
      level: string
      msg: string
`
	dir2 := filepath.Join(dir, "run2")
	rulesPath := writeFile(t, dir2, "rules.yaml", altRulesYAML)
	logPath := writeFile(t, dir2, "app.log", "[INFO] hello\n")
	outDir := filepath.Join(dir2, "out")
	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("import with alternate schema failed: exit %d, stderr=%s", code, stderr.String())
	}
	src2 := filepath.Join(outDir, "app_log.parquet")

	dst := filepath.Join(dir, "cat.parquet")
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"cat", "--output", dst, src1, src2}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for schema mismatch, got %d", code)
	}
	// Both fixtures are named app_log.parquet (same rule name in both
	// rules.yaml), so asserting on filepath.Base would trivially pass even
	// if the error only named one of them - assert on the full paths
	// instead, which differ by parent directory (run1 vs run2/out).
	if !strings.Contains(stderr.String(), src1) || !strings.Contains(stderr.String(), src2) {
		t.Errorf("expected error to name both files, got: %s", stderr.String())
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("expected dst to not be created on schema mismatch")
	}
}

func TestRun_CatOutputPathSameAsInputReturnsExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir)

	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", "--output", src, src}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 when output path equals an input path, got %d", code)
	}
}
```

`importedParquet`は現状`t.TempDir()`ではなく渡された`dir`引数の下に`out/app_log.parquet`を作る(145-159行目参照)ため、上記のように`filepath.Join(dir, "run1")`のような別々のサブディレクトリを渡せば、同じ`cliRulesYAML`から複数の独立した`app_log.parquet`が作れる。`main_test.go`の先頭importに`"slices"`が無ければ追加する(既存のimportブロックを確認する — 現状すでにimport済みのはず)。

- [ ] **Step 2: テストを実行して失敗を確認する**

Run: `go test ./cmd/logidx/... -run TestRun_Cat -v`
Expected: Task 4完了後であれば、この時点で追加した新規テストのみ実行して確認する(Task 1〜4がすでに実装済みなら最初からPASSしている可能性が高い — その場合は「PASSすることの確認」に読み替えてよい)

- [ ] **Step 3: テストを実行して通ることを確認する**

Run: `go test ./cmd/logidx/... -run TestRun_Cat -v`
Expected: PASS(全テスト)

- [ ] **Step 4: リポジトリ全体をビルド・テストする**

Run: `go build ./... && go test ./...`
Expected: ビルド成功、全テストPASS

- [ ] **Step 5: コミット**

```bash
git add cmd/logidx/main_test.go
git commit -m "test: add end-to-end cat regression tests (multi-file concat, timestamp merge, schema mismatch, same-path error)"
```

---

## Task 6: README.mdドキュメント更新と最終ゲート

**Files:**
- Modify: `README.md`

- [ ] **Step 1: `### copy: ...`セクションを`### cat: ...`セクションに置き換える**

`README.md`の`### copy: Parquetファイルを圧縮方式を変えて複製する`(385行目)から、次の`### dump / restore: ...`セクション(397行目)の直前までを、次の内容に置き換える:

```markdown
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

```

- [ ] **Step 2: `restore`セクションの`copy`への言及を更新する**

`README.md`の`restore`セクション内(旧418行目付近)、`--compression`を省略した場合の挙動を説明する行:

```markdown
- `--compression`を省略した場合、ヘッダーに記録された圧縮コーデックを引き継ぐ(`copy`のデフォルト挙動と同様)
```

を次に置き換える:

```markdown
- `--compression`を省略した場合、ヘッダーに記録された圧縮コーデックを引き継ぐ(`cat`のデフォルト挙動と同様)
```

- [ ] **Step 3: READMEが正しくレンダリングされることを目視確認する**

Run: `sed -n '375,420p' README.md`
Expected: `### info: ...`、新しい`### cat: ...`、`### dump / restore: ...`の順に並び、見出しレベル・コードフェンスが崩れていないこと

- [ ] **Step 4: リポジトリ全体の最終ゲート**

Run: `task fmt`
Expected: 差分なし(または空白の正規化のみ — 差分があれば内容を確認してから進める)

Run: `task lint`
Expected: 指摘なし

Run: `task test`
Expected: 全パッケージPASS

Run: `task build`
Expected: `bin/logidx`のビルドに成功する

- [ ] **Step 5: コミット**

```bash
git add README.md
git commit -m "docs: replace the copy command's README section with cat"
```

---

## Self-Review Checklist (実施済み)

- **Spec coverage:** 「CLI仕様」→Task 4/5(usage・引数検証・サマリフォーマット)、「パッケージ設計」の`pqcat.SourceCodec`/`pqcat.Cat`→Task 2/3、「タイムスタンプ順マージ」の`rowCursor`/`mergeRows`→Task 3、「internal/schemaへの追加」の`ForceCompression`/`Equal`→Task 1、「CLI側の変更」→Task 4、「エラーハンドリング」の4項目→Task 2(ファイル欠損・同一パス)・Task 2/3(スキーマ不一致)・Task 4(バリデーションエラー)、「移行・削除対象」の4項目→Task 4(pqcopy削除・main.go・main_test.go)・Task 6(README)、「テスト方針」の各項目→Task 1(schema)・Task 2(pqcat単体、非マージ系)・Task 3(pqcat単体、マージ系)・Task 5(cmd/logidx E2E)。すべて対応済み。
- **Placeholder scan:** 全ステップに実コード・実コマンドを記載済み、TODO/TBD等なし。
- **Type consistency:** `schema.Equal(a, b *parquet.Schema) error`(Task 1)は設計doc記載のシグネチャそのまま、Task 2の`pqcat.Cat`で同じシグネチャのまま呼び出している。`schema.ForceCompression(node parquet.Node, codec compress.Codec) parquet.Node`(Task 1)も同様にTask 2で使用。`pqcat.Cat(srcPaths []string, dstPath string, comp compression.Settings, rg rowgroup.Settings) (rows int64, err error)`(Task 2で確定)は、Task 3(内部分岐追加のみ、シグネチャ変更なし)・Task 4(CLIからの呼び出し)を通じて一貫している。`detectMergeKey`/`mergeRows`/`rowCursor`(Task 3)は同一ファイル内で完結し、他タスクから直接参照されない。
- **設計docとの既知の乖離:** 「Global Constraints」に記載した通り、`schema.Equal`が返すエラーメッセージはファイルパスの値ごと注釈([ファイル名]の埋め込み)を省いた簡略形にしている(シグネチャに合わないため)。テストは「テスト方針」が実際に要求する「ファイル名と列位置が含まれること」を検証し、設計docの例示メッセージとの逐語一致は求めない。
