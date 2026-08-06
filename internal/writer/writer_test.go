package writer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/rules"
	"logidx/internal/schema"
)

func buildTestSchemas(t *testing.T) map[string]*schema.Built {
	t.Helper()
	built, err := schema.BuildAll([]rules.Rule{
		{
			Name: "app_log",
			Fields: map[string]rules.Field{
				"level":   {Type: "string"},
				"message": {Type: "string"},
				"time":    {Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"},
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
	set := NewSet(dir, "access", built)

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

	outPath := filepath.Join(dir, "access.app_log.parquet")
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

func TestSet_NoFileCreatedForUnusedName(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, "access", built)

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "access.app_log.parquet")); !os.IsNotExist(err) {
		t.Errorf("expected no output file to be created, stat err = %v", err)
	}
}

func TestSet_WriteUnmatched_CreatesFileLazilyWithLineNumbers(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, "access", built)

	if err := set.WriteUnmatched(3, "garbled line"); err != nil {
		t.Fatalf("WriteUnmatched: %v", err)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Unmatched != 1 {
		t.Errorf("expected Unmatched=1, got %d", summary.Unmatched)
	}

	content, err := os.ReadFile(filepath.Join(dir, "access.unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "3\tgarbled line\n"
	if string(content) != want {
		t.Errorf("got %q, want %q", string(content), want)
	}
}

func TestSet_NoUnmatchedFileWhenNoUnmatchedLines(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	set := NewSet(dir, "access", built)

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "access.unmatched.txt")); !os.IsNotExist(err) {
		t.Errorf("expected no unmatched file to be created, stat err = %v", err)
	}
}
