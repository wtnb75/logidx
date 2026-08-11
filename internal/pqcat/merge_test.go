package pqcat

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/wtnb75/logidx/internal/compression"
	"github.com/wtnb75/logidx/internal/rowgroup"
	"github.com/wtnb75/logidx/internal/rules"
	"github.com/wtnb75/logidx/internal/schema"
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

func TestCat_MergeAppliesRowGroupLimit(t *testing.T) {
	dir := t.TempDir()
	fields := []rules.Field{{Name: "ts", Type: "timestamp"}, {Name: "name", Type: "string"}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := func(offsetSec int, name string) map[string]any {
		return map[string]any{"ts": base.Add(time.Duration(offsetSec) * time.Second).UnixMicro(), "name": name}
	}

	// 5 rows total across two files, taking the timestamp-merge path (both
	// files share a timestamp column), with max_rows=2 - should produce
	// ceil(5/2) = 3 row groups, mirroring
	// TestRun_ImportMergesMultipleFilesByTimestampAndAppliesRowGroupLimit's
	// row-group-count math for the import path's identical flag.
	src1 := filepath.Join(dir, "src1.parquet")
	src2 := filepath.Join(dir, "src2.parquet")
	writeRows(t, src1, fields, []map[string]any{row(0, "a"), row(20, "c"), row(40, "e")})
	writeRows(t, src2, fields, []map[string]any{row(10, "b"), row(30, "d")})
	dst := filepath.Join(dir, "dst.parquet")

	maxRows := int64(2)
	rows, err := Cat([]string{src1, src2}, dst, compression.Settings{Codec: "snappy"}, rowgroup.Settings{MaxRows: &maxRows})
	if err != nil {
		t.Fatalf("Cat: %v", err)
	}
	if rows != 5 {
		t.Fatalf("rows = %d, want 5", rows)
	}

	got := readNameColumn(t, dst)
	want := []string{"a", "b", "c", "d", "e"}
	if !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v (ascending timestamp order across files)", got, want)
	}

	pf := openParquetFileForTest(t, dst)
	if numGroups := len(pf.Metadata().RowGroups); numGroups != 3 {
		t.Errorf("NumRowGroups = %d, want 3 for 5 merged rows at max-rows-per-row-group=2", numGroups)
	}
}

// openParquetFileForTest opens path and returns its parquet.File, closing
// the underlying os.File automatically at test cleanup.
func openParquetFileForTest(t *testing.T, path string) *parquet.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		t.Fatalf("open parquet %s: %v", path, err)
	}
	return pf
}

// TestMergeRows_ClosesEachCursorOnceWhenEarlierFileAlreadyExhausted exercises
// the scenario the code review flagged: src1 is empty, so mergeRows' initial
// cursor-population loop closes and nils its cursor slot immediately (the
// normal, non-error exhaustion path); src2's merge-key column has the wrong
// type, so processing it hits the isInt-false error path and calls
// closeRemaining() while src1's slot is already nil. Before the fix,
// closeRemaining() would have closed src1's already-closed cursor a second
// time - harmless with this vendored parquet-go version (Close is
// defensively idempotent), but a real violation of "each cursor is closed
// exactly once" that would be fragile against a future parquet-go version.
// This test calls mergeRows directly (bypassing Cat's own schema.Equal
// check, which would normally rule this scenario out) so it can force the
// error path without needing to fabricate a lower-level I/O failure; its
// main job is to keep this exact code path exercised so a future nil-guard
// regression here panics instead of going unnoticed.
func TestMergeRows_ClosesEachCursorOnceWhenEarlierFileAlreadyExhausted(t *testing.T) {
	dir := t.TempDir()

	emptyFields := []rules.Field{{Name: "ts", Type: "timestamp"}, {Name: "name", Type: "string"}}
	src1 := filepath.Join(dir, "src1.parquet")
	writeRows(t, src1, emptyFields, nil)

	mismatchedFields := []rules.Field{{Name: "ts", Type: "string"}, {Name: "name", Type: "string"}}
	src2 := filepath.Join(dir, "src2.parquet")
	writeRows(t, src2, mismatchedFields, []map[string]any{{"ts": "not-a-timestamp", "name": "x"}})

	pf1 := openParquetFileForTest(t, src1)
	pf2 := openParquetFileForTest(t, src2)

	dst := filepath.Join(dir, "dst.parquet")
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create dst: %v", err)
	}
	defer func() { _ = out.Close() }()
	writer := parquet.NewGenericWriter[map[string]any](out, pf2.Schema())
	defer func() { _ = writer.Close() }()

	_, err = mergeRows([]*parquet.File{pf1, pf2}, []string{src1, src2}, "ts", writer)
	if err == nil {
		t.Fatal("mergeRows: want error for non-timestamp merge key column, got nil")
	}
	if !strings.Contains(err.Error(), "not a timestamp column") {
		t.Errorf("mergeRows error = %v, want error mentioning %q", err, "not a timestamp column")
	}
}
