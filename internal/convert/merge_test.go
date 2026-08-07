package convert

import (
	"bytes"
	"path/filepath"
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
