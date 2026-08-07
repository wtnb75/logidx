package convert

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/compression"
	"logidx/internal/logging"
	"logidx/internal/rowgroup"
	"logidx/internal/rules"
	"logidx/internal/schema"
)

const specExampleRulesYAML = `
rules:
  - name: nginx_access
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: "02/Jan/2006:15:04:05 -0700"
      method: string
      path: string
      proto: string
      status: int
      bytes: int

  - name: app_log
    pattern: '^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      level: string
      message: string
`

const specExampleLog = `192.168.1.1 - - [06/Aug/2026:12:00:00 +0900] "GET /index.html HTTP/1.1" 200 512
2026-08-06T12:00:01+09:00 [INFO] user logged in
this is a garbled line that matches nothing
192.168.1.2 - - [06/Aug/2026:12:00:02 +0900] "GET /api HTTP/1.1" 200 128
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestFile_SpecExample_ProducesExpectedOutputs(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", specExampleRulesYAML)
	logPath := writeFile(t, dir, "access.log", specExampleLog)
	outDir := filepath.Join(dir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)

	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if err := Files([]string{logPath}, outDir, cfg, compression.Settings{}, rowgroup.Settings{}, logger, now); err != nil {
		t.Fatalf("Files: %v", err)
	}

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}

	nginxPath := filepath.Join(outDir, "nginx_access.parquet")
	if countParquetRows(t, nginxPath, built["nginx_access"].Schema) != 2 {
		t.Errorf("expected 2 rows in %s", nginxPath)
	}

	wantColumnOrder := []string{"remote_addr", "remote_user", "time", "method", "path", "proto", "status", "bytes"}
	if gotColumnOrder := parquetFieldNames(t, nginxPath); !slices.Equal(gotColumnOrder, wantColumnOrder) {
		t.Errorf("nginx_access column order = %v, want %v (rules.yaml's fields: declaration order, not alphabetical)", gotColumnOrder, wantColumnOrder)
	}

	appPath := filepath.Join(outDir, "app_log.parquet")
	if countParquetRows(t, appPath, built["app_log"].Schema) != 1 {
		t.Errorf("expected 1 row in %s", appPath)
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := logPath + "\t3\tthis is a garbled line that matches nothing\n"
	if string(unmatchedContent) != want {
		t.Errorf("got %q, want %q", string(unmatchedContent), want)
	}
}

func TestFiles_MultipleInputsMergeIntoOneOutputPerRule(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", twoRuleRulesYAML)
	logA := writeFile(t, dir, "a.log", "A from file a\nnot matched\n")
	logB := writeFile(t, dir, "b.log", "A from file b\nB also from file b\n")
	outDir := filepath.Join(dir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	if err := Files([]string{logA, logB}, outDir, cfg, compression.Settings{}, rowgroup.Settings{}, logger, now); err != nil {
		t.Fatalf("Files: %v", err)
	}

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}

	// rule_a matched once in each input file: merged into a single
	// rule_a.parquet with 2 rows, not two separate per-file files.
	ruleAPath := filepath.Join(outDir, "rule_a.parquet")
	if got := countParquetRows(t, ruleAPath, built["rule_a"].Schema); got != 2 {
		t.Errorf("expected 2 merged rule_a rows, got %d", got)
	}
	ruleBPath := filepath.Join(outDir, "rule_b.parquet")
	if got := countParquetRows(t, ruleBPath, built["rule_b"].Schema); got != 1 {
		t.Errorf("expected 1 rule_b row, got %d", got)
	}

	// One shared unmatched.txt, with each line tagged by its source file so
	// "line 2" from two different inputs isn't ambiguous.
	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := logA + "\t2\tnot matched\n"
	if string(unmatchedContent) != want {
		t.Errorf("got %q, want %q", string(unmatchedContent), want)
	}
}

func TestFiles_ContinuesPastAFailedInputAndStillMergesTheRest(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", twoRuleRulesYAML)
	goodLog := writeFile(t, dir, "good.log", "A from good file\n")
	missingLog := filepath.Join(dir, "does-not-exist.log")
	outDir := filepath.Join(dir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	// Missing file listed first: Files must not stop there and must still
	// merge the good file that follows it into the output.
	err = Files([]string{missingLog, goodLog}, outDir, cfg, compression.Settings{}, rowgroup.Settings{}, logger, now)
	if err == nil {
		t.Fatal("expected an error for the missing input file")
	}

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	ruleAPath := filepath.Join(outDir, "rule_a.parquet")
	if got := countParquetRows(t, ruleAPath, built["rule_a"].Schema); got != 1 {
		t.Errorf("expected the good file's row to still be merged in, got %d rows", got)
	}
}

const twoRuleRulesYAML = `
rules:
  - name: rule_a
    pattern: '^A (?P<msg>.*)$'
    fields:
      msg: string
  - name: rule_b
    pattern: '^B (?P<msg>.*)$'
    fields:
      msg: string
`

// TestFile_WriteErrorMidFile_StillClosesEarlierWriters forces a write-time
// error partway through a file (rule_b's output path is pre-occupied by a
// directory, so os.Create fails when rule_b's writer is first needed) and
// asserts that File still returns an error AND that rule_a's Parquet file -
// already opened and written to before the failure - was closed properly
// (flushed footer, fully readable) rather than left truncated/corrupt with
// a leaked open descriptor.
func TestFile_WriteErrorMidFile_StillClosesEarlierWriters(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", twoRuleRulesYAML)
	logPath := writeFile(t, dir, "input.log", "A first line\nB second line\n")
	outDir := filepath.Join(dir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	// Pre-occupy rule_b's would-be output path with a directory so that
	// writer.Set's os.Create for it fails once line 2 is processed - after
	// rule_a's writer has already been created and written to.
	blockedPath := filepath.Join(outDir, "rule_b.parquet")
	if err := os.Mkdir(blockedPath, 0o755); err != nil {
		t.Fatalf("mkdir blocked path: %v", err)
	}

	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	if err := Files([]string{logPath}, outDir, cfg, compression.Settings{}, rowgroup.Settings{}, logger, now); err == nil {
		t.Fatal("expected Files to return an error when rule_b's output path is blocked")
	}

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}

	ruleAPath := filepath.Join(outDir, "rule_a.parquet")
	if _, err := os.Stat(ruleAPath); err != nil {
		t.Fatalf("expected rule_a's file to exist: %v", err)
	}
	// A truncated/unclosed Parquet file (missing footer) fails to open as a
	// GenericReader or yields 0 rows instead of the 1 row actually written;
	// countParquetRows reading back exactly 1 row proves Close() ran.
	if got := countParquetRows(t, ruleAPath, built["rule_a"].Schema); got != 1 {
		t.Errorf("expected rule_a's file to be validly closed with 1 row, got %d rows", got)
	}
}

func countParquetRows(t *testing.T, path string, sch *parquet.Schema) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	reader := parquet.NewGenericReader[map[string]any](f, sch)
	defer func() { _ = reader.Close() }()

	total := 0
	buf := make([]map[string]any, 8)
	for i := range buf {
		buf[i] = map[string]any{}
	}
	for {
		n, err := reader.Read(buf)
		total += n
		if err != nil {
			break
		}
	}
	return total
}

func parquetFieldNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		t.Fatalf("open parquet %s: %v", path, err)
	}

	var names []string
	for _, field := range pf.Schema().Fields() {
		names = append(names, field.Name())
	}
	return names
}
