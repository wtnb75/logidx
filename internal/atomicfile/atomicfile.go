// Package atomicfile writes a file's full contents to a temporary sibling
// first and only replaces the destination path with a single os.Rename once
// every write has succeeded, so an interrupted write (an error, or a panic
// unwinding through a deferred Abort) never truncates or corrupts a
// pre-existing file at the destination.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// File is an *os.File opened at a temporary path alongside a destination,
// meant to be written exactly like a normal file and then finalized with
// Close (success) or Abort (failure) - never both, and never neither: a
// leftover temp file lingers if the caller does neither.
type File struct {
	*os.File

	tmpPath  string
	destPath string
	done     bool
}

// New creates a temp file in the same directory as path - required for the
// os.Rename in Close to be a same-filesystem, atomic rename - and returns a
// File wrapping it. The file at path itself is not touched: it is neither
// opened, truncated, nor removed until Close succeeds.
func New(path string) (*File, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file for %s: %w", path, err)
	}
	// os.CreateTemp always creates with mode 0600, unlike os.Create's
	// umask-adjusted 0666; align with what callers replacing os.Create
	// previously got for a typical umask.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	return &File{File: tmp, tmpPath: tmp.Name(), destPath: path}, nil
}

// Close closes the temp file and renames it into place at the destination
// path, atomically replacing whatever was there. Call it only once every
// write to File has succeeded; on any earlier failure call Abort instead.
func (f *File) Close() error {
	if f.done {
		return nil
	}
	f.done = true

	if err := f.File.Close(); err != nil {
		_ = os.Remove(f.tmpPath)
		return fmt.Errorf("close temp file for %s: %w", f.destPath, err)
	}
	if err := os.Rename(f.tmpPath, f.destPath); err != nil {
		_ = os.Remove(f.tmpPath)
		return fmt.Errorf("rename temp file into place at %s: %w", f.destPath, err)
	}
	return nil
}

// Abort closes and removes the temp file, leaving the destination path
// untouched. Safe to call after Close (a no-op then) or more than once -
// callers typically defer it unconditionally and let Close's success mark
// it a no-op, so it only actually fires on an earlier error or a panic
// unwinding past the write.
func (f *File) Abort() error {
	if f.done {
		return nil
	}
	f.done = true

	closeErr := f.File.Close()
	removeErr := os.Remove(f.tmpPath)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}
