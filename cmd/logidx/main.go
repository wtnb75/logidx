package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"logidx/internal/convert"
	"logidx/internal/logging"
	"logidx/internal/rules"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logidx", flag.ContinueOnError)
	fs.SetOutput(stderr)

	rulesPath := fs.String("rules", "", "path to rules YAML file (required)")
	outDir := fs.String("out", "./out", "output directory")
	logFormat := fs.String("log-format", "text", "log format: text or json")
	verbose := fs.Bool("v", false, "verbose (debug) logging")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *rulesPath == "" || fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: logidx --rules <path> [--out <dir>] [--log-format text|json] [-v] <input-log-file>...")
		return 2
	}

	logger := logging.New(stderr, *logFormat, *verbose)

	cfg, err := rules.Load(*rulesPath)
	if err != nil {
		logger.Error("invalid rules config", "error", err)
		return 1
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		logger.Error("cannot create output directory", "dir", *outDir, "error", err)
		return 1
	}

	exitCode := 0
	for _, inputPath := range fs.Args() {
		if err := convert.File(inputPath, *outDir, cfg, logger); err != nil {
			logger.Error("failed to process file", "file", inputPath, "error", err)
			exitCode = 1
		}
	}

	return exitCode
}
