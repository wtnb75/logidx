package pqdump

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/wtnb75/logidx/internal/compression"
	"github.com/wtnb75/logidx/internal/schema"
)

const batchSize = 1000

// Column describes one field in a dump's header: its name and its logical
// type, in the same vocabulary rules.yaml field types use ("string", "int",
// "float", "timestamp") via schema.NodeForType/TypeName.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Header is the first line of a dump: the source file's schema and
// compression codec, so Restore can recreate an equivalent Parquet file
// without the caller re-specifying either.
type Header struct {
	Columns     []Column             `json:"columns"`
	Compression compression.Settings `json:"compression"`
}

// Dump reads the Parquet file at srcPath and writes a text dump to w: one
// Header line, then one JSON object per row. Timestamp columns (stored on
// disk as microseconds-since-epoch int64s, see internal/writer) are
// rendered as RFC3339Nano strings in UTC so the dump stays human-readable
// without losing precision - RFC3339Nano's fractional seconds go to
// nanoseconds, more than enough to represent an exact microsecond value,
// and Restore parses them back to the identical int64.
func Dump(srcPath string, w io.Writer) (rows int64, err error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return 0, fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat source: %w", err)
	}

	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		return 0, fmt.Errorf("parse source parquet footer: %w", err)
	}

	fields := pf.Schema().Fields()
	header := Header{Columns: make([]Column, len(fields))}
	timestampCols := map[string]bool{}
	for i, field := range fields {
		typeName, err := schema.TypeName(field)
		if err != nil {
			return 0, fmt.Errorf("column %q: %w", field.Name(), err)
		}
		header.Columns[i] = Column{Name: field.Name(), Type: typeName}
		if typeName == "timestamp" {
			timestampCols[field.Name()] = true
		}
	}

	meta := pf.Metadata()
	if len(meta.RowGroups) > 0 && len(meta.RowGroups[0].Columns) > 0 {
		if codec, ok := compression.NameFromFormatCodec(meta.RowGroups[0].Columns[0].MetaData.Codec); ok {
			header.Compression.Codec = codec
		}
	}

	enc := json.NewEncoder(w)
	if err := enc.Encode(header); err != nil {
		return 0, fmt.Errorf("write header: %w", err)
	}

	reader := parquet.NewGenericReader[map[string]any](pf, pf.Schema())
	defer func() { _ = reader.Close() }()

	buf := make([]map[string]any, batchSize)
	for {
		for i := range buf {
			buf[i] = map[string]any{}
		}

		n, readErr := reader.Read(buf)
		for _, row := range buf[:n] {
			for name := range timestampCols {
				micros, ok := row[name].(int64)
				if !ok {
					continue
				}
				row[name] = time.UnixMicro(micros).UTC().Format(time.RFC3339Nano)
			}
			if err := enc.Encode(row); err != nil {
				return rows, fmt.Errorf("write row %d: %w", rows+1, err)
			}
			rows++
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return rows, fmt.Errorf("read rows: %w", readErr)
		}
	}

	return rows, nil
}

// Restore reads a dump produced by Dump from r and writes an equivalent
// Parquet file to dstPath, reconstructing the schema from the dump's header
// and applying comp's compression settings, falling back to the header's
// recorded codec/level wherever comp leaves a field unset (the same
// CLI-flag-beats-file precedence compression.Resolve applies elsewhere).
func Restore(r io.Reader, dstPath string, comp compression.Settings) (rows int64, err error) {
	dec := json.NewDecoder(r)

	var header Header
	if err := dec.Decode(&header); err != nil {
		return 0, fmt.Errorf("read header: %w", err)
	}
	if len(header.Columns) == 0 {
		return 0, errors.New("dump header declares no columns")
	}

	group := map[string]parquet.Node{}
	names := make([]string, len(header.Columns))
	timestampCols := map[string]bool{}
	for i, col := range header.Columns {
		node, err := schema.NodeForType(col.Type)
		if err != nil {
			return 0, fmt.Errorf("column %q: %w", col.Name, err)
		}
		group[col.Name] = parquet.Required(node)
		names[i] = col.Name
		if col.Type == "timestamp" {
			timestampCols[col.Name] = true
		}
	}
	// schema.NewOrderedGroup, not a plain parquet.Group (a map, which would
	// silently re-alphabetize), so the restored file's column order matches
	// the dump header's - which is itself the order Dump read from the
	// source file (see internal/schema.Build's own ordering fix).
	sch := parquet.NewSchema("row", schema.NewOrderedGroup(group, names))

	resolved := compression.Resolve(comp, header.Compression)
	if err := resolved.Validate(); err != nil {
		return 0, fmt.Errorf("invalid compression settings: %w", err)
	}

	out, err := os.Create(dstPath)
	if err != nil {
		return 0, fmt.Errorf("create destination: %w", err)
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close destination: %w", closeErr))
		}
	}()

	writer := parquet.NewGenericWriter[map[string]any](out, sch, resolved.WriterOption())

	for {
		row := map[string]any{}
		if err := dec.Decode(&row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return rows, fmt.Errorf("read row %d: %w", rows+1, err)
		}

		for name := range timestampCols {
			raw, ok := row[name]
			if !ok {
				continue
			}
			s, ok := raw.(string)
			if !ok {
				return rows, fmt.Errorf("row %d: column %q: expected a timestamp string, got %T", rows+1, name, raw)
			}
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return rows, fmt.Errorf("row %d: column %q: %w", rows+1, name, err)
			}
			row[name] = t.UnixMicro()
		}

		if _, err := writer.Write([]map[string]any{row}); err != nil {
			return rows, fmt.Errorf("write row %d: %w", rows+1, err)
		}
		rows++
	}

	if err := writer.Close(); err != nil {
		return rows, fmt.Errorf("close writer: %w", err)
	}

	return rows, nil
}
