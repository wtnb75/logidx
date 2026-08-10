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
