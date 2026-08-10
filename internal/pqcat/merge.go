package pqcat

import (
	"container/heap"
	"errors"
	"fmt"
	"io"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/schema"
)

// detectMergeKey returns the name of canonical's first timestamp-typed
// column in declared order, or "" if it has none - mirrors
// internal/convert.mergeKeyField's per-rule detection, applied to a single
// already-schema-validated column set instead of a rule's Fields.
func detectMergeKey(canonical *parquet.Schema) string {
	for _, field := range canonical.Fields() {
		if name, err := schema.TypeName(field); err == nil && name == "timestamp" {
			return field.Name()
		}
	}
	return ""
}

// rowCursor streams one input file's rows in constant-memory batches, for
// use by mergeRows' k-way merge - one rowCursor per file. Mirrors
// internal/convert/merge.go's fileCursor, but over already-typed Parquet
// rows (map[string]any) instead of unconverted log lines.
type rowCursor struct {
	reader *parquet.GenericReader[map[string]any]
	buf    []map[string]any
	pos    int
	n      int
	done   bool
}

func newRowCursor(pf *parquet.File) *rowCursor {
	return &rowCursor{
		reader: parquet.NewGenericReader[map[string]any](pf, pf.Schema()),
		buf:    make([]map[string]any, batchSize),
	}
}

// next returns the cursor's next row. ok is false once the underlying file
// is exhausted. parquet-go's GenericReader.Read can return a positive
// count together with io.EOF on its final call, so done only short-circuits
// the *next* call once every already-buffered row has been served.
func (c *rowCursor) next() (row map[string]any, ok bool, err error) {
	for c.pos >= c.n {
		if c.done {
			return nil, false, nil
		}
		for i := range c.buf {
			c.buf[i] = map[string]any{}
		}
		n, readErr := c.reader.Read(c.buf)
		c.n = n
		c.pos = 0
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil, false, readErr
			}
			c.done = true
		}
	}
	row = c.buf[c.pos]
	c.pos++
	return row, true, nil
}

func (c *rowCursor) close() error {
	return c.reader.Close()
}

// mergeCandidate is one row held back from writing because mergeRows is
// still comparing it against the other open cursors' current rows.
type mergeCandidate struct {
	cursor    *rowCursor
	row       map[string]any
	sortValue int64
	fileIndex int
}

// mergeHeap is a min-heap of mergeCandidates ordered by sortValue, with the
// originating file's position among mergeRows' srcPaths as a tiebreak, so
// two candidates with the exact same merge-key value still pop in a fixed,
// repeatable order across runs - mirrors internal/convert.candidateHeap.
type mergeHeap []*mergeCandidate

func (h mergeHeap) Len() int { return len(h) }
func (h mergeHeap) Less(i, j int) bool {
	if h[i].sortValue != h[j].sortValue {
		return h[i].sortValue < h[j].sortValue
	}
	return h[i].fileIndex < h[j].fileIndex
}
func (h mergeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x any)   { *h = append(*h, x.(*mergeCandidate)) }
func (h *mergeHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// mergeKeyValue extracts row's merge-key column as the microsecond-epoch
// int64 parquet-go's GenericReader returns for timestamp columns (not
// time.Time - see the package's design doc). ok is false if the column is
// missing or not an int64, which should be unreachable once schema.Equal
// has validated every file shares mergeKey as a timestamp column, but is
// still checked defensively rather than trusting that invariant blindly.
func mergeKeyValue(row map[string]any, mergeKey string) (int64, bool) {
	v, ok := row[mergeKey].(int64)
	return v, ok
}

// mergeRows performs a k-way streaming merge of every file in pf, in
// ascending order of each row's mergeKey column, writing the merged result
// to writer in batchSize batches. pf[i] must correspond to srcPaths[i].
// Rows with equal mergeKey values are ordered by their originating file's
// position in srcPaths, so output is stable and repeatable across runs.
// With a single input file the heap never holds more than one candidate,
// so this naturally degenerates to reading that file in order - mirrors
// internal/convert.mergeFiles' own same observation about its single-file
// case.
func mergeRows(pf []*parquet.File, srcPaths []string, mergeKey string, writer *parquet.GenericWriter[map[string]any]) (rows int64, err error) {
	cursors := make([]*rowCursor, len(pf))
	h := mergeHeap{}

	// closeRemaining closes every cursor still open after an error aborts
	// the merge. Both loops below nil out cursors[i] immediately after a
	// successful close on their normal (non-error) exhaustion path, so
	// closeRemaining's nil-check here only ever closes each cursor once,
	// even when it fires after some cursors were already drained normally.
	closeRemaining := func() {
		for _, c := range cursors {
			if c != nil {
				_ = c.close()
			}
		}
	}

	for i, f := range pf {
		cursors[i] = newRowCursor(f)
		row, ok, nextErr := cursors[i].next()
		if nextErr != nil {
			closeRemaining()
			return rows, fmt.Errorf("read %s: %w", srcPaths[i], nextErr)
		}
		if !ok {
			closeErr := cursors[i].close()
			cursors[i] = nil
			if closeErr != nil {
				closeRemaining()
				return rows, fmt.Errorf("close %s: %w", srcPaths[i], closeErr)
			}
			continue
		}
		sortValue, isInt := mergeKeyValue(row, mergeKey)
		if !isInt {
			closeRemaining()
			return rows, fmt.Errorf("read %s: merge key %q is not a timestamp column", srcPaths[i], mergeKey)
		}
		heap.Push(&h, &mergeCandidate{cursor: cursors[i], row: row, sortValue: sortValue, fileIndex: i})
	}

	batch := make([]map[string]any, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, writeErr := writer.Write(batch); writeErr != nil {
			return fmt.Errorf("write rows: %w", writeErr)
		}
		rows += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	for h.Len() > 0 {
		cand := heap.Pop(&h).(*mergeCandidate)
		batch = append(batch, cand.row)
		if len(batch) == batchSize {
			if flushErr := flush(); flushErr != nil {
				closeRemaining()
				return rows, flushErr
			}
		}

		next, ok, nextErr := cand.cursor.next()
		if nextErr != nil {
			closeRemaining()
			return rows, fmt.Errorf("read %s: %w", srcPaths[cand.fileIndex], nextErr)
		}
		if !ok {
			closeErr := cand.cursor.close()
			cursors[cand.fileIndex] = nil
			if closeErr != nil {
				closeRemaining()
				return rows, fmt.Errorf("close %s: %w", srcPaths[cand.fileIndex], closeErr)
			}
			continue
		}
		sortValue, isInt := mergeKeyValue(next, mergeKey)
		if !isInt {
			closeRemaining()
			return rows, fmt.Errorf("read %s: merge key %q is not a timestamp column", srcPaths[cand.fileIndex], mergeKey)
		}
		heap.Push(&h, &mergeCandidate{cursor: cand.cursor, row: next, sortValue: sortValue, fileIndex: cand.fileIndex})
	}

	if flushErr := flush(); flushErr != nil {
		return rows, flushErr
	}
	return rows, nil
}
