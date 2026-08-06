package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
