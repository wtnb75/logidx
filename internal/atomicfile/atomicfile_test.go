package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewCloseReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed original: %v", err)
	}

	f, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := f.WriteString("replacement"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "replacement" {
		t.Fatalf("got %q, want %q", got, "replacement")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the published file to remain, got %v", entries)
	}
}

func TestAbortLeavesOriginalFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed original: %v", err)
	}

	f, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := f.WriteString("partial write before failure"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("original file was modified: got %q, want %q", got, "original")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the temp file to be removed, got %v", entries)
	}
}

// TestPanicDuringWriteLeavesOriginalFileIntact simulates the scenario a
// caller guards against with `defer` + a committed flag: a panic partway
// through writing must not touch the pre-existing file at path, and must
// not leave a stray temp file behind once the deferred Abort runs during
// unwinding.
func TestPanicDuringWriteLeavesOriginalFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed original: %v", err)
	}

	func() {
		defer func() {
			_ = recover()
		}()

		f, err := New(path)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = f.Abort()
			}
		}()

		if _, err := f.WriteString("half-written"); err != nil {
			t.Fatalf("write: %v", err)
		}
		panic("simulated failure mid-write")
	}()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("original file was modified: got %q, want %q", got, "original")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the temp file to be cleaned up, got %v", entries)
	}
}

func TestCloseAndAbortAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	f, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := f.Abort(); err != nil {
		t.Fatalf("Abort after Close: %v", err)
	}
}
