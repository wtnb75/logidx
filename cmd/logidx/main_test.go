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
	if !strings.Contains(stdout.String(), "bytes") || !strings.Contains(stdout.String(), "%") {
		t.Errorf("expected compression ratio info on stdout, got: %s", stdout.String())
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
