package pqcopy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/compression"
	"logidx/internal/schema"
)

// batchSize is how many rows are read from the source and written to the
// destination per Read/Write call.
const batchSize = 1000

// SourceCodec reads the compression codec used in the source file's first
// column chunk and maps it to the name used in compression.Settings.Codec.
// It returns "" if the file has no rows/columns to inspect, or if it uses a
// codec compression.Settings does not support selecting explicitly (LZO or
// deprecated LZ4) — either way, callers should treat "" as "unknown" and
// fall back to their own default.
func SourceCodec(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat source: %w", err)
	}

	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		return "", fmt.Errorf("parse source parquet footer: %w", err)
	}

	meta := pf.Metadata()
	if len(meta.RowGroups) == 0 || len(meta.RowGroups[0].Columns) == 0 {
		return "", nil
	}

	name, _ := compression.NameFromFormatCodec(meta.RowGroups[0].Columns[0].MetaData.Codec)
	return name, nil
}

// Copy reads every row from the Parquet file at srcPath and writes them,
// unchanged, to a new Parquet file at dstPath with the same schema but
// comp's compression codec/level applied instead of the source's. It
// returns the number of rows copied.
func Copy(srcPath, dstPath string, comp compression.Settings) (rows int64, err error) {
	if filepath.Clean(srcPath) == filepath.Clean(dstPath) {
		return 0, errors.New("source and destination must be different files")
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return 0, fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = in.Close() }()

	fi, err := in.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat source: %w", err)
	}

	pf, err := parquet.OpenFile(in, fi.Size())
	if err != nil {
		return 0, fmt.Errorf("parse source parquet footer: %w", err)
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

	reader := parquet.NewGenericReader[map[string]any](pf, pf.Schema())
	defer func() { _ = reader.Close() }()

	// pf.Schema()'s leaf nodes are backed by the source file's *parquet.Column
	// values, whose Compression() reports that column's on-disk codec. The
	// writer prefers a leaf's own reported codec over its Compression
	// WriterOption, so writing with pf.Schema() unmodified would silently
	// keep the source's compression instead of applying comp. Rebuild the
	// schema with every leaf forced onto comp's codec to actually change it.
	dstSchema := parquet.NewSchema(pf.Schema().Name(), schema.ForceCompression(pf.Schema(), comp.CodecInstance()))
	writer := parquet.NewGenericWriter[map[string]any](out, dstSchema)

	buf := make([]map[string]any, batchSize)
	for {
		for i := range buf {
			buf[i] = map[string]any{}
		}

		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, writeErr := writer.Write(buf[:n]); writeErr != nil {
				return rows, fmt.Errorf("write rows: %w", writeErr)
			}
			rows += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return rows, fmt.Errorf("read rows: %w", readErr)
		}
	}

	if closeErr := writer.Close(); closeErr != nil {
		return rows, fmt.Errorf("close writer: %w", closeErr)
	}

	return rows, nil
}
