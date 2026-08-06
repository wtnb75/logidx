package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"logidx/internal/compression"
	"logidx/internal/convert"
	"logidx/internal/logging"
	"logidx/internal/pqinfo"
	"logidx/internal/rules"

	"github.com/spf13/cobra"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// exitCodeError carries a specific process exit code through cobra's error
// return path, since RunE only signals success/failure, not which code.
type exitCodeError struct {
	code int
}

func (e *exitCodeError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

func run(args []string, stdout, stderr io.Writer) int {
	root := &cobra.Command{
		Use:           "logidx",
		Short:         "logidx converts logs into structured formats",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddCommand(newImportCmd(stdout, stderr))
	root.AddCommand(newInfoCmd(stdout, stderr))
	root.SetArgs(args)

	if err := root.Execute(); err != nil {
		var ec *exitCodeError
		if errors.As(err, &ec) {
			return ec.code
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}

func newImportCmd(_, stderr io.Writer) *cobra.Command {
	var (
		rulesPath        string
		outDir           string
		logFormat        string
		verbose          bool
		compressionCodec string
		compressionLevel int
	)

	cmd := &cobra.Command{
		Use:           "import <input-log-file>...",
		Short:         "Convert logs to parquet according to a rules file",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if rulesPath == "" || len(args) == 0 {
				_, _ = fmt.Fprintln(stderr, "usage: logidx import --rules <path> [--out <dir>] [--log-format text|json] [-v|--verbose] [--compression <codec>] [--compression-level <n>] <input-log-file>...")
				return &exitCodeError{2}
			}

			logger := logging.New(stderr, logFormat, verbose)

			cfg, err := rules.Load(rulesPath)
			if err != nil {
				logger.Error("invalid rules config", "error", err)
				return &exitCodeError{1}
			}

			cliCompression := compression.Settings{Codec: compressionCodec}
			if cmd.Flags().Changed("compression-level") {
				level := compressionLevel
				cliCompression.Level = &level
			}
			comp := compression.Resolve(cliCompression, cfg.Compression)
			if err := comp.Validate(); err != nil {
				logger.Error("invalid compression settings", "error", err)
				return &exitCodeError{2}
			}

			if err := os.MkdirAll(outDir, 0o755); err != nil {
				logger.Error("cannot create output directory", "dir", outDir, "error", err)
				return &exitCodeError{1}
			}

			// now is fixed once at CLI startup and reused for every input file in
			// this run, so year-less-timestamp resolution has one consistent,
			// testable reference instant across the whole invocation.
			now := time.Now()

			exitCode := 0
			for _, inputPath := range args {
				if err := convert.File(inputPath, outDir, cfg, comp, logger, now); err != nil {
					logger.Error("failed to process file", "file", inputPath, "error", err)
					exitCode = 1
				}
			}

			if exitCode != 0 {
				return &exitCodeError{exitCode}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&rulesPath, "rules", "", "path to rules YAML file (required)")
	cmd.Flags().StringVar(&outDir, "out", "./out", "output directory")
	cmd.Flags().StringVar(&logFormat, "log-format", "text", "log format: text or json")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose (debug) logging")
	cmd.Flags().StringVar(&compressionCodec, "compression", "", "parquet compression codec: uncompressed, snappy, gzip, brotli, zstd (default), lz4; overrides the rules file's compression.codec")
	cmd.Flags().IntVar(&compressionLevel, "compression-level", 0, "codec-specific compression level; overrides the rules file's compression.level (see docs)")

	return cmd
}

func newInfoCmd(stdout, stderr io.Writer) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:           "info <parquet-file>...",
		Short:         "Show schema, compression, and row count info for parquet files",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				_, _ = fmt.Fprintln(stderr, "usage: logidx info [--format text|json] <parquet-file>...")
				return &exitCodeError{2}
			}
			if format != "text" && format != "json" {
				_, _ = fmt.Fprintf(stderr, "invalid --format %q: must be text or json\n", format)
				return &exitCodeError{2}
			}

			var infos []*pqinfo.Info
			exitCode := 0
			for _, path := range args {
				info, err := pqinfo.Read(path)
				if err != nil {
					_, _ = fmt.Fprintf(stderr, "%s: %v\n", path, err)
					exitCode = 1
					continue
				}
				infos = append(infos, info)
			}

			if format == "json" {
				if err := pqinfo.WriteJSONAll(stdout, infos); err != nil {
					_, _ = fmt.Fprintln(stderr, err)
					return &exitCodeError{1}
				}
			} else {
				for i, info := range infos {
					if i > 0 {
						_, _ = fmt.Fprintln(stdout)
					}
					if err := info.WriteText(stdout); err != nil {
						_, _ = fmt.Fprintln(stderr, err)
						return &exitCodeError{1}
					}
				}
			}

			if exitCode != 0 {
				return &exitCodeError{exitCode}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")

	return cmd
}
