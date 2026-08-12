package pqcat

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/parquet-go/parquet-go"

	"github.com/wtnb75/logidx/internal/atomicfile"
	"github.com/wtnb75/logidx/internal/compression"
	"github.com/wtnb75/logidx/internal/rowgroup"
	"github.com/wtnb75/logidx/internal/schema"
)

// batchSize is how many rows are read from a source and written to the
// destination per Read/Write call, both for plain concatenation (this
// file) and for the timestamp-ordered merge (merge.go).
const batchSize = 1000

// SourceCodec reads the compression codec used in path's first column
// chunk and maps it to the name used in compression.Settings.Codec. It
// returns "" if the file has no rows/columns to inspect, or if it uses a
// codec compression.Settings does not support selecting explicitly (LZO or
// deprecated LZ4) - either way, callers should treat "" as "unknown" and
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

// Cat reads every row from each file in srcPaths and writes them to a new
// Parquet file at dstPath with comp's compression codec/level and rg's row
// group limit applied. All srcPaths must share the exact same schema
// (column names, types, order); the first path's schema is canonical, and
// every other file's schema must equal it (see schema.Equal) or Cat
// returns an error without creating dstPath. If the canonical schema has a
// timestamp-typed column, rows from every file are merged into ascending
// order by that column instead of being concatenated in argument order -
// see detectMergeKey and mergeRows in merge.go. It returns the total
// number of rows written.
func Cat(srcPaths []string, dstPath string, comp compression.Settings, rg rowgroup.Settings) (rows int64, err error) {
	for _, src := range srcPaths {
		if filepath.Clean(src) == filepath.Clean(dstPath) {
			return 0, fmt.Errorf("output path must differ from every input file: %s", src)
		}
	}

	pf := make([]*parquet.File, len(srcPaths))
	files := make([]*os.File, len(srcPaths))
	defer func() {
		for _, f := range files {
			if f != nil {
				_ = f.Close()
			}
		}
	}()

	for i, src := range srcPaths {
		f, openErr := os.Open(src)
		if openErr != nil {
			return 0, fmt.Errorf("open %s: %w", src, openErr)
		}
		files[i] = f

		fi, statErr := f.Stat()
		if statErr != nil {
			return 0, fmt.Errorf("stat %s: %w", src, statErr)
		}
		parsed, parseErr := parquet.OpenFile(f, fi.Size())
		if parseErr != nil {
			return 0, fmt.Errorf("parse %s parquet footer: %w", src, parseErr)
		}
		pf[i] = parsed
	}

	canonical := pf[0].Schema()
	for i := 1; i < len(pf); i++ {
		if eqErr := schema.Equal(pf[i].Schema(), canonical); eqErr != nil {
			return 0, fmt.Errorf("schema mismatch: %s does not match %s (canonical): %w", srcPaths[i], srcPaths[0], eqErr)
		}
	}

	out, createErr := atomicfile.New(dstPath)
	if createErr != nil {
		return 0, fmt.Errorf("create destination: %w", createErr)
	}
	committed := false
	defer func() {
		if !committed {
			if abortErr := out.Abort(); abortErr != nil {
				err = errors.Join(err, fmt.Errorf("abort destination: %w", abortErr))
			}
		}
	}()

	dstSchema := parquet.NewSchema(canonical.Name(), schema.ForceCompression(canonical, comp.CodecInstance()))
	opts := []parquet.WriterOption{dstSchema}
	if opt, ok := rg.Option(); ok {
		opts = append(opts, opt)
	}
	writer := parquet.NewGenericWriter[map[string]any](out, opts...)

	mergeKey := detectMergeKey(canonical)

	if mergeKey == "" {
		for i, parsed := range pf {
			reader := parquet.NewGenericReader[map[string]any](parsed, parsed.Schema())
			n, readErr := copyRows(reader, writer)
			rows += n
			closeErr := reader.Close()
			if readErr != nil {
				return rows, fmt.Errorf("read %s: %w", srcPaths[i], readErr)
			}
			if closeErr != nil {
				return rows, fmt.Errorf("close reader for %s: %w", srcPaths[i], closeErr)
			}
		}
	} else {
		mergedRows, mergeErr := mergeRows(pf, srcPaths, mergeKey, writer)
		rows = mergedRows
		if mergeErr != nil {
			return rows, mergeErr
		}
	}

	if closeErr := writer.Close(); closeErr != nil {
		return rows, fmt.Errorf("close writer: %w", closeErr)
	}
	if closeErr := out.Close(); closeErr != nil {
		return rows, fmt.Errorf("publish destination: %w", closeErr)
	}
	committed = true
	return rows, nil
}

// copyRows reads every row from reader in batchSize batches and writes
// them to writer, unchanged. It returns the number of rows copied.
func copyRows(reader *parquet.GenericReader[map[string]any], writer *parquet.GenericWriter[map[string]any]) (rows int64, err error) {
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
				return rows, nil
			}
			return rows, readErr
		}
	}
}
