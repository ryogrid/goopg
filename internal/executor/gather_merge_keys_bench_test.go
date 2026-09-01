package executor

import (
	"container/heap"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// BenchmarkGatherMergeHeap measures the merge front (review/260831 EO1-13).
// The comparator used to call evalSortKeyValue on both sides of every heap
// comparison, so each output row re-evaluated the sort keys O(log sources)
// times; advance now evaluates them once per row and the heap compares plain
// Datums. The sources are pre-filled channels, so what is timed is the merge
// itself, not worker execution.
func BenchmarkGatherMergeHeap(b *testing.B) {
	const nsrc, nrows = 4, 2000
	o := &gatherMergeOp{
		ctx:  &Context{},
		keys: []optimizer.SortKey{{Expr: &optimizer.ColumnRef{Index: 0}}, {Expr: &optimizer.ColumnRef{Index: 1}}},
	}

	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		o.sources = nil
		for s := 0; s < nsrc; s++ {
			ch := make(chan rowBatch, 1)
			rows := make([]Row, 0, nrows)
			for i := 0; i < nrows; i++ {
				// Each source is individually ordered, as a worker's Sort
				// would have left it.
				rows = append(rows, Row{NewIntDatum(int64(i*nsrc + s)), NewIntDatum(int64(i))})
			}
			ch <- rowBatch{rows: rows}
			close(ch)
			o.sources = append(o.sources, &gmSource{ch: ch, live: true})
		}
		live := o.sources[:0]
		for _, src := range o.sources {
			ok, err := o.advance(src)
			if err != nil {
				b.Fatal(err)
			}
			if ok {
				live = append(live, src)
			}
		}
		o.sources = live
		o.h = &gmHeap{less: o.lessKeys}
		o.h.srcs = append(o.h.srcs[:0], o.sources...)
		heap.Init(o.h)
		b.StartTimer()

		// Drain the heap directly rather than through Next(): Next()'s
		// end-of-stream arm calls group.Wait(), which needs a live worker
		// group. Everything the comparator touches is exercised here.
		emitted := 0
		for o.h.Len() > 0 {
			src := o.h.srcs[0]
			_ = src.cur
			ok, err := o.advance(src)
			if err != nil {
				b.Fatal(err)
			}
			emitted++
			if ok {
				heap.Fix(o.h, 0)
			} else {
				heap.Pop(o.h)
			}
		}
		if emitted != nsrc*nrows {
			b.Fatalf("emitted %d rows, want %d", emitted, nsrc*nrows)
		}
	}
}
