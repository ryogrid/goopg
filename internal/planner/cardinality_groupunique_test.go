package planner

// M0127-P5.6-g-ii — `examine_simple_variable`'s GROUP-BY / DISTINCT arm and
// `get_variable_numdistinct`'s `isunique` branch (design leftdeep-joins/09
// §5.12).
//
// The item was filed as "add a `*HashAggregate` arm to `resolveBaseColumn`",
// and the measurement that made it a successor rather than a fix said the arm
// ALONE reads worse. It does, and the oracle says why: upstream has no such
// arm. `examine_simple_variable` walks into a grouped subquery, sets
// `vardata->isunique` when the referenced output IS the sole grouping column,
// and then RETURNS — "cannot go further" — without a statistics tuple. What
// crosses a grouping node upstream is uniqueness, never a distribution.
//
// These tests pin both halves of that: which shapes are unique (the walk), and
// what a unique one answers (its own row count, upstream's negative
// `stadistinct`). The end-to-end movement is the estimate-audit run in 09 §5.12.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// gtConst is `<output col idx> > n`, the shape a HAVING qual takes above an
// aggregate: the left operand resolves to no base column, so it is priced at
// DEFAULT_INEQ_SEL.
func gtConst(idx int, n int64) Expr {
	return &BinaryOp{Op: parser.OpGt, Left: jrCol(idx), Right: &IntegerConst{Value: n}}
}

// groupAgg builds the `GROUP BY <child col groupIdx>` shape the planner builds:
// output is [group exprs..., aggregate calls...], so the grouping column is
// output index 0.
func groupAgg(child Node, groupIdx ...int) *Aggregate {
	a := &Aggregate{Child: child}
	for _, gi := range groupIdx {
		a.GroupExprs = append(a.GroupExprs, jrCol(gi))
		a.schema = append(a.schema, SchemaColumn{Name: "g", Type: catalog.Type{Name: "int4"}})
	}
	a.Aggs = []AggregateCall{{Name: "sum", Arg: jrCol(0), Type: catalog.Type{Name: "int4"}}}
	a.schema = append(a.schema, SchemaColumn{Name: "sum", Type: catalog.Type{Name: "int4"}})
	return a
}

// --- (1) the walk: which shapes are unique ---------------------------------

func TestGroupUniqueNDistinctIsTheAggregateRowCount(t *testing.T) {
	// 5000 rows, 700 distinct in column 0 → the aggregate emits 700 rows, and
	// the grouping column is unique across them. Upstream's `isunique` branch
	// makes stadistinct = -(1 - stanullfrac) with no statistics tuple to read
	// a null fraction from, i.e. exactly the relation's row count.
	agg := groupAgg(scanWithStats("l", 5000, 700), 0)

	if got, want := EstimateRows(agg), int64(700); got != want {
		t.Fatalf("aggregate rows = %d, want %d", got, want)
	}
	if got, want := columnNDistinctForChild(0, agg), int64(700); got != want {
		t.Fatalf("ndistinct through the grouping column = %d, want %d", got, want)
	}
}

func TestGroupUniqueNDistinctRefusesTwoGroupingColumns(t *testing.T) {
	// `list_length(subquery->groupClause) == 1` is upstream's test and it is
	// load-bearing: with two grouping columns the PAIR is unique but neither
	// column is, so the counting argument does not exist. Q20's
	// `GROUP BY ps_partkey, ps_suppkey` is this shape.
	agg := groupAgg(scanWithStats("ps", 800000, 200000, 10000), 0, 1)

	if got := columnNDistinctForChild(0, agg); got != 0 {
		t.Fatalf("ndistinct through a two-column GROUP BY = %d, want 0 (not unique)", got)
	}
	if got := columnNDistinctForChild(1, agg); got != 0 {
		t.Fatalf("ndistinct through a two-column GROUP BY = %d, want 0 (not unique)", got)
	}
}

func TestGroupUniqueNDistinctRefusesAnAggregateOutputColumn(t *testing.T) {
	// `targetIsInSortList(ste, ...)`: the referenced output must BE the
	// grouping column. `sum(x)` is not unique over the groups.
	agg := groupAgg(scanWithStats("l", 5000, 700), 0)

	if got := columnNDistinctForChild(1, agg); got != 0 {
		t.Fatalf("ndistinct of the sum() output = %d, want 0", got)
	}
}

func TestGroupUniqueNDistinctRefusesAPartialAggregate(t *testing.T) {
	// A Partial node's rows are one worker's share of the groups, not the
	// group count, so its output column is not unique — two workers can both
	// emit the same key. (The Finalize node above it is a whole-input
	// aggregate and does answer.)
	agg := groupAgg(scanWithStats("l", 5000, 700), 0)
	agg.Mode = AggModePartial

	if got := columnNDistinctForChild(0, agg); got != 0 {
		t.Fatalf("ndistinct through a Partial aggregate = %d, want 0", got)
	}
}

func TestGroupUniqueNDistinctThroughAHavingFilter(t *testing.T) {
	// Q18's shape, and the reason the row count rather than the group count is
	// the right answer: HAVING is a `*Filter` above the `*Aggregate`, and
	// upstream reads `vardata->rel->tuples` — the SUBQUERY relation's size,
	// after its qual. The filter's selectivity reaches a semi-join estimate
	// only through this number.
	agg := groupAgg(scanWithStats("l", 6000000, 1200000), 0)
	// `sum > 313` resolves to no base column, so it is priced at
	// DEFAULT_INEQ_SEL — the same 1/3 upstream charges an Aggref comparison.
	having := &Filter{Child: agg, Predicate: gtConst(1, 313)}

	rows := EstimateRows(having)
	if want := int64(1200000 / 3); rows != want {
		t.Fatalf("HAVING filter rows = %d, want %d", rows, want)
	}
	if got := columnNDistinctForChild(0, having); got != rows {
		t.Fatalf("ndistinct through the HAVING filter = %d, want %d (the filtered rows)", got, rows)
	}
}

func TestGroupUniqueNDistinctThroughAProjectRemap(t *testing.T) {
	// The wrapper walk must agree with `resolveBaseColumn`'s about which node
	// a coordinate lands on (hard-won rule #2), including the Project remap
	// that answers for another column when it is missing.
	agg := groupAgg(scanWithStats("l", 5000, 700), 0)
	proj := &Project{Child: agg, Targets: []Expr{jrCol(1), jrCol(0)}}
	proj.schema = Schema{
		SchemaColumn{Name: "sum", Type: catalog.Type{Name: "int4"}},
		SchemaColumn{Name: "g", Type: catalog.Type{Name: "int4"}},
	}

	if got := columnNDistinctForChild(0, proj); got != 0 {
		t.Fatalf("ndistinct of the projected sum() = %d, want 0", got)
	}
	if got, want := columnNDistinctForChild(1, proj), int64(700); got != want {
		t.Fatalf("ndistinct of the projected grouping column = %d, want %d", got, want)
	}
}

func TestDistinctSubqueryIsUniqueOnlyWhenItHasOneColumn(t *testing.T) {
	// The DISTINCT half of the same upstream test
	// (`list_length(subquery->distinctClause) == 1`). goopg's `*Distinct` is
	// whole-row over its output schema, so "one DISTINCT column" is a
	// one-column output.
	scan := scanWithStats("t", 5000, 700, 90)

	one := &Distinct{Child: &Project{Child: scan, Targets: []Expr{jrCol(0)},
		schema: Schema{SchemaColumn{Name: "c", Type: catalog.Type{Name: "int4"}}}}}
	one.schema = Schema{SchemaColumn{Name: "c", Type: catalog.Type{Name: "int4"}}}
	if got, want := columnNDistinctForChild(0, one), int64(5000); got != want {
		// *Distinct passes EstimateRows through unchanged (goopg does not size
		// it), so the answer is the child's row count — still the faithful
		// `rel->tuples` reading, and still an upper bound on the true count.
		t.Fatalf("ndistinct through a one-column DISTINCT = %d, want %d", got, want)
	}

	two := &Distinct{Child: scan}
	two.schema = tableSchema(scan.Table)
	if got := columnNDistinctForChild(0, two); got != 0 {
		t.Fatalf("ndistinct through a two-column DISTINCT = %d, want 0", got)
	}
}

// --- (2) statistics do NOT cross the grouping node -------------------------

func TestGroupingNodeDoesNotPropagateStatistics(t *testing.T) {
	// The half of this item that must NOT be implemented. Grouping mashes the
	// underlying column's distribution beyond recognition — the MCV list and
	// the histogram describe the PRE-grouping multiset — so upstream returns
	// without a statistics tuple. Handing the base column's MCV list up would
	// make `eqjoinsel_semi` take its MCV arm on the wrong relation's
	// frequencies, which is the P5.6-e-ii defect class in a new place.
	agg := groupAgg(mcvScan("l", 5000, catalog.ColumnStats{
		NDistinct: 700,
		MCV:       mcvList("a", 0.30, "b", 0.25),
	}), 0)

	if st := columnStatsForChildBase(0, agg); st != nil {
		t.Fatalf("statistics crossed the grouping node: %+v", st)
	}
	if _, ok := resolveBaseColumn(0, agg); ok {
		t.Fatalf("resolveBaseColumn resolved a base column through a grouping node")
	}
}

// --- (3) end to end: the SEMI join leaves the 0.5 punt ---------------------

func TestSemiJoinAgainstAGroupedSubqueryLeavesThePunt(t *testing.T) {
	// Q18's shape reduced to its arithmetic. Before this arm the inner's
	// distinct count was unknowable, `nd2` fell back to defaultNumDistinct
	// (200) — far below the inner's row estimate, so the clamp never rescued
	// it — and `eqjoinsel_semi` punted at 0.5, i.e. half the outer whatever
	// the inner was. With the grouping column unique, nd2 is the inner's own
	// row count and the fraction is measured.
	outer := scanWithStats("o", 6000000, 1500000)
	agg := groupAgg(scanWithStats("l", 6000000, 1200000), 0)
	inner := &Filter{Child: agg, Predicate: gtConst(1, 313)}
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	nd2 := int64(1200000 / 3) // the HAVING-filtered group count
	want := scaleByFloat(6000000, float64(nd2)/1500000.0)
	if got := EstimateRows(j); got != want {
		t.Fatalf("semi join estimate = %d, want %d (outer × nd2/nd1)", got, want)
	}
	if punt := scaleByFloat(6000000, 0.5); want >= punt {
		t.Fatalf("test does not exercise the punt: want %d >= punt %d", want, punt)
	}
}

// --- (4) the outer-join floor the grouping arm exposed ---------------------

func TestOuterJoinRowsNeverGoUnderTheNonNullableInput(t *testing.T) {
	// `calc_joinrel_size_estimate` (costsize.c): "the output must be at least
	// as large as the non-nullable input". `estimateJoin` had no outer-join
	// arm at all — LEFT/RIGHT/FULL took the INNER product, which is upstream's
	// first line for each of them, and stopped before the clamp.
	//
	// TPC-DS Q77 is the shape that made it reachable: a LEFT join whose inner
	// is `… GROUP BY s_store_sk`. Once the grouping arm above made nd2
	// resolvable the product came out at 885 rows for a join whose outer alone
	// is 8 885.
	outer := scanWithStats("o", 8885, 8885)
	inner := groupAgg(scanWithStats("i", 100000, 885), 0)

	for _, tc := range []struct {
		name string
		typ  JoinType
		want int64
	}{
		// 8885 · 885 / 8885 = 885, below the outer's own 8885.
		{"LEFT floors at the outer", JoinTypeLeft, 8885},
		// RIGHT is upstream's JOIN_LEFT read from the other side: goopg does
		// not commute it, so the non-nullable input is the inner.
		{"RIGHT floors at the inner", JoinTypeRight, 885},
		{"FULL floors at both", JoinTypeFull, 8885},
		// INNER is the one join type with no floor — 885 is the answer.
		{"INNER keeps the product", JoinTypeInner, 885},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := keyed(mergedJoin(tc.typ, outer, inner), 0, 0)
			if got := EstimateRows(j); got != tc.want {
				t.Fatalf("%v estimate = %d, want %d", tc.typ, got, tc.want)
			}
		})
	}
}

func TestOuterJoinFloorAppliesToTheUnmeasurableFallbackToo(t *testing.T) {
	// Upstream clamps whatever `jselec` produced, so the arm that charges
	// defaultEqSelectivity because nothing resolved gets the floor as well:
	// 200 · 3 · 0.005 = 3, under the outer's 200.
	outer := scanWithStats("o", 200, 0)
	inner := scanWithStats("i", 3, 0)
	j := keyed(mergedJoin(JoinTypeLeft, outer, inner), 0, 0)

	if got, want := EstimateRows(j), int64(200); got != want {
		t.Fatalf("LEFT join fallback estimate = %d, want %d (the outer's own rows)", got, want)
	}
}

func TestDistinctOnKeyColumnIsUnique(t *testing.T) {
	// "We do the test this way so that it works for cases involving DISTINCT
	// ON" (selfuncs.c): `SELECT DISTINCT ON (a) a, b` has a one-element
	// distinctClause, so `a` is unique in the output and `b` is not.
	//
	// The `*DistinctOn` pass-through arm of `EstimateRows` went in with this:
	// without it the node estimated 0 and zeroed every estimate above it (the
	// M0125-0038 class), which would also have made this arm permanently dead.
	scan := scanWithStats("t", 5000, 700, 90)
	don := &DistinctOn{Child: scan, KeyCols: []int{0}}
	don.schema = tableSchema(scan.Table)

	if got, want := EstimateRows(don), int64(5000); got != want {
		t.Fatalf("DistinctOn rows = %d, want %d (pass-through)", got, want)
	}
	if got, want := columnNDistinctForChild(0, don), int64(5000); got != want {
		t.Fatalf("ndistinct of the DISTINCT ON key = %d, want %d", got, want)
	}
	if got := columnNDistinctForChild(1, don); got != 0 {
		// Column 1 is not the DISTINCT ON key, and upstream's DISTINCT arm
		// ends in "cannot go further" — it returns without a statistics tuple
		// whether or not it set `isunique`. So the base column's own ANALYZE
		// count must NOT leak up: de-duplication has already reshaped that
		// column's distribution. `resolveBaseColumn` has no `*Distinct` /
		// `*DistinctOn` arm for exactly this reason.
		t.Fatalf("statistics crossed a DISTINCT ON node: ndistinct = %d, want 0", got)
	}

	two := &DistinctOn{Child: scan, KeyCols: []int{0, 1}}
	two.schema = tableSchema(scan.Table)
	if got := columnNDistinctForChild(0, two); got != 0 {
		t.Fatalf("two-key DISTINCT ON made a column unique: %d, want 0", got)
	}
}
