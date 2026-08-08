package convert

import (
	"bufio"
	"container/heap"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"logidx/internal/parse"
	"logidx/internal/rules"
	"logidx/internal/writer"
)

// mergeKeyField returns, for each distinct rule name in ruleList, the name
// of its first Type == "timestamp" field in declaration order — the field
// internal/convert.mergeFiles uses to globally order that rule's matched
// rows across every input file. Rules with no timestamp field are omitted
// from the result; their matched rows are written in plain file-arrival
// order instead (see fileCursor.advance).
func mergeKeyField(ruleList []rules.Rule) map[string]string {
	result := map[string]string{}
	for _, r := range ruleList {
		if _, exists := result[r.Name]; exists {
			continue
		}
		for _, field := range r.Fields {
			if field.Type == "timestamp" {
				result[r.Name] = field.Name
				break
			}
		}
	}
	return result
}

// candidate is one matched row held back from immediate writing because
// its rule has a merge key (see mergeKeyField): mergeFiles compares
// candidates from every open fileCursor and writes the earliest one first.
type candidate struct {
	cursor    *fileCursor
	name      string
	values    map[string]any
	sortValue time.Time
	lineNum   int
}

// fileCursor scans one input file's lines in order. Lines that don't match
// any rule, or that match a rule with no merge key, are written
// immediately as advance() passes over them — exactly like the old
// sequential processInput did. Lines that match a rule with a merge key
// are held as the cursor's returned candidate instead, so mergeFiles can
// compare candidates across every input file before any of them is
// actually written.
//
// logger must be non-nil: advance() logs through it unconditionally.
type fileCursor struct {
	inputPath string
	fileIndex int
	file      *os.File // nil when reading os.Stdin
	scanner   *bufio.Scanner
	lineNum   int

	cfg      *rules.Config
	mergeKey map[string]string
	set      *writer.Set
	logger   *slog.Logger
	now      time.Time

	counts    map[string]int
	unmatched int
}

// newFileCursor opens inputPath (or os.Stdin if inputPath is "-") and
// returns a cursor ready for advance(). fileIndex is inputPath's position
// among the inputPaths mergeFiles was given, used only to break ties when
// two candidates from different files have the exact same sortValue.
func newFileCursor(inputPath string, fileIndex int, cfg *rules.Config, mergeKey map[string]string, set *writer.Set, logger *slog.Logger, now time.Time) (*fileCursor, error) {
	var f *os.File
	in := io.Reader(os.Stdin)
	if inputPath != "-" {
		var err error
		f, err = os.Open(inputPath)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		in = f
	}

	return &fileCursor{
		inputPath: inputPath,
		fileIndex: fileIndex,
		file:      f,
		scanner:   bufio.NewScanner(in),
		cfg:       cfg,
		mergeKey:  mergeKey,
		set:       set,
		logger:    logger,
		now:       now,
		counts:    map[string]int{},
	}, nil
}

// advance reads forward from where it last stopped until it finds a row
// eligible for merging, writing every ineligible row it passes along the
// way (unmatched lines to the shared sidecar, matched-but-no-merge-key rows
// straight to their rule's writer). ok is false once the file is
// exhausted, at which point every one of its rows has been written or
// returned as a candidate — there is nothing left to do with this cursor
// but close() it.
func (c *fileCursor) advance() (cand *candidate, ok bool, err error) {
	for c.scanner.Scan() {
		c.lineNum++
		line := c.scanner.Text()

		name, values, matched := parse.Match(c.cfg.Rules, line, c.now)
		if !matched {
			c.logger.Debug("line did not match any rule", "file", c.inputPath, "line", c.lineNum)
			if err := c.set.WriteUnmatched(c.inputPath, c.lineNum, line); err != nil {
				return nil, false, fmt.Errorf("write unmatched line %d: %w", c.lineNum, err)
			}
			c.unmatched++
			continue
		}

		keyField, hasMergeKey := c.mergeKey[name]
		if !hasMergeKey {
			if err := c.set.WriteMatched(name, values); err != nil {
				return nil, false, fmt.Errorf("write matched row (rule %q, line %d): %w", name, c.lineNum, err)
			}
			c.counts[name]++
			continue
		}

		sortValue, isTime := values[keyField].(time.Time)
		if !isTime {
			// Defensively unreachable: parse.Match and rules.Validate
			// guarantee a timestamp-typed field always yields a time.Time.
			// If this ever did fire, degrade to skipping just this one row
			// rather than aborting the rest of the file.
			c.logger.Error("merge key value is not a timestamp, skipping row", "rule", name, "field", keyField, "file", c.inputPath, "line", c.lineNum)
			continue
		}
		c.counts[name]++
		return &candidate{cursor: c, name: name, values: values, sortValue: sortValue, lineNum: c.lineNum}, true, nil
	}

	if err := c.scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("read input: %w", err)
	}
	return nil, false, nil
}

// close closes the underlying file, if any (nothing to close for os.Stdin).
func (c *fileCursor) close() error {
	if c.file == nil {
		return nil
	}
	return c.file.Close()
}

// candidateHeap is a min-heap of candidates ordered by sortValue, with the
// originating file's position among mergeFiles' inputPaths as a tiebreak,
// so two candidates with the exact same timestamp still pop in a fixed,
// repeatable order across runs.
type candidateHeap []*candidate

func (h candidateHeap) Len() int { return len(h) }

func (h candidateHeap) Less(i, j int) bool {
	if !h[i].sortValue.Equal(h[j].sortValue) {
		return h[i].sortValue.Before(h[j].sortValue)
	}
	return h[i].cursor.fileIndex < h[j].cursor.fileIndex
}

func (h candidateHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *candidateHeap) Push(x any) {
	*h = append(*h, x.(*candidate))
}

func (h *candidateHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// mergeFiles processes every input, merging rows from rules with a merge
// key (see mergeKeyField) into ascending-timestamp order across all inputs
// combined, while rows from rules without one are written in each file's
// own arrival order — matching Files' pre-merge behavior exactly when no
// rule has a merge key at all, or when there's only one input file (the
// heap never holds more than one candidate at a time in either case).
//
// Processing continues past a failed input: its cursor is dropped from the
// merge and its error is joined into the returned error, so one bad input
// doesn't stop the others from being merged and written.
func mergeFiles(inputPaths []string, cfg *rules.Config, set *writer.Set, logger *slog.Logger, now time.Time) error {
	mergeKey := mergeKeyField(cfg.Rules)

	var errs []error
	h := candidateHeap{}

	for i, inputPath := range inputPaths {
		cursor, err := newFileCursor(inputPath, i, cfg, mergeKey, set, logger, now)
		if err != nil {
			logger.Error("failed to process file", "file", inputPath, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", inputPath, err))
			continue
		}
		advanceOrRecord(cursor, &h, logger, &errs)
	}

	for h.Len() > 0 {
		cand := heap.Pop(&h).(*candidate)
		if err := set.WriteMatched(cand.name, cand.values); err != nil {
			err = fmt.Errorf("write matched row (rule %q, line %d): %w", cand.name, cand.lineNum, err)
			logger.Error("failed to process file", "file", cand.cursor.inputPath, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", cand.cursor.inputPath, err))
			if closeErr := cand.cursor.close(); closeErr != nil {
				closeErr = fmt.Errorf("%s: close: %w", cand.cursor.inputPath, closeErr)
				logger.Error("failed to close input file", "file", cand.cursor.inputPath, "error", closeErr)
				errs = append(errs, closeErr)
			}
			continue
		}

		advanceOrRecord(cand.cursor, &h, logger, &errs)
	}

	return errors.Join(errs...)
}

// advanceOrRecord calls cursor.advance(), pushing a new candidate onto h on
// success. Once the cursor has nothing left to contribute (EOF or error) it
// closes the cursor itself — logging and recording any close error onto
// errs the same way as every other exit path in mergeFiles — and, for EOF,
// logs its "file processed" summary.
func advanceOrRecord(cursor *fileCursor, h *candidateHeap, logger *slog.Logger, errs *[]error) {
	cand, ok, err := cursor.advance()
	if err != nil {
		logger.Error("failed to process file", "file", cursor.inputPath, "error", err)
		*errs = append(*errs, fmt.Errorf("%s: %w", cursor.inputPath, err))
		if closeErr := cursor.close(); closeErr != nil {
			closeErr = fmt.Errorf("%s: close: %w", cursor.inputPath, closeErr)
			logger.Error("failed to close input file", "file", cursor.inputPath, "error", closeErr)
			*errs = append(*errs, closeErr)
		}
		return
	}
	if !ok {
		logFileProcessed(logger, cursor)
		if closeErr := cursor.close(); closeErr != nil {
			closeErr = fmt.Errorf("%s: close: %w", cursor.inputPath, closeErr)
			logger.Error("failed to close input file", "file", cursor.inputPath, "error", closeErr)
			*errs = append(*errs, closeErr)
		}
		return
	}
	heap.Push(h, cand)
}

// logFileProcessed logs the same "file processed" summary the old
// sequential processInput logged once it finished a file: its own
// per-rule-name match counts (not the merged Set's running totals) and how
// many of its lines matched no rule.
func logFileProcessed(logger *slog.Logger, c *fileCursor) {
	args := []any{"file", c.inputPath}
	for name, count := range c.counts {
		args = append(args, name, count)
	}
	args = append(args, "unmatched", c.unmatched)
	logger.Info("file processed", args...)
}
