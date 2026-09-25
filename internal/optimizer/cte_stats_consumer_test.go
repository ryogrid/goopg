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

	"github.com/goopg/goopg/internal/catalog"
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

// statsBearingAggEntry is the group-key fixture with real column statistics.
// The no-stats fixture cannot exercise the group-combo rule at all, because
// the rule requires a KNOWN input ndistinct — `yr` has 5 distinct values, well
// under any group count, so the input bound decides.
func statsBearingAggEntry() *plannedCTE {
	tbl := &catalog.Table{
		Name: "gk_entry_src",
		Columns: []catalog.Column{
			{Name: "yr", Type: catalog.Type{Name: "int4"}},
			{Name: "v", Type: catalog.Type{Name: "int4"}},
		},
		Stats: &catalog.TableStats{
			RowCount: 100000,
			Columns:  []catalog.ColumnStats{{NDistinct: 5}, {NDistinct: 1000}},
		},
	}
	scan := &SeqScan{Table: tbl, EstRelRows: 100000, schema: tableSchema(tbl)}
	agg := &Aggregate{
		Child:      scan,
		GroupExprs: []Expr{&ColumnRef{Index: 0, Name: "yr"}},
		Aggs:       []AggregateCall{{Name: "count"}},
		schema:     Schema{{Name: "yr"}, {Name: "c"}},
	}
	return &plannedCTE{name: "gk", body: agg, schema: agg.Output()}
}

// TestCTESynthNDistinctGroupKeyIsNumbered is the slice-1 boundary pin,
// inverted deliberately now that slice 2 landed the group-combo rule. Slice 1
// asserted group keys stayed unknown and said in so many words that a later
// slice must update this test rather than delete it; this is that update.
// Group-key columns are now numbered and reach the consumer.
func TestCTESynthNDistinctGroupKeyIsNumbered(t *testing.T) {
	entry := statsBearingAggEntry()
	st := entry.outputStats()
	if k := st.cols[0].kind; k != cteColGroupKey {
		t.Fatalf("col 0 kind = %v, want groupkey", k)
	}
	if st.cols[0].ndistinct < 0 {
		t.Fatal("group key must be numbered by the group-combo rule")
	}
	got := columnNDistinctForChild(0, cteScanOver(entry))
	if got != saturateRowEst(st.cols[0].ndistinct) {
		t.Fatalf("group-key ndistinct = %d, want %d — the rule is not reaching the consumer",
			got, saturateRowEst(st.cols[0].ndistinct))
	}
}

// TestGroupKeyNDistinctTakesTheTighterBound exercises the half the
// no-stats fixture cannot: when the input column's own ndistinct is KNOWN and
// smaller than the group count, that is the bound. This is the half that pays
// — a low-cardinality key (TPC-DS `d_year`) inside a CTE with many groups
// would otherwise be priced at the group count.
func TestGroupKeyNDistinctTakesTheTighterBound(t *testing.T) {
	tbl := &catalog.Table{
		Name: "gk_src",
		Columns: []catalog.Column{
			{Name: "yr", Type: catalog.Type{Name: "int4"}},
			{Name: "cust", Type: catalog.Type{Name: "int4"}},
		},
		Stats: &catalog.TableStats{
			RowCount: 100000,
			Columns:  []catalog.ColumnStats{{NDistinct: 5}, {NDistinct: 50000}},
		},
	}
	scan := &SeqScan{Table: tbl, EstRelRows: 100000, schema: tableSchema(tbl)}

	// Input ndistinct 5 is far below any plausible group count: the input
	// bound must win.
	if got := groupKeyNDistinct(&ColumnRef{Index: 0, Name: "yr"}, scan, 40000); got != 5 {
		t.Errorf("low-cardinality key = %v, want 5 (input bound)", got)
	}
	// Input ndistinct 50000 exceeds the group count: grouping cannot emit
	// more distinct values than it emits rows, so the group count wins.
	if got := groupKeyNDistinct(&ColumnRef{Index: 1, Name: "cust"}, scan, 40000); got != 40000 {
		t.Errorf("high-cardinality key = %v, want 40000 (group-count bound)", got)
	}
	// A computed grouping expression has no readable input distinctness, so
	// the rule declines rather than falling back to the group count.
	computed := &BinaryOp{Op: parser.OpAdd, Left: &ColumnRef{Index: 0}, Right: &IntegerConst{Value: 1}}
	if got := groupKeyNDistinct(computed, scan, 40000); got != -1 {
		t.Errorf("computed group expr = %v, want -1 (unknown)", got)
	}
	// And an UNKNOWN input ndistinct must also decline. This is the case that
	// regressed TPC-DS Q59 (43 rows -> 1, Hash Join -> Nested Loop) when an
	// earlier version of the rule fell back to the group count: sound as a
	// bound, wrong as an estimate for one key of a multi-key grouping.
	nostats := &SeqScan{Table: &catalog.Table{Name: "gk_nostats",
		Columns: []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}}},
		EstRelRows: 100000}
	if got := groupKeyNDistinct(&ColumnRef{Index: 0, Name: "a"}, nostats, 40000); got != -1 {
		t.Errorf("unknown input ndistinct = %v, want -1 (unknown) — the group count is not a usable fallback", got)
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
