package executor

// EXPLAIN ANALYZE instrumentation wrapper (M0018-0003).
// instrumentedOp wraps an inner Operator and tracks rows
// emitted, loops (Open call count), and wall-clock timing.
//
// See docs/design/0018-0003-explain-analyze-instrumentation.md.

import (
	"time"

	"github.com/goopg/goopg/internal/planner"
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
}

// instrumentedOp wraps inner so the EXPLAIN ANALYZE renderer can
// surface actual-rows / loops / timing per Node.
type instrumentedOp struct {
	inner Operator
	plan  planner.Node
	stats *nodeStats
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
	if o.stats.timing {
		now := time.Now()
		o.stats.openTime = now
		o.stats.rowDeltaT = now
		o.stats.gotFirst = false
	}
	return o.inner.Open(ctx)
}

// Next records the per-row delta into the running total and
// (on the first non-EOF row of this Open cycle) records the
// startup time.
func (o *instrumentedOp) Next() (Row, error) {
	row, err := o.inner.Next()
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
	return row, nil
}

// Close finalises the duration. RowsAffected (DML) is forwarded
// via type-assertion so `INSERT N` still reaches the wire layer
// when the operator is wrapped.
func (o *instrumentedOp) Close() error {
	return o.inner.Close()
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
	return &instrumentedOp{inner: op, plan: plan, stats: stats}
}
