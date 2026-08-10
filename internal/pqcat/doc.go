// Package pqcat concatenates one or more Parquet files sharing the exact
// same schema into a single Parquet file, optionally applying a different
// compression codec, level, or row group size. If the shared schema has a
// timestamp-typed column, rows are merged into ascending order by that
// column across every input file instead of being concatenated in argument
// order (see merge.go).
package pqcat
