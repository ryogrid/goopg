package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0134: TPC-DS Q64's cross_sales groups on `i_item_sk, …` over a join
// input ordered on catalog_sales.cs_item_sk, which the join equates with
// i_item_sk. PG's canonical (equivalence-class) pathkeys see that ordering as
// presorted on the group key and plan `Incremental Sort (Presorted Key:
// item.i_item_sk)`. Four goopg gaps stood between the two; one pin each.

// TestGroupQueryPathkeysDropFunctionallyDependentColumns: standard_qp_callback
// builds group_pathkeys from the processed group clause, after
// remove_useless_groupby_columns has dropped the columns a unique key
// determines. `GROUP BY c, b` with b the primary key groups — and so wants its
// input ordered — on b alone.
func TestGroupQueryPathkeysDropFunctionallyDependentColumns(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "int4"}, Ordinal: 1, NotNull: true},
		{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 2},
	}
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "t_pkey"}, tbl, []string{"b"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	ctx := newResolveContext([]rangeBinding{{table: tbl, alias: "t", offset: 0, sourceIdx: 0}},
		tableSchemaWithSource(tbl, 0), DefaultPlannerSettings())
	ctx.cat = c
	got := deriveQueryPathkeys(qpkParse(t, "SELECT c, b, count(*) FROM t GROUP BY c, b"), ctx)
	if !qpkEqual(got, []string{"b/ASC/NL"}) {
		t.Fatalf("group pathkeys = %v, want [b/ASC/NL] (c is determined by the key b)", qpkKeys(got))
	}
	// Without a key nothing is determined, and every group column stays.
	ctx.cat = catalog.NewInMemory()
	if got := deriveQueryPathkeys(qpkParse(t, "SELECT c, b, count(*) FROM t GROUP BY c, b"), ctx); len(got) != 2 {
		t.Fatalf("keyless group pathkeys = %v, want both columns", qpkKeys(got))
	}
}

// TestCountContainedInThroughEquivalenceClass: an ordering on one member of an
// equivalence class the rel enforces satisfies a requirement on another member
// (pathkeys_count_contained_in over canonical pathkeys); a rel that knows no
// classes compares syntactically.
func TestCountContainedInThroughEquivalenceClass(t *testing.T) {
	cs := &ColumnRef{Index: 0, Name: "cs_item_sk", Type: catalog.Type{Name: "int4"}}
	it := &ColumnRef{Index: 5, Name: "i_item_sk", Type: catalog.Type{Name: "int4"}}
	other := &ColumnRef{Index: 6, Name: "s_store_name", Type: catalog.Type{Name: "text"}}
	u := &pathkeyUsefulness{classMembers: []classMember{{cs, 0b01, 3}, {it, 0b10, 3}}}
	keys := []PathKey{{Expr: cs, SortAsc: true}}
	required := []PathKey{{Expr: it, SortAsc: true}, {Expr: other, SortAsc: true}}
	if contained, n := u.countContainedIn(keys, required); contained || n != 1 {
		t.Fatalf("class-aware prefix = (%v, %d), want (false, 1)", contained, n)
	}
	if _, n := (*pathkeyUsefulness)(nil).countContainedIn(keys, required); n != 0 {
		t.Fatalf("classless prefix = %d, want 0", n)
	}
	desc := []PathKey{{Expr: cs, SortAsc: false}}
	if _, n := u.countContainedIn(desc, required); n != 0 {
		t.Fatalf("a descending member satisfied an ascending requirement (n=%d)", n)
	}
}

// TestGEQOTourContextCarriesQueryLevelFacts: PG's GEQO tours share the one
// PlannerInfo, so every query-level fact the DP search reads must survive
// freshEvalCtx. Without queryPathkeys each tour truncated every ordering as
// useless (Q64's cs_item_sk order); without parallelModeOK it built no
// partial join paths.
func TestGEQOTourContextCarriesQueryLevelFacts(t *testing.T) {
	col := &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}
	cat := catalog.NewInMemory()
	s := &searchCtx{
		joinrels:           make([][]*RelOptInfo, 2),
		relMap:             map[RelSet]*RelOptInfo{},
		nrels:              1,
		queryPathkeys:      []PathKey{{Expr: col, SortAsc: true}},
		parallelModeOK:     true,
		cat:                cat,
		itemSpans:          []leafSpan{{lo: 0, hi: 1}},
		problemItems:       []joinlistRel{{}},
		semiDerivedRHS:     0b1,
		orClauseSelDivisor: map[Expr]float64{col: 0.5},
	}
	n := s.freshEvalCtx()
	if len(n.queryPathkeys) != 1 || !n.parallelModeOK || n.cat != cat || len(n.itemSpans) != 1 ||
		len(n.problemItems) != 1 || n.semiDerivedRHS != 0b1 || len(n.orClauseSelDivisor) != 1 {
		t.Fatalf("a GEQO tour context dropped a query-level fact: %+v", n)
	}
}

// TestIncrementalSortClampsTuplesBeforeGroupEstimate: cost_incremental_sort
// clamps input_tuples to 2 before estimating the presorted groups, so a
// one-row input counts two groups and starts after half its input's run — a
// startup below the input's total, which is what lets PG elect the
// Incremental Sort over a Sort on a near-tie.
func TestIncrementalSortClampsTuplesBeforeGroupEstimate(t *testing.T) {
	cp := defaultCostParams()
	keys := []PathKey{
		{Expr: &ColumnRef{Index: 0, Name: "k1", Type: catalog.Type{Name: "int4"}}, SortAsc: true},
		{Expr: &ColumnRef{Index: 1, Name: "k2", Type: catalog.Type{Name: "int4"}}, SortAsc: true},
	}
	rel := &RelOptInfo{Rows: 1}
	sub := &Path{Kind: PathPrebuilt, Rel: rel, Rows: 1, Cost: Cost{Startup: 100, Total: 200}}
	p := incrementalSortPathOver(rel, sub, nil, keys, 1, cp, -1)
	if p == nil {
		t.Fatal("no incremental sort path")
	}
	if !(p.Cost.Startup < sub.Cost.Total) {
		t.Fatalf("incremental sort startup %.2f is not below its input's total %.2f — the one-row input was counted as one group",
			p.Cost.Startup, sub.Cost.Total)
	}
}

// TestIncrementalSortRowsAreTheClampedInput pins M0146-0149:
// cost_incremental_sort sets path->rows = input_tuples AFTER clamping it to
// two (costsize.c), so an Incremental Sort over a one-row input carries two
// rows on the path, and its plan node (EstimateRows) reports the same. TPC-DS
// Q70's window subquery is therefore 2 rows in PG and is hashed in a semi
// join. A larger input keeps its own count.
func TestIncrementalSortRowsAreTheClampedInput(t *testing.T) {
	cp := defaultCostParams()
	keys := []PathKey{
		{Expr: &ColumnRef{Index: 0, Name: "k1", Type: catalog.Type{Name: "int4"}}, SortAsc: true},
		{Expr: &ColumnRef{Index: 1, Name: "k2", Type: catalog.Type{Name: "int4"}}, SortAsc: true},
	}
	for _, tc := range []struct{ in, want float64 }{{1, 2}, {0, 2}, {37, 37}} {
		rel := &RelOptInfo{Rows: tc.in}
		sub := &Path{Kind: PathPrebuilt, Rel: rel, Rows: tc.in, Cost: Cost{Startup: 100, Total: 200}}
		if p := incrementalSortPathOver(rel, sub, nil, keys, 1, cp, -1); p == nil || p.Rows != tc.want {
			t.Errorf("input %v rows: path rows = %v, want %v", tc.in, p.Rows, tc.want)
		}
	}
	scan := func(rows int64) Node {
		return &SeqScan{Table: statsTable("isr", rows), schema: cpjSchema("k", 2)}
	}
	node := &IncrementalSort{Child: scan(1), Keys: []SortKey{
		{Expr: &ColumnRef{Index: 0, Name: "k0", Type: catalog.Type{Name: "int4"}}},
		{Expr: &ColumnRef{Index: 1, Name: "k1", Type: catalog.Type{Name: "int4"}}},
	}, PresortedCount: 1}
	if base := EstimateRows(node.Child); base != 1 {
		t.Fatalf("fixture scan estimates %d rows, want 1", base)
	}
	if got := EstimateRows(node); got != 2 {
		t.Errorf("IncrementalSort node over a 1-row input: EstimateRows = %d, want 2 (the path's clamped rows)", got)
	}
	if got := EstimateRows(&IncrementalSort{Child: scan(37), PresortedCount: 1}); got != 37 {
		t.Errorf("IncrementalSort node over 37 rows: EstimateRows = %d, want 37", got)
	}
}

// TestSearchRootHandsUpCheapestTotalUnderAnAggregate pins M0146-0150: PG
// applies the tuple fraction only at final_rel, above the grouping step, and
// create_grouping_paths reads the scan/join rel's cheapest_total_path. With a
// LIMIT fraction and an ordering the statement wants, the search root's
// fractional pick can elect a fast-start ordered path (TPC-DS Q70's ordered
// nested loop over its Hash Semi Join); with an aggregate stage above
// (rootCheapestTotal) the root hands up CheapestTotal instead.
func TestSearchRootHandsUpCheapestTotalUnderAnAggregate(t *testing.T) {
	key := PathKey{Expr: &ColumnRef{Index: 0, Name: "s_state", Type: catalog.Type{Name: "text"}}, SortAsc: true}
	rel := &RelOptInfo{Rows: 3902, ConsiderStartup: true}
	hashSemi := &Path{Kind: PathHashJoin, Rel: rel, Rows: 3902, Cost: Cost{Startup: 22053.29, Total: 38367.56}}
	orderedNL := &Path{Kind: PathNestLoop, Rel: rel, Rows: 3902, Cost: Cost{Startup: 21000, Total: 38440.70},
		Pathkeys: []PathKey{key}}
	rel.Pathlist = []*Path{hashSemi, orderedNL}
	rel.CheapestTotal = hashSemi
	s := &searchCtx{tupleFraction: 100, queryPathkeys: []PathKey{key}}
	if got := s.rootPathOf(rel); got != orderedNL {
		t.Fatalf("fractional root pick = %v, want the fast-start ordered path (the fixture must exercise the fraction)", got.Kind)
	}
	s.rootCheapestTotal = true
	if got := s.rootPathOf(rel); got != hashSemi {
		t.Errorf("under an aggregate stage the root picked %v, want CheapestTotal (the hash semi join)", got.Kind)
	}
}
