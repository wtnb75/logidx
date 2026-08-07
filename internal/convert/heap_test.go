package convert

import (
	"container/heap"
	"slices"
	"testing"
	"time"
)

func TestCandidateHeap_PopsInAscendingTimestampOrder(t *testing.T) {
	t1 := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	t3 := t1.Add(2 * time.Minute)

	h := candidateHeap{}
	heap.Init(&h)
	heap.Push(&h, &candidate{name: "c", sortValue: t3, cursor: &fileCursor{fileIndex: 0}})
	heap.Push(&h, &candidate{name: "a", sortValue: t1, cursor: &fileCursor{fileIndex: 0}})
	heap.Push(&h, &candidate{name: "b", sortValue: t2, cursor: &fileCursor{fileIndex: 0}})

	var order []string
	for h.Len() > 0 {
		order = append(order, heap.Pop(&h).(*candidate).name)
	}

	want := []string{"a", "b", "c"}
	if !slices.Equal(order, want) {
		t.Errorf("pop order = %v, want %v", order, want)
	}
}

func TestCandidateHeap_TiesBreakByFileIndex(t *testing.T) {
	tie := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	h := candidateHeap{}
	heap.Init(&h)
	heap.Push(&h, &candidate{name: "from-file-1", sortValue: tie, cursor: &fileCursor{fileIndex: 1}})
	heap.Push(&h, &candidate{name: "from-file-0", sortValue: tie, cursor: &fileCursor{fileIndex: 0}})

	first := heap.Pop(&h).(*candidate)
	if first.name != "from-file-0" {
		t.Errorf("first popped = %q, want from-file-0 (lower fileIndex breaks the tie)", first.name)
	}
}
