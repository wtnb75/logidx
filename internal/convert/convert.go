package convert

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"time"

	"logidx/internal/compression"
	"logidx/internal/parse"
	"logidx/internal/pqinfo"
	"logidx/internal/rules"
	"logidx/internal/schema"
	"logidx/internal/writer"
)

// Files processes one or more input logs, merging their matched rows into a
// single set of output Parquet files (one per distinct rule name in
// cfg.Rules, named "<rule name>.parquet") and their unmatched lines into a
// single "unmatched.txt" sidecar, all under outDir - regardless of how many
// inputs there are or what they're named. Each element of inputPaths is a
// file path, or "-" to read from os.Stdin - the same convention used for
// dst.txt/src.txt in the dump/restore subcommands. now is the
// CLI-startup-fixed reference instant used to resolve year-less timestamps
// (see parse.Match); it is passed in by the caller rather than captured
// here so a single run uses one consistent value across every input. comp
// is the already-resolved (CLI > config file > default) Parquet
// compression setting, applied to every output file.
//
// Processing continues past a failed input (its error is logged and joined
// into the returned error) rather than aborting the whole merge, so one bad
// input doesn't discard rows already gathered from the others.
func Files(inputPaths []string, outDir string, cfg *rules.Config, comp compression.Settings, logger *slog.Logger, now time.Time) (err error) {
	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		return fmt.Errorf("build schemas: %w", err)
	}

	set := writer.NewSet(outDir, built, comp)
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

	var errs []error
	for _, inputPath := range inputPaths {
		if procErr := processInput(inputPath, cfg, set, logger, now); procErr != nil {
			logger.Error("failed to process file", "file", inputPath, "error", procErr)
			errs = append(errs, fmt.Errorf("%s: %w", inputPath, procErr))
		}
	}

	return errors.Join(errs...)
}

// processInput scans one input's lines into set and logs a "file processed"
// summary of what it contributed (its own counts, not set's running
// totals - those cover every input merged into set and are logged once,
// after all inputs are done, by Files' deferred summary). It returns an
// error only for failures that abort reading this one input early
// (open/write/scan failures); a line matching no rule isn't an error.
func processInput(inputPath string, cfg *rules.Config, set *writer.Set, logger *slog.Logger, now time.Time) error {
	in := io.Reader(os.Stdin)
	if inputPath != "-" {
		f, err := os.Open(inputPath)
		if err != nil {
			return fmt.Errorf("open input: %w", err)
		}
		defer func() { _ = f.Close() }()
		in = f
	}

	counts := map[string]int{}
	unmatched := 0

	scanner := bufio.NewScanner(in)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		name, values, ok := parse.Match(cfg.Rules, line, now)
		if !ok {
			logger.Debug("line did not match any rule", "file", inputPath, "line", lineNum)
			if err := set.WriteUnmatched(inputPath, lineNum, line); err != nil {
				return fmt.Errorf("write unmatched line %d: %w", lineNum, err)
			}
			unmatched++
			continue
		}

		if err := set.WriteMatched(name, values); err != nil {
			return fmt.Errorf("write matched row (rule %q, line %d): %w", name, lineNum, err)
		}
		counts[name]++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	args := []any{"file", inputPath}
	for name, count := range counts {
		args = append(args, name, count)
	}
	args = append(args, "unmatched", unmatched)
	logger.Info("file processed", args...)

	return nil
}
