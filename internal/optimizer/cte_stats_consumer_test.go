package optimizer

// M0145-0009 slice 1 — the ndistinct consumer for synthesized CTE-output
// statistics.
//
// B-06 gap G1: `resolveBaseColumn` recurses through a `*CTEScan` into the
// body and finds no `*Aggregate`/`*SetOp` arm, so an aggregate or set-op CTE
// output resolves to nothing and every consumer falls to
// `defaultNumDistinct`. `cte_stats_synthesis.go` already derives the right
// number for those shapes; these pins cover the path that lets a consumer
// see it, and — just as importantly — the shapes where it must NOT fire.
//
// Note what this slice does and does not wire. The landed synthesis leaves
// GROUP KEY columns `unknown` (its own step 3), so the numbers that flow
// here are the agg-output bound (an aggregate output has at most one
// distinct value per group — B-06's gap G3) and the union-literal count.
// The group-combo registry (G2) is slice 2 and stays unwired.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// cteScanOver wraps an entry's body in a CTEScan bound to that entry, which
// is how a real consumer site sees it (planScanRangeVar stamps `cte`).
func cteScanOver(entry *plannedCTE) *CTEScan {
	return &CTEScan{Name: entry.name, Child: entry.body, schema: entry.schema, cte: entry}
}

// TestCTESynthNDistinctReachesConsumer is the point of the slice: the agg
// output's synthesized ndistinct arrives at columnNDistinctForChild, where
// before it returned 0 ("unknown") and every caller used a default.
func TestCTESynthNDistinctReachesConsumer(t *testing.T) {
	entry := synthAggEntry(t)
	scan := cteScanOver(entry)

	// Column 2 is the aggregate output — the one the synthesis numbers.
	got := columnNDistinctForChild(2, scan)
	if got <= 0 {
		t.Fatalf("agg-output ndistinct = %d, want a derived positive value — the consumer is not wired", got)
	}
	st := entry.outputStats()
	want := st.cols[2].ndistinct
	if st.rows > 0 && want > st.rows {
		want = st.rows
	}
	if got != saturateRowEst(want) {
		t.Fatalf("agg-output ndistinct = %d, want %d (synthesized, clamped to rows)", got, saturateRowEst(want))
	}
}

// TestCTESynthNDistinctGroupKeyStaysUnknown pins the boundary with slice 2:
// group-key columns are classified but NOT numbered by the landed synthesis,
// so the consumer must still report unknown for them rather than inventing a
// value. If a later slice lands the group-combo rule this test should be
// updated deliberately, not deleted silently.
func TestCTESynthNDistinctGroupKeyStaysUnknown(t *testing.T) {
	entry := synthAggEntry(t)
	if k := entry.outputStats().cols[0].kind; k != cteColGroupKey {
		t.Fatalf("col 0 kind = %v, want groupkey", k)
	}
	if got := columnNDistinctForChild(0, cteScanOver(entry)); got != 0 {
		t.Fatalf("group-key ndistinct = %d, want 0 (unknown) — the group-combo rule is slice 2", got)
	}
}

// TestCTESynthNDistinctIsFailClosed covers every way the walk must decline.
// The contract is that this path can only ever REPLACE a default with a
// derived number, never invent one, so each decline leaves the caller
// exactly where it was.
func TestCTESynthNDistinctIsFailClosed(t *testing.T) {
	entry := synthAggEntry(t)
	scan := cteScanOver(entry)

	if _, ok := cteSynthNDistinct(-1, scan); ok {
		t.Error("negative index must decline")
	}
	if _, ok := cteSynthNDistinct(99, scan); ok {
		t.Error("out-of-range index must decline")
	}
	// An unrecognised wrapper is not index-preserving as far as this walk
	// knows, so it must stop rather than pass the index through.
	if _, ok := cteSynthNDistinct(2, &Aggregate{Child: scan}); ok {
		t.Error("unrecognised wrapper must decline")
	}
	// A computed Project target is a new value; the synthesis says nothing
	// about its distinctness.
	computed := &Project{Child: scan, Targets: []Expr{
		&BinaryOp{Op: parser.OpAdd, Left: &ColumnRef{Index: 2}, Right: &IntegerConst{Value: 1}},
	}}
	if _, ok := cteSynthNDistinct(0, computed); ok {
		t.Error("computed Project target must decline")
	}
	// A CTEScan with no entry (scans built outside preplanWithClause) must
	// chain through nil without panicking.
	bare := &CTEScan{Name: "w", Child: entry.body, schema: entry.schema}
	if _, ok := cteSynthNDistinct(2, bare); ok {
		t.Error("CTEScan without a planned entry must decline")
	}
}

// TestCTESynthNDistinctCrossesIndexPreservingWrappers pins that the derived
// number survives the wrappers a real plan stacks over a CTE scan, and that a
// bare ColumnRef Project remaps the index rather than dropping it.
func TestCTESynthNDistinctCrossesIndexPreservingWrappers(t *testing.T) {
	entry := synthAggEntry(t)
	scan := cteScanOver(entry)
	direct, ok := cteSynthNDistinct(2, scan)
	if !ok {
		t.Fatal("direct lookup must succeed")
	}
	wrapped := &Filter{Child: &Sort{Child: scan}}
	if got, ok := cteSynthNDistinct(2, wrapped); !ok || got != direct {
		t.Fatalf("through Filter(Sort(...)) = %d ok=%v, want %d true", got, ok, direct)
	}
	// Project reordering: target 0 reads the CTE's column 2.
	proj := &Project{Child: scan, Targets: []Expr{&ColumnRef{Index: 2, Name: "c"}}}
	if got, ok := cteSynthNDistinct(0, proj); !ok || got != direct {
		t.Fatalf("through Project remap = %d ok=%v, want %d true", got, ok, direct)
	}
}

// TestCTEOutputStatsMemoizedOnce pins the design's identity constraint: both
// references of a multi-reference CTE must see the SAME synthesis, which the
// entry pointer gives by construction.
func TestCTEOutputStatsMemoizedOnce(t *testing.T) {
	entry := synthAggEntry(t)
	a := entry.outputStats()
	b := entry.outputStats()
	if a != b {
		t.Fatal("outputStats must memoize — two references would otherwise cost differently")
	}
	var nilEntry *plannedCTE
	if nilEntry.outputStats() != nil {
		t.Fatal("nil entry must yield nil, so callers can chain without a guard")
	}
}
