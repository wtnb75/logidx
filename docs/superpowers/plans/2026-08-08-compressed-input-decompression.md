# importの圧縮済み入力ファイル対応 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `logidx import` accept gzip/xz/bzip2/zstd-compressed input log files directly, auto-detected by file extension, with no external decompression command required.

**Architecture:** A new `internal/decompress` package exposes one function, `Wrap(path string, r io.Reader) (io.Reader, io.Closer, error)`, that inspects `path`'s extension and wraps `r` in the matching decompressing reader (or returns it unchanged for an unrecognized extension). `internal/convert/merge.go`'s `newFileCursor` calls it right after `os.Open`, and `fileCursor` gains a `decompressCloser` field so `close()` releases the decompressor alongside the file.

**Tech Stack:** Go 1.25, `compress/gzip` and `compress/bzip2` (standard library), `github.com/ulikunitz/xz` (new dependency, pure Go), `github.com/klauspost/compress/zstd` (already an indirect dependency via parquet-go, promoted to direct).

## Global Constraints

- Supported formats and their extensions: `.gz` → gzip, `.xz` → xz, `.bz2` → bzip2, `.zst` → zstd. Extension comparison is case-insensitive (`.GZ` counts as `.gz`).
- An unrecognized extension (including no extension) is not an error: the reader is returned unchanged (plain-text behavior, unaffected).
- Standard input (`-`) is always treated as uncompressed — `decompress.Wrap` is never called for it.
- No CLI flag for explicitly naming the compression format — detection is extension-only.
- `dump`/`restore`/`copy`/`info` are out of scope — they read Parquet files directly and are unaffected.
- gzip, xz, and zstd validate their header immediately when wrapped (`NewReader` returns an error right away for corrupt/mismatched data); bzip2 does not — its `NewReader` never errors, and corruption only surfaces once `fileCursor` actually scans a line (via the existing `scanner.Err()` path).

## File Structure

- `internal/decompress/decompress.go` (new) — the `Wrap` function and its per-format branches.
- `internal/decompress/decompress_test.go` (new) — unit tests for all 4 formats plus the unrecognized/no-extension and corrupt-data cases.
- `internal/convert/merge.go` (modify) — `fileCursor` gains `decompressCloser io.Closer`; `newFileCursor` calls `decompress.Wrap`; `close()` closes both.
- `internal/convert/merge_test.go` (modify) — new tests for gzip-compressed input end-to-end through `fileCursor`, the corrupt-input-closes-the-file-and-errors path, and `close()` releasing the decompressor.
- `cmd/logidx/main_test.go` (modify) — one end-to-end CLI test importing a `.gz` file.
- `go.mod` / `go.sum` (modify) — add `github.com/ulikunitz/xz`, promote `github.com/klauspost/compress` from indirect to direct.
- `README.md` (modify) — new `### 圧縮済み入力ファイルの自動解凍` section.

---

### Task 1: `internal/decompress` package

**Files:**
- Create: `internal/decompress/decompress.go`
- Test: `internal/decompress/decompress_test.go`

**Interfaces:**
- Produces: `decompress.Wrap(path string, r io.Reader) (io.Reader, io.Closer, error)` — the returned `io.Closer` is non-nil only for gzip and zstd (nil for xz, bzip2, and unrecognized extensions).

- [ ] **Step 1: Add the new dependency and promote the indirect one**

Run from the repo root:

```bash
go get github.com/ulikunitz/xz@v0.5.15
go mod tidy
```

Expected: `go.mod` gains a direct `require github.com/ulikunitz/xz v0.5.15` line, and `github.com/klauspost/compress`'s `// indirect` comment is removed (it becomes a direct dependency once `internal/decompress` imports it in Step 3). `go.sum` gains matching entries. If `go mod tidy` reports nothing to change for `klauspost/compress` yet, that's expected — it only becomes direct once Step 3's code actually imports it; re-run `go mod tidy` after Step 3 if needed.

- [ ] **Step 2: Write the failing tests**

Create `internal/decompress/decompress_test.go`:

```go
package decompress

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

func TestWrap_GzipRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte("hello, gzip\n")); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	r, closer, err := Wrap("input.gz", &buf)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer == nil {
		t.Fatal("expected a non-nil Closer for gzip")
	}
	defer func() { _ = closer.Close() }()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello, gzip\n" {
		t.Errorf("got %q, want %q", got, "hello, gzip\n")
	}
}

func TestWrap_GzipUppercaseExtensionIsRecognized(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	r, _, err := Wrap("input.GZ", &buf)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("got %q, want %q", got, "hello\n")
	}
}

func TestWrap_XzRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("xz.NewWriter: %v", err)
	}
	if _, err := xw.Write([]byte("hello, xz\n")); err != nil {
		t.Fatalf("write xz: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("close xz writer: %v", err)
	}

	r, closer, err := Wrap("input.xz", &buf)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer != nil {
		t.Error("expected a nil Closer for xz")
	}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello, xz\n" {
		t.Errorf("got %q, want %q", got, "hello, xz\n")
	}
}

func TestWrap_Bzip2RoundTrip(t *testing.T) {
	// Precomputed with: printf 'hello, bzip2\n' | bzip2 -9
	data := []byte{
		0x42, 0x5a, 0x68, 0x39, 0x31, 0x41, 0x59, 0x26, 0x53, 0x59, 0xb1, 0x23,
		0xde, 0x43, 0x00, 0x00, 0x03, 0x59, 0x80, 0x00, 0x10, 0x40, 0x04, 0x10,
		0x00, 0x12, 0x64, 0xc0, 0x10, 0x20, 0x00, 0x31, 0x03, 0x40, 0xd0, 0x20,
		0x01, 0xa6, 0x91, 0x03, 0xab, 0x6c, 0x82, 0x84, 0xf8, 0xbb, 0x92, 0x29,
		0xc2, 0x84, 0x85, 0x89, 0x1e, 0xf2, 0x18,
	}

	r, closer, err := Wrap("input.bz2", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer != nil {
		t.Error("expected a nil Closer for bzip2")
	}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello, bzip2\n" {
		t.Errorf("got %q, want %q", got, "hello, bzip2\n")
	}
}

func TestWrap_ZstdRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, err := zw.Write([]byte("hello, zstd\n")); err != nil {
		t.Fatalf("write zstd: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}

	r, closer, err := Wrap("input.zst", &buf)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer == nil {
		t.Fatal("expected a non-nil Closer for zstd")
	}
	defer func() { _ = closer.Close() }()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello, zstd\n" {
		t.Errorf("got %q, want %q", got, "hello, zstd\n")
	}
}

func TestWrap_UnrecognizedExtensionReturnsReaderUnchanged(t *testing.T) {
	src := bytes.NewReader([]byte("plain text\n"))

	r, closer, err := Wrap("input.log", src)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer != nil {
		t.Error("expected a nil Closer for an unrecognized extension")
	}
	if r != src {
		t.Error("expected the original reader to be returned unchanged")
	}
}

func TestWrap_NoExtensionReturnsReaderUnchanged(t *testing.T) {
	src := bytes.NewReader([]byte("plain text\n"))

	r, closer, err := Wrap("input", src)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer != nil {
		t.Error("expected a nil Closer with no extension")
	}
	if r != src {
		t.Error("expected the original reader to be returned unchanged")
	}
}

func TestWrap_CorruptGzipReturnsError(t *testing.T) {
	_, _, err := Wrap("input.gz", bytes.NewReader([]byte("not actually gzip data")))
	if err == nil {
		t.Fatal("expected an error for corrupt gzip data")
	}
}

func TestWrap_CorruptXzReturnsError(t *testing.T) {
	_, _, err := Wrap("input.xz", bytes.NewReader([]byte("not actually xz data")))
	if err == nil {
		t.Fatal("expected an error for corrupt xz data")
	}
}

func TestWrap_CorruptZstdReturnsError(t *testing.T) {
	_, _, err := Wrap("input.zst", bytes.NewReader([]byte("not actually zstd data")))
	if err == nil {
		t.Fatal("expected an error for corrupt zstd data")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/decompress/... -v`
Expected: FAIL with "no Go files in ..." or "undefined: Wrap" (the package/function don't exist yet).

- [ ] **Step 4: Implement `Wrap`**

Create `internal/decompress/decompress.go`:

```go
// Package decompress wraps an input reader in a decompressing reader based
// on the compressed file's extension, so logidx import can read
// gzip/xz/bzip2/zstd-compressed log files directly.
package decompress

import (
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// closerFunc adapts a plain func() error to io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// Wrap inspects path's extension and, if it names a supported compression
// format, wraps r in the matching decompressing reader. If the extension is
// unrecognized (including no extension), r is returned unchanged along with
// a nil Closer. The returned io.Closer releases any resources the
// decompressor itself holds beyond r (currently only gzip and zstd need
// this); callers must call it (if non-nil) in addition to closing the
// original reader/file.
//
// gzip, xz, and zstd validate their header immediately - a mismatched or
// corrupt input makes this call return an error right away. bzip2's
// decoder does not validate anything until it is first read from; a
// corrupt bzip2 input surfaces as a read error later instead.
func Wrap(path string, r io.Reader) (io.Reader, io.Closer, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gz":
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("open gzip reader: %w", err)
		}
		return gr, gr, nil
	case ".xz":
		xr, err := xz.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("open xz reader: %w", err)
		}
		return xr, nil, nil
	case ".bz2":
		return bzip2.NewReader(r), nil, nil
	case ".zst":
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("open zstd reader: %w", err)
		}
		return zr, closerFunc(func() error { zr.Close(); return nil }), nil
	default:
		return r, nil, nil
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/decompress/... -v`
Expected: PASS (all 10 tests).

- [ ] **Step 6: gofmt, vet, and tidy check**

Run: `gofmt -l . && go vet ./... && go mod tidy && git diff --stat go.mod go.sum`
Expected: `gofmt`/`go vet` clean; `go mod tidy` makes no further changes (or only removes the `// indirect` comment on `klauspost/compress` if Step 1 hadn't already picked it up — either way, `go.mod`/`go.sum` end up consistent with the new imports).

- [ ] **Step 7: Commit**

```bash
git add internal/decompress/decompress.go internal/decompress/decompress_test.go go.mod go.sum
git commit -m "Add internal/decompress: extension-based gzip/xz/bzip2/zstd decompression"
```

---

### Task 2: Wire `decompress.Wrap` into `fileCursor`

**Files:**
- Modify: `internal/convert/merge.go`
- Test: `internal/convert/merge_test.go`
- Test: `internal/convert/convert_test.go`

**Interfaces:**
- Consumes: `decompress.Wrap(path string, r io.Reader) (io.Reader, io.Closer, error)` (Task 1).
- Produces: no new exported symbols. `fileCursor` gains an unexported `decompressCloser io.Closer` field; `newFileCursor`'s and `(*fileCursor) close()`'s signatures are unchanged.

- [ ] **Step 1: Write the failing tests**

Add `"compress/gzip"` and `"strings"` to the import block of `internal/convert/merge_test.go`, then add these test functions:

```go
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
```

The pre-existing `TestFileCursor_Advance_SplitsEligibleFromIneligibleRows` and `TestFileCursor_Advance_ReturnsErrorOnMissingFile` in the same file must be left untouched — they cover plain-text (uncompressed) input and are this task's regression check.

Also add this test to `internal/convert/convert_test.go` (no new imports needed — `os`, `bytes`, `filepath`, `testing`, `time`, `rules`, `schema`, `compression`, `rowgroup`, `logging` are all already imported there; it reuses the existing `twoRuleRulesYAML` constant and `countParquetRows` helper defined in that file):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/convert/... -run 'TestNewFileCursor|TestFileCursor_Close|TestFiles_ContinuesPastACorruptGzipInput' -v`
Expected: the 4 new tests FAIL — `TestNewFileCursor_DecompressesGzipInput` fails because the raw gzip bytes get scanned as garbled text lines instead of being decompressed; `TestNewFileCursor_CorruptGzipReturnsWrappedOpenErrorAndClosesFile` and `TestFiles_ContinuesPastACorruptGzipInputAndStillMergesTheRest` fail because no error is returned (there's no decompression attempt yet, so a `.gz` file full of garbage text just reads as plain text lines and fails to match any rule instead of failing to open); `TestFileCursor_Close_ClosesDecompressorAndFile` fails to compile (`cursor.decompressCloser` doesn't exist yet).

- [ ] **Step 3: Add the `decompressCloser` field and wire in `decompress.Wrap`**

In `internal/convert/merge.go`, add the import:

```go
	"logidx/internal/decompress"
```

(alongside the existing `"logidx/internal/parse"`, `"logidx/internal/rules"`, `"logidx/internal/writer"` imports.)

Change the `fileCursor` struct (currently ending with `pending *scannedLine`) to add one field:

```go
type fileCursor struct {
	inputPath string
	fileIndex int
	file      *os.File // nil when reading os.Stdin
	// decompressCloser releases the decompressor wrapping file's contents,
	// if inputPath's extension named a supported compression format (see
	// decompress.Wrap). nil for uncompressed input, stdin, or a format
	// (like bzip2) whose decoder holds nothing beyond the underlying
	// reader.
	decompressCloser io.Closer
	scanner           *bufio.Scanner
	lineNum           int

	cfg      *rules.Config
	mergeKey map[string]string
	set      *writer.Set
	logger   *slog.Logger
	now      time.Time

	counts    map[string]int
	unmatched int

	open    *openEntry
	pending *scannedLine
}
```

Change `newFileCursor` (currently opens `f`, sets `in = f`, and constructs the cursor) to:

```go
func newFileCursor(inputPath string, fileIndex int, cfg *rules.Config, mergeKey map[string]string, set *writer.Set, logger *slog.Logger, now time.Time) (*fileCursor, error) {
	var f *os.File
	in := io.Reader(os.Stdin)
	var decompressCloser io.Closer
	if inputPath != "-" {
		var err error
		f, err = os.Open(inputPath)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		in, decompressCloser, err = decompress.Wrap(inputPath, f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("open input: %w", err)
		}
	}

	return &fileCursor{
		inputPath:         inputPath,
		fileIndex:         fileIndex,
		file:               f,
		decompressCloser:   decompressCloser,
		scanner:            bufio.NewScanner(in),
		cfg:                cfg,
		mergeKey:           mergeKey,
		set:                set,
		logger:             logger,
		now:                now,
		counts:             map[string]int{},
	}, nil
}
```

Change `close()` (currently `if c.file == nil { return nil }; return c.file.Close()`) to:

```go
// close closes the underlying decompressor (if any) and file (if any -
// nothing to close for os.Stdin). Both are closed even if one errors, so a
// decompressor-close failure never leaks the underlying file descriptor.
func (c *fileCursor) close() error {
	var errs []error
	if c.decompressCloser != nil {
		if err := c.decompressCloser.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close decompressor: %w", err))
		}
	}
	if c.file != nil {
		if err := c.file.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close file: %w", err))
		}
	}
	return errors.Join(errs...)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/convert/... -v`
Expected: PASS (every test in the package, including the 4 new tests and both pre-existing `TestFileCursor_*` tests — proving uncompressed input is unaffected).

- [ ] **Step 5: Run the full test suite and vet/lint**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all pass, no vet warnings, no gofmt diffs.

- [ ] **Step 6: Commit**

```bash
git add internal/convert/merge.go internal/convert/merge_test.go internal/convert/convert_test.go
git commit -m "Decompress gzip/xz/bzip2/zstd input files in fileCursor"
```

---

### Task 3: End-to-end CLI test with a gzip-compressed input file

**Files:**
- Modify: `cmd/logidx/main_test.go`

**Interfaces:**
- Consumes: `run()` (existing CLI entrypoint used by every other test in the file).
- Produces: nothing new — this is a black-box test of the whole `import`→`dump` pipeline.

- [ ] **Step 1: Write the failing test**

Add `"compress/gzip"` to the import block of `cmd/logidx/main_test.go`, then add:

```go
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
```

This reuses the existing `cliRulesYAML` constant already defined near the top of `main_test.go` (rule `app_log`, pattern `^\[(?P<level>\w+)\] (?P<message>.*)$`).

- [ ] **Step 2: Run the test**

Run: `go test ./cmd/logidx/... -run TestRun_ImportDecompressesGzipInput -v`
Expected: PASS (Tasks 1-2 already implement the underlying behavior; this test proves it end-to-end through the real CLI).

- [ ] **Step 3: Run the full test suite**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/logidx/main_test.go
git commit -m "Add end-to-end test for importing a gzip-compressed input file"
```

---

### Task 4: Document compressed input support in README

**Files:**
- Modify: `README.md`

**Interfaces:**
- None (documentation only).

- [ ] **Step 1: Add the section**

In `README.md`, insert a new subsection right after the existing `### 複数行ログエントリのマージ(\`continuation\`)` section's last bullet (`- \`continuation\`を設定しないルールの挙動は従来通り(1行=1エントリ)。`) and before the `### info: Parquetファイルの中身を見る` heading:

```markdown
### 圧縮済み入力ファイルの自動解凍

`logidx import`の入力ファイルは、拡張子から自動判定して透過的に解凍される。外部コマンド(`gzip`等)は不要で、Goライブラリのみで完結する。

| 拡張子 | フォーマット |
|---|---|
| `.gz` | gzip |
| `.xz` | xz |
| `.bz2` | bzip2 |
| `.zst` | zstd |

- 拡張子の大文字小文字は区別しない(`.GZ`も`.gz`として扱われる)。
- 上記以外の拡張子(拡張子なしも含む)は無圧縮として扱われる — フォーマットを明示指定するCLIフラグはない。
- 標準入力(`-`)は常に無圧縮として扱われる。圧縮データを標準入力から渡したい場合は、呼び出し側で先に解凍してパイプすること(例: `gzip -dc access.log.gz | logidx import --rules rules.yaml -`)。
- gzip/xz/zstdは、ファイルを開いた時点でヘッダが検証される。フォーマットに合わない・壊れたデータの場合はその場でエラーになり、そのファイルだけがマージ対象から除外される(他の入力ファイルの処理は継続する)。bzip2はストリーミング検証のため、壊れている場合は読み込み中にエラーになる。
```

- [ ] **Step 2: Verify the README renders sensibly**

Run: `rg -n "^### " README.md` and confirm the new heading appears between `複数行ログエントリのマージ(\`continuation\`)` and `info: Parquetファイルの中身を見る`.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "Document automatic decompression of compressed import input files"
```
