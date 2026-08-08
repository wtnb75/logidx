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

// scannedLine is one physical line read from a fileCursor's underlying
// scanner, tagged with its 1-based line number.
type scannedLine struct {
	text    string
	lineNum int
}

// openEntry accumulates one in-progress multi-line log entry: a matched
// rule plus its raw (un-converted) field captures, updated in place as
// continuation lines are folded in. rawLines keeps every physical line
// that contributed to the entry, in original order, so a type-conversion
// failure can still report each one as its own unmatched.txt record (see
// fileCursor.finalizeEntry).
type openEntry struct {
	rule     *rules.Rule
	raw      map[string]string
	rawLines []scannedLine
}

// fileCursor scans one input file's lines in order. Lines that don't match
// any rule, or that match a rule with no merge key, are written
// immediately as advance() passes over them — exactly like the old
// sequential processInput did. Lines that match a rule with a merge key
// are held as the cursor's returned candidate instead, so mergeFiles can
// compare candidates across every input file before any of them is
// actually written.
//
// A rule with a Continuation pattern (see rules.Rule) can span several
// physical lines: while one of its entries is open (open != nil),
// subsequent lines are matched against the rule's ContinuationRegexp
// instead of the full rule list, and folded into the entry, until a
// non-continuation line, a new rule match, or EOF closes it. pending holds
// one line read back so the line that closed an entry can be reprocessed
// from scratch as a fresh candidate for a new entry.
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

	open    *openEntry
	pending *scannedLine
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

// nextLine returns the next physical line to process: a previously pushed
// back line, if any (see fileCursor.pending), otherwise the next line from
// the underlying scanner. ok is false at EOF.
func (c *fileCursor) nextLine() (line scannedLine, ok bool, err error) {
	if c.pending != nil {
		line = *c.pending
		c.pending = nil
		return line, true, nil
	}
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return scannedLine{}, false, fmt.Errorf("read input: %w", err)
		}
		return scannedLine{}, false, nil
	}
	c.lineNum++
	return scannedLine{text: c.scanner.Text(), lineNum: c.lineNum}, true, nil
}

// matchContinuation tries rule's continuation pattern against a line and,
// if it matches, returns its named captures. The returned map is non-nil
// but may be empty: a continuation pattern with zero named capture groups
// is valid (it still ends the search for a new entry and keeps this one
// open) and simply contributes nothing to any field — useful for
// absorbing decorative separator lines.
func matchContinuation(rule *rules.Rule, line string) (raw map[string]string, matched bool) {
	m := rule.ContinuationRegexp.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	raw = map[string]string{}
	for i, name := range rule.ContinuationRegexp.SubexpNames() {
		if i == 0 || name == "" {
			continue
		}
		raw[name] = m[i]
	}
	return raw, true
}

// appendContinuation folds a continuation line's captures into entry's
// accumulated raw values: each captured field is newline-joined onto its
// existing value, or set outright if this is that field's first
// contribution.
func appendContinuation(entry *openEntry, raw map[string]string) {
	for name, v := range raw {
		if existing, ok := entry.raw[name]; ok {
			entry.raw[name] = existing + "\n" + v
		} else {
			entry.raw[name] = v
		}
	}
}

// writeUnmatchedLine writes one physical line to the shared unmatched.txt
// sidecar and updates this cursor's unmatched count.
func (c *fileCursor) writeUnmatchedLine(line scannedLine) error {
	if err := c.set.WriteUnmatched(c.inputPath, line.lineNum, line.text); err != nil {
		return fmt.Errorf("write unmatched line %d: %w", line.lineNum, err)
	}
	c.unmatched++
	return nil
}

// finalizeEntry converts entry's accumulated raw values and disposes of
// the result. A type-conversion failure splits the entry back into its
// original per-line unmatched.txt records instead of writing one record
// with embedded newlines, preserving unmatched.txt's one-record-per-line
// format. A successfully converted row is either returned as a candidate
// (its rule has a merge key, see mergeKeyField) for the caller to hand to
// mergeFiles, or written immediately otherwise — the same two outcomes a
// single-line match without Continuation configured has always had. The
// returned error is only non-nil for a genuine write/I-O failure; a
// conversion failure is reported by returning (nil, nil), same as any
// other row finalizeEntry disposed of by writing it out itself.
func (c *fileCursor) finalizeEntry(entry *openEntry) (*candidate, error) {
	values, convErr := parse.Convert(*entry.rule, entry.raw, c.now)
	if convErr != nil {
		c.logger.Debug("multi-line entry failed type conversion", "file", c.inputPath, "rule", entry.rule.Name, "start_line", entry.rawLines[0].lineNum, "error", convErr)
		for _, rl := range entry.rawLines {
			if err := c.writeUnmatchedLine(rl); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}

	name := entry.rule.Name
	startLine := entry.rawLines[0].lineNum

	keyField, hasMergeKey := c.mergeKey[name]
	if !hasMergeKey {
		if err := c.set.WriteMatched(name, values); err != nil {
			return nil, fmt.Errorf("write matched row (rule %q, line %d): %w", name, startLine, err)
		}
		c.counts[name]++
		return nil, nil
	}

	sortValue, isTime := values[keyField].(time.Time)
	if !isTime {
		// Defensively unreachable: parse.Convert and rules.Validate
		// guarantee a timestamp-typed field always yields a time.Time. If
		// this ever did fire, degrade to skipping just this one row rather
		// than aborting the rest of the file.
		c.logger.Error("merge key value is not a timestamp, skipping row", "rule", name, "field", keyField, "file", c.inputPath, "line", startLine)
		return nil, nil
	}
	c.counts[name]++
	return &candidate{cursor: c, name: name, values: values, sortValue: sortValue, lineNum: startLine}, nil
}

// advance reads forward from where it last stopped until it finds a row
// eligible for merging, writing every ineligible row it passes along the
// way (unmatched lines to the shared sidecar, matched-but-no-merge-key
// rows straight to their rule's writer). A rule with Continuation
// configured accumulates matching lines into an open entry (see
// fileCursor.open) instead of finalizing on the first line; the entry is
// finalized once a non-continuation line, a fresh rule match, or EOF ends
// it. ok is false once the file is exhausted, at which point every one of
// its rows has been written or returned as a candidate — there is nothing
// left to do with this cursor but close() it.
func (c *fileCursor) advance() (*candidate, bool, error) {
	for {
		line, hasLine, err := c.nextLine()
		if err != nil {
			return nil, false, err
		}
		if !hasLine {
			if c.open == nil {
				return nil, false, nil
			}
			entry := c.open
			c.open = nil
			cand, err := c.finalizeEntry(entry)
			if err != nil {
				return nil, false, err
			}
			return cand, cand != nil, nil
		}

		if c.open != nil {
			if raw, matched := matchContinuation(c.open.rule, line.text); matched {
				appendContinuation(c.open, raw)
				c.open.rawLines = append(c.open.rawLines, line)
				continue
			}

			entry := c.open
			c.open = nil
			c.pending = &line
			cand, err := c.finalizeEntry(entry)
			if err != nil {
				return nil, false, err
			}
			if cand != nil {
				return cand, true, nil
			}
			continue
		}

		rule, raw, matched := parse.MatchRaw(c.cfg.Rules, line.text)
		if !matched {
			c.logger.Debug("line did not match any rule", "file", c.inputPath, "line", line.lineNum)
			if err := c.writeUnmatchedLine(line); err != nil {
				return nil, false, err
			}
			continue
		}

		if rule.ContinuationRegexp != nil {
			c.open = &openEntry{rule: rule, raw: raw, rawLines: []scannedLine{line}}
			continue
		}

		cand, err := c.finalizeEntry(&openEntry{rule: rule, raw: raw, rawLines: []scannedLine{line}})
		if err != nil {
			return nil, false, err
		}
		if cand != nil {
			return cand, true, nil
		}
	}
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
