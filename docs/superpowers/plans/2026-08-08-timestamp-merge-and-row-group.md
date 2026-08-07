# 複数ファイルのタイムスタンプ順マージ / row group分割制御 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge multiple `logidx import` input files into globally timestamp-ordered output rows (per rule, when the rule has a `timestamp` field), and let users cap Parquet row group size via `--max-rows-per-row-group` / `rules.yaml`'s `row_group.max_rows`.

**Architecture:** A new `internal/rowgroup` package mirrors `internal/compression`'s CLI>config>default `Settings`/`Resolve`/`Validate` pattern and plugs into `internal/writer.Set` via `parquet.MaxRowsPerRowGroup`. `internal/convert` replaces its "process each file fully, one after another" loop with a streaming k-way merge: one `fileCursor` per input file exposes `advance()`, which writes ineligible rows (no matching rule, or a rule with no `timestamp` field) immediately and returns the next timestamp-bearing row as a `candidate`; a `container/heap`-based min-heap across all cursors' current candidates picks the globally-earliest one to write next.

**Tech Stack:** Go 1.25, `github.com/parquet-go/parquet-go`, `container/heap`, `testing` (table-driven + integration tests reading back written Parquet files).

## Global Constraints

- Follow existing patterns exactly: `internal/compression`'s `Settings`/`Resolve`/`Validate`/`WriterOption` shape is the template for `internal/rowgroup`.
- Every exported type/function needs a doc comment in the existing terse style (why, not what — see `internal/writer/writer.go`, `internal/compression/compression.go`).
- Code comments in English; this is a Go codebase with no other-language comments anywhere.
- No behavior change for existing callers when a rule has no `timestamp` field or there's only one input file — this must hold by construction (the merge heap degenerates to file-arrival order), not by a special-cased branch.
- Run `go build ./... && go test ./...` before every commit in every task. Run `task fmt && task lint` in the final task as a whole-repo gate.
- Commit after each task with a message describing the behavior added, not the files touched.

---

## Task 1: `internal/rowgroup` package

**Files:**
- Create: `internal/rowgroup/rowgroup.go`
- Test: `internal/rowgroup/rowgroup_test.go`

**Interfaces:**
- Produces: `rowgroup.Settings{MaxRows *int64}`, `rowgroup.Resolve(cli, file Settings) Settings`, `(Settings) Validate() error`, `(Settings) Option() (parquet.WriterOption, bool)`

- [ ] **Step 1: Write the failing tests**

Create `internal/rowgroup/rowgroup_test.go`:

```go
package rowgroup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"
)

func int64Ptr(n int64) *int64 { return &n }

func TestResolve_NoSettingLeavesMaxRowsNil(t *testing.T) {
	got := Resolve(Settings{}, Settings{})
	if got.MaxRows != nil {
		t.Errorf("Resolve() = %+v, want MaxRows nil", got)
	}
}

func TestResolve_FileOverridesDefault(t *testing.T) {
	got := Resolve(Settings{}, Settings{MaxRows: int64Ptr(1000)})
	if got.MaxRows == nil || *got.MaxRows != 1000 {
		t.Errorf("Resolve() = %+v, want MaxRows=1000", got)
	}
}

func TestResolve_CLIOverridesFile(t *testing.T) {
	got := Resolve(Settings{MaxRows: int64Ptr(500)}, Settings{MaxRows: int64Ptr(1000)})
	if got.MaxRows == nil || *got.MaxRows != 500 {
		t.Errorf("Resolve() = %+v, want MaxRows=500", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		s       Settings
		wantErr bool
	}{
		{"unset valid", Settings{}, false},
		{"positive valid", Settings{MaxRows: int64Ptr(1)}, false},
		{"zero invalid", Settings{MaxRows: int64Ptr(0)}, true},
		{"negative invalid", Settings{MaxRows: int64Ptr(-1)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOption_UnsetReturnsNotOK(t *testing.T) {
	_, ok := Settings{}.Option()
	if ok {
		t.Error("Option() ok = true, want false when MaxRows is unset")
	}
}

func TestOption_SetSplitsIntoMultipleRowGroups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	schema := parquet.SchemaOf(struct{ X int64 }{})
	opt, ok := Settings{MaxRows: int64Ptr(2)}.Option()
	if !ok {
		t.Fatal("Option() ok = false, want true")
	}
	w := parquet.NewGenericWriter[struct{ X int64 }](f, schema, opt)

	rows := make([]struct{ X int64 }, 5)
	for i := range rows {
		rows[i].X = int64(i)
	}
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = rf.Close() }()
	fi, err := rf.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(rf, fi.Size())
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rowgroup/... -v`
Expected: FAIL — package `rowgroup` does not exist yet (build failure).

- [ ] **Step 3: Write the implementation**

Create `internal/rowgroup/rowgroup.go`:

```go
// Package rowgroup selects the Parquet row group row-count limit used when
// writing output files, and resolves that choice from CLI flags, the rules
// config file, and a built-in default (unlimited — parquet-go's own
// default), in that priority order.
package rowgroup

import (
	"fmt"

	"github.com/parquet-go/parquet-go"
)

// Settings selects a Parquet row group row-count limit. The zero value
// means "unset" (MaxRows == nil), used as an input to Resolve. Unlike
// compression.Settings, Resolve does not force this to a non-nil default:
// unset stays unset, meaning unlimited row group size (parquet-go's own
// default), so existing output file structure doesn't silently change for
// callers that never configure this.
type Settings struct {
	MaxRows *int64 `yaml:"max_rows" json:"max_rows,omitempty"`
}

// Resolve merges CLI-flag and config-file settings, with cli taking
// priority over file. There is no built-in non-nil default: if neither
// sets MaxRows, the result leaves it nil.
func Resolve(cli, file Settings) Settings {
	resolved := Settings{}
	if file.MaxRows != nil {
		resolved.MaxRows = file.MaxRows
	}
	if cli.MaxRows != nil {
		resolved.MaxRows = cli.MaxRows
	}
	return resolved
}

// Validate checks that MaxRows, if set, is positive.
func (s Settings) Validate() error {
	if s.MaxRows != nil && *s.MaxRows <= 0 {
		return fmt.Errorf("row group max_rows must be positive, got %d", *s.MaxRows)
	}
	return nil
}

// Option returns the parquet.WriterOption that applies this row group
// limit, and whether one applies at all. Callers must call Validate first.
// When MaxRows is nil (unset), ok is false and callers should not add any
// option — parquet-go's own default (unlimited) applies.
func (s Settings) Option() (option parquet.WriterOption, ok bool) {
	if s.MaxRows == nil {
		return nil, false
	}
	return parquet.MaxRowsPerRowGroup(*s.MaxRows), true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rowgroup/... -v`
Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add internal/rowgroup/rowgroup.go internal/rowgroup/rowgroup_test.go
git commit -m "Add internal/rowgroup package for row group row-count limits"
```

---

## Task 2: Wire `rowgroup.Settings` into `internal/writer.Set`

**Files:**
- Modify: `internal/writer/writer.go`
- Modify: `internal/writer/writer_test.go`
- Modify: `internal/convert/convert.go:43` (temporary literal — fully wired in Task 3)

**Interfaces:**
- Consumes: `rowgroup.Settings`, `(rowgroup.Settings) Option() (parquet.WriterOption, bool)` (Task 1)
- Produces: `writer.NewSet(outDir string, built map[string]*schema.Built, comp compression.Settings, rg rowgroup.Settings) *Set` (signature change — every existing caller must be updated in this task)

- [ ] **Step 1: Write the failing test**

In `internal/writer/writer_test.go`, add `"fmt"` to the import block and `"logidx/internal/rowgroup"`, then add this test after `TestSet_WriteMatched_MergesMultipleWritesIntoOneFile`:

```go
func TestSet_WriteMatched_AppliesRowGroupLimit(t *testing.T) {
	dir := t.TempDir()
	built := buildTestSchemas(t)
	maxRows := int64(2)
	set := NewSet(dir, built, compression.Settings{}, rowgroup.Settings{MaxRows: &maxRows})

	ts := time.Date(2026, 8, 6, 12, 0, 1, 0, time.UTC)
	for i := 0; i < 5; i++ {
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
```

Also update every existing `NewSet(dir, built, compression.Settings{})` call in this file (there are 6: in `TestSet_WriteMatched_RoundTrip`, `TestSet_WriteMatched_MergesMultipleWritesIntoOneFile`, `TestSet_NoFileCreatedForUnusedName`, `TestSet_WriteUnmatched_CreatesFileLazilyWithSourceAndLineNumber`, `TestSet_WriteUnmatched_DisambiguatesSameLineNumberFromDifferentSources`, `TestSet_NoUnmatchedFileWhenNoUnmatchedLines`) to add a fourth argument: `NewSet(dir, built, compression.Settings{}, rowgroup.Settings{})`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/writer/... -v`
Expected: FAIL — build failure (`NewSet` called with 3 args where 4 are expected, since we haven't updated `writer.go` yet). This is expected: the test file and implementation change in this task together, but writing the test edits first documents the target signature.

- [ ] **Step 3: Update the implementation**

In `internal/writer/writer.go`, add the import:

```go
	"logidx/internal/compression"
	"logidx/internal/rowgroup"
	"logidx/internal/schema"
```

Add a field to `Set` (after `compression compression.Settings`):

```go
type Set struct {
	outDir      string
	built       map[string]*schema.Built
	compression compression.Settings
	rowGroup    rowgroup.Settings
```

Update `NewSet`:

```go
// NewSet creates a writer Set writing outputs into outDir. built maps rule
// name -> derived Parquet schema (from schema.BuildAll), used lazily when
// the first row for that name arrives. comp selects the Parquet page
// compression codec applied to every output file in this Set. rg, if its
// MaxRows is set, caps the number of rows per row group on every output
// file in this Set; if unset, parquet-go's own default (unlimited) applies.
func NewSet(outDir string, built map[string]*schema.Built, comp compression.Settings, rg rowgroup.Settings) *Set {
	return &Set{
		outDir:         outDir,
		built:          built,
		compression:    comp,
		rowGroup:       rg,
		parquetWriters: map[string]*parquet.GenericWriter[map[string]any]{},
		parquetFiles:   map[string]*os.File{},
		paths:          map[string]string{},
		counts:         map[string]int{},
	}
}
```

Update `writerFor`'s writer construction:

```go
	opts := []parquet.WriterOption{s.compression.WriterOption()}
	if opt, ok := s.rowGroup.Option(); ok {
		opts = append(opts, opt)
	}
	w := parquet.NewGenericWriter[map[string]any](f, built.Schema, opts...)
```

Finally, in `internal/convert/convert.go:43`, update the one call site so the package still builds (this becomes a real config value in Task 3):

```go
	set := writer.NewSet(outDir, built, comp, rowgroup.Settings{})
```

Add `"logidx/internal/rowgroup"` to `convert.go`'s import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/writer/... ./internal/convert/... -v`
Expected: PASS (all tests, including the new `TestSet_WriteMatched_AppliesRowGroupLimit`)

Run: `go build ./...`
Expected: builds cleanly (confirms `cmd/logidx/main.go`'s `convert.Files` call is untouched and still compiles — it doesn't call `writer.NewSet` directly)

- [ ] **Step 5: Commit**

```bash
git add internal/writer/writer.go internal/writer/writer_test.go internal/convert/convert.go
git commit -m "Wire rowgroup.Settings into writer.Set's Parquet writer options"
```

---

## Task 3: Wire `rowgroup.Settings` into `rules.Config` and `convert.Files`

**Files:**
- Modify: `internal/rules/rules.go`
- Modify: `internal/rules/validate.go`
- Modify: `internal/rules/validate_test.go`
- Modify: `internal/convert/convert.go`
- Modify: `internal/convert/convert_test.go`
- Modify: `cmd/logidx/main.go:110` (temporary literal — fully wired in Task 4)

**Interfaces:**
- Consumes: `rowgroup.Settings`, `Resolve`, `(Settings) Validate() error` (Task 1)
- Produces: `rules.Config.RowGroup rowgroup.Settings`; `convert.Files(inputPaths []string, outDir string, cfg *rules.Config, comp compression.Settings, rg rowgroup.Settings, logger *slog.Logger, now time.Time) error` (signature change — every existing caller must be updated in this task)

- [ ] **Step 1: Write the failing test**

In `internal/rules/validate_test.go`, add `"logidx/internal/rowgroup"` to the imports, then add:

```go
func TestValidate_InvalidRowGroupSettingIsError(t *testing.T) {
	badMaxRows := int64(0)
	cfg := &Config{
		RowGroup: rowgroup.Settings{MaxRows: &badMaxRows},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "row_group") {
		t.Errorf("expected error to mention row_group, got: %v", err)
	}
}
```

In `internal/convert/convert_test.go`, update every `Files(...)` call to pass `rowgroup.Settings{}` as the 5th argument (before `logger, now`) — there are 4 call sites, in `TestFile_SpecExample_ProducesExpectedOutputs`, `TestFiles_MultipleInputsMergeIntoOneOutputPerRule`, `TestFiles_ContinuesPastAFailedInputAndStillMergesTheRest`, `TestFile_WriteErrorMidFile_StillClosesEarlierWriters`. Example:

```go
	if err := Files([]string{logPath}, outDir, cfg, compression.Settings{}, rowgroup.Settings{}, logger, now); err != nil {
```

Add `"logidx/internal/rowgroup"` to `convert_test.go`'s imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/rules/... ./internal/convert/... -v`
Expected: FAIL — build failures (`Config` has no field `RowGroup`; `Files` called with 6 args where 7 are expected).

- [ ] **Step 3: Update the implementation**

In `internal/rules/rules.go`, add the import `"logidx/internal/rowgroup"` and extend `Config`:

```go
// Config is the top-level rules.yaml document.
type Config struct {
	Rules []Rule `yaml:"rules"`
	// Compression optionally sets the output Parquet compression codec and
	// level; unset fields fall back to the CLI flags, then to the default
	// (see internal/compression).
	Compression compression.Settings `yaml:"compression"`
	// RowGroup optionally caps the number of rows per Parquet row group on
	// every output file; unset falls back to the CLI flag, then to
	// unlimited (see internal/rowgroup).
	RowGroup rowgroup.Settings `yaml:"row_group"`
}
```

In `internal/rules/validate.go`, add the row group check right after the compression check:

```go
	if err := c.Compression.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("compression: %w", err))
	}
	if err := c.RowGroup.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("row_group: %w", err))
	}
```

In `internal/convert/convert.go`, add `"logidx/internal/rowgroup"` to the imports (already added in Task 2 if not already present — verify it's there), then change `Files`'s signature and its call to `writer.NewSet`:

```go
func Files(inputPaths []string, outDir string, cfg *rules.Config, comp compression.Settings, rg rowgroup.Settings, logger *slog.Logger, now time.Time) (err error) {
	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		return fmt.Errorf("build schemas: %w", err)
	}

	set := writer.NewSet(outDir, built, comp, rg)
```

Update the doc comment above `Files` to mention `rg` alongside `comp`:

```go
// ... comp is the already-resolved (CLI > config file > default) Parquet
// compression setting, applied to every output file. rg is the
// already-resolved row group row-count limit, applied the same way (see
// internal/rowgroup); its zero value means unlimited.
```

Finally, in `cmd/logidx/main.go:110`, update the one call site so the package still builds (this becomes a real config value in Task 4):

```go
			if err := convert.Files(args, outDir, cfg, comp, rowgroup.Settings{}, logger, now); err != nil {
```

Add `"logidx/internal/rowgroup"` to `main.go`'s import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rules/... ./internal/convert/... -v`
Expected: PASS (all tests, including the new `TestValidate_InvalidRowGroupSettingIsError`)

Run: `go build ./...`
Expected: builds cleanly

- [ ] **Step 5: Commit**

```bash
git add internal/rules/rules.go internal/rules/validate.go internal/rules/validate_test.go internal/convert/convert.go internal/convert/convert_test.go cmd/logidx/main.go
git commit -m "Add rules.yaml row_group.max_rows config and thread it through convert.Files"
```

---

## Task 4: `--max-rows-per-row-group` CLI flag

**Files:**
- Modify: `cmd/logidx/main.go`
- Modify: `cmd/logidx/main_test.go`

**Interfaces:**
- Consumes: `rowgroup.Settings`, `Resolve`, `(Settings) Validate() error` (Task 1); `cfg.RowGroup` (Task 3); `convert.Files(..., rg rowgroup.Settings, ...)` (Task 3)
- Produces: `logidx import --max-rows-per-row-group <n>` flag

- [ ] **Step 1: Write the failing test**

In `cmd/logidx/main_test.go`, add this test after `TestRun_ImportReadsLogFromStdin`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/logidx/... -run TestRun_ImportAppliesMaxRowsPerRowGroup -v`
Expected: FAIL — `unknown flag: --max-rows-per-row-group`

- [ ] **Step 3: Write the implementation**

In `cmd/logidx/main.go`, add `"logidx/internal/rowgroup"` to the imports (if not already present from Task 3), then in `newImportCmd`, add the flag variable:

```go
	var (
		rulesPath          string
		outDir             string
		logFormat          string
		verbose            bool
		compressionCodec   string
		compressionLevel   int
		maxRowsPerRowGroup int64
	)
```

Replace the temporary `rowgroup.Settings{}` literal added in Task 3 with real resolution, right after the existing compression resolution block:

```go
			comp := compression.Resolve(cliCompression, cfg.Compression)
			if err := comp.Validate(); err != nil {
				logger.Error("invalid compression settings", "error", err)
				return &exitCodeError{2}
			}

			cliRowGroup := rowgroup.Settings{}
			if cmd.Flags().Changed("max-rows-per-row-group") {
				maxRows := maxRowsPerRowGroup
				cliRowGroup.MaxRows = &maxRows
			}
			rg := rowgroup.Resolve(cliRowGroup, cfg.RowGroup)
			if err := rg.Validate(); err != nil {
				logger.Error("invalid row group settings", "error", err)
				return &exitCodeError{2}
			}
```

Update the `convert.Files` call:

```go
			if err := convert.Files(args, outDir, cfg, comp, rg, logger, now); err != nil {
```

Register the flag alongside the existing ones:

```go
	cmd.Flags().StringVar(&compressionCodec, "compression", "", "parquet compression codec: uncompressed, snappy, gzip, brotli, zstd (default), lz4; overrides the rules file's compression.codec")
	cmd.Flags().IntVar(&compressionLevel, "compression-level", 0, "codec-specific compression level; overrides the rules file's compression.level (see docs)")
	cmd.Flags().Int64Var(&maxRowsPerRowGroup, "max-rows-per-row-group", 0, "parquet row group row-count limit; unset = unlimited (default); overrides the rules file's row_group.max_rows")
```

Also update the usage string in the `len(args) == 0` branch to mention the new flag:

```go
				_, _ = fmt.Fprintln(stderr, "usage: logidx import --rules <path> [--out <dir>] [--log-format text|json] [-v|--verbose] [--compression <codec>] [--compression-level <n>] [--max-rows-per-row-group <n>] <input-log-file|->...")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/logidx/... -v`
Expected: PASS (all tests, including the two new ones)

Run: `go build ./... && go test ./...`
Expected: everything builds and passes

- [ ] **Step 5: Commit**

```bash
git add cmd/logidx/main.go cmd/logidx/main_test.go
git commit -m "Add --max-rows-per-row-group CLI flag to logidx import"
```

---

## Task 5: `mergeKeyField` — auto-detect each rule's merge key

**Files:**
- Create: `internal/convert/merge.go`
- Create: `internal/convert/merge_test.go`

**Interfaces:**
- Consumes: `rules.Rule`, `rules.Field{Name, Type string}` (existing, `internal/rules/rules.go:23,58`)
- Produces: `mergeKeyField(ruleList []rules.Rule) map[string]string` — rule name → its first `Type == "timestamp"` field name, in declaration order; rules with no timestamp field are absent from the map

- [ ] **Step 1: Write the failing test**

Create `internal/convert/merge_test.go`:

```go
package convert

import (
	"testing"

	"logidx/internal/rules"
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/convert/... -run TestMergeKeyField -v`
Expected: FAIL — `mergeKeyField` undefined

- [ ] **Step 3: Write the implementation**

Create `internal/convert/merge.go`:

```go
package convert

import "logidx/internal/rules"

// mergeKeyField returns, for each distinct rule name in ruleList, the name
// of its first Type == "timestamp" field in declaration order — the field
// internal/convert.mergeFiles uses to globally order that rule's matched
// rows across every input file. Rules with no timestamp field are omitted
// from the result; their matched rows are written in plain file-arrival
// order instead (see fileCursor.advance).
func mergeKeyField(ruleList []rules.Rule) map[string]string {
	result := map[string]string{}
	for _, r := range ruleList {
		if _, exists := result[r.Name]; exists {
			continue
		}
		for _, field := range r.Fields {
			if field.Type == "timestamp" {
				result[r.Name] = field.Name
				break
			}
		}
	}
	return result
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/convert/... -run TestMergeKeyField -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/convert/merge.go internal/convert/merge_test.go
git commit -m "Add mergeKeyField to auto-detect each rule's timestamp merge key"
```

---

## Task 6: `fileCursor` — per-file scan with pending-candidate hold

**Files:**
- Modify: `internal/convert/merge.go`
- Modify: `internal/convert/merge_test.go`

**Interfaces:**
- Consumes: `mergeKeyField` (Task 5); `parse.Match(ruleList []rules.Rule, line string, now time.Time) (name string, values map[string]any, ok bool)` (existing, `internal/parse/match.go:15`); `writer.Set.WriteMatched(name string, values map[string]any) error` and `WriteUnmatched(source string, lineNum int, raw string) error` (existing, `internal/writer/writer.go`)
- Produces: `candidate{cursor *fileCursor, name string, values map[string]any, sortValue time.Time}`; `fileCursor{inputPath string, fileIndex int, counts map[string]int, unmatched int, ...}`; `newFileCursor(inputPath string, fileIndex int, cfg *rules.Config, mergeKey map[string]string, set *writer.Set, logger *slog.Logger, now time.Time) (*fileCursor, error)`; `(*fileCursor) advance() (cand *candidate, ok bool, err error)`; `(*fileCursor) close() error`

- [ ] **Step 1: Write the failing test**

Add to `internal/convert/merge_test.go` (add imports `"bytes"`, `"time"`, `"logidx/internal/compression"`, `"logidx/internal/logging"`, `"logidx/internal/rowgroup"`, `"logidx/internal/schema"`, `"logidx/internal/writer"`):

```go
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
```

Add `"path/filepath"` and `"logidx/internal/rules"` to `merge_test.go`'s imports too (`rules` may already be imported from Task 5's test; `filepath` is new).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/convert/... -run TestFileCursor -v`
Expected: FAIL — `newFileCursor` undefined

- [ ] **Step 3: Write the implementation**

Append to `internal/convert/merge.go` (replace the whole file's import block and add the new types/functions):

```go
package convert

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"logidx/internal/parse"
	"logidx/internal/rules"
	"logidx/internal/writer"
)

// mergeKeyField returns, for each distinct rule name in ruleList, the name
// of its first Type == "timestamp" field in declaration order — the field
// internal/convert.mergeFiles uses to globally order that rule's matched
// rows across every input file. Rules with no timestamp field are omitted
// from the result; their matched rows are written in plain file-arrival
// order instead (see fileCursor.advance).
func mergeKeyField(ruleList []rules.Rule) map[string]string {
	result := map[string]string{}
	for _, r := range ruleList {
		if _, exists := result[r.Name]; exists {
			continue
		}
		for _, field := range r.Fields {
			if field.Type == "timestamp" {
				result[r.Name] = field.Name
				break
			}
		}
	}
	return result
}

// candidate is one matched row held back from immediate writing because
// its rule has a merge key (see mergeKeyField): mergeFiles compares
// candidates from every open fileCursor and writes the earliest one first.
type candidate struct {
	cursor    *fileCursor
	name      string
	values    map[string]any
	sortValue time.Time
}

// fileCursor scans one input file's lines in order. Lines that don't match
// any rule, or that match a rule with no merge key, are written
// immediately as advance() passes over them — exactly like the old
// sequential processInput did. Lines that match a rule with a merge key
// are held as the cursor's returned candidate instead, so mergeFiles can
// compare candidates across every input file before any of them is
// actually written.
type fileCursor struct {
	inputPath string
	fileIndex int
	file      *os.File // nil when reading os.Stdin
	scanner   *bufio.Scanner
	lineNum   int

	cfg      *rules.Config
	mergeKey map[string]string
	set      *writer.Set
	logger   *slog.Logger
	now      time.Time

	counts    map[string]int
	unmatched int
}

// newFileCursor opens inputPath (or os.Stdin if inputPath is "-") and
// returns a cursor ready for advance(). fileIndex is inputPath's position
// among the inputPaths mergeFiles was given, used only to break ties when
// two candidates from different files have the exact same sortValue.
func newFileCursor(inputPath string, fileIndex int, cfg *rules.Config, mergeKey map[string]string, set *writer.Set, logger *slog.Logger, now time.Time) (*fileCursor, error) {
	var f *os.File
	in := io.Reader(os.Stdin)
	if inputPath != "-" {
		var err error
		f, err = os.Open(inputPath)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		in = f
	}

	return &fileCursor{
		inputPath: inputPath,
		fileIndex: fileIndex,
		file:      f,
		scanner:   bufio.NewScanner(in),
		cfg:       cfg,
		mergeKey:  mergeKey,
		set:       set,
		logger:    logger,
		now:       now,
		counts:    map[string]int{},
	}, nil
}

// advance reads forward from where it last stopped until it finds a row
// eligible for merging, writing every ineligible row it passes along the
// way (unmatched lines to the shared sidecar, matched-but-no-merge-key rows
// straight to their rule's writer). ok is false once the file is
// exhausted, at which point every one of its rows has been written or
// returned as a candidate — there is nothing left to do with this cursor
// but close() it.
func (c *fileCursor) advance() (cand *candidate, ok bool, err error) {
	for c.scanner.Scan() {
		c.lineNum++
		line := c.scanner.Text()

		name, values, matched := parse.Match(c.cfg.Rules, line, c.now)
		if !matched {
			c.logger.Debug("line did not match any rule", "file", c.inputPath, "line", c.lineNum)
			if err := c.set.WriteUnmatched(c.inputPath, c.lineNum, line); err != nil {
				return nil, false, fmt.Errorf("write unmatched line %d: %w", c.lineNum, err)
			}
			c.unmatched++
			continue
		}

		keyField, hasMergeKey := c.mergeKey[name]
		if !hasMergeKey {
			if err := c.set.WriteMatched(name, values); err != nil {
				return nil, false, fmt.Errorf("write matched row (rule %q, line %d): %w", name, c.lineNum, err)
			}
			c.counts[name]++
			continue
		}

		sortValue, isTime := values[keyField].(time.Time)
		if !isTime {
			return nil, false, fmt.Errorf("rule %q field %q: merge key value is not a timestamp (line %d)", name, keyField, c.lineNum)
		}
		c.counts[name]++
		return &candidate{cursor: c, name: name, values: values, sortValue: sortValue}, true, nil
	}

	if err := c.scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("read input: %w", err)
	}
	return nil, false, nil
}

// close closes the underlying file, if any (nothing to close for os.Stdin).
func (c *fileCursor) close() error {
	if c.file == nil {
		return nil
	}
	return c.file.Close()
}
```

Note: `values[keyField].(time.Time)` can only fail if `internal/rules.Field.Type == "timestamp"` but the value in the map isn't a `time.Time` — `internal/parse.convertValue` guarantees timestamp fields convert to `time.Time`, so this branch is unreachable in practice but kept as a defensive error rather than a panic, consistent with the rest of this codebase's fail-fast-with-error style.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/convert/... -v`
Expected: PASS (all tests, including Task 5's)

Run: `go build ./...`
Expected: builds cleanly

- [ ] **Step 5: Commit**

```bash
git add internal/convert/merge.go internal/convert/merge_test.go
git commit -m "Add fileCursor to stream a file's rows, holding merge-key rows as candidates"
```

---

## Task 7: `candidateHeap` — min-heap ordering by timestamp

**Files:**
- Modify: `internal/convert/merge.go`
- Create: `internal/convert/heap_test.go`

**Interfaces:**
- Consumes: `candidate{cursor *fileCursor, sortValue time.Time}`, `fileCursor{fileIndex int}` (Task 6)
- Produces: `candidateHeap []*candidate` implementing `container/heap.Interface`

- [ ] **Step 1: Write the failing test**

Create `internal/convert/heap_test.go`:

```go
package convert

import (
	"container/heap"
	"slices"
	"testing"
	"time"
)

func TestCandidateHeap_PopsInAscendingTimestampOrder(t *testing.T) {
	t1 := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	t3 := t1.Add(2 * time.Minute)

	h := candidateHeap{}
	heap.Init(&h)
	heap.Push(&h, &candidate{name: "c", sortValue: t3, cursor: &fileCursor{fileIndex: 0}})
	heap.Push(&h, &candidate{name: "a", sortValue: t1, cursor: &fileCursor{fileIndex: 0}})
	heap.Push(&h, &candidate{name: "b", sortValue: t2, cursor: &fileCursor{fileIndex: 0}})

	var order []string
	for h.Len() > 0 {
		order = append(order, heap.Pop(&h).(*candidate).name)
	}

	want := []string{"a", "b", "c"}
	if !slices.Equal(order, want) {
		t.Errorf("pop order = %v, want %v", order, want)
	}
}

func TestCandidateHeap_TiesBreakByFileIndex(t *testing.T) {
	tie := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	h := candidateHeap{}
	heap.Init(&h)
	heap.Push(&h, &candidate{name: "from-file-1", sortValue: tie, cursor: &fileCursor{fileIndex: 1}})
	heap.Push(&h, &candidate{name: "from-file-0", sortValue: tie, cursor: &fileCursor{fileIndex: 0}})

	first := heap.Pop(&h).(*candidate)
	if first.name != "from-file-0" {
		t.Errorf("first popped = %q, want from-file-0 (lower fileIndex breaks the tie)", first.name)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/convert/... -run TestCandidateHeap -v`
Expected: FAIL — `candidateHeap` undefined

- [ ] **Step 3: Write the implementation**

Append to `internal/convert/merge.go`, and add `"container/heap"` to its import block:

```go
// candidateHeap is a min-heap of candidates ordered by sortValue, with the
// originating file's position among mergeFiles' inputPaths as a tiebreak,
// so two candidates with the exact same timestamp still pop in a fixed,
// repeatable order across runs.
type candidateHeap []*candidate

func (h candidateHeap) Len() int { return len(h) }

func (h candidateHeap) Less(i, j int) bool {
	if !h[i].sortValue.Equal(h[j].sortValue) {
		return h[i].sortValue.Before(h[j].sortValue)
	}
	return h[i].cursor.fileIndex < h[j].cursor.fileIndex
}

func (h candidateHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *candidateHeap) Push(x any) {
	*h = append(*h, x.(*candidate))
}

func (h *candidateHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/convert/... -v`
Expected: PASS (all tests so far)

- [ ] **Step 5: Commit**

```bash
git add internal/convert/merge.go internal/convert/heap_test.go
git commit -m "Add candidateHeap: a min-heap over fileCursors' pending candidates"
```

---

## Task 8: `mergeFiles` — orchestrate the k-way merge, and wire it into `Files`

**Files:**
- Modify: `internal/convert/merge.go`
- Modify: `internal/convert/convert.go`
- Modify: `internal/convert/convert_test.go`

**Interfaces:**
- Consumes: `mergeKeyField`, `newFileCursor`, `(*fileCursor) advance/close` (Task 6), `candidateHeap` (Task 7)
- Produces: `mergeFiles(inputPaths []string, cfg *rules.Config, set *writer.Set, logger *slog.Logger, now time.Time) error`; `Files` (existing signature, `internal/convert/convert.go`) now delegates to `mergeFiles` instead of looping over `processInput`

- [ ] **Step 1: Write the failing tests**

Add to `internal/convert/convert_test.go` (add `"slices"` if not already imported — it already is):

```go
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
		for i := 0; i < n; i++ {
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/convert/... -run 'TestFiles_Merges|TestFiles_NoInputFiles' -v`
Expected: only `TestFiles_MergesMultipleFilesByTimestampAcrossRuleTypes` FAILs — `Files` still processes files sequentially (file A fully, then file B fully), so with file A's and file B's timestamps interleaved, `ts_event.parquet`'s rows come out as `[from-a-1, from-a-2, from-b-1, from-b-2]`, not the globally-sorted order that test expects. The other two new tests (`TestFiles_MergesNonOverlappingFileTimeRangesInGlobalOrder`, `TestFiles_NoInputFilesProducesNoOutputFiles`) already PASS even against the old sequential implementation — their timestamp ranges don't overlap across files (so sequential order already matches global order) or there are no files to process at all. They're included here as regression coverage for the new code path, not as red/green markers; keep them passing after Step 3 rather than expecting them to flip.

- [ ] **Step 3: Write the implementation**

Append to `internal/convert/merge.go`:

```go
// mergeFiles processes every input, merging rows from rules with a merge
// key (see mergeKeyField) into ascending-timestamp order across all inputs
// combined, while rows from rules without one are written in each file's
// own arrival order — matching Files' pre-merge behavior exactly when no
// rule has a merge key at all, or when there's only one input file (the
// heap never holds more than one candidate at a time in either case).
//
// Processing continues past a failed input: its cursor is dropped from the
// merge and its error is joined into the returned error, so one bad input
// doesn't stop the others from being merged and written.
func mergeFiles(inputPaths []string, cfg *rules.Config, set *writer.Set, logger *slog.Logger, now time.Time) error {
	mergeKey := mergeKeyField(cfg.Rules)

	var errs []error
	h := candidateHeap{}

	for i, inputPath := range inputPaths {
		cursor, err := newFileCursor(inputPath, i, cfg, mergeKey, set, logger, now)
		if err != nil {
			logger.Error("failed to process file", "file", inputPath, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", inputPath, err))
			continue
		}
		advanceOrRecord(cursor, &h, logger, &errs)
	}

	for h.Len() > 0 {
		cand := heap.Pop(&h).(*candidate)
		if err := set.WriteMatched(cand.name, cand.values); err != nil {
			err = fmt.Errorf("write matched row (rule %q): %w", cand.name, err)
			logger.Error("failed to process file", "file", cand.cursor.inputPath, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", cand.cursor.inputPath, err))
			_ = cand.cursor.close()
			continue
		}

		advanceOrRecord(cand.cursor, &h, logger, &errs)
	}

	return errors.Join(errs...)
}

// advanceOrRecord calls cursor.advance(), pushing a new candidate onto h on
// success. It returns false once the cursor has nothing left to contribute
// (EOF or error) — in both cases the cursor has already been closed and,
// for EOF, its "file processed" summary already logged.
func advanceOrRecord(cursor *fileCursor, h *candidateHeap, logger *slog.Logger, errs *[]error) bool {
	cand, ok, err := cursor.advance()
	if err != nil {
		logger.Error("failed to process file", "file", cursor.inputPath, "error", err)
		*errs = append(*errs, fmt.Errorf("%s: %w", cursor.inputPath, err))
		_ = cursor.close()
		return false
	}
	if !ok {
		logFileProcessed(logger, cursor)
		if err := cursor.close(); err != nil {
			*errs = append(*errs, fmt.Errorf("%s: close: %w", cursor.inputPath, err))
		}
		return false
	}
	heap.Push(h, cand)
	return true
}

// logFileProcessed logs the same "file processed" summary the old
// sequential processInput logged once it finished a file: its own
// per-rule-name match counts (not the merged Set's running totals) and how
// many of its lines matched no rule.
func logFileProcessed(logger *slog.Logger, c *fileCursor) {
	args := []any{"file", c.inputPath}
	for name, count := range c.counts {
		args = append(args, name, count)
	}
	args = append(args, "unmatched", c.unmatched)
	logger.Info("file processed", args...)
}
```

Add `"errors"` to `merge.go`'s import block alongside `"container/heap"`.

Now replace `internal/convert/convert.go`'s body: delete `processInput` entirely, and replace `Files`'s loop:

```go
	return mergeFiles(inputPaths, cfg, set, logger, now)
```

in place of:

```go
	var errs []error
	for _, inputPath := range inputPaths {
		if procErr := processInput(inputPath, cfg, set, logger, now); procErr != nil {
			logger.Error("failed to process file", "file", inputPath, "error", procErr)
			errs = append(errs, fmt.Errorf("%s: %w", inputPath, procErr))
		}
	}

	return errors.Join(errs...)
```

After deleting `processInput`, `convert.go` no longer uses `"bufio"`, `"io"`, `"os"`, `"logidx/internal/parse"`, or (check carefully) `"errors"` — `errors.Join` is still used in the `defer` block for `closeErr`, so keep `"errors"`. Remove `"bufio"`, `"io"`, `"os"`, and `"logidx/internal/parse"` from `convert.go`'s imports if nothing else in the file uses them (they don't, once `processInput` is gone).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/convert/... -v`
Expected: PASS — every test in the package, including all pre-existing tests from before this plan (`TestFile_SpecExample_ProducesExpectedOutputs`, `TestFiles_MultipleInputsMergeIntoOneOutputPerRule`, `TestFiles_ContinuesPastAFailedInputAndStillMergesTheRest`, `TestFile_WriteErrorMidFile_StillClosesEarlierWriters`) — this is the regression check: single-file and no-merge-key behavior must be byte-for-byte the same as before.

Run: `go build ./... && go test ./...`
Expected: everything builds and passes across the whole repo

- [ ] **Step 5: Commit**

```bash
git add internal/convert/merge.go internal/convert/convert.go internal/convert/convert_test.go
git commit -m "Merge multiple input files by timestamp across rules with a merge key"
```

---

## Task 9: End-to-end CLI test combining both features

**Files:**
- Modify: `cmd/logidx/main_test.go`

**Interfaces:**
- Consumes: `logidx import --max-rows-per-row-group <n> <files...>` (Task 4), the merge behavior wired into `convert.Files` (Task 8), `logidx dump <src> -` (existing, unmodified)

- [ ] **Step 1: Write the failing test**

Add `"encoding/json"` and `"slices"` to `cmd/logidx/main_test.go`'s imports, then add:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/logidx/... -run TestRun_ImportMergesMultipleFilesByTimestampAndAppliesRowGroupLimit -v`
Expected: This should actually already PASS if Tasks 1-8 are all correctly implemented — there's no new production code in this task, only a test that exercises the full, already-wired pipeline. If it fails, that means an earlier task's implementation has a bug; treat a failure here as a signal to go back and fix the relevant task rather than adding new code in this task.

- [ ] **Step 3: N/A**

No implementation step — this task is pure verification that Tasks 1-8 compose correctly end to end.

- [ ] **Step 4: Run the full test suite**

Run: `go build ./... && go test ./...`
Expected: everything builds and passes

- [ ] **Step 5: Commit**

```bash
git add cmd/logidx/main_test.go
git commit -m "Add end-to-end test combining multi-file timestamp merge and row group limit"
```

---

## Task 10: README documentation and final repo-wide gate

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add a "row group分割設定" section**

In `README.md`, right after the existing `### 圧縮設定` section (which ends around line 102 with the `--compression`/`--compression-level` paragraph), add:

```markdown
### row group分割設定

Parquetのrow group行数上限は以下の優先順位で決まる: **CLI引数(`--max-rows-per-row-group`) > rules.yamlの`row_group.max_rows` > デフォルト(無制限)**。

rules.yamlで指定する場合:

```yaml
row_group:
  max_rows: 500000

rules:
  - name: app_log
    ...
```

行数は圧縮後のバイトサイズを直接指定する代わりの代理指標(parquet-go自体がバイトサイズを直接制御する手段を提供していないため)。目的のファイルサイズに近づけたい場合は、対象ルールの1行あたりの平均サイズから逆算して行数を決める。
```

- [ ] **Step 2: Add a "複数ファイルのマージ順" section**

Right after the new row group section, add:

```markdown
### 複数ファイルのマージ順

`logidx import`に複数の入力ファイルを渡した場合、ルールに`type: timestamp`のフィールドが1つでもあれば、そのルールの宣言順で最初のtimestampフィールドの値を使って、全入力ファイルをまたいでタイムスタンプ昇順にマージしてから書き込む(設定不要、自動検出)。timestampフィールドを持たないルールの行は、従来通り各ファイルの出現順のまま書き込まれる。

各入力ファイル自体が既に時系列順であることを前提にしたストリーミングマージなので、ファイル内が時系列順に並んでいないログを渡した場合の出力順は保証されない。
```

- [ ] **Step 3: Verify the README renders sensibly**

Run: `sed -n '1,160p' README.md` and visually confirm the new sections sit correctly between `### 圧縮設定` and `### info: Parquetファイルの中身を見る`, with consistent heading levels and no broken code fences.

- [ ] **Step 4: Whole-repo gate**

Run: `task fmt`
Expected: no changes (or only whitespace normalization — review any diff before proceeding)

Run: `task lint`
Expected: no findings

Run: `task test`
Expected: all packages pass

Run: `task build`
Expected: builds `bin/logidx` successfully

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "Document row_group.max_rows config and automatic timestamp-based file merging"
```
