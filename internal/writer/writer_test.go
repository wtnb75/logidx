package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/wtnb75/logidx/internal/compression"
	"github.com/wtnb75/logidx/internal/rowgroup"
	"github.com/wtnb75/logidx/internal/rules"
	"github.com/wtnb75/logidx/internal/schema"
)

func buildTestSchemas(t *testing.T) map[string]*schema.Built {
	t.Helper()
	built, err := schema.BuildAll([]rules.Rule{
		{
			Name: "app_log",
			Fields: []rules.Field{
				{Name: "level", Type: "string"},
				{Name: "message", Type: "string"},
				{Name: "time", Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildTestSchemas: %v", err)
	}
	return built
}

func TestSet_WriteMatched_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	ts := time.Date(2026, 8, 6, 12, 0, 1, 0, time.UTC)
	err := set.WriteMatched("app_log", map[string]any{
		"level":   "WARN",
		"message": "disk almost full",
		"time":    ts,
	})
	if err != nil {
		t.Fatalf("WriteMatched: %v", err)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["app_log"] != 1 {
		t.Errorf("expected 1 app_log row, got %d", summary.Counts["app_log"])
	}

	outPath := filepath.Join(dir, "app_log.parquet")
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected output file %s: %v", outPath, err)
	}

	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	reader := parquet.NewGenericReader[map[string]any](f, built["app_log"].Schema)
	defer func() { _ = reader.Close() }()
	rows := make([]map[string]any, 1)
	for i := range rows {
		rows[i] = map[string]any{}
	}
	n, err := reader.Read(rows)
	if n != 1 {
		t.Fatalf("expected to read 1 row, got %d (err=%v, size=%d)", n, err, stat.Size())
	}
	if rows[0]["level"] != "WARN" || rows[0]["message"] != "disk almost full" {
		t.Errorf("unexpected row content: %+v", rows[0])
	}
}

func TestSet_WriteMatched_MergesMultipleWritesIntoOneFile(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	ts := time.Date(2026, 8, 6, 12, 0, 1, 0, time.UTC)
	if err := set.WriteMatched("app_log", map[string]any{"level": "INFO", "message": "from file A", "time": ts}); err != nil {
		t.Fatalf("WriteMatched: %v", err)
	}
	if err := set.WriteMatched("app_log", map[string]any{"level": "WARN", "message": "from file B", "time": ts}); err != nil {
		t.Fatalf("WriteMatched: %v", err)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["app_log"] != 2 {
		t.Errorf("expected 2 merged app_log rows, got %d", summary.Counts["app_log"])
	}

	// Exactly one output file for the rule name, regardless of how many
	// separate WriteMatched calls (representing separate input files)
	// contributed rows to it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var parquetFiles []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".parquet" {
			parquetFiles = append(parquetFiles, e.Name())
		}
	}
	if len(parquetFiles) != 1 || parquetFiles[0] != "app_log.parquet" {
		t.Errorf("expected exactly one merged output file app_log.parquet, got %v", parquetFiles)
	}
}

func TestSet_WriteMatched_AppliesRowGroupLimit(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	maxRows := int64(2)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{MaxRows: &maxRows})

	ts := time.Date(2026, 8, 6, 12, 0, 1, 0, time.UTC)
	for i := range 5 {
		err := set.WriteMatched("app_log", map[string]any{
			"level":   "INFO",
			"message": fmt.Sprintf("line %d", i),
			"time":    ts,
		})
		if err != nil {
			t.Fatalf("WriteMatched: %v", err)
		}
	}
	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	outPath := filepath.Join(dir, "app_log.parquet")
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		t.Fatalf("open parquet file: %v", err)
	}

	rowGroups := pf.Metadata().RowGroups
	if len(rowGroups) != 3 {
		t.Fatalf("NumRowGroups = %d, want 3 for 5 rows at MaxRows=2", len(rowGroups))
	}
	wantCounts := []int64{2, 2, 1}
	for i, rg := range rowGroups {
		if rg.NumRows != wantCounts[i] {
			t.Errorf("row group %d NumRows = %d, want %d", i, rg.NumRows, wantCounts[i])
		}
	}
}

func TestSet_NoFileCreatedForUnusedName(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "app_log.parquet")); !os.IsNotExist(err) {
		t.Errorf("expected no output file to be created, stat err = %v", err)
	}
}

func TestSet_WriteUnmatched_CreatesFileLazilyWithSourceAndLineNumber(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	if err := set.WriteUnmatched("access.log", 3, "unmatched", "garbled line"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Unmatched != 1 {
		t.Errorf("expected Unmatched=1, got %d", summary.Unmatched)
	}

	content, err := os.ReadFile(filepath.Join(dir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "access.log\t3\tunmatched\tgarbled line\n"
	if string(content) != want {
		t.Errorf("got %q, want %q", string(content), want)
	}
}

func TestSet_WriteUnmatched_DisambiguatesSameLineNumberFromDifferentSources(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	if err := set.WriteUnmatched("a.log", 5, "unmatched", "from a"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}
	if err := set.WriteUnmatched("b.log", 5, "unmatched", "from b"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "a.log\t5\tunmatched\tfrom a\nb.log\t5\tunmatched\tfrom b\n"
	if string(content) != want {
		t.Errorf("got %q, want %q", string(content), want)
	}
}

func TestSet_WriteUnmatched_WritesArbitraryReasonVerbatim(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	if err := set.WriteUnmatched("access.log", 7, "ignored:max_length", "a very long line"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}
	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "access.log\t7\tignored:max_length\ta very long line\n"
	if string(content) != want {
		t.Errorf("got %q, want %q", string(content), want)
	}
}

func TestSet_NoUnmatchedFileWhenNoUnmatchedLines(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "unmatched.txt")); !os.IsNotExist(err) {
		t.Errorf("expected no unmatched file to be created, stat err = %v", err)
	}
}
