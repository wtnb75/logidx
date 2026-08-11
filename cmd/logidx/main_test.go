package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// withStdin temporarily replaces os.Stdin with a pipe pre-loaded with
// content, for testing the "-" (read from stdin) convention shared by
// import and restore. os.Stdin is a package-level *os.File var, so this is
// the standard way to redirect it in-process without spawning a subprocess.
func withStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatalf("write to stdin pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close stdin pipe writer: %v", err)
	}

	original := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = original
		_ = r.Close()
	})
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

	if _, err := os.Stat(filepath.Join(outDir, "app_log.parquet")); err != nil {
		t.Errorf("expected output parquet file: %v", err)
	}
	if !strings.Contains(stderr.String(), "file processed") {
		t.Errorf("expected summary log on stderr, got: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "output parquet file") ||
		!strings.Contains(stderr.String(), "compression_ratio") {
		t.Errorf("expected per-file compression stats log on stderr, got: %s", stderr.String())
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

	if _, err := os.Stat(filepath.Join(outDir, "app_log.parquet")); err != nil {
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
	return filepath.Join(outDir, "app_log.parquet")
}

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

func TestRun_CatConcatenatesMultipleFilesInArgumentOrderWithoutMergeKey(t *testing.T) {
	dir := t.TempDir()
	dir1 := filepath.Join(dir, "run1")
	dir2 := filepath.Join(dir, "run2")
	if err := os.MkdirAll(dir1, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir1, err)
	}
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir2, err)
	}
	src1 := importedParquet(t, dir1)
	src2 := importedParquet(t, dir2)
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
	dir1 := filepath.Join(dir, "run1")
	if err := os.MkdirAll(dir1, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir1, err)
	}
	src1 := importedParquet(t, dir1)

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
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir2, err)
	}
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

func TestRun_CatAppliesMaxRowsPerRowGroup(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir)
	dst := filepath.Join(dir, "cat.parquet")

	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", "--output", dst, "--max-rows-per-row-group", "1", src}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	info, err := pqinfo.Read(dst)
	if err != nil {
		t.Fatalf("pqinfo.Read(%s): %v", dst, err)
	}
	if info.NumRows != 2 {
		t.Errorf("NumRows = %d, want 2", info.NumRows)
	}
	if info.NumRowGroups != 2 {
		t.Errorf("NumRowGroups = %d, want 2 for 2 rows at max-rows-per-row-group=1", info.NumRowGroups)
	}
}

func TestRun_CatRejectsInvalidMaxRowsPerRowGroup(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir)
	dst := filepath.Join(dir, "cat.parquet")

	var stdout, stderr bytes.Buffer
	code := run([]string{"cat", "--output", dst, "--max-rows-per-row-group", "-1", src}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2 for an invalid row group setting, got %d (stderr=%s)", code, stderr.String())
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("expected dst to not be created when max-rows-per-row-group is invalid")
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

func TestRun_DumpUsageErrorOnWrongArgCount(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"dump", "onlyone.parquet"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("expected usage message on stderr, got: %s", stderr.String())
	}
}

func TestRun_DumpMissingSourceReturnsExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"dump", filepath.Join(dir, "missing.parquet"), filepath.Join(dir, "dst.txt")}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for missing source, got %d", code)
	}
}

func TestRun_DumpWritesHeaderAndRowsAsText(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir, "--compression", "gzip")
	dumpPath := filepath.Join(dir, "dump.txt")

	var stdout, stderr bytes.Buffer
	code := run([]string{"dump", src, dumpPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "dumped") {
		t.Errorf("expected dump summary on stdout, got: %s", stdout.String())
	}

	content, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) != 3 { // header + 2 matched rows ("[INFO] hello", "[WARN] careful")
		t.Fatalf("got %d lines, want 3 (header + 2 rows): %q", len(lines), content)
	}
	if !strings.Contains(lines[0], `"gzip"`) {
		t.Errorf("expected header to record source codec gzip, got: %s", lines[0])
	}
	if !strings.Contains(lines[1], "INFO") {
		t.Errorf("expected first data row to contain level INFO, got: %s", lines[1])
	}
}

func TestRun_RestoreUsageErrorOnWrongArgCount(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"restore", "onlyone.txt"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("expected usage message on stderr, got: %s", stderr.String())
	}
}

func TestRun_RestoreMissingSourceReturnsExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"restore", filepath.Join(dir, "missing.txt"), filepath.Join(dir, "dst.parquet")}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for missing source, got %d", code)
	}
}

func TestRun_DumpRestoreRoundTripPreservesRowsAndDefaultsToHeaderCodec(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir, "--compression", "gzip")
	dumpPath := filepath.Join(dir, "dump.txt")
	restoredPath := filepath.Join(dir, "restored.parquet")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"dump", src, dumpPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("dump: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"restore", dumpPath, restoredPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("restore: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "restored") {
		t.Errorf("expected restore summary on stdout, got: %s", stdout.String())
	}

	srcInfo, err := pqinfo.Read(src)
	if err != nil {
		t.Fatalf("pqinfo.Read(src): %v", err)
	}
	restoredInfo, err := pqinfo.Read(restoredPath)
	if err != nil {
		t.Fatalf("pqinfo.Read(restored): %v", err)
	}
	if restoredInfo.NumRows != srcInfo.NumRows {
		t.Errorf("restored NumRows = %d, want %d", restoredInfo.NumRows, srcInfo.NumRows)
	}
	if len(restoredInfo.Columns) == 0 || restoredInfo.Columns[0].Codec != "GZIP" {
		t.Errorf("expected restored file to default to dump's recorded codec GZIP, got %+v", restoredInfo.Columns)
	}
}

func TestRun_RestoreCompressionOverridesHeader(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir, "--compression", "gzip")
	dumpPath := filepath.Join(dir, "dump.txt")
	restoredPath := filepath.Join(dir, "restored.parquet")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"dump", src, dumpPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("dump: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"restore", "--compression", "zstd", dumpPath, restoredPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("restore: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	restoredInfo, err := pqinfo.Read(restoredPath)
	if err != nil {
		t.Fatalf("pqinfo.Read(restored): %v", err)
	}
	if len(restoredInfo.Columns) == 0 || restoredInfo.Columns[0].Codec != "ZSTD" {
		t.Errorf("expected restored file codec ZSTD, got %+v", restoredInfo.Columns)
	}
}

func TestRun_ImportReadsLogFromStdin(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)
	outDir := filepath.Join(dir, "out")

	withStdin(t, "[INFO] hello\n[WARN] careful\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	outPath := filepath.Join(outDir, "app_log.parquet")
	info, err := pqinfo.Read(outPath)
	if err != nil {
		t.Fatalf("pqinfo.Read(%s): %v", outPath, err)
	}
	if info.NumRows != 2 {
		t.Errorf("NumRows = %d, want 2", info.NumRows)
	}
}

func TestRun_ImportAppliesMaxRowsPerRowGroup(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)
	outDir := filepath.Join(dir, "out")

	withStdin(t, "[INFO] one\n[WARN] two\n[INFO] three\n[WARN] four\n[INFO] five\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, "--max-rows-per-row-group", "2", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	outPath := filepath.Join(outDir, "app_log.parquet")
	info, err := pqinfo.Read(outPath)
	if err != nil {
		t.Fatalf("pqinfo.Read(%s): %v", outPath, err)
	}
	if info.NumRows != 5 {
		t.Errorf("NumRows = %d, want 5", info.NumRows)
	}
	if info.NumRowGroups != 3 {
		t.Errorf("NumRowGroups = %d, want 3 for 5 rows at max-rows-per-row-group=2", info.NumRowGroups)
	}
}

func TestRun_ImportRejectsInvalidMaxRowsPerRowGroup(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)
	outDir := filepath.Join(dir, "out")

	withStdin(t, "[INFO] one\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, "--max-rows-per-row-group", "-1", "-"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected a non-zero exit code for an invalid row group setting, stderr=%s", stderr.String())
	}
}

func TestRun_DumpToStdoutRoutesSummaryToStderr(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir)

	var stdout, stderr bytes.Buffer
	code := run([]string{"dump", src, "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), `"columns"`) {
		t.Errorf("expected dump JSON Lines payload on stdout, got: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "dumped") {
		t.Errorf("summary line must not be mixed into the piped dump payload on stdout, got: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "dumped") {
		t.Errorf("expected dump summary on stderr when dst is -, got: %s", stderr.String())
	}
}

func TestRun_ImportSupportsTimestampPresetsAndStrptime(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: epoch_event
    pattern: '^(?P<ts>\d+) (?P<message>.*)$'
    fields:
      ts:
        type: timestamp
        format: unix
      message: string
  - name: py_event
    pattern: '^(?P<ts>\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d+[+-]\d{4}) (?P<message>.*)$'
    fields:
      ts:
        type: timestamp
        format: "%Y-%m-%d %H:%M:%S,%f%z"
      message: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)

	epochTime := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	pyTime := time.Date(2026, 8, 7, 9, 15, 30, 500000000, time.UTC)

	logContent := fmt.Sprintf("%d unix epoch message\n2026-08-07 09:15:30,500+0000 python style message\n", epochTime.Unix())
	logPath := writeFile(t, dir, "app.log", logContent)
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("import: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	dumpPath := filepath.Join(dir, "dump.jsonl")

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"dump", filepath.Join(outDir, "epoch_event.parquet"), dumpPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("dump epoch_event: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	epochDump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	wantEpoch := epochTime.Format(time.RFC3339Nano)
	if !strings.Contains(string(epochDump), wantEpoch) {
		t.Errorf("epoch_event dump missing expected timestamp %q, got: %s", wantEpoch, epochDump)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"dump", filepath.Join(outDir, "py_event.parquet"), dumpPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("dump py_event: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	pyDump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	wantPy := pyTime.Format(time.RFC3339Nano)
	if !strings.Contains(string(pyDump), wantPy) {
		t.Errorf("py_event dump missing expected timestamp %q, got: %s", wantPy, pyDump)
	}
}

func TestRun_RestoreReadsDumpFromStdin(t *testing.T) {
	dir := t.TempDir()
	src := importedParquet(t, dir, "--compression", "gzip")
	dumpPath := filepath.Join(dir, "dump.txt")
	restoredPath := filepath.Join(dir, "restored.parquet")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"dump", src, dumpPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("dump: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}
	dumpContent, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump file: %v", err)
	}

	withStdin(t, string(dumpContent))

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"restore", "-", restoredPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("restore: expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	srcInfo, err := pqinfo.Read(src)
	if err != nil {
		t.Fatalf("pqinfo.Read(src): %v", err)
	}
	restoredInfo, err := pqinfo.Read(restoredPath)
	if err != nil {
		t.Fatalf("pqinfo.Read(restored): %v", err)
	}
	if restoredInfo.NumRows != srcInfo.NumRows {
		t.Errorf("restored NumRows = %d, want %d", restoredInfo.NumRows, srcInfo.NumRows)
	}
}

func TestRun_ImportMergesMultipleFilesByTimestampAndAppliesRowGroupLimit(t *testing.T) {
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
	logA := writeFile(t, dir, "a.log", "TS 2026-08-06T12:00:00Z from-a-1\nTS 2026-08-06T12:00:10Z from-a-2\nTS 2026-08-06T12:00:20Z from-a-3\n")
	logB := writeFile(t, dir, "b.log", "TS 2026-08-06T12:00:05Z from-b-1\nTS 2026-08-06T12:00:15Z from-b-2\n")
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, "--max-rows-per-row-group", "2", logA, logB}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	outPath := filepath.Join(outDir, "ts_event.parquet")
	info, err := pqinfo.Read(outPath)
	if err != nil {
		t.Fatalf("pqinfo.Read(%s): %v", outPath, err)
	}
	if info.NumRows != 5 {
		t.Errorf("NumRows = %d, want 5", info.NumRows)
	}
	if info.NumRowGroups != 3 {
		t.Errorf("NumRowGroups = %d, want 3 for 5 rows at max-rows-per-row-group=2", info.NumRowGroups)
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", outPath, "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 6 { // 1 header line + 5 row lines
		t.Fatalf("expected 6 dump lines (header + 5 rows), got %d: %q", len(lines), stdout.String())
	}

	var gotMsgs []string
	for _, line := range lines[1:] {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("unmarshal dump line %q: %v", line, err)
		}
		gotMsgs = append(gotMsgs, row["msg"].(string))
	}

	want := []string{"from-a-1", "from-b-1", "from-a-2", "from-b-2", "from-a-3"}
	if !slices.Equal(gotMsgs, want) {
		t.Errorf("merged order = %v, want %v", gotMsgs, want)
	}
}

const macSyslogRulesYAML = `
rules:
  - name: syslog
    pattern: '^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<process>\S+): (?P<message>.*)$'
    continuation: '^\s+(?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "syslog"
      host: string
      process: string
      message: string
`

const macSyslogSample = `Aug  8 00:30:05 WatanabenoMacBook-Pro syslogd[149]: Configuration Notice:
        ASL Module "com.apple.cdscheduler" claims selected messages.
        Those messages may not appear in standard system log files or in the ASL database.
Aug  8 00:30:10 WatanabenoMacBook-Pro syslogd[149]: single line entry
`

func TestRun_ImportMergesMultiLineSyslogEntryIntoOneRow(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", macSyslogRulesYAML)
	logPath := writeFile(t, dir, "syslog.log", macSyslogSample)
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	outPath := filepath.Join(outDir, "syslog.parquet")
	info, err := pqinfo.Read(outPath)
	if err != nil {
		t.Fatalf("pqinfo.Read(%s): %v", outPath, err)
	}
	if info.NumRows != 2 {
		t.Fatalf("NumRows = %d, want 2 (one merged multi-line entry + one single-line entry)", info.NumRows)
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", outPath, "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 3 { // 1 header + 2 rows
		t.Fatalf("expected 3 dump lines (header + 2 rows), got %d: %q", len(lines), stdout.String())
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &first); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[1], err)
	}
	wantMsg := "Configuration Notice:\n" +
		`ASL Module "com.apple.cdscheduler" claims selected messages.` + "\n" +
		"Those messages may not appear in standard system log files or in the ASL database."
	if first["message"] != wantMsg {
		t.Errorf("first row message = %q, want %q", first["message"], wantMsg)
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &second); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[2], err)
	}
	if second["message"] != "single line entry" {
		t.Errorf("second row message = %q, want %q", second["message"], "single line entry")
	}
}

func TestRun_ImportDecompressesGzipInput(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", cliRulesYAML)

	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	if _, err := gw.Write([]byte("[INFO] hello from gzip\n")); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	logPath := filepath.Join(dir, "app.log.gz")
	if err := os.WriteFile(logPath, gz.Bytes(), 0o644); err != nil {
		t.Fatalf("write gzip file: %v", err)
	}

	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", filepath.Join(outDir, "app_log.parquet"), "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 2 { // 1 header + 1 row
		t.Fatalf("expected 2 dump lines (header + 1 row), got %d: %q", len(lines), stdout.String())
	}

	var row map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &row); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[1], err)
	}
	if row["message"] != "hello from gzip" {
		t.Errorf("message = %q, want %q", row["message"], "hello from gzip")
	}
}

func TestRun_ImportAppliesReplaceRulesToFieldValues(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: app_log
    pattern: '^\[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      level: string
      message:
        type: string
        replace:
          - pattern: '#\d{3}'
            value: ''
          - pattern: '\x1b\[[0-9;]*m'
            value: ''
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)

	// "#015" is a literal 4-character octal control-char escape (not an
	// actual \r byte); "\x1b[31m"/"\x1b[0m" are real ANSI color escape
	// bytes. Both are noise the replace rules must strip while the rest
	// of the message text is preserved.
	logPath := writeFile(t, dir, "app.log", "[INFO] \x1b[31mhello#015 world\x1b[0m\n")

	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", filepath.Join(outDir, "app_log.parquet"), "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 2 { // 1 header + 1 row
		t.Fatalf("expected 2 dump lines (header + 1 row), got %d: %q", len(lines), stdout.String())
	}

	var row map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &row); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[1], err)
	}
	if row["message"] != "hello world" {
		t.Errorf("message = %q, want %q", row["message"], "hello world")
	}
}

func TestRun_ImportExtractsStructuredJSONFieldsAndCollectsExtra(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: container_log
    pattern: '^(?P<time>\S+) (?P<host>\S+) (?P<tag>\S+) (?P<json>\{.*\})$'
    structured:
      source: json
      format: json
    fields:
      time:
        type: timestamp
        format: iso8601
      host: string
      tag: string
      level:
        type: string
        key: level
      event_time:
        type: timestamp
        format: iso8601
        key: time
      message:
        type: string
        key: msg
      extra:
        type: string
        extra: true
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)

	logContent := `2026-08-04T23:26:39.247486+09:00 wtnb4 container/clc/137272bf8941[874] {"time":"2026-08-04T14:26:39.229216178Z","level":"INFO","msg":"caught signal","signal":15}
2026-08-04T23:26:47.661639+09:00 wtnb4 container/clc/131568006cb0[874] {"time":"2026-08-04T14:26:47.661294297Z","level":"INFO","msg":"server starting","listen":{"IP":"::","Port":3000,"Zone":""},"pid":1}
`
	logPath := writeFile(t, dir, "container.log", logContent)
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", filepath.Join(outDir, "container_log.parquet"), "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 3 { // 1 header + 2 rows
		t.Fatalf("expected 3 dump lines (header + 2 rows), got %d: %q", len(lines), stdout.String())
	}

	var row0, row1 map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &row0); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[1], err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &row1); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[2], err)
	}

	if row0["host"] != "wtnb4" || row0["tag"] != "container/clc/137272bf8941[874]" {
		t.Errorf("row0 host/tag = %v/%v, want wtnb4/container/clc/137272bf8941[874]", row0["host"], row0["tag"])
	}
	if row0["level"] != "INFO" || row0["message"] != "caught signal" {
		t.Errorf("row0 level/message = %v/%v, want INFO/caught signal", row0["level"], row0["message"])
	}
	wantExtra0 := `{"signal":15}`
	if row0["extra"] != wantExtra0 {
		t.Errorf("row0 extra = %v, want %q (numbers must stay unquoted JSON numbers, not stringified)", row0["extra"], wantExtra0)
	}
	if eventTime0, _ := row0["event_time"].(string); !strings.HasPrefix(eventTime0, "2026-08-04T14:26:39") {
		t.Errorf("row0 event_time = %v, want prefix 2026-08-04T14:26:39", row0["event_time"])
	}

	if row1["level"] != "INFO" || row1["message"] != "server starting" {
		t.Errorf("row1 level/message = %v/%v, want INFO/server starting", row1["level"], row1["message"])
	}
	wantExtra1 := `{"listen":{"IP":"::","Port":3000,"Zone":""},"pid":1}`
	if row1["extra"] != wantExtra1 {
		t.Errorf("row1 extra = %v, want %q (nested object must stay nested JSON, not a re-stringified/escaped blob)", row1["extra"], wantExtra1)
	}
}

func TestRun_ImportExtractsPresetStructuredFormatFields(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: docker_apprise_access
    pattern: '^(?P<ts>\S+) (?P<host>\S+) (?P<tag>[^\[]+)\[(?P<pid>\d+)\] (?P<access>.*)$'
    structured:
      source: access
      format: apache_clf
    fields:
      ts:
        type: timestamp
        format: iso8601
      host: string
      tag: string
      pid: string
      remote_addr:
        type: string
        key: remote_addr
      method:
        type: string
        key: method
      path:
        type: string
        key: path
      status:
        type: int
        key: status
      access_time:
        type: timestamp
        format: clf
        key: time
      extra:
        type: string
        extra: true
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)

	// Note: the design doc's overview example line ends with a quoted
	// referer/user-agent suffix (`"-" "Deno/2.2.4"`), but apache_clf's
	// preset pattern is anchored with a trailing `$` right after `bytes`
	// (it's CLF, not Combined) - that suffix would make the preset
	// pattern fail to match, sending the whole line to unmatched
	// instead of demonstrating a successful conversion. This line drops
	// that suffix so it's consistent with the `format: apache_clf`
	// rules.yaml in the design doc's own "1. rules.yaml設定" section,
	// which is what this test exercises.
	logContent := `2026-01-01T11:19:03.727584+09:00 wtnb4 container/apprise/209c6867d22d[1019] 172.20.0.20 - - [01/Jan/2026:11:19:03 +0900] "POST /notify/ HTTP/1.1" 200 113
`
	logPath := writeFile(t, dir, "container.log", logContent)
	outDir := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"import", "--rules", rulesPath, "--out", outDir, logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"dump", filepath.Join(outDir, "docker_apprise_access.parquet"), "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%s)", code, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 2 { // 1 header + 1 row
		t.Fatalf("expected 2 dump lines (header + 1 row), got %d: %q", len(lines), stdout.String())
	}

	var row map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &row); err != nil {
		t.Fatalf("unmarshal dump line %q: %v", lines[1], err)
	}

	if row["host"] != "wtnb4" || row["tag"] != "container/apprise/209c6867d22d" || row["pid"] != "1019" {
		t.Errorf("host/tag/pid = %v/%v/%v, want wtnb4/container/apprise/209c6867d22d/1019", row["host"], row["tag"], row["pid"])
	}
	if row["remote_addr"] != "172.20.0.20" || row["method"] != "POST" || row["path"] != "/notify/" {
		t.Errorf("remote_addr/method/path = %v/%v/%v, want 172.20.0.20/POST//notify/", row["remote_addr"], row["method"], row["path"])
	}
	if row["status"] != float64(200) {
		t.Errorf("status = %v, want float64(200)", row["status"])
	}
	if accessTime, _ := row["access_time"].(string); !strings.HasPrefix(accessTime, "2026-01-01T02:19:03") {
		t.Errorf("access_time = %v, want prefix 2026-01-01T02:19:03 (UTC)", row["access_time"])
	}
	wantExtra := `{"bytes":"113","proto":"HTTP/1.1","remote_user":"-"}`
	if row["extra"] != wantExtra {
		t.Errorf("extra = %v, want %q", row["extra"], wantExtra)
	}
}

const expandableRulesYAML = `rules:
  - name: access_log
    preset: apache_clf
`

const collapsibleRulesYAML = `rules:
  - name: access_log
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: clf
      method: string
      path: string
      proto: string
      status: int
      bytes: int
`

func TestExpandCmd_ExpandsPresetToPatternAndFields(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "rules.yaml", expandableRulesYAML)
	dst := filepath.Join(dir, "expanded.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{"expand", src, dst}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read expanded output: %v", err)
	}
	if strings.Contains(string(got), "preset:") {
		t.Errorf("expanded output still has preset::\n%s", got)
	}
	if !strings.Contains(string(got), "pattern:") {
		t.Errorf("expanded output missing pattern::\n%s", got)
	}
	if !strings.Contains(stderr.String(), "expanded rules") {
		t.Errorf("stderr missing completion log, got: %s", stderr.String())
	}
}

func TestExpandCmd_StdinToStdout(t *testing.T) {
	withStdin(t, expandableRulesYAML)

	var stdout, stderr bytes.Buffer
	code := run([]string{"expand", "-", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "pattern:") {
		t.Errorf("stdout missing pattern::\n%s", stdout.String())
	}
}

func TestExpandCmd_UnknownPresetIsError(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "rules.yaml", "rules:\n  - name: r\n    preset: nope\n")
	dst := filepath.Join(dir, "out.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{"expand", src, dst}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1, stderr = %s", code, stderr.String())
	}
}

func TestExpandCmd_WrongArgCountIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"expand", "onlyone.yaml"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: logidx expand") {
		t.Errorf("stderr missing usage message, got: %s", stderr.String())
	}
}

func TestCollapseCmd_CollapsesMatchingPatternToPreset(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "rules.yaml", collapsibleRulesYAML)
	dst := filepath.Join(dir, "collapsed.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{"collapse", src, dst}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read collapsed output: %v", err)
	}
	if !strings.Contains(string(got), "preset: apache_clf") {
		t.Errorf("collapsed output missing preset: apache_clf:\n%s", got)
	}
	if !strings.Contains(stderr.String(), "collapsed rules") {
		t.Errorf("stderr missing completion log, got: %s", stderr.String())
	}
}

func TestCollapseCmd_StdinToStdout(t *testing.T) {
	withStdin(t, collapsibleRulesYAML)

	var stdout, stderr bytes.Buffer
	code := run([]string{"collapse", "-", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "preset: apache_clf") {
		t.Errorf("stdout missing preset: apache_clf:\n%s", stdout.String())
	}
}

func TestCollapseCmd_WrongArgCountIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"collapse"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: logidx collapse") {
		t.Errorf("stderr missing usage message, got: %s", stderr.String())
	}
}
