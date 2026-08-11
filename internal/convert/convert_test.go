package convert

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/wtnb75/logidx/internal/compression"
	"github.com/wtnb75/logidx/internal/logging"
	"github.com/wtnb75/logidx/internal/rowgroup"
	"github.com/wtnb75/logidx/internal/rules"
	"github.com/wtnb75/logidx/internal/schema"
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

func TestFiles_MetaFieldsCaptureSourceFileAndLineNumberInOutput(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: access
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
      log_file:
        type: string
        meta: source_file
      log_line:
        type: int
        meta: source_line
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	logA := writeFile(t, dir, "a.log", "first from a\nsecond from a\n")
	logB := writeFile(t, dir, "b.log", "first from b\n")
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

	rows := readParquetRows(t, filepath.Join(outDir, "access.parquet"), built["access"].Schema)
	if len(rows) != 3 {
		t.Fatalf("expected 3 merged rows, got %d", len(rows))
	}

	byMsg := map[string]map[string]any{}
	for _, row := range rows {
		byMsg[row["msg"].(string)] = row
	}

	if got := byMsg["first from a"]; got["log_file"] != logA || got["log_line"] != int64(1) {
		t.Errorf(`"first from a" row = %+v, want log_file=%q log_line=1`, got, logA)
	}
	if got := byMsg["second from a"]; got["log_file"] != logA || got["log_line"] != int64(2) {
		t.Errorf(`"second from a" row = %+v, want log_file=%q log_line=2`, got, logA)
	}
	if got := byMsg["first from b"]; got["log_file"] != logB || got["log_line"] != int64(1) {
		t.Errorf(`"first from b" row = %+v, want log_file=%q log_line=1`, got, logB)
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

// TestFiles_ContinuesPastACorruptGzipInputAndStillMergesTheRest mirrors
// TestFiles_ContinuesPastAFailedInputAndStillMergesTheRest, but the failure
// is a corrupt .gz file (caught by decompress.Wrap at open time) instead of
// a missing file (caught by os.Open) - both must leave the rest of the
// merge unaffected.
func TestFiles_ContinuesPastACorruptGzipInputAndStillMergesTheRest(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", twoRuleRulesYAML)

	badPath := filepath.Join(dir, "bad.gz")
	if err := os.WriteFile(badPath, []byte("not actually gzip data"), 0o644); err != nil {
		t.Fatalf("write bad gzip file: %v", err)
	}
	goodLog := writeFile(t, dir, "good.log", "A from good file\n")
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

	// Corrupt file listed first: Files must not stop there and must still
	// merge the good file that follows it into the output.
	err = Files([]string{badPath, goodLog}, outDir, cfg, compression.Settings{}, rowgroup.Settings{}, logger, now)
	if err == nil {
		t.Fatal("expected an error for the corrupt gzip input file")
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

func readParquetRows(t *testing.T, path string, sch *parquet.Schema) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	reader := parquet.NewGenericReader[map[string]any](f, sch)
	defer func() { _ = reader.Close() }()

	var rows []map[string]any
	buf := make([]map[string]any, 8)
	for i := range buf {
		buf[i] = map[string]any{}
	}
	for {
		n, err := reader.Read(buf)
		for i := range n {
			rows = append(rows, buf[i])
			buf[i] = map[string]any{}
		}
		if err != nil {
			break
		}
	}
	return rows
}

func TestFiles_MergesMultipleFilesByTimestampAcrossRuleTypes(t *testing.T) {
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
	logA := writeFile(t, dir, "a.log", "TS 2026-08-06T12:00:00Z from-a-1\nTS 2026-08-06T12:00:10Z from-a-2\n")
	logB := writeFile(t, dir, "b.log", "TS 2026-08-06T12:00:05Z from-b-1\nTS 2026-08-06T12:00:15Z from-b-2\n")
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
	rows := readParquetRows(t, filepath.Join(outDir, "ts_event.parquet"), built["ts_event"].Schema)

	var gotMsgs []string
	for _, row := range rows {
		gotMsgs = append(gotMsgs, row["msg"].(string))
	}
	want := []string{"from-a-1", "from-b-1", "from-a-2", "from-b-2"}
	if !slices.Equal(gotMsgs, want) {
		t.Errorf("merged order = %v, want %v", gotMsgs, want)
	}
}

func TestFiles_MergesNonOverlappingFileTimeRangesInGlobalOrder(t *testing.T) {
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
	// file B's times are entirely later than file A's: the merge still
	// needs to visit every candidate from A before any from B, exercising
	// the heap machinery even though the result matches plain file order.
	logA := writeFile(t, dir, "a.log", "TS 2026-08-06T12:00:00Z from-a-1\nTS 2026-08-06T12:00:01Z from-a-2\n")
	logB := writeFile(t, dir, "b.log", "TS 2026-08-06T13:00:00Z from-b-1\nTS 2026-08-06T13:00:01Z from-b-2\n")
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
	rows := readParquetRows(t, filepath.Join(outDir, "ts_event.parquet"), built["ts_event"].Schema)

	var gotMsgs []string
	for _, row := range rows {
		gotMsgs = append(gotMsgs, row["msg"].(string))
	}
	want := []string{"from-a-1", "from-a-2", "from-b-1", "from-b-2"}
	if !slices.Equal(gotMsgs, want) {
		t.Errorf("merged order = %v, want %v", gotMsgs, want)
	}
}

func TestFiles_NoInputFilesProducesNoOutputFiles(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", twoRuleRulesYAML)
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

	if err := Files(nil, outDir, cfg, compression.Settings{}, rowgroup.Settings{}, logger, now); err != nil {
		t.Fatalf("Files: %v", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no output files for zero inputs, got %v", entries)
	}
}

func TestFiles_MergeContinuesPastAFailedInputWhenRulesHaveMergeKeys(t *testing.T) {
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
	goodLog := writeFile(t, dir, "good.log", "TS 2026-08-06T12:00:00Z from good file\n")
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

	err = Files([]string{missingLog, goodLog}, outDir, cfg, compression.Settings{}, rowgroup.Settings{}, logger, now)
	if err == nil {
		t.Fatal("expected an error for the missing input file")
	}

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	tsPath := filepath.Join(outDir, "ts_event.parquet")
	if got := countParquetRows(t, tsPath, built["ts_event"].Schema); got != 1 {
		t.Errorf("expected the good file's merge-key row to still be merged in, got %d rows", got)
	}
}

// TestFiles_MixedMergeKeyAndPlainRulesAcrossFiles covers the design's test
// plan item that plain (no merge key) rule rows preserve their own file's
// arrival order, in a scenario with BOTH a timestamp rule and a
// non-timestamp rule present, across multiple input files. Once any rule in
// the config has a merge key, fileCursor.advance() stops scanning at each
// merge-key-eligible row and yields control back to mergeFiles, so
// plain_event rows (and unmatched.txt lines) from different files can now
// interleave - unlike the old strictly-sequential processInput. Only each
// file's own relative order is guaranteed, not a global "all of A then all
// of B" ordering, so this test checks per-file relative order rather than a
// single fixed global sequence.
func TestFiles_MixedMergeKeyAndPlainRulesAcrossFiles(t *testing.T) {
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
  - name: plain_event
    pattern: '^PLAIN (?P<msg>.*)$'
    fields:
      msg: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	logA := writeFile(t, dir, "a.log", "TS 2026-08-06T12:00:00Z from-a-1\nPLAIN a-plain-1\nTS 2026-08-06T12:00:10Z from-a-2\nPLAIN a-plain-2\n")
	logB := writeFile(t, dir, "b.log", "TS 2026-08-06T12:00:05Z from-b-1\nPLAIN b-plain-1\nTS 2026-08-06T12:00:15Z from-b-2\nPLAIN b-plain-2\n")
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

	// ts_event has a merge key: its rows must be in global ascending
	// timestamp order across both files.
	tsRows := readParquetRows(t, filepath.Join(outDir, "ts_event.parquet"), built["ts_event"].Schema)
	var gotTS []string
	for _, row := range tsRows {
		gotTS = append(gotTS, row["msg"].(string))
	}
	wantTS := []string{"from-a-1", "from-b-1", "from-a-2", "from-b-2"}
	if !slices.Equal(gotTS, wantTS) {
		t.Errorf("ts_event merged order = %v, want %v", gotTS, wantTS)
	}

	// plain_event has no merge key: each file's own rows must keep that
	// file's relative order, even though rows from A and B may now
	// interleave with each other.
	plainRows := readParquetRows(t, filepath.Join(outDir, "plain_event.parquet"), built["plain_event"].Schema)
	var gotAOrder, gotBOrder []string
	for _, row := range plainRows {
		msg := row["msg"].(string)
		switch msg {
		case "a-plain-1", "a-plain-2":
			gotAOrder = append(gotAOrder, msg)
		case "b-plain-1", "b-plain-2":
			gotBOrder = append(gotBOrder, msg)
		default:
			t.Errorf("unexpected plain_event msg %q", msg)
		}
	}
	if want := []string{"a-plain-1", "a-plain-2"}; !slices.Equal(gotAOrder, want) {
		t.Errorf("file a's plain_event rows out of order: got %v, want %v", gotAOrder, want)
	}
	if want := []string{"b-plain-1", "b-plain-2"}; !slices.Equal(gotBOrder, want) {
		t.Errorf("file b's plain_event rows out of order: got %v, want %v", gotBOrder, want)
	}
}

// TestFiles_RejectsMoreThanOneStdinInput guards against two "-" entries
// both wrapping os.Stdin: mergeFiles would open two independent fileCursors
// over the same underlying stdin, and since both get read from at
// different times during the k-way merge (not simply sequentially), their
// bufio.Scanner read buffers would race for the same bytes.
func TestFiles_RejectsMoreThanOneStdinInput(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", twoRuleRulesYAML)
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

	err = Files([]string{"-", "-"}, outDir, cfg, compression.Settings{}, rowgroup.Settings{}, logger, now)
	if err == nil {
		t.Fatal("expected an error for two \"-\" (stdin) inputs")
	}
	if got := err.Error(); got != `only one input may be "-" (stdin), got 2` {
		t.Errorf("error = %q, want %q", got, `only one input may be "-" (stdin), got 2`)
	}
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
