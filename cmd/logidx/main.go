package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"logidx/internal/convert"
	"logidx/internal/logging"
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
		rulesPath string
		outDir    string
		logFormat string
		verbose   bool
	)

	cmd := &cobra.Command{
		Use:           "import <input-log-file>...",
		Short:         "Convert logs to parquet according to a rules file",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if rulesPath == "" || len(args) == 0 {
				_, _ = fmt.Fprintln(stderr, "usage: logidx import --rules <path> [--out <dir>] [--log-format text|json] [-v|--verbose] <input-log-file>...")
				return &exitCodeError{2}
			}

			logger := logging.New(stderr, logFormat, verbose)

			cfg, err := rules.Load(rulesPath)
			if err != nil {
				logger.Error("invalid rules config", "error", err)
				return &exitCodeError{1}
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
				if err := convert.File(inputPath, outDir, cfg, logger, now); err != nil {
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

	return cmd
}
