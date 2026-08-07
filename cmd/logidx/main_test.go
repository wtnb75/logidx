package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"logidx/internal/pqinfo"
)

const cliRulesYAML = `
rules:
  - name: app_log
    pattern: '^\[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      level: string
      message: string
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestRun_MissingRulesFlagReturnsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "somefile.log"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("expected usage message on stderr, got: %s", stderr.String())
	}
}

func TestRun_InvalidRulesFileReturnsExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", "not: [valid, yaml: structure")
	logPath := writeFile(t, dir, "app.log", "[INFO] hello\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", filepath.Join(dir, "out"), logPath}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for invalid rules, got %d", code)
	}
}

func TestRun_ProcessesInputAndWritesOutput(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)
	logPath := writeFile(t, dir, "app.log", "[INFO] hello\n[WARN] careful\n")
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(outDir, "app.app_log.parquet")); err != nil {
		t.Errorf("expected output parquet file: %v", err)
	}
	if !strings.Contains(stderr.String(), "file processed") {
		t.Errorf("expected summary log on stderr, got: %s", stderr.String())
	}
}

func TestRun_MissingInputFileSkipsAndReturnsExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, filepath.Join(dir, "does-not-exist.log")}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for missing input file, got %d", code)
	}
}

func TestRun_ContinuesProcessingRemainingFilesAfterOneFails(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)
	goodLogPath := writeFile(t, dir, "good.log", "[INFO] hello\n")
	missingLogPath := filepath.Join(dir, "does-not-exist.log")
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	// Missing file listed first: the run must not stop there and must still
	// process the good file that follows it.
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, missingLogPath, goodLogPath}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 when one of several files fails, got %d", code)
	}

	if _, err := os.Stat(filepath.Join(outDir, "good.app_log.parquet")); err != nil {
		t.Errorf("expected the good file to still be processed and produce output: %v", err)
	}
	if !strings.Contains(stderr.String(), "file processed") {
		t.Errorf("expected summary log for the good file on stderr, got: %s", stderr.String())
	}
}

// importedParquet runs `import` on a small fixture log and returns the path
// to the resulting parquet file, for use as a copy-command source.
func importedParquet(t *testing.T, dir string, compressionArgs ...string) string {
	t.Helper()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)
	logPath := writeFile(t, dir, "app.log", "[INFO] hello\n[WARN] careful\n")
	outDir := filepath.Join(dir, "out")

	args := append([]string{"import", "--rules", rulesPath, "--out", outDir}, compressionArgs...)
	args = append(args, logPath)

	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("import fixture failed: exit %d, stderr=%s", code, stderr.String())
	}
	return filepath.Join(outDir, "app.app_log.parquet")
}

func TestRun_CopyUsageErrorOnWrongArgCount(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"copy", "onlyone.parquet"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("expected usage message on stderr, got: %s", stderr.String())
	}
}

func TestRun_CopyMissingSourceReturnsExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"copy", filepath.Join(dir, "missing.parquet"), filepath.Join(dir, "dst.parquet")}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for missing source, got %d", code)
	}
}

func TestRun_CopyPreservesRowsAndDefaultsToSourceCodec(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir, "--compression", "gzip")
	dst := filepath.Join(dir, "copy.parquet")

	var stdout, stderr bytes.Buffer
	code := run([]string{"copy", src, dst}, &stdout, &stderr)
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

func TestRun_CopyChangesCompression(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir, "--compression", "gzip")
	dst := filepath.Join(dir, "copy.parquet")

	var stdout, stderr bytes.Buffer
	code := run([]string{"copy", "--compression", "zstd", src, dst}, &stdout, &stderr)
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
	if !strings.Contains(stdout.String(), "copied") {
		t.Errorf("expected copy summary on stdout, got: %s", stdout.String())
	}
}

func TestRun_CopyInvalidCompressionLevelReturnsUsageError(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir)
	dst := filepath.Join(dir, "copy.parquet")

	var stdout, stderr bytes.Buffer
	code := run([]string{"copy", "--compression", "snappy", "--compression-level", "5", src, dst}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2 for invalid compression level, got %d", code)
	}
}
