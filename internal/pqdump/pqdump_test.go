package pqdump

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/wtnb75/logidx/internal/compression"
	"github.com/wtnb75/logidx/internal/pqinfo"
	"github.com/wtnb75/logidx/internal/rowgroup"
)

func writeSourceParquet(t *testing.T) string {
	t.Helper()
	group := parquet.Group{
		"level":   parquet.Required(parquet.String()),
		"count":   parquet.Required(parquet.Int(64)),
		"ratio":   parquet.Required(parquet.Leaf(parquet.DoubleType)),
		"seen_at": parquet.Required(parquet.Timestamp(parquet.Microsecond)),
	}
	sch := parquet.NewSchema("row", group)

	path := filepath.Join(t.TempDir(), "src.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := parquet.NewGenericWriter[map[string]any](f, sch, parquet.Compression(&parquet.Gzip))

	ts := time.Date(2026, 8, 7, 12, 34, 56, 123456000, time.UTC)
	rows := []map[string]any{
		{"level": "INFO", "count": int64(1), "ratio": 0.5, "seen_at": ts.UnixMicro()},
		{"level": "WARN", "count": int64(2), "ratio": 1.5, "seen_at": ts.Add(time.Second).UnixMicro()},
	}
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

func TestDump_WritesHeaderThenOneJSONObjectPerRow(t *testing.T) {
	src := writeSourceParquet(t)

	var buf bytes.Buffer
	rows, err := Dump(src, &buf)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	if rows != 2 {
		t.Errorf("rows = %d, want 2", rows)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (header + 2 rows): %q", len(lines), buf.String())
	}

	var header Header
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header.Compression.Codec != "gzip" {
		t.Errorf("header.Compression.Codec = %q, want gzip", header.Compression.Codec)
	}
	wantCols := map[string]string{"level": "string", "count": "int", "ratio": "float", "seen_at": "timestamp"}
	if len(header.Columns) != len(wantCols) {
		t.Fatalf("got %d columns, want %d", len(header.Columns), len(wantCols))
	}
	for _, col := range header.Columns {
		if wantCols[col.Name] != col.Type {
			t.Errorf("column %q type = %q, want %q", col.Name, col.Type, wantCols[col.Name])
		}
	}

	var row0 map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &row0); err != nil {
		t.Fatalf("unmarshal row 0: %v", err)
	}
	if row0["level"] != "INFO" {
		t.Errorf("row0[level] = %v, want INFO", row0["level"])
	}
	seenAt, ok := row0["seen_at"].(string)
	if !ok {
		t.Fatalf("row0[seen_at] = %v (%T), want a string", row0["seen_at"], row0["seen_at"])
	}
	if _, err := time.Parse(time.RFC3339Nano, seenAt); err != nil {
		t.Errorf("seen_at %q is not RFC3339Nano: %v", seenAt, err)
	}
	if !strings.Contains(seenAt, "123456") {
		t.Errorf("seen_at %q does not preserve microsecond precision", seenAt)
	}
}

func writeSourceParquetWithOptionalColumn(t *testing.T) string {
	t.Helper()
	group := parquet.Group{
		"level": parquet.Required(parquet.String()),
		"note":  parquet.Optional(parquet.String()),
	}
	sch := parquet.NewSchema("row", group)

	path := filepath.Join(t.TempDir(), "src.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := parquet.NewGenericWriter[map[string]any](f, sch)

	rows := []map[string]any{
		{"level": "INFO", "note": "hello"},
		{"level": "WARN", "note": nil},
	}
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

func TestDump_HeaderRecordsOptionalColumns(t *testing.T) {
	src := writeSourceParquetWithOptionalColumn(t)

	var buf bytes.Buffer
	if _, err := Dump(src, &buf); err != nil {
		t.Fatalf("Dump: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	var header Header
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}

	byName := map[string]Column{}
	for _, col := range header.Columns {
		byName[col.Name] = col
	}
	if byName["level"].Optional {
		t.Error(`"level": expected Optional=false, got true`)
	}
	if !byName["note"].Optional {
		t.Error(`"note": expected Optional=true, got false`)
	}

	var row1 map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &row1); err != nil {
		t.Fatalf("unmarshal row 1: %v", err)
	}
	if v, ok := row1["note"]; !ok || v != nil {
		t.Errorf(`row1["note"] = %v (present=%v), want null`, v, ok)
	}
}

func TestRestore_OptionalColumnRoundTripsNullValues(t *testing.T) {
	src := writeSourceParquetWithOptionalColumn(t)
	var dump bytes.Buffer
	if _, err := Dump(src, &dump); err != nil {
		t.Fatalf("Dump: %v", err)
	}

	dstPath := filepath.Join(t.TempDir(), "restored.parquet")
	if _, err := Restore(&dump, dstPath, compression.Settings{}, rowgroup.Settings{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	f, err := os.Open(dstPath)
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

	for _, field := range pf.Schema().Fields() {
		if field.Name() == "note" && !field.Optional() {
			t.Error(`restored column "note": expected Optional, got Required`)
		}
		if field.Name() == "level" && !field.Required() {
			t.Error(`restored column "level": expected Required, got Optional`)
		}
	}

	reader := parquet.NewGenericReader[map[string]any](pf, pf.Schema())
	defer func() { _ = reader.Close() }()
	buf := make([]map[string]any, 2)
	buf[0], buf[1] = map[string]any{}, map[string]any{}
	n, _ := reader.Read(buf)
	if n != 2 {
		t.Fatalf("read %d rows back, want 2", n)
	}
	if buf[0]["note"] != "hello" {
		t.Errorf(`restored row0["note"] = %v, want "hello"`, buf[0]["note"])
	}
	if buf[1]["note"] != nil {
		t.Errorf(`restored row1["note"] = %v, want nil`, buf[1]["note"])
	}
}

func TestDump_MissingSource(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Dump("/nonexistent/src.parquet", &buf); err == nil {
		t.Error("expected error for missing source, got nil")
	}
}

func TestRestore_RoundTripsRowsAndTimestampPrecision(t *testing.T) {
	src := writeSourceParquet(t)

	var dump bytes.Buffer
	if _, err := Dump(src, &dump); err != nil {
		t.Fatalf("Dump: %v", err)
	}

	dstPath := filepath.Join(t.TempDir(), "restored.parquet")
	rows, err := Restore(&dump, dstPath, compression.Settings{}, rowgroup.Settings{})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rows != 2 {
		t.Errorf("rows = %d, want 2", rows)
	}

	srcInfo, err := pqinfo.Read(src)
	if err != nil {
		t.Fatalf("pqinfo.Read(src): %v", err)
	}
	dstInfo, err := pqinfo.Read(dstPath)
	if err != nil {
		t.Fatalf("pqinfo.Read(dst): %v", err)
	}
	if dstInfo.NumRows != srcInfo.NumRows {
		t.Errorf("dst NumRows = %d, want %d", dstInfo.NumRows, srcInfo.NumRows)
	}
	// Header carried no explicit codec override, so Restore should fall back
	// to the dump's recorded source codec (gzip), matching pqcopy's default
	// behavior for an unspecified --compression.
	if len(dstInfo.Columns) == 0 || dstInfo.Columns[0].Codec != "GZIP" {
		t.Errorf("expected dst codec GZIP (inherited from dump header), got %+v", dstInfo.Columns)
	}

	f, err := os.Open(dstPath)
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
	reader := parquet.NewGenericReader[map[string]any](pf, pf.Schema())
	defer func() { _ = reader.Close() }()
	buf := make([]map[string]any, 2)
	buf[0], buf[1] = map[string]any{}, map[string]any{}
	n, _ := reader.Read(buf)
	if n != 2 {
		t.Fatalf("read %d rows back, want 2", n)
	}

	wantTS := time.Date(2026, 8, 7, 12, 34, 56, 123456000, time.UTC).UnixMicro()
	if buf[0]["seen_at"] != wantTS {
		t.Errorf("restored seen_at = %v, want %v (exact microsecond round-trip)", buf[0]["seen_at"], wantTS)
	}
	if buf[0]["count"] != int64(1) {
		t.Errorf("restored count = %v (%T), want int64(1)", buf[0]["count"], buf[0]["count"])
	}
	if buf[0]["ratio"] != 0.5 {
		t.Errorf("restored ratio = %v, want 0.5", buf[0]["ratio"])
	}
}

func TestRestore_CLICompressionOverridesHeader(t *testing.T) {
	src := writeSourceParquet(t)
	var dump bytes.Buffer
	if _, err := Dump(src, &dump); err != nil {
		t.Fatalf("Dump: %v", err)
	}

	dstPath := filepath.Join(t.TempDir(), "restored.parquet")
	if _, err := Restore(&dump, dstPath, compression.Settings{Codec: "zstd"}, rowgroup.Settings{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	dstInfo, err := pqinfo.Read(dstPath)
	if err != nil {
		t.Fatalf("pqinfo.Read: %v", err)
	}
	if len(dstInfo.Columns) == 0 || dstInfo.Columns[0].Codec != "ZSTD" {
		t.Errorf("expected dst codec ZSTD, got %+v", dstInfo.Columns)
	}
}

func TestRestore_PreservesHeaderColumnOrder(t *testing.T) {
	// Deliberately not alphabetical, so a schema rebuild that silently
	// re-sorts columns (e.g. building a plain parquet.Group, a map) would
	// be caught.
	dump := strings.NewReader(
		`{"columns":[{"name":"zzz","type":"string"},{"name":"aaa","type":"int"},{"name":"mmm","type":"string"}],"compression":{"codec":"zstd"}}` + "\n" +
			`{"zzz":"z","aaa":1,"mmm":"m"}` + "\n")
	dstPath := filepath.Join(t.TempDir(), "restored.parquet")

	if _, err := Restore(dump, dstPath, compression.Settings{}, rowgroup.Settings{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	f, err := os.Open(dstPath)
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

	want := []string{"zzz", "aaa", "mmm"}
	var got []string
	for _, field := range pf.Schema().Fields() {
		got = append(got, field.Name())
	}
	if len(got) != len(want) {
		t.Fatalf("got %d fields, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column order = %v, want %v (header's declared order, not alphabetical)", got, want)
			break
		}
	}
}

func TestRestore_EmptyHeaderColumns(t *testing.T) {
	dump := strings.NewReader(`{"columns":[],"compression":{"codec":"zstd"}}` + "\n")
	dstPath := filepath.Join(t.TempDir(), "restored.parquet")
	if _, err := Restore(dump, dstPath, compression.Settings{}, rowgroup.Settings{}); err == nil {
		t.Error("expected error for header with no columns, got nil")
	}
}

func TestRestore_MalformedHeader(t *testing.T) {
	dump := strings.NewReader("not json\n")
	dstPath := filepath.Join(t.TempDir(), "restored.parquet")
	if _, err := Restore(dump, dstPath, compression.Settings{}, rowgroup.Settings{}); err == nil {
		t.Error("expected error for malformed header, got nil")
	}
}

func TestRestore_BadTimestampValue(t *testing.T) {
	dump := strings.NewReader(`{"columns":[{"name":"seen_at","type":"timestamp"}],"compression":{"codec":"zstd"}}` + "\n" +
		`{"seen_at":"not-a-timestamp"}` + "\n")
	dstPath := filepath.Join(t.TempDir(), "restored.parquet")
	if _, err := Restore(dump, dstPath, compression.Settings{}, rowgroup.Settings{}); err == nil {
		t.Error("expected error for malformed timestamp value, got nil")
	}
}
