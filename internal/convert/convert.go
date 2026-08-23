package convert

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/wtnb75/logidx/internal/compression"
	"github.com/wtnb75/logidx/internal/pqinfo"
	"github.com/wtnb75/logidx/internal/rowgroup"
	"github.com/wtnb75/logidx/internal/rules"
	"github.com/wtnb75/logidx/internal/schema"
	"github.com/wtnb75/logidx/internal/writer"
)

// Files processes one or more input logs, merging their matched rows into a
// single set of output Parquet files (one per distinct rule name in
// cfg.Rules, named "<rule name>.parquet") and their unmatched lines into a
// single "unmatched.txt" sidecar, all under outDir - regardless of how many
// inputs there are or what they're named. Each element of inputPaths is a
// file path, or "-" to read from os.Stdin - the same convention used for
// dst.txt/src.txt in the dump/restore subcommands. now is the
// CLI-startup-fixed reference instant used to resolve year-less timestamps
// (see parse.Convert); it is passed in by the caller rather than captured
// here so a single run uses one consistent value across every input.
// assumedYear, if non-zero, is the CLI --assume-year value used in place
// of now-based inference to resolve missing-year timestamps (see
// parse.parseTimestampLayout). comp is the already-resolved (CLI > config
// file > default) Parquet compression setting, applied to every output
// file. rg is the already-resolved row group row-count limit, applied the
// same way (see internal/rowgroup); its zero value means unlimited.
//
// Processing continues past a failed input (its error is logged and joined
// into the returned error) rather than aborting the whole merge, so one bad
// input doesn't discard rows already gathered from the others.
func Files(inputPaths []string, outDir string, cfg *rules.Config, comp compression.Settings, rg rowgroup.Settings, logger *slog.Logger, now time.Time, assumedYear int) (err error) {
	if stdinCount := countStdinInputs(inputPaths); stdinCount > 1 {
		return fmt.Errorf("only one input may be \"-\" (stdin), got %d", stdinCount)
	}

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		return fmt.Errorf("build schemas: %w", err)
	}

	set := writer.NewSet(outDir, built, comp, rg)
	// set.Close() flushes each Parquet writer and writes its footer, so it
	// must run even if one or more inputs failed - otherwise a failure
	// partway through the merge leaves a truncated, unreadable .parquet
	// file and leaked file descriptors behind. A close error is joined onto
	// any earlier error rather than dropped or allowed to overwrite it; the
	// merged-output summary is only logged when the close itself succeeded.
	defer func() {
		summary, closeErr := set.Close()
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close writers: %w", closeErr))
			return
		}

		// Sorted for stable log ordering; summary.Paths is a map because
		// each rule's file is created lazily and keyed by name.
		names := make([]string, 0, len(summary.Paths))
		for name := range summary.Paths {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			path := summary.Paths[name]
			info, infoErr := pqinfo.Read(path)
			if infoErr != nil {
				logger.Warn("could not read output file for compression stats", "file", path, "error", infoErr)
				continue
			}

			fields := []any{
				"file", path,
				"rows", summary.Counts[name],
				"compressed_bytes", info.CompressedBytes,
				"uncompressed_bytes", info.UncompressedBytes,
			}
			if pct, ratio, ok := info.CompressionRatio(); ok {
				fields = append(fields, "compression_pct", fmt.Sprintf("%.1f%%", pct), "compression_ratio", fmt.Sprintf("%.2fx", ratio))
			}
			logger.Info("output parquet file", fields...)
		}
	}()

	return mergeFiles(inputPaths, cfg, set, logger, now, assumedYear)
}

// countStdinInputs returns how many elements of inputPaths are "-" (the
// os.Stdin convention). mergeFiles opens every input concurrently as its
// own fileCursor, and two cursors both wrapping os.Stdin would race for the
// same underlying bytes via their independent bufio.Scanner read buffers,
// so Files rejects more than one "-" up front instead of letting that
// non-determinism happen.
func countStdinInputs(inputPaths []string) int {
	count := 0
	for _, p := range inputPaths {
		if p == "-" {
			count++
		}
	}
	return count
}
