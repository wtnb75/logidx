package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wtnb75/logidx/internal/compression"
	"github.com/wtnb75/logidx/internal/convert"
	"github.com/wtnb75/logidx/internal/logging"
	"github.com/wtnb75/logidx/internal/pqcat"
	"github.com/wtnb75/logidx/internal/pqdump"
	"github.com/wtnb75/logidx/internal/pqinfo"
	"github.com/wtnb75/logidx/internal/rowgroup"
	"github.com/wtnb75/logidx/internal/rules"

	"github.com/spf13/cobra"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// Version, Commit, and Built are overwritten via -ldflags by .goreleaser.yaml;
// they keep these defaults for plain `go build`/`go run`.
var (
	Version = "dev"
	Commit  = "none"
	Built   = "unknown"
)

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
	root.AddCommand(newCatCmd(stdout, stderr))
	root.AddCommand(newDumpCmd(stdout, stderr))
	root.AddCommand(newRestoreCmd(stdout, stderr))
	root.AddCommand(newExpandCmd(stdout, stderr))
	root.AddCommand(newCollapseCmd(stdout, stderr))
	root.AddCommand(newVersionCmd(stdout))
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
		rulesPath          string
		outDir             string
		logFormat          string
		verbose            bool
		compressionCodec   string
		compressionLevel   int
		maxRowsPerRowGroup int64
	)

	cmd := &cobra.Command{
		Use:           "import <input-log-file>...",
		Short:         "Convert and merge logs into parquet according to a rules file (- reads a log from stdin)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if rulesPath == "" || len(args) == 0 {
				_, _ = fmt.Fprintln(stderr, "usage: logidx import --rules <path> [--out <dir>] [--log-format text|json] [-v|--verbose] [--compression <codec>] [--compression-level <n>] [--max-rows-per-row-group <n>] <input-log-file|->...")
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

			cliRowGroup := rowgroup.Settings{}
			if cmd.Flags().Changed("max-rows-per-row-group") {
				maxRows := maxRowsPerRowGroup
				cliRowGroup.MaxRows = &maxRows
			}
			rg := rowgroup.Resolve(cliRowGroup, cfg.RowGroup)
			if err := rg.Validate(); err != nil {
				logger.Error("invalid row group settings", "error", err)
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

			if err := convert.Files(args, outDir, cfg, comp, rg, logger, now); err != nil {
				return &exitCodeError{1}
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
	cmd.Flags().Int64Var(&maxRowsPerRowGroup, "max-rows-per-row-group", 0, "parquet row group row-count limit; unset = unlimited (default); overrides the rules file's row_group.max_rows")

	return cmd
}

func newVersionCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:           "version",
		Short:         "Print version, commit, and build date",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(stdout, "logidx %s (commit %s, built %s)\n", Version, Commit, Built)
			return err
		},
	}
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

func newCatCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		outputPath         string
		compressionCodec   string
		compressionLevel   int
		maxRowsPerRowGroup int64
	)

	cmd := &cobra.Command{
		Use:           "cat --output <dst.parquet> <src.parquet>...",
		Short:         "Concatenate same-schema parquet files into one, merging by timestamp if the schema has one",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputPath == "" || len(args) == 0 {
				_, _ = fmt.Fprintln(stderr, "usage: logidx cat --output <dst.parquet> [--compression <codec>] [--compression-level <n>] [--max-rows-per-row-group <n>] <src.parquet>...")
				return &exitCodeError{2}
			}

			srcCodec, err := pqcat.SourceCodec(args[0])
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "%s: %v\n", args[0], err)
				return &exitCodeError{1}
			}

			cliCompression := compression.Settings{Codec: compressionCodec}
			if cmd.Flags().Changed("compression-level") {
				level := compressionLevel
				cliCompression.Level = &level
			}
			// With no --compression flag, fall back to the first source
			// file's own codec (not the package default), matching the old
			// `copy` command's behavior for its single input file.
			comp := compression.Resolve(cliCompression, compression.Settings{Codec: srcCodec})
			if err := comp.Validate(); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return &exitCodeError{2}
			}

			cliRowGroup := rowgroup.Settings{}
			if cmd.Flags().Changed("max-rows-per-row-group") {
				maxRows := maxRowsPerRowGroup
				cliRowGroup.MaxRows = &maxRows
			}
			if err := cliRowGroup.Validate(); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return &exitCodeError{2}
			}

			rows, err := pqcat.Cat(args, outputPath, comp, cliRowGroup)
			if err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return &exitCodeError{1}
			}

			msg := fmt.Sprintf("concatenated %d files, %d rows: %s -> %s (%s)", len(args), rows, strings.Join(args, ","), outputPath, comp.Codec)
			if dstInfo, infoErr := pqinfo.Read(outputPath); infoErr == nil {
				if pct, ratio, ok := dstInfo.CompressionRatio(); ok {
					msg += fmt.Sprintf(", %d/%d bytes (%.1f%%, %.2fx)", dstInfo.CompressedBytes, dstInfo.UncompressedBytes, pct, ratio)
				}
			}
			_, _ = fmt.Fprintln(stdout, msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&outputPath, "output", "", "output parquet file path (required)")
	cmd.Flags().StringVar(&compressionCodec, "compression", "", "parquet compression codec: uncompressed, snappy, gzip, brotli, zstd, lz4; default preserves the first source file's codec")
	cmd.Flags().IntVar(&compressionLevel, "compression-level", 0, "codec-specific compression level; default uses the new codec's own default level")
	cmd.Flags().Int64Var(&maxRowsPerRowGroup, "max-rows-per-row-group", 0, "parquet row group row-count limit; unset = unlimited (default)")

	return cmd
}

func newDumpCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "dump <src.parquet> <dst.txt>",
		Short:         "Dump a parquet file's schema and rows as human-readable JSON lines (dst - writes to stdout)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) != 2 {
				_, _ = fmt.Fprintln(stderr, "usage: logidx dump <src.parquet> <dst.txt|->")
				return &exitCodeError{2}
			}
			src, dst := args[0], args[1]

			// summaryOut defaults to stdout, matching every other command's
			// success message - except when dst is "-": stdout is then the
			// dump's own JSON Lines payload, and mixing a summary line into
			// it would corrupt anything reading that stream (jq, a piped
			// `logidx restore -`, ...), so the summary moves to stderr.
			var rows int64
			var err error
			summaryOut := stdout
			if dst == "-" {
				rows, err = pqdump.Dump(src, stdout)
				summaryOut = stderr
			} else {
				rows, err = dumpToFile(src, dst)
			}
			if err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return &exitCodeError{1}
			}

			_, _ = fmt.Fprintf(summaryOut, "dumped %d rows: %s -> %s\n", rows, src, dst)
			return nil
		},
	}

	return cmd
}

// dumpToFile creates dst and writes srcPath's dump to it, joining any error
// closing the file onto an earlier failure rather than dropping it -
// mirrors the deferred-close idiom used throughout this project's
// path-to-path file operations (see pqcat.Cat, pqdump.Restore).
func dumpToFile(srcPath, dstPath string) (rows int64, err error) {
	out, createErr := os.Create(dstPath)
	if createErr != nil {
		return 0, fmt.Errorf("create destination: %w", createErr)
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close destination: %w", closeErr))
		}
	}()

	return pqdump.Dump(srcPath, out)
}

func newRestoreCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		compressionCodec string
		compressionLevel int
	)

	cmd := &cobra.Command{
		Use:           "restore <dump.txt> <dst.parquet>",
		Short:         "Restore a parquet file from a dump produced by `logidx dump` (src - reads from stdin)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				_, _ = fmt.Fprintln(stderr, "usage: logidx restore [--compression <codec>] [--compression-level <n>] <dump.txt|-> <dst.parquet>")
				return &exitCodeError{2}
			}
			src, dst := args[0], args[1]

			in := io.Reader(os.Stdin)
			if src != "-" {
				f, err := os.Open(src)
				if err != nil {
					_, _ = fmt.Fprintln(stderr, err)
					return &exitCodeError{1}
				}
				defer func() { _ = f.Close() }()
				in = f
			}

			cliCompression := compression.Settings{Codec: compressionCodec}
			if cmd.Flags().Changed("compression-level") {
				level := compressionLevel
				cliCompression.Level = &level
			}

			rows, err := pqdump.Restore(in, dst, cliCompression)
			if err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return &exitCodeError{1}
			}

			msg := fmt.Sprintf("restored %d rows: %s -> %s", rows, src, dst)
			if dstInfo, infoErr := pqinfo.Read(dst); infoErr == nil {
				if pct, ratio, ok := dstInfo.CompressionRatio(); ok {
					msg += fmt.Sprintf(", %d/%d bytes (%.1f%%, %.2fx)", dstInfo.CompressedBytes, dstInfo.UncompressedBytes, pct, ratio)
				}
			}
			_, _ = fmt.Fprintln(stdout, msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&compressionCodec, "compression", "", "parquet compression codec: uncompressed, snappy, gzip, brotli, zstd, lz4; default preserves the dump's recorded codec")
	cmd.Flags().IntVar(&compressionLevel, "compression-level", 0, "codec-specific compression level; default uses the new codec's own default level")

	return cmd
}

// convertCmdSpec is the per-subcommand configuration newConvertCmd needs:
// expand and collapse are identical in shape (src/dst args, --log-format,
// -v, completion log line) and differ only in which rules.Expand/
// rules.Collapse function does the rewriting and what verb describes it.
type convertCmdSpec struct {
	use   string
	short string
	verb  string
	fn    func([]byte) ([]byte, int, error)
}

func newExpandCmd(stdout, stderr io.Writer) *cobra.Command {
	return newConvertCmd(stdout, stderr, convertCmdSpec{
		use:   "expand <src.yaml> <dst.yaml>",
		short: "Rewrite every rule's preset: into the pattern/fields it names (- reads/writes stdin/stdout)",
		verb:  "expanded",
		fn:    rules.Expand,
	})
}

func newCollapseCmd(stdout, stderr io.Writer) *cobra.Command {
	return newConvertCmd(stdout, stderr, convertCmdSpec{
		use:   "collapse <src.yaml> <dst.yaml>",
		short: "Rewrite rules whose pattern/fields exactly match a preset into preset: <name> (- reads/writes stdin/stdout)",
		verb:  "collapsed",
		fn:    rules.Collapse,
	})
}

func newConvertCmd(stdout, stderr io.Writer, spec convertCmdSpec) *cobra.Command {
	var (
		logFormat string
		verbose   bool
	)

	cmd := &cobra.Command{
		Use:           spec.use,
		Short:         spec.short,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) != 2 {
				_, _ = fmt.Fprintf(stderr, "usage: logidx %s [--log-format text|json] [-v|--verbose] <src.yaml|-> <dst.yaml|->\n", strings.Fields(spec.use)[0])
				return &exitCodeError{2}
			}
			src, dst := args[0], args[1]
			logger := logging.New(stderr, logFormat, verbose)

			data, err := readSrcFile(src)
			if err != nil {
				logger.Error("cannot read source", "error", err)
				return &exitCodeError{1}
			}

			out, count, err := spec.fn(data)
			if err != nil {
				logger.Error("conversion failed", "error", err)
				return &exitCodeError{1}
			}

			if err := writeDstFile(stdout, dst, out); err != nil {
				logger.Error("cannot write destination", "error", err)
				return &exitCodeError{1}
			}

			logger.Info(spec.verb+" rules", "count", count)
			return nil
		},
	}

	cmd.Flags().StringVar(&logFormat, "log-format", "text", "log format: text or json")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose (debug) logging")

	return cmd
}

// readSrcFile reads path, or os.Stdin if path is "-" - the same convention
// used by restore's src (see newRestoreCmd).
func readSrcFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// writeDstFile writes data to path, or stdout if path is "-" - the same
// convention used by dump's dst (see newDumpCmd). Writes through the
// injected stdout io.Writer, not os.Stdout directly, so tests can capture
// it via a bytes.Buffer.
func writeDstFile(stdout io.Writer, path string, data []byte) error {
	if path == "-" {
		_, err := stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
