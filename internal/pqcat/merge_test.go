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
