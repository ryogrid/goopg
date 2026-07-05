package executor

// EXPLAIN ANALYZE instrumentation wrapper (M0018-0003).
// instrumentedOp wraps an inner Operator and tracks rows
// emitted, loops (Open call count), and wall-clock timing.
//
// See docs/design/0018-0003-explain-analyze-instrumentation.md.

import (
	"time"

	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// nodeStats holds the per-Node runtime counters EXPLAIN ANALYZE
// renders. The map below is keyed by planner.Node identity so
// the EXPLAIN renderer can look up stats while walking the
// (already-built) plan tree.
type nodeStats struct {
	rowsOut    int64
	loops      int64
	startupNs  int64 // first-row latency in nanoseconds (per current Open)
	totalNs    int64 // accumulated time across all Next calls (per current Open)
	timing     bool  // tracks whether time.Now() snapshots are taken
	gotFirst   bool  // first Next-since-Open returned a row
	openTime   time.Time
	rowDeltaT  time.Time
	// heapFetches counts the heap tuples an IndexOnlyScan had to fetch
	// because the page was not ALL_VISIBLE (design 0118-0102). Mirrors
	// upstream's IndexOnlyScanState.ioss_HeapFetches, surfaced by EXPLAIN
	// ANALYZE as the "Heap Fetches" line / JSON key. The IOS operator
	// increments it directly via a pointer handed to it in maybeInstrument.
	heapFetches int64

	// bufHit / bufRead / bufDirtied / bufWritten are the cumulative
	// shared-buffer hit/read/dirtied/written counts EXPLAIN (ANALYZE,
	// BUFFERS) attributes to this node, inclusive of its children (same
	// nested-stopwatch semantics as totalNs above — a parent's Next() call
	// fully executes its child's Next() calls before returning, so the
	// pool-counter delta since the last checkpoint naturally rolls up
	// whatever the subtree touched). The bufBase* fields hold the
	// last-seen storage.Pool snapshot for delta computation; bufSeeded
	// guards the very first checkpoint on Open.
	bufHit, bufRead, bufDirtied, bufWritten                 int64
	bufBaseHit, bufBaseRead, bufBaseDirtied, bufBaseWritten int64
	bufSeeded                                               bool
}

// instrumentedOp wraps inner so the EXPLAIN ANALYZE renderer can
// surface actual-rows / loops / timing per Node.
type instrumentedOp struct {
	inner Operator
	plan  planner.Node
	stats *nodeStats
	pool  *storage.Pool // captured from ctx.Pool in Open; nil-safe (BUFFERS off / no ctx.Pool)
}

// underlying lets `setChildBorrow` (M0054-0005a-followup) reach
// the wrapped operator so EXPLAIN ANALYZE wiring does not block
// the borrow contract.
func (o *instrumentedOp) underlying() Operator { return o.inner }

// Schema delegates to the wrapped operator. EXPLAIN ANALYZE never
// changes the executed plan's output schema — the wrap is a pure
// counter sidecar.
func (o *instrumentedOp) Schema() planner.Schema { return o.inner.Schema() }

// Open increments the loop counter, snapshots the start time
// (when timing is enabled), and resets per-Open stats so a
// rescan produces fresh numbers.
func (o *instrumentedOp) Open(ctx *Context) error {
	o.stats.loops++
	if ctx != nil && ctx.Pool != nil {
		o.pool = ctx.Pool
		if !o.stats.bufSeeded {
			o.stats.bufBaseHit, o.stats.bufBaseRead, o.stats.bufBaseDirtied, o.stats.bufBaseWritten = o.pool.BufferCounters()
			o.stats.bufSeeded = true
		}
	}
	if o.stats.timing {
		now := time.Now()
		o.stats.openTime = now
		o.stats.rowDeltaT = now
		o.stats.gotFirst = false
	}
	return o.inner.Open(ctx)
}

// accountBuffers rolls the storage.Pool's global hit/read/dirtied/written
// counters forward into this node's running total, attributing everything
// touched since the last checkpoint (by this node or any descendant called
// from within it) to this node — see the bufHit/bufRead doc comment on
// nodeStats.
func (o *instrumentedOp) accountBuffers() {
	if o.pool == nil {
		return
	}
	hit, read, dirtied, written := o.pool.BufferCounters()
	o.stats.bufHit += hit - o.stats.bufBaseHit
	o.stats.bufRead += read - o.stats.bufBaseRead
	o.stats.bufDirtied += dirtied - o.stats.bufBaseDirtied
	o.stats.bufWritten += written - o.stats.bufBaseWritten
	o.stats.bufBaseHit, o.stats.bufBaseRead = hit, read
	o.stats.bufBaseDirtied, o.stats.bufBaseWritten = dirtied, written
}

// Next records the per-row delta into the running total and
// (on the first non-EOF row of this Open cycle) records the
// startup time.
func (o *instrumentedOp) Next() (TupleSlot, error) {
	slot, err := o.inner.Next()
	o.accountBuffers()
	if err == EOF {
		return nil, EOF
	}
	if err != nil {
		return nil, err
	}
	o.stats.rowsOut++
	if o.stats.timing {
		now := time.Now()
		if !o.stats.gotFirst {
			o.stats.startupNs = now.Sub(o.stats.openTime).Nanoseconds()
			o.stats.gotFirst = true
		}
		o.stats.totalNs += now.Sub(o.stats.rowDeltaT).Nanoseconds()
		o.stats.rowDeltaT = now
	}
	return slot, nil
}

// Close finalises the duration. RowsAffected (DML) is forwarded
// via type-assertion so `INSERT N` still reaches the wire layer
// when the operator is wrapped.
func (o *instrumentedOp) Close() error {
	err := o.inner.Close()
	o.accountBuffers()
	return err
}

// RowsAffected delegates so wrapped DML operators continue to
// report their RowsAffected through the wire layer's
// CommandComplete tag.
func (o *instrumentedOp) RowsAffected() int64 {
	if rc, ok := o.inner.(RowCounter); ok {
		return rc.RowsAffected()
	}
	return 0
}

// heapFetchCounter is implemented by operators that fetch heap tuples
// behind an index-only scan and want EXPLAIN ANALYZE to report a "Heap
// Fetches" count (design 0118-0102). maybeInstrument hands the operator
// a pointer to its node's stats.heapFetches so it can ++ on each fetch.
type heapFetchCounter interface {
	setHeapFetchCounter(*int64)
}

// nodeStatsTable maps a planner.Node back to its instrumentation
// counters. The EXPLAIN ANALYZE renderer walks the plan tree the
// same way the static path does and looks up stats in this map
// for each visited node.
type nodeStatsTable map[planner.Node]*nodeStats

// instrumentScope wires the package-local instrumentation state
// the Build dispatch consults. When non-nil, every successful
// Build returns an *instrumentedOp wrapping its result so child-
// site Build calls inside the dispatch arms get their wraps too
// (the wrap propagates recursively through the natural Build
// tree). nil disables instrumentation — every existing caller
// path is byte-for-byte unchanged.
//
// This package global mirrors the existing planParent / outerScope
// pattern. Save/restore happens in withInstrumentation so an
// EXPLAIN ANALYZE call doesn't leak state across queries.
var instrumentScope *instrumenter

type instrumenter struct {
	timing bool
	table  nodeStatsTable
}

// withInstrumentation runs fn under a fresh instrumenter. Returns
// the populated stats table. Save/restore is defer-driven so a
// panic in fn doesn't leak the global.
func withInstrumentation(timing bool, fn func() (Operator, error)) (Operator, nodeStatsTable, error) {
	prev := instrumentScope
	cur := &instrumenter{timing: timing, table: make(nodeStatsTable)}
	instrumentScope = cur
	defer func() { instrumentScope = prev }()
	op, err := fn()
	if err != nil {
		return nil, nil, err
	}
	return op, cur.table, nil
}

// maybeInstrument is called by Build right before each switch arm
// returns. When instrumentScope is non-nil it allocates a stats
// record keyed on plan and wraps op; when nil it returns op
// unchanged (keeping every existing path unchanged byte-for-byte).
func maybeInstrument(plan planner.Node, op Operator) Operator {
	if instrumentScope == nil || op == nil {
		return op
	}
	stats := &nodeStats{timing: instrumentScope.timing}
	instrumentScope.table[plan] = stats
	// Hand an IndexOnlyScan (or any heap-fetch-counting operator) a pointer
	// to its stats.heapFetches so it can tally heap fetches during Open;
	// the EXPLAIN ANALYZE renderer reads them back via the table. (0118-0102)
	if hf, ok := op.(heapFetchCounter); ok {
		hf.setHeapFetchCounter(&stats.heapFetches)
	}
	return &instrumentedOp{inner: op, plan: plan, stats: stats}
}
