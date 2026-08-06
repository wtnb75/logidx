package convert

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"logidx/internal/parse"
	"logidx/internal/rules"
	"logidx/internal/schema"
	"logidx/internal/writer"
)

// File processes a single input log file: it matches each line against
// cfg.Rules, writes matched rows into per-rule-name Parquet files and
// unmatched lines into a raw-text sidecar, both under outDir, then logs a
// summary at Info level. now is the CLI-startup-fixed reference instant used
// to resolve year-less timestamps (see parse.Match); it is passed in by the
// caller rather than captured here so a single run uses one consistent
// value across every input file.
func File(inputPath, outDir string, cfg *rules.Config, logger *slog.Logger, now time.Time) (err error) {
	in, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer func() { _ = in.Close() }()

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		return fmt.Errorf("build schemas: %w", err)
	}

	basename := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	set := writer.NewSet(outDir, basename, built)
	// set.Close() flushes each Parquet writer and writes its footer, so it
	// must run on every path out of this function (including early error
	// returns from the scan loop below) - otherwise an error mid-file
	// leaves a truncated, unreadable .parquet file and leaked file
	// descriptors behind. A close error is joined onto any earlier error
	// rather than dropped or allowed to overwrite it; the summary is only
	// logged when the whole run - including the close - succeeded.
	defer func() {
		summary, closeErr := set.Close()
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close writers: %w", closeErr))
			return
		}
		if err != nil {
			return
		}

		args := []any{"file", inputPath}
		for name, count := range summary.Counts {
			args = append(args, name, count)
		}
		args = append(args, "unmatched", summary.Unmatched)
		logger.Info("file processed", args...)
	}()

	scanner := bufio.NewScanner(in)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		name, values, ok := parse.Match(cfg.Rules, line, now)
		if !ok {
			logger.Debug("line did not match any rule", "file", inputPath, "line", lineNum)
			if err := set.WriteUnmatched(lineNum, line); err != nil {
				return fmt.Errorf("write unmatched line %d: %w", lineNum, err)
			}
			continue
		}

		if err := set.WriteMatched(name, values); err != nil {
			return fmt.Errorf("write matched row (rule %q, line %d): %w", name, lineNum, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	return nil
}
