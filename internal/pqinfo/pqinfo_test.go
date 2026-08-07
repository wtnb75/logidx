package pqinfo

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"
)

type testRow struct {
	Name  string `parquet:"name"`
	Count int64  `parquet:"count"`
}

func writeTestParquet(t *testing.T, rows []testRow, opts ...parquet.WriterOption) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.parquet")
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

func TestRead_ReportsRowAndColumnCounts(t *testing.T) {
	path := writeTestParquet(t, []testRow{{Name: "a", Count: 1}, {Name: "b", Count: 2}, {Name: "c", Count: 3}})

	info, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if info.NumRows != 3 {
		t.Errorf("NumRows = %d, want 3", info.NumRows)
	}
	if info.NumRowGroups != 1 {
		t.Errorf("NumRowGroups = %d, want 1", info.NumRowGroups)
	}
	if len(info.Columns) != 2 {
		t.Fatalf("len(Columns) = %d, want 2", len(info.Columns))
	}
	if info.Columns[0].Name != "name" || info.Columns[1].Name != "count" {
		t.Errorf("Columns = %+v, want [name, count] (struct field order)", info.Columns)
	}
	for _, col := range info.Columns {
		if col.NumValues != 3 {
			t.Errorf("column %q NumValues = %d, want 3", col.Name, col.NumValues)
		}
		if col.Codec == "" {
			t.Errorf("column %q Codec is empty", col.Name)
		}
	}
	if info.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want > 0", info.SizeBytes)
	}
}

func TestRead_ReportsCodecFromWriterOption(t *testing.T) {
	path := writeTestParquet(t, []testRow{{Name: "a", Count: 1}}, parquet.Compression(&parquet.Gzip))

	info, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	for _, col := range info.Columns {
		if col.Codec != "GZIP" {
			t.Errorf("column %q Codec = %q, want GZIP", col.Name, col.Codec)
		}
	}
	if info.CompressedBytes <= 0 || info.UncompressedBytes <= 0 {
		t.Errorf("expected positive compressed/uncompressed totals, got %d/%d", info.CompressedBytes, info.UncompressedBytes)
	}
}

func TestRead_MissingFile(t *testing.T) {
	if _, err := Read("/nonexistent/path.parquet"); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestWriteText_ContainsKeyFields(t *testing.T) {
	path := writeTestParquet(t, []testRow{{Name: "a", Count: 1}})
	info, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var buf bytes.Buffer
	if err := info.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	for _, want := range []string{path, "Rows:", "Row groups:", "Columns:", "name", "count", "Compression:"} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteText output missing %q; got:\n%s", want, out)
		}
	}
}

func TestWriteJSON_RoundTrips(t *testing.T) {
	path := writeTestParquet(t, []testRow{{Name: "a", Count: 1}, {Name: "b", Count: 2}})
	info, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var buf bytes.Buffer
	if err := info.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	var decoded Info
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.NumRows != info.NumRows {
		t.Errorf("decoded.NumRows = %d, want %d", decoded.NumRows, info.NumRows)
	}
	if len(decoded.Columns) != len(info.Columns) {
		t.Errorf("decoded columns = %d, want %d", len(decoded.Columns), len(info.Columns))
	}
}

func TestCompressionRatio(t *testing.T) {
	tests := []struct {
		name         string
		compressed   int64
		uncompressed int64
		wantOK       bool
		wantPct      float64
		wantRatio    float64
	}{
		{"typical", 50, 200, true, 25, 4},
		{"empty file", 0, 0, false, 0, 0},
		{"no compression benefit", 200, 200, true, 100, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &Info{CompressedBytes: tt.compressed, UncompressedBytes: tt.uncompressed}
			pct, ratio, ok := info.CompressionRatio()
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if pct != tt.wantPct {
				t.Errorf("pct = %v, want %v", pct, tt.wantPct)
			}
			if ratio != tt.wantRatio {
				t.Errorf("ratio = %v, want %v", ratio, tt.wantRatio)
			}
		})
	}
}
