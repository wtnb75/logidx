package convert

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logidx/internal/compression"
	"logidx/internal/logging"
	"logidx/internal/rowgroup"
	"logidx/internal/rules"
	"logidx/internal/schema"
	"logidx/internal/writer"
)

func TestMergeKeyField_PicksFirstTimestampFieldInDeclarationOrder(t *testing.T) {
	ruleList := []rules.Rule{
		{
			Name: "with_two_timestamps",
			Fields: []rules.Field{
				{Name: "level", Type: "string"},
				{Name: "start", Type: "timestamp"},
				{Name: "end", Type: "timestamp"},
			},
		},
		{
			Name: "no_timestamp",
			Fields: []rules.Field{
				{Name: "level", Type: "string"},
			},
		},
	}

	got := mergeKeyField(ruleList)

	if got["with_two_timestamps"] != "start" {
		t.Errorf("with_two_timestamps merge key = %q, want %q", got["with_two_timestamps"], "start")
	}
	if _, ok := got["no_timestamp"]; ok {
		t.Errorf("no_timestamp should have no merge key, got %q", got["no_timestamp"])
	}
}

func TestMergeKeyField_SameNameRulesUseFirstOccurrence(t *testing.T) {
	// rules.Validate guarantees same-name rules declare identical
	// name+type fields, so taking the first occurrence (like
	// schema.BuildAll does) is always consistent with the rest.
	ruleList := []rules.Rule{
		{
			Name:   "dup",
			Fields: []rules.Field{{Name: "ts", Type: "timestamp"}},
		},
		{
			Name:   "dup",
			Fields: []rules.Field{{Name: "ts", Type: "timestamp"}},
		},
	}

	got := mergeKeyField(ruleList)

	if len(got) != 1 || got["dup"] != "ts" {
		t.Errorf("mergeKeyField() = %v, want map[dup:ts]", got)
	}
}

func TestFileCursor_Advance_SplitsEligibleFromIneligibleRows(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: with_ts
    pattern: '^TS (?P<time>\S+) (?P<msg>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      msg: string
  - name: no_ts
    pattern: '^PLAIN (?P<msg>.*)$'
    fields:
      msg: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "PLAIN first\nTS 2026-08-06T12:00:00Z second\nnot matched\nPLAIN third\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	// The two "PLAIN" lines and the unmatched line are written immediately
	// as advance() passes over them; only the "TS ..." line comes back as a
	// candidate.
	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.name != "with_ts" {
		t.Errorf("candidate name = %q, want with_ts", cand.name)
	}
	wantTime := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if !cand.sortValue.Equal(wantTime) {
		t.Errorf("candidate sortValue = %v, want %v", cand.sortValue, wantTime)
	}
	if cursor.counts["no_ts"] != 1 {
		t.Errorf("expected no_ts to be counted once already, got %d", cursor.counts["no_ts"])
	}

	// Second advance reaches EOF: "PLAIN third" was already written
	// immediately, and no further candidate remains.
	_, ok, err = cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false at EOF")
	}
	if cursor.counts["no_ts"] != 2 {
		t.Errorf("expected no_ts to be counted twice by EOF, got %d", cursor.counts["no_ts"])
	}
	if cursor.unmatched != 1 {
		t.Errorf("expected 1 unmatched line, got %d", cursor.unmatched)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["no_ts"] != 2 {
		t.Errorf("expected 2 no_ts rows written, got %d", summary.Counts["no_ts"])
	}
	// with_ts was returned as a candidate, never written via WriteMatched by
	// advance() itself — the caller (mergeFiles, Task 8) is responsible for
	// writing candidates once they're popped off the merge heap.
	if summary.Counts["with_ts"] != 0 {
		t.Errorf("expected with_ts NOT written by advance() itself, got %d", summary.Counts["with_ts"])
	}
}

func TestFileCursor_Advance_MetaFieldsCaptureSourceFileAndLineNumber(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: access
    pattern: '^(?P<time>\S+) (?P<msg>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      msg: string
      log_file:
        type: string
        meta: source_file
      log_line:
        type: int
        meta: source_line
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "2026-08-06T12:00:00Z first\n2026-08-06T12:00:01Z second\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["log_file"] != logPath {
		t.Errorf("log_file = %v, want %q", cand.values["log_file"], logPath)
	}
	if cand.values["log_line"] != int64(1) {
		t.Errorf("log_line = %v, want int64(1)", cand.values["log_line"])
	}

	cand2, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("second advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand2.values["log_file"] != logPath {
		t.Errorf("second log_file = %v, want %q", cand2.values["log_file"], logPath)
	}
	if cand2.values["log_line"] != int64(2) {
		t.Errorf("second log_line = %v, want int64(2)", cand2.values["log_line"])
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFileCursor_Advance_MetaSourceLineUsesEntryStartLineForContinuation(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: syslog
    pattern: '^TS (?P<time>\S+) (?P<host>\S+) (?P<message>.*)$'
    continuation: '^  (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      host: string
      message: string
      log_line:
        type: int
        meta: source_line
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z host1 Configuration Notice:\n  ASL Module claims messages.\n  Those messages may not appear.\nTS 2026-08-06T12:00:05Z host1 next entry\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["log_line"] != int64(1) {
		t.Errorf("log_line = %v, want int64(1) (the entry's starting physical line, not a continuation line)", cand.values["log_line"])
	}

	cand2, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("second advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand2.values["log_line"] != int64(4) {
		t.Errorf("second log_line = %v, want int64(4)", cand2.values["log_line"])
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFileCursor_Advance_ReturnsErrorOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", "rules: []\n")
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	_, err = newFileCursor(filepath.Join(dir, "does-not-exist.log"), 0, cfg, mergeKeyField(cfg.Rules), nil, logger, now)
	if err == nil {
		t.Fatal("expected an error opening a missing file")
	}
}

func TestNewFileCursor_DecompressesGzipInput(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: with_ts
    pattern: '^TS (?P<time>\S+) (?P<msg>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      msg: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	if _, err := gw.Write([]byte("TS 2026-08-06T12:00:00Z from gzip\n")); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	logPath := filepath.Join(dir, "in.log.gz")
	if err := os.WriteFile(logPath, gz.Bytes(), 0o644); err != nil {
		t.Fatalf("write gzip file: %v", err)
	}

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["msg"] != "from gzip" {
		t.Errorf("msg = %q, want %q", cand.values["msg"], "from gzip")
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestNewFileCursor_CorruptGzipReturnsWrappedOpenErrorAndClosesFile(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.gz")
	if err := os.WriteFile(badPath, []byte("not actually gzip data"), 0o644); err != nil {
		t.Fatalf("write bad gzip file: %v", err)
	}

	rulesPath := writeFile(t, dir, "rules.yaml", "rules: []\n")
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	_, err = newFileCursor(badPath, 0, cfg, mergeKeyField(cfg.Rules), nil, logger, now)
	if err == nil {
		t.Fatal("expected an error opening a corrupt .gz file")
	}
	if !strings.Contains(err.Error(), "open input") {
		t.Errorf("expected error to mention \"open input\", got: %v", err)
	}
}

func TestFileCursor_Close_ClosesDecompressorAndFile(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", "rules: []\n")
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	if _, err := gw.Write([]byte("irrelevant\n")); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	logPath := filepath.Join(dir, "in.log.gz")
	if err := os.WriteFile(logPath, gz.Bytes(), 0o644); err != nil {
		t.Fatalf("write gzip file: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), nil, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	if cursor.decompressCloser == nil {
		t.Fatal("expected a non-nil decompressCloser for a .gz input")
	}
	if err := cursor.close(); err != nil {
		t.Errorf("close() returned error: %v", err)
	}
}

func TestFileCursor_Advance_MergesContinuationLinesIntoOneEntry(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: syslog
    pattern: '^TS (?P<time>\S+) (?P<host>\S+) (?P<message>.*)$'
    continuation: '^  (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      host: string
      message: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z host1 Configuration Notice:\n  ASL Module claims messages.\n  Those messages may not appear.\nTS 2026-08-06T12:00:05Z host1 next entry\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	wantMsg := "Configuration Notice:\nASL Module claims messages.\nThose messages may not appear."
	if cand.values["message"] != wantMsg {
		t.Errorf("message = %q, want %q", cand.values["message"], wantMsg)
	}
	if cand.lineNum != 1 {
		t.Errorf("lineNum = %d, want 1 (entry's starting line)", cand.lineNum)
	}

	cand2, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("second advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand2.values["message"] != "next entry" {
		t.Errorf("second message = %q, want %q", cand2.values["message"], "next entry")
	}

	_, ok, err = cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false at EOF")
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFileCursor_Advance_OrphanContinuationLineIsUnmatched(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: syslog
    pattern: '^TS (?P<time>\S+) (?P<message>.*)$'
    continuation: '^  (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      message: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "  orphan continuation line\nTS 2026-08-06T12:00:00Z real entry\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["message"] != "real entry" {
		t.Errorf("message = %q, want %q", cand.values["message"], "real entry")
	}
	if cursor.unmatched != 1 {
		t.Errorf("unmatched = %d, want 1", cursor.unmatched)
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t1\t  orphan continuation line\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}

func TestFileCursor_Advance_MultiLineEntryFlushedAtEOF(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: syslog
    pattern: '^TS (?P<time>\S+) (?P<message>.*)$'
    continuation: '^  (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      message: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z Notice:\n  continuation line\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["message"] != "Notice:\ncontinuation line" {
		t.Errorf("message = %q, want %q", cand.values["message"], "Notice:\ncontinuation line")
	}

	_, ok, err = cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false at EOF")
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFileCursor_Advance_ContinuationConversionFailureFallsThroughToNextRule(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: counter
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    continuation: '^MORE (?P<count>\d+)$'
    fields:
      time: string
      count: int
  - name: raw_start_line
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    fields:
      time: string
      count: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// Folding two "count" captures together with a newline ("5\n6") is not
	// parseable as an int, forcing a type-conversion failure on the closed
	// multi-line entry. The first line then falls back to raw_start_line,
	// which matches the same text but converts count as a string; the
	// second line no longer belongs to any entry and is rematched on its
	// own, matching neither rule.
	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z START 5\nMORE 6\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	_, ok, err := cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false: neither raw_start_line nor the unmatched line has a merge key")
	}
	if cursor.counts["raw_start_line"] != 1 {
		t.Errorf("counts[raw_start_line] = %d, want 1", cursor.counts["raw_start_line"])
	}
	if cursor.counts["counter"] != 0 {
		t.Errorf("counts[counter] = %d, want 0", cursor.counts["counter"])
	}
	if cursor.unmatched != 1 {
		t.Errorf("unmatched = %d, want 1 (only the second line, MORE 6)", cursor.unmatched)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["raw_start_line"] != 1 {
		t.Errorf("expected 1 raw_start_line row written, got %d", summary.Counts["raw_start_line"])
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t2\tMORE 6\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}

func TestFileCursor_Advance_ContinuationConversionFailureFallsThroughToAnotherContinuationRule(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: counter
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    continuation: '^MORE (?P<count>\d+)$'
    fields:
      time: string
      count: int
  - name: counter_loose
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    continuation: '^MORE (?P<count>\d+)$'
    fields:
      time: string
      count: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// counter's entry fails (count folds to "5\n6", not a valid int). The
	// first line falls back to counter_loose, whose continuation pattern
	// is tried fresh against the second line (MORE 6) rather than being
	// replayed from counter's already-consumed state - it folds in
	// successfully this time since counter_loose's count field is a
	// string.
	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z START 5\nMORE 6\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	_, ok, err := cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false: counter_loose has no merge key")
	}
	if cursor.counts["counter_loose"] != 1 {
		t.Errorf("counts[counter_loose] = %d, want 1", cursor.counts["counter_loose"])
	}
	if cursor.counts["counter"] != 0 {
		t.Errorf("counts[counter] = %d, want 0", cursor.counts["counter"])
	}
	if cursor.unmatched != 0 {
		t.Errorf("unmatched = %d, want 0: both lines ended up folded into counter_loose", cursor.unmatched)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["counter_loose"] != 1 {
		t.Errorf("expected 1 counter_loose row written, got %d", summary.Counts["counter_loose"])
	}
}

func TestFileCursor_Advance_ContinuationAllCandidatesExhaustedFirstLineUnmatchedRestRematchIndependently(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: counter
    pattern: '^TS (?P<time>\S+) START (?P<count>\d+)$'
    continuation: '^MORE (?P<count>\d+)$'
    fields:
      time: string
      count: int
  - name: more_line
    pattern: '^MORE (?P<count>\d+)$'
    fields:
      count: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// counter's entry fails (count folds to "5\n6"). Its only remaining
	// candidate, more_line, doesn't match the first line's text at all, so
	// the first line alone becomes an unmatched record. The second line,
	// rematched independently from scratch, does match more_line on its
	// own.
	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z START 5\nMORE 6\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	_, ok, err := cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false: more_line has no merge key")
	}
	if cursor.counts["more_line"] != 1 {
		t.Errorf("counts[more_line] = %d, want 1", cursor.counts["more_line"])
	}
	if cursor.unmatched != 1 {
		t.Errorf("unmatched = %d, want 1 (only the first line)", cursor.unmatched)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["more_line"] != 1 {
		t.Errorf("expected 1 more_line row written, got %d", summary.Counts["more_line"])
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched: %v", err)
	}
	want := logPath + "\t1\tTS 2026-08-06T12:00:00Z START 5\n"
	if string(unmatchedContent) != want {
		t.Errorf("unmatched.txt = %q, want %q", string(unmatchedContent), want)
	}
}

func TestFileCursor_Advance_EOFClosedEntryCascadesThroughMultipleContinuationCandidates(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: counter_int
    pattern: '^TS (?P<time>\S+) START (?P<count>\S+)$'
    continuation: '^MORE (?P<count>\S+)$'
    fields:
      time: string
      count: int
  - name: counter_int2
    pattern: '^TS (?P<time>\S+) START (?P<count>\S+)$'
    continuation: '^MORE (?P<count>\S+)$'
    fields:
      time: string
      count: int
  - name: raw_line
    pattern: '^(?P<line>.*)$'
    fields:
      line: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	// A single line with no continuation line following it, so the entry
	// closes at EOF (not by a fresh line breaking it out). count fails int
	// conversion under counter_int, then again under counter_int2 - both
	// retries happen inside advance()'s EOF branch, with no line left in
	// the file, before raw_line finally succeeds.
	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z START notanumber\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	_, ok, err := cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false: raw_line has no merge key")
	}
	if cursor.counts["raw_line"] != 1 {
		t.Errorf("counts[raw_line] = %d, want 1", cursor.counts["raw_line"])
	}
	if cursor.counts["counter_int"] != 0 || cursor.counts["counter_int2"] != 0 {
		t.Errorf("counts[counter_int]=%d counts[counter_int2]=%d, want both 0", cursor.counts["counter_int"], cursor.counts["counter_int2"])
	}
	if cursor.unmatched != 0 {
		t.Errorf("unmatched = %d, want 0", cursor.unmatched)
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["raw_line"] != 1 {
		t.Errorf("expected 1 raw_line row written, got %d", summary.Counts["raw_line"])
	}
}

func TestFileCursor_Advance_SingleLineFallsThroughToNextRuleOnConversionFailure(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: strict
    pattern: '^(?P<status>\S+)$'
    fields:
      status: int
  - name: loose
    pattern: '^(?P<status>\S+)$'
    fields:
      status: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "not-a-number\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	// "strict" matches the pattern but "not-a-number" fails int conversion;
	// the line falls through to "loose", which has no merge key, so it's
	// written immediately rather than returned as a candidate.
	_, ok, err := cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false: loose has no merge key and is written immediately")
	}
	if cursor.unmatched != 0 {
		t.Errorf("unmatched = %d, want 0: the line matched loose after strict failed conversion", cursor.unmatched)
	}
	if cursor.counts["loose"] != 1 {
		t.Errorf("counts[loose] = %d, want 1", cursor.counts["loose"])
	}

	summary, err := set.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary.Counts["loose"] != 1 {
		t.Errorf("expected 1 loose row written, got %d", summary.Counts["loose"])
	}
	if summary.Counts["strict"] != 0 {
		t.Errorf("expected 0 strict rows written, got %d", summary.Counts["strict"])
	}
}

func TestFileCursor_Advance_ZeroCaptureContinuationAbsorbsDecorativeLine(t *testing.T) {
	dir := t.TempDir()
	rulesYAML := `
rules:
  - name: syslog
    pattern: '^TS (?P<time>\S+) (?P<message>.*)$'
    continuation: '^----$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      message: string
`
	rulesPath := writeFile(t, dir, "rules.yaml", rulesYAML)
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	logPath := writeFile(t, dir, "in.log", "TS 2026-08-06T12:00:00Z hello\n----\n")

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}
	outDir := t.TempDir()
	set := writer.NewSet(outDir, built, compression.Settings{}, rowgroup.Settings{})

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cursor, err := newFileCursor(logPath, 0, cfg, mergeKeyField(cfg.Rules), set, logger, now)
	if err != nil {
		t.Fatalf("newFileCursor: %v", err)
	}
	defer func() { _ = cursor.close() }()

	cand, ok, err := cursor.advance()
	if err != nil || !ok {
		t.Fatalf("advance() = ok=%v err=%v, want ok=true", ok, err)
	}
	if cand.values["message"] != "hello" {
		t.Errorf("message = %q, want %q (decorative line must not be appended)", cand.values["message"], "hello")
	}

	_, ok, err = cursor.advance()
	if err != nil {
		t.Fatalf("advance() error = %v", err)
	}
	if ok {
		t.Fatal("advance() ok = true, want false at EOF")
	}

	if _, err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
