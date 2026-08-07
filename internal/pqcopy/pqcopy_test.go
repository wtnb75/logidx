package pqcopy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/compression"
)

type testRow struct {
	Name  string `parquet:"name"`
	Count int64  `parquet:"count"`
}

func writeTestParquet(t *testing.T, rows []testRow, opts ...parquet.WriterOption) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	schema := parquet.SchemaOf(testRow{})
	allOpts := append([]parquet.WriterOption{schema}, opts...)
	w := parquet.NewGenericWriter[testRow](f, allOpts...)
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
	return path
}

// readAllRows reads dst as map[string]any, matching how pqcopy.Copy itself
// reads and writes rows; reading a pqcopy-produced file back with a
// struct-typed GenericReader panics (parquet-go reconstructs based on the
// schema's original Go-type hint, which Copy leaves as map-shaped).
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

func TestCopy_PreservesRowsAndCount(t *testing.T) {
	want := []testRow{{Name: "a", Count: 1}, {Name: "b", Count: 2}, {Name: "c", Count: 3}}
	src := writeTestParquet(t, want)
	dst := filepath.Join(t.TempDir(), "dst.parquet")

	rows, err := Copy(src, dst, compression.Settings{Codec: "snappy"})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if rows != int64(len(want)) {
		t.Errorf("rows = %d, want %d", rows, len(want))
	}

	got := readAllRows(t, dst)
	if len(got) != len(want) {
		t.Fatalf("copied %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCopy_ChangesCompressionCodec(t *testing.T) {
	src := writeTestParquet(t, []testRow{{Name: "a", Count: 1}}, parquet.Compression(&parquet.Gzip))
	dst := filepath.Join(t.TempDir(), "dst.parquet")

	if _, err := Copy(src, dst, compression.Settings{Codec: "zstd"}); err != nil {
		t.Fatalf("Copy: %v", err)
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
	meta := pf.Metadata()
	if got := meta.RowGroups[0].Columns[0].MetaData.Codec.String(); got != "ZSTD" {
		t.Errorf("dst codec = %q, want ZSTD", got)
	}
}

func TestCopy_ManyRowsSpansMultipleBatches(t *testing.T) {
	var want []testRow
	for i := 0; i < batchSize*2+7; i++ {
		want = append(want, testRow{Name: "row", Count: int64(i)})
	}
	src := writeTestParquet(t, want)
	dst := filepath.Join(t.TempDir(), "dst.parquet")

	rows, err := Copy(src, dst, compression.Settings{Codec: "snappy"})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if rows != int64(len(want)) {
		t.Errorf("rows = %d, want %d", rows, len(want))
	}

	got := readAllRows(t, dst)
	if len(got) != len(want) {
		t.Fatalf("copied %d rows, want %d", len(got), len(want))
	}
}

func TestCopy_SameSourceAndDestination(t *testing.T) {
	src := writeTestParquet(t, []testRow{{Name: "a", Count: 1}})

	if _, err := Copy(src, src, compression.Settings{Codec: "snappy"}); err == nil {
		t.Error("expected error when src == dst, got nil")
	}
}

func TestCopy_MissingSource(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst.parquet")
	if _, err := Copy("/nonexistent/src.parquet", dst, compression.Settings{Codec: "snappy"}); err == nil {
		t.Error("expected error for missing source, got nil")
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
			path := writeTestParquet(t, []testRow{{Name: "a", Count: 1}}, tt.opt)
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
