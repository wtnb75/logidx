package writer

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/wtnb75/logidx/internal/atomicfile"
	"github.com/wtnb75/logidx/internal/compression"
	"github.com/wtnb75/logidx/internal/rowgroup"
	"github.com/wtnb75/logidx/internal/schema"
)

// Summary reports per-name matched row counts, their output Parquet file
// paths, and the unmatched line count across every input merged into a Set.
type Summary struct {
	Counts map[string]int
	Paths  map[string]string
	// Unmatched is every line written to unmatched.txt: lines that matched
	// no rule, and (if rules.Config.Ignore is configured) lines dropped by
	// ignore: before pattern matching - see writer.WriteUnmatched's reason
	// parameter. It does not distinguish between the two.
	Unmatched int
}

// Set lazily manages one Parquet writer per rule name plus one unmatched
// raw-text writer, all scoped to a single outDir. A Set can be fed rows from
// multiple input files (see internal/convert.Files): every input sharing a
// Set merges into the same per-rule-name output files, rather than each
// input producing its own separate output files.
type Set struct {
	outDir      string
	built       map[string]*schema.Built
	compression compression.Settings
	rowGroup    rowgroup.Settings

	parquetWriters map[string]*parquet.GenericWriter[map[string]any]
	parquetFiles   map[string]*atomicfile.File
	paths          map[string]string
	counts         map[string]int

	unmatchedFile  *atomicfile.File
	unmatchedCount int
}

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
		parquetFiles:   map[string]*atomicfile.File{},
		paths:          map[string]string{},
		counts:         map[string]int{},
	}
}

// WriteMatched writes one row of values (keyed by field name) for the given
// rule name, creating that name's Parquet file on first use.
func (s *Set) WriteMatched(name string, values map[string]any) error {
	w, err := s.writerFor(name)
	if err != nil {
		return err
	}

	built := s.built[name]
	row := make(map[string]any, len(built.Columns))
	for _, col := range built.Columns {
		v := values[col]
		if t, ok := v.(time.Time); ok {
			v = t.UnixMicro()
		}
		row[col] = v
	}

	if _, err := w.Write([]map[string]any{row}); err != nil {
		return fmt.Errorf("write row for %q: %w", name, err)
	}
	s.counts[name]++
	return nil
}

func (s *Set) writerFor(name string) (*parquet.GenericWriter[map[string]any], error) {
	if w, ok := s.parquetWriters[name]; ok {
		return w, nil
	}

	built, ok := s.built[name]
	if !ok {
		return nil, fmt.Errorf("no schema registered for rule name %q", name)
	}

	path := filepath.Join(s.outDir, fmt.Sprintf("%s.parquet", name))
	f, err := atomicfile.New(path)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}

	opts := []parquet.WriterOption{built.Schema, s.compression.WriterOption()}
	if opt, ok := s.rowGroup.Option(); ok {
		opts = append(opts, opt)
	}
	w := parquet.NewGenericWriter[map[string]any](f, opts...)

	s.parquetFiles[name] = f
	s.parquetWriters[name] = w
	s.paths[name] = path
	return w, nil
}

// WriteUnmatched appends one "<source>\t<lineNum>\t<reason>\t<raw>\n" record
// to the shared unmatched raw-text sidecar, creating it on first use.
// source identifies which input the line came from (its path, or "-" for
// stdin) - necessary because a Set merges multiple inputs, so lineNum alone
// would be ambiguous (e.g. line 5 of two different input files). reason is
// "unmatched" for a line that matched no rule, or "ignored:<condition>" for
// one dropped by rules.IgnoreConfig before pattern matching even ran (see
// internal/convert.fileCursor.nextLine).
func (s *Set) WriteUnmatched(source string, lineNum int, reason, raw string) error {
	if s.unmatchedFile == nil {
		path := filepath.Join(s.outDir, "unmatched.txt")
		f, err := atomicfile.New(path)
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		s.unmatchedFile = f
	}

	if _, err := fmt.Fprintf(s.unmatchedFile, "%s\t%d\t%s\t%s\n", source, lineNum, reason, raw); err != nil {
		return fmt.Errorf("write unmatched line: %w", err)
	}
	s.unmatchedCount++
	return nil
}

// Close flushes every writer opened by this Set and atomically publishes
// each one's output file - via atomicfile, so any pre-existing file at that
// path is only replaced once its writer has fully and successfully closed.
// A writer that fails to close has its temp file aborted (removed) instead
// of published, leaving a pre-existing file at that path untouched rather
// than replaced by a truncated or corrupt one. It returns a Summary of what
// was written.
func (s *Set) Close() (Summary, error) {
	var errs []error

	for name, w := range s.parquetWriters {
		f := s.parquetFiles[name]
		if err := w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close parquet writer %q: %w", name, err))
			if abortErr := f.Abort(); abortErr != nil {
				errs = append(errs, fmt.Errorf("abort parquet file %q: %w", name, abortErr))
			}
			continue
		}
		if err := f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("publish parquet file %q: %w", name, err))
		}
	}
	if s.unmatchedFile != nil {
		if err := s.unmatchedFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("publish unmatched file: %w", err))
		}
	}

	return Summary{Counts: s.counts, Paths: s.paths, Unmatched: s.unmatchedCount}, errors.Join(errs...)
}
