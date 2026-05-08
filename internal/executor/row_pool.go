package executor

import "sync"

// M0068-0004: cross-query Row pool.
//
// Rationale: pre-M0068, every operator's Open() / clone path called
// `make(Row, width)`. For TPC-H Q5 SF=1 that's millions of slice
// allocations per query (6 M lineitem × 1 row each). With GOGC=off
// (M0066-0001), allocations don't trigger GC immediately but they
// still add scan-time work and grow the live heap.
//
// A `sync.Pool` keyed by row width recycles slices across queries.
// TPC-H rows are well-bounded (lineitem 16, orders 9, supplier 7,
// joined-row widths up to ~50), so a fixed-size array of pools
// covers nearly all allocations without runtime keying overhead.
//
// Width 0 is a degenerate case (empty row); we still pool it so
// `cloneRow(empty)` doesn't allocate.
//
// See docs/design/0068-0004-row-slot-pool.md.

const maxPooledRowWidth = 64

var rowPool [maxPooledRowWidth + 1]sync.Pool

func init() {
	for w := 0; w <= maxPooledRowWidth; w++ {
		width := w
		rowPool[w].New = func() any { return make(Row, width) }
	}
}

// acquireRow returns a Row of length `width`. Backing is recycled
// from the per-width sync.Pool when width is in range; otherwise
// a fresh `make(Row, width)` is returned (uncommon — TPC-H widths
// stay well below 64).
//
// Returned slices have all-zero Datums (callers assigning to
// individual columns don't observe stale data). The slice's cap
// equals its len.
func acquireRow(width int) Row {
	if width < 0 || width > maxPooledRowWidth {
		return make(Row, width)
	}
	r := rowPool[width].Get().(Row)
	// Zero out in case a Put left non-zero (defensive — releaseRow
	// always zeros, but a future caller might Put without zero).
	for i := range r {
		r[i] = Datum{}
	}
	return r
}

// releaseRow returns a row to its width-keyed pool. The slice is
// zeroed first to avoid retaining string / big.Int pointers in
// pool-resident memory (which would extend their live-heap
// lifetime past the producer query's end).
//
// No-op when the row is nil, length 0, or wider than the pool's
// capped size.
func releaseRow(r Row) {
	if r == nil {
		return
	}
	width := cap(r)
	if width <= 0 || width > maxPooledRowWidth {
		return
	}
	for i := range r {
		r[i] = Datum{}
	}
	rowPool[width].Put(r[:width])
}
