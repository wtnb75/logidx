package pqinfo

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/parquet-go/parquet-go"
)

// ColumnInfo describes one leaf column: its declared type and repetition
// from the schema, plus its codec and byte sizes summed across every row
// group.
type ColumnInfo struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	Repetition        string `json:"repetition"`
	Codec             string `json:"codec"`
	NumValues         int64  `json:"num_values"`
	CompressedBytes   int64  `json:"compressed_bytes"`
	UncompressedBytes int64  `json:"uncompressed_bytes"`
}

// Info is a Parquet file's footer metadata, summarized for display: file
// size, row/row-group counts, writer identification, and per-column detail.
//
// Column-chunk sizes and codecs are matched to schema columns by position,
// which assumes a flat (non-nested) schema — true for every file this CLI
// writes, but not guaranteed for arbitrary Parquet files.
type Info struct {
	Path              string       `json:"path"`
	SizeBytes         int64        `json:"size_bytes"`
	NumRows           int64        `json:"num_rows"`
	NumRowGroups      int          `json:"num_row_groups"`
	Version           int32        `json:"version"`
	CreatedBy         string       `json:"created_by"`
	Columns           []ColumnInfo `json:"columns"`
	CompressedBytes   int64        `json:"compressed_bytes"`
	UncompressedBytes int64        `json:"uncompressed_bytes"`
}

// Read opens the Parquet file at path and summarizes its footer metadata.
func Read(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read parquet file: %w", err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat parquet file: %w", err)
	}

	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		return nil, fmt.Errorf("parse parquet footer: %w", err)
	}

	meta := pf.Metadata()
	fields := pf.Schema().Fields()

	info := &Info{
		Path:         path,
		SizeBytes:    fi.Size(),
		NumRows:      pf.NumRows(),
		NumRowGroups: len(meta.RowGroups),
		Version:      meta.Version,
		CreatedBy:    meta.CreatedBy,
		Columns:      make([]ColumnInfo, len(fields)),
	}

	for i, field := range fields {
		info.Columns[i] = ColumnInfo{
			Name:       field.Name(),
			Type:       field.Type().String(),
			Repetition: repetitionOf(field),
		}
	}

	for _, rg := range meta.RowGroups {
		for i, col := range rg.Columns {
			if i >= len(info.Columns) {
				continue // more column chunks than schema fields: nested schema, not handled
			}
			md := col.MetaData
			info.Columns[i].Codec = md.Codec.String()
			info.Columns[i].NumValues += md.NumValues
			info.Columns[i].CompressedBytes += md.TotalCompressedSize
			info.Columns[i].UncompressedBytes += md.TotalUncompressedSize
			info.CompressedBytes += md.TotalCompressedSize
			info.UncompressedBytes += md.TotalUncompressedSize
		}
	}

	return info, nil
}

func repetitionOf(field parquet.Field) string {
	switch {
	case field.Optional():
		return "optional"
	case field.Repeated():
		return "repeated"
	default:
		return "required"
	}
}

// WriteText renders a human-readable summary to w.
func (info *Info) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "File:        %s\n", info.Path); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Size:        %d bytes\n", info.SizeBytes); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Rows:        %d\n", info.NumRows); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Row groups:  %d\n", info.NumRowGroups); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Version:     %d\n", info.Version); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Created by:  %s\n", info.CreatedBy); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(w, "\nColumns:"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "  NAME\tTYPE\tREPETITION\tVALUES\tCODEC\tCOMPRESSED\tUNCOMPRESSED"); err != nil {
		return err
	}
	for _, col := range info.Columns {
		if _, err := fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\t%d\t%d\n",
			col.Name, col.Type, col.Repetition, col.NumValues, col.Codec, col.CompressedBytes, col.UncompressedBytes); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	ratioText := "n/a"
	pct, ratio, ok := info.CompressionRatio()
	if ok {
		ratioText = fmt.Sprintf("%.2fx", ratio)
	}
	_, err := fmt.Fprintf(w, "\nCompression: %d / %d bytes (%.1f%%, %s)\n",
		info.CompressedBytes, info.UncompressedBytes, pct, ratioText)
	return err
}

// CompressionRatio summarizes info's compressed-vs-uncompressed size: pct is
// compressed/uncompressed as a percentage (lower is better), ratio is
// uncompressed/compressed (e.g. 4.0 means 4x smaller than raw). ok is false
// when UncompressedBytes is 0, meaning there is nothing to compute a ratio
// from (e.g. an empty file).
func (info *Info) CompressionRatio() (pct, ratio float64, ok bool) {
	if info.UncompressedBytes == 0 {
		return 0, 0, false
	}
	pct = float64(info.CompressedBytes) / float64(info.UncompressedBytes) * 100
	if info.CompressedBytes > 0 {
		ratio = float64(info.UncompressedBytes) / float64(info.CompressedBytes)
	}
	return pct, ratio, true
}

// WriteJSON renders info as indented JSON to w.
func (info *Info) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

// WriteJSONAll renders infos as a single indented JSON array to w, so
// output is valid JSON regardless of how many files were inspected.
func WriteJSONAll(w io.Writer, infos []*Info) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(infos)
}
