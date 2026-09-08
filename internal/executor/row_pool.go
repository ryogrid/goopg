package executor

// M0068-0004 introduced a cross-query Row pool keyed by width. Part 3 of the
// row-decode work REMOVED it, because it was measured to be a net cost.
// See docs/design/not_ralph/optimize-row-decode/DESIGN-3.md.
//
// WHY IT WAS REMOVED, so it is not reintroduced by someone reading the
// original rationale and finding it persuasive (it is persuasive; it was
// simply not true of this workload).
//
// The pool's HIT RATE WAS 0.1%. `sync.Pool.Get`'s own profile breakdown put
// the fast path -- `popHead`, an actual hit -- at 0.09% of Get on TPC-H and
// 0.40% on TPC-DS, while `New` (i.e. allocate a fresh row) was 68-76% and
// `getSlow` a further 18-19%. Direct counters on a real join query agreed:
// 26,720 acquires, 26,704 fresh allocations, 20 releases.
//
// It missed for a STRUCTURAL reason, not a tunable one. acquireRow runs once
// per row (from cloneRowOwned, 95% of it under bitmapHeapScanOp.fetchExact on
// TPC-DS and 82% under seqScanOp.Next on TPC-H), while releaseRow runs at
// about a dozen operator Close sites. Rows flow downstream to consumers with
// no ownership contract and are garbage-collected. The pool was drained by
// design and refilled by nobody -- the two call counts differ by three orders
// of magnitude, so no sizing or width change could have fixed it.
//
// To deliver that 0.1% it charged every row for: getSlow's cross-P steal and
// victim-cache scan; poolChain.popTail; pin/procUnpin; runtime.convTslice
// (sync.Pool stores `any`, so every Put BOXES the slice header -- an
// allocation caused by the allocation-avoidance mechanism, 2.0-3.8% cum); and
// a "defensive" zeroing loop that re-zeroed memory `make` had already zeroed.
//
// Measured overhead beyond plain allocation: ~4.2% of TPC-H CPU and ~4.4% of
// TPC-DS CPU. acquireRow cum was 8.4% and 16.6% respectively.
//
// If a row pool is ever reconsidered, the prerequisite is an OWNERSHIP
// CONTRACT for rows crossing into joins, aggregates and sorts -- i.e. knowing
// when a consumer is done with a row. That is a lifetime redesign. Without it
// any pool here degenerates to the same 0.1%.

// acquireRow returns a Row of length `width` with all-zero Datums and
// cap == len. `make` provides exactly those three properties directly; the
// zeroing loop the pooled version needed is what `make` already does, and
// doing it again was measurable cost.
func acquireRow(width int) Row {
	if width < 0 {
		return nil
	}
	return make(Row, width)
}

// releaseRow is retained as an explicit no-op rather than deleted at its ~12
// call sites, for two reasons: the diff stays reviewable, and a future
// ownership design (see above) has an obvious hook already threaded through
// the operators that would need it.
//
// It deliberately does NOT zero. The pooled version zeroed to avoid retaining
// string / big.Int pointers in pool-resident memory; with no pool there is no
// pool-resident memory, and zeroing a row nobody will reuse is pure cost.
func releaseRow(r Row) {
	_ = r
}
