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

	"github.com/wtnb75/logidx/internal/decompress"
	"github.com/wtnb75/logidx/internal/parse"
	"github.com/wtnb75/logidx/internal/rules"
	"github.com/wtnb75/logidx/internal/writer"
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
// failure can retry just the first line against the next candidate rule
// and push the rest back onto the pending queue to be reprocessed
// independently (see fileCursor.finalizeEntry).
type openEntry struct {
	rule      *rules.Rule
	ruleIndex int // index within cfg.Rules that rule matched at - see finalizeEntry
	raw       map[string]string
	rawLines  []scannedLine
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
// non-continuation line, a new rule match, or EOF closes it. pending is a
// queue of lines read back for reprocessing, drained from the front by
// nextLine() (see pushPending) in original file order before the next real
// scanner read: the line that closed an entry so it can be rematched from
// scratch as a fresh candidate for a new entry, and a failed entry's
// rawLines[1:] once its first line is retried against another rule (see
// finalizeEntry).
//
// logger must be non-nil: advance() logs through it unconditionally.
type fileCursor struct {
	inputPath string
	fileIndex int
	file      *os.File // nil when reading os.Stdin
	// decompressCloser releases the decompressor wrapping file's contents,
	// if inputPath's extension named a supported compression format (see
	// decompress.Wrap). nil for uncompressed input, stdin, or a format
	// (like bzip2) whose decoder holds nothing beyond the underlying
	// reader.
	decompressCloser io.Closer
	scanner          *bufio.Scanner
	lineNum          int

	cfg      *rules.Config
	mergeKey map[string]string
	set      *writer.Set
	logger   *slog.Logger
	now      time.Time
	// assumedYear, if non-zero, is the CLI-supplied --assume-year value
	// used to resolve missing-year timestamps in place of the now-based
	// nearest-year inference (see parse.parseTimestampLayout).
	assumedYear int
	// patternMaskRules is cfg.Mask's Type == "pattern" subset (see
	// parse.SplitMaskRules), precomputed once here so writeUnmatchedLine
	// doesn't re-filter cfg.Mask on every unmatched line.
	patternMaskRules []rules.MaskRule

	counts    map[string]int
	unmatched int
	ignored   int

	open    *openEntry
	pending []scannedLine
}

// newFileCursor opens inputPath (or os.Stdin if inputPath is "-") and
// returns a cursor ready for advance(). fileIndex is inputPath's position
// among the inputPaths mergeFiles was given, used only to break ties when
// two candidates from different files have the exact same sortValue.
func newFileCursor(inputPath string, fileIndex int, cfg *rules.Config, mergeKey map[string]string, set *writer.Set, logger *slog.Logger, now time.Time, assumedYear int) (*fileCursor, error) {
	var f *os.File
	in := io.Reader(os.Stdin)
	var decompressCloser io.Closer
	if inputPath != "-" {
		var err error
		f, err = os.Open(inputPath)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		in, decompressCloser, err = decompress.Wrap(inputPath, f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("open input: %w", err)
		}
	}

	_, patternMaskRules := parse.SplitMaskRules(cfg.Mask)

	return &fileCursor{
		inputPath:        inputPath,
		fileIndex:        fileIndex,
		file:             f,
		decompressCloser: decompressCloser,
		scanner:          bufio.NewScanner(in),
		cfg:              cfg,
		mergeKey:         mergeKey,
		set:              set,
		logger:           logger,
		now:              now,
		assumedYear:      assumedYear,
		patternMaskRules: patternMaskRules,
		counts:           map[string]int{},
	}, nil
}

// nextLine returns the next physical line to process: a previously pushed
// back line, if any (see fileCursor.pending / pushPending), otherwise the
// next line from the underlying scanner that isn't skipped by cfg.Ignore
// (see rules.IgnoreConfig.Reason) - a skipped line is written to
// unmatched.txt with an "ignored:<condition>" reason and never returned to
// the caller, so continuation/pattern-matching logic never sees it. ok is
// false at EOF. c.lineNum is incremented for every physical line read,
// including skipped ones, so source_line metadata (see rules.Field.Meta)
// stays a correct physical line count.
func (c *fileCursor) nextLine() (line scannedLine, ok bool, err error) {
	if len(c.pending) > 0 {
		line = c.pending[0]
		c.pending = c.pending[1:]
		return line, true, nil
	}
	for {
		if !c.scanner.Scan() {
			if err := c.scanner.Err(); err != nil {
				return scannedLine{}, false, fmt.Errorf("read input: %w", err)
			}
			return scannedLine{}, false, nil
		}
		c.lineNum++
		text := c.scanner.Text()
		if reason := c.cfg.Ignore.Reason(text); reason != "" {
			line := scannedLine{text: text, lineNum: c.lineNum}
			if err := c.writeUnmatchedLine(line, "ignored:"+reason); err != nil {
				return scannedLine{}, false, err
			}
			c.ignored++
			continue
		}
		return scannedLine{text: text, lineNum: c.lineNum}, true, nil
	}
}

// pushPending prepends lines to the front of the pending queue, preserving
// their relative order, so nextLine() replays them - in original file
// order - before anything already queued (e.g. the line that closed a
// continuation entry, pushed there before finalizeEntry runs - see
// advance()).
func (c *fileCursor) pushPending(lines []scannedLine) {
	if len(lines) == 0 {
		return
	}
	c.pending = append(append([]scannedLine{}, lines...), c.pending...)
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
// sidecar, tagged with reason ("unmatched" for a line that matched no
// rule, "ignored:<condition>" for one dropped by rules.IgnoreConfig before
// pattern matching even ran - see nextLine). It does not update any
// counter itself; callers bump whichever of c.unmatched/c.ignored applies.
func (c *fileCursor) writeUnmatchedLine(line scannedLine, reason string) error {
	text := parse.ApplyPatternMask(line.text, c.patternMaskRules)
	if err := c.set.WriteUnmatched(c.inputPath, line.lineNum, reason, text); err != nil {
		return fmt.Errorf("write unmatched line %d: %w", line.lineNum, err)
	}
	return nil
}

// writeConverted disposes of a rule's already-converted values: written
// immediately if name's rule has no merge key (see mergeKeyField), or
// returned as a candidate for the caller to hand to mergeFiles otherwise.
// The returned error is only non-nil for a genuine write/I-O failure.
func (c *fileCursor) writeConverted(name string, values map[string]any, startLine int) (*candidate, error) {
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

// tryCandidates searches cfg.Rules starting at startIndex for a rule that
// disposes of line: a non-continuation rule that matches and converts
// successfully, or a continuation rule whose pattern matches, which
// becomes the new open entry (c.open) instead of an immediate result. If
// every candidate from startIndex onward either doesn't match or fails
// conversion, line is written to unmatched.txt. startIndex is 0 for a
// fresh line, or one past the index of a rule whose entry already failed
// at finalize time (see finalizeEntry), so a retry never reconsiders a
// rule that already lost for this line.
func (c *fileCursor) tryCandidates(line scannedLine, startIndex int) (*candidate, error) {
	rule, ruleIndex, raw, values, attempts, matched := parse.MatchAndConvertFrom(c.cfg.Rules, startIndex, line.text, parse.SourceMeta{File: c.inputPath, Line: line.lineNum}, c.now, c.assumedYear, c.cfg.Mask)
	for _, a := range attempts {
		c.logger.Debug("candidate rule matched but failed conversion", "file", c.inputPath, "line", line.lineNum, "rule", a.RuleName, "error", a.Err)
	}
	if !matched {
		c.logger.Debug("line did not match any rule", "file", c.inputPath, "line", line.lineNum)
		if err := c.writeUnmatchedLine(line, "unmatched"); err != nil {
			return nil, err
		}
		c.unmatched++
		return nil, nil
	}

	if values == nil {
		c.open = &openEntry{rule: rule, ruleIndex: ruleIndex, raw: raw, rawLines: []scannedLine{line}}
		return nil, nil
	}

	return c.writeConverted(rule.Name, values, line.lineNum)
}

// finalizeEntry converts entry's accumulated raw values. A type-conversion
// failure no longer immediately splits the entry into unmatched.txt
// records: every line after the first is put back at the front of the
// pending queue (see pushPending) so it's reprocessed as an independent
// fresh line, and the first line is retried against whichever candidate
// rules after entry.rule haven't been tried yet (see tryCandidates) - the
// same declaration-order fallback single-line rules already get. Only
// once every remaining candidate for the first line also fails does that
// first line alone end up in unmatched.txt.
func (c *fileCursor) finalizeEntry(entry *openEntry) (*candidate, error) {
	values, convErr := parse.Convert(*entry.rule, entry.raw, parse.SourceMeta{File: c.inputPath, Line: entry.rawLines[0].lineNum}, c.now, c.assumedYear, c.cfg.Mask)
	if convErr == nil {
		return c.writeConverted(entry.rule.Name, values, entry.rawLines[0].lineNum)
	}

	c.logger.Debug("entry failed type conversion, trying next candidate rule", "file", c.inputPath, "rule", entry.rule.Name, "start_line", entry.rawLines[0].lineNum, "error", convErr)
	c.pushPending(entry.rawLines[1:])
	return c.tryCandidates(entry.rawLines[0], entry.ruleIndex+1)
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
			if cand != nil {
				return cand, true, nil
			}
			continue // finalizeEntry's retry may have reopened c.open
		}

		if c.open != nil {
			if raw, matched := matchContinuation(c.open.rule, line.text); matched {
				appendContinuation(c.open, raw)
				c.open.rawLines = append(c.open.rawLines, line)
				continue
			}

			entry := c.open
			c.open = nil
			c.pushPending([]scannedLine{line})
			cand, err := c.finalizeEntry(entry)
			if err != nil {
				return nil, false, err
			}
			if cand != nil {
				return cand, true, nil
			}
			continue
		}

		cand, err := c.tryCandidates(line, 0)
		if err != nil {
			return nil, false, err
		}
		if cand != nil {
			return cand, true, nil
		}
	}
}

// close closes the underlying decompressor (if any) and file (if any -
// nothing to close for os.Stdin). Both are closed even if one errors, so a
// decompressor-close failure never leaks the underlying file descriptor.
func (c *fileCursor) close() error {
	var errs []error
	if c.decompressCloser != nil {
		if err := c.decompressCloser.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close decompressor: %w", err))
		}
	}
	if c.file != nil {
		if err := c.file.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close file: %w", err))
		}
	}
	return errors.Join(errs...)
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
func mergeFiles(inputPaths []string, cfg *rules.Config, set *writer.Set, logger *slog.Logger, now time.Time, assumedYear int) error {
	mergeKey := mergeKeyField(cfg.Rules)

	var errs []error
	h := candidateHeap{}

	for i, inputPath := range inputPaths {
		cursor, err := newFileCursor(inputPath, i, cfg, mergeKey, set, logger, now, assumedYear)
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
	args = append(args, "unmatched", c.unmatched, "ignored", c.ignored)
	logger.Info("file processed", args...)
}
