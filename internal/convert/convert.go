package convert

import (
	"bufio"
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
// summary at Info level.
func File(inputPath, outDir string, cfg *rules.Config, logger *slog.Logger) error {
	in, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer in.Close()

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		return fmt.Errorf("build schemas: %w", err)
	}

	basename := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	set := writer.NewSet(outDir, basename, built)

	now := time.Now()
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

	summary, err := set.Close()
	if err != nil {
		return fmt.Errorf("close writers: %w", err)
	}

	args := []any{"file", inputPath}
	for name, count := range summary.Counts {
		args = append(args, name, count)
	}
	args = append(args, "unmatched", summary.Unmatched)
	logger.Info("file processed", args...)

	return nil
}
