package writer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/compression"
	"logidx/internal/schema"
)

// Summary reports per-name matched row counts, their output Parquet file
// paths, and the unmatched line count for one processed input file.
type Summary struct {
	Counts    map[string]int
	Paths     map[string]string
	Unmatched int
}

// Set lazily manages one Parquet writer per rule name plus one unmatched
// raw-text writer, all scoped to a single input file's basename.
type Set struct {
	outDir      string
	basename    string
	built       map[string]*schema.Built
	compression compression.Settings

	parquetWriters map[string]*parquet.GenericWriter[map[string]any]
	parquetFiles   map[string]*os.File
	paths          map[string]string
	counts         map[string]int

	unmatchedFile  *os.File
	unmatchedCount int
}

// NewSet creates a writer Set for input file basename, writing outputs into
// outDir. built maps rule name -> derived Parquet schema (from
// schema.BuildAll), used lazily when the first row for that name arrives.
// comp selects the Parquet page compression codec applied to every output
// file in this Set.
func NewSet(outDir, basename string, built map[string]*schema.Built, comp compression.Settings) *Set {
	return &Set{
		outDir:         outDir,
		basename:       basename,
		built:          built,
		compression:    comp,
		parquetWriters: map[string]*parquet.GenericWriter[map[string]any]{},
		parquetFiles:   map[string]*os.File{},
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

	path := filepath.Join(s.outDir, fmt.Sprintf("%s.%s.parquet", s.basename, name))
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}

	w := parquet.NewGenericWriter[map[string]any](f, built.Schema, s.compression.WriterOption())

	s.parquetFiles[name] = f
	s.parquetWriters[name] = w
	s.paths[name] = path
	return w, nil
}

// WriteUnmatched appends one "<lineNum>\t<raw>\n" record to this input
// file's unmatched raw-text sidecar, creating it on first use.
func (s *Set) WriteUnmatched(lineNum int, raw string) error {
	if s.unmatchedFile == nil {
		path := filepath.Join(s.outDir, s.basename+".unmatched.txt")
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		s.unmatchedFile = f
	}

	if _, err := fmt.Fprintf(s.unmatchedFile, "%d\t%s\n", lineNum, raw); err != nil {
		return fmt.Errorf("write unmatched line: %w", err)
	}
	s.unmatchedCount++
	return nil
}

// Close flushes and closes every writer/file opened by this Set and
// returns a Summary of what was written.
func (s *Set) Close() (Summary, error) {
	var errs []error

	for name, w := range s.parquetWriters {
		if err := w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close parquet writer %q: %w", name, err))
		}
	}
	for name, f := range s.parquetFiles {
		if err := f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close parquet file %q: %w", name, err))
		}
	}
	if s.unmatchedFile != nil {
		if err := s.unmatchedFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close unmatched file: %w", err))
		}
	}

	return Summary{Counts: s.counts, Paths: s.paths, Unmatched: s.unmatchedCount}, errors.Join(errs...)
}
