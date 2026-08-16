package optimizer

// M0127-P5.6-f-vii — `estimateAggregate` runs `estimate_num_groups`.
//
// What it ran before was `child / 2` for every GROUP BY that was not a single
// bare ColumnRef, and for the one that was, the column's whole-table NDistinct
// with no clamp at all. The four numbers upstream computes and goopg did not
// are each pinned by one test below:
//
//   - the group count is clamped to the ROWS BEING GROUPED (a grouped scan of
//     5 surviving rows cannot have 200 groups);
//   - several keys of ONE relation multiply, then clamp to the relation's
//     tuples/10, floored at the largest single ndistinct (step 4);
//   - keys of DIFFERENT relations multiply without that clamp (step 5);
//   - a relation the plan RESTRICTED contributes the Yao/Dell'Era expected
//     distinct count of the surviving rows, not the whole table's — the only
//     term that survives when `input_rows` is large because the grouping sits
//     above a fan-out join, which is exactly the TPC-DS shape.
//
// Fixtures use `estimateNumGroups` directly wherever the discriminating
// property needs an `input_rows` the fixture's own child does not produce;
// `TestNumGroupsThroughAggregate` covers the `*Aggregate` wiring itself.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// numGroupsEq is `col = <value>` in the child's own coordinate space.
func numGroupsEq(idx int, value int64) Expr {
	return &BinaryOp{Op: parser.OpEq, Left: jrCol(idx), Right: &IntegerConst{Value: value}}
}

// numGroupsScan is `scanWithStats` with DISTINCT column names. The shared
// helper names every column "c", and grouping-variable identity is (relation,
// column name) — upstream's (varno, varattno) — so a fixture with repeated
// names would silently de-duplicate two different keys of one relation.
func numGroupsScan(name string, rows int64, ndistinct ...int64) *SeqScan {
	cols := make([]catalog.ColumnStats, len(ndistinct))
	columns := make([]catalog.Column, len(ndistinct))
	for i, nd := range ndistinct {
		cols[i] = catalog.ColumnStats{NDistinct: nd}
		columns[i] = catalog.Column{
			Name: string(rune('a' + i)),
			Type: catalog.Type{Name: "int4"},
		}
	}
	tbl := &catalog.Table{
		Name:    name,
		Columns: columns,
		Stats:   &catalog.TableStats{RowCount: rows, Columns: cols},
	}
	return &SeqScan{Table: tbl, schema: tableSchema(tbl)}
}

func TestNumGroupsSingleKeyIsColumnNDistinct(t *testing.T) {
	dim := numGroupsScan("dim", 1000, 200)
	if got, want := estimateNumGroups([]Expr{jrCol(0)}, dim, 1000), int64(200); got != want {
		t.Fatalf("groups = %d, want %d (the column's own ndistinct)", got, want)
	}
}

func TestNumGroupsClampsToInputRows(t *testing.T) {
	// 200 distinct values cannot appear among 50 rows. The old single-key
	// arm returned the unclamped 200; upstream's closing clamp is
	// `if (numdistinct > input_rows) numdistinct = input_rows`.
	dim := numGroupsScan("dim", 1000, 200)
	if got, want := estimateNumGroups([]Expr{jrCol(0)}, dim, 50), int64(50); got != want {
		t.Fatalf("groups = %d, want %d (clamped to the input row count)", got, want)
	}
}

func TestNumGroupsNoGroupingIsOneGroup(t *testing.T) {
	dim := numGroupsScan("dim", 1000, 200)
	if got := estimateNumGroups(nil, dim, 1000); got != 1 {
		t.Fatalf("groups = %d, want 1 for an ungrouped aggregate", got)
	}
}

// Two keys of ONE relation: the product is the worst case, so upstream clamps
// it to tuples/10 — but never below the largest single ndistinct, "since there
// will surely be at least that many groups".
func TestNumGroupsSameRelationClampsToTenthOfTuples(t *testing.T) {
	dim := numGroupsScan("dim", 1000, 200, 100)
	got := estimateNumGroups([]Expr{jrCol(0), jrCol(1)}, dim, 1000)
	// product 20 000 → clamp 1000/10 = 100 → floored back up to the
	// largest single ndistinct, 200.
	if want := int64(200); got != want {
		t.Fatalf("groups = %d, want %d (tuples/10 = 100, floored at max(nd) = 200)", got, want)
	}
	// The pre-fix behaviour for this shape was child/2 = 500; assert the
	// direction changed, not just the value.
	if got >= 500 {
		t.Fatalf("groups = %d, still at or above the old child/2 = 500", got)
	}
}

// A single key of a relation with 12 tuples and no per-column stats: the
// no-data arm of get_variable_numdistinct estimates ndistinct = ntuples for a
// table smaller than DEFAULT_NUM_DISTINCT.
func TestNumGroupsSmallUnanalysedRelationUsesTupleCount(t *testing.T) {
	store := numGroupsScan("store", 12, 0)
	if got, want := estimateNumGroups([]Expr{jrCol(0)}, store, 1000), int64(12); got != want {
		t.Fatalf("groups = %d, want %d (ntuples < DEFAULT_NUM_DISTINCT)", got, want)
	}
}

// A key the resolver cannot trace to any relation gets DEFAULT_NUM_DISTINCT,
// which is what upstream does when `examine_variable` finds no rel.
func TestNumGroupsUnresolvableKeyUsesDefault(t *testing.T) {
	opaque := &SeqScan{} // no Table: nothing to resolve against
	if got, want := estimateNumGroups([]Expr{jrCol(0)}, opaque, 100000), int64(defaultNumDistinct); got != want {
		t.Fatalf("groups = %d, want %d (DEFAULT_NUM_DISTINCT)", got, want)
	}
}

// Keys of two DIFFERENT relations multiply: neither relation's clamp applies
// across the pair, because cross-relation correlation is exactly what upstream
// says it cannot know.
func TestNumGroupsDifferentRelationsMultiply(t *testing.T) {
	a := numGroupsScan("a", 100, 10)
	b := numGroupsScan("b", 1000, 20)
	j := mergedJoin(JoinTypeInner, a, b)
	lw := len(a.Output())
	got := estimateNumGroups([]Expr{jrCol(0), jrCol(lw)}, j, 100000)
	if want := int64(200); got != want {
		t.Fatalf("groups = %d, want %d (10 × 20, no cross-relation clamp)", got, want)
	}
}

// The discriminating test for the restriction term. The relation survives at 5
// of its 1000 rows, but the grouping sits above a fan-out so `input_rows` is
// 500 and cannot clamp anything. Without the Yao/Dell'Era adjustment the
// answer is the whole table's 200 distinct values — a 40× over-estimate of a
// GROUP BY that can produce at most 5 groups.
func TestNumGroupsAccountsForRestrictionOnTheRelation(t *testing.T) {
	dim := numGroupsScan("dim", 1000, 200, 200)
	// c1's ndistinct is 200, so the equality's selectivity is 1/200 and
	// the Filter carries 5 rows.
	filtered := &Filter{Child: dim, Predicate: numGroupsEq(1, 7), LeafLocal: true}
	if got := EstimateRows(filtered); got != 5 {
		t.Fatalf("fixture: filtered rows = %d, want 5", got)
	}
	got := estimateNumGroups([]Expr{jrCol(0)}, filtered, 500)
	if want := int64(5); got != want {
		t.Fatalf("groups = %d, want %d (200·(1-0.995^5) = 4.95 → 5)", got, want)
	}
	// Same tree, no restriction: the whole table's distinct count stands.
	if got, want := estimateNumGroups([]Expr{jrCol(0)}, dim, 500), int64(200); got != want {
		t.Fatalf("unfiltered control: groups = %d, want %d", got, want)
	}
}

// `f(x)` is treated as `x` (step 2), and the same variable reached twice is
// counted once ("GROUP BY a, a + b is treated the same as GROUP BY a, b").
func TestNumGroupsReducesExpressionsToUniqueVars(t *testing.T) {
	dim := numGroupsScan("dim", 1000, 200, 100)
	plusOne := &BinaryOp{Op: parser.OpAdd, Left: jrCol(0), Right: &IntegerConst{Value: 1}}

	if got, want := estimateNumGroups([]Expr{plusOne}, dim, 1000), int64(200); got != want {
		t.Fatalf("GROUP BY a+1: groups = %d, want %d (the ndistinct of a)", got, want)
	}
	// a and a+1 are ONE variable, so this must not become the two-variable
	// case (which would clamp at 200 by a different route) — it must equal
	// the single-key answer exactly.
	if got, want := estimateNumGroups([]Expr{jrCol(0), plusOne}, dim, 1000), int64(200); got != want {
		t.Fatalf("GROUP BY a, a+1: groups = %d, want %d (duplicate variable dropped)", got, want)
	}
}

// The `*Aggregate` node itself must feed its child's row estimate in as
// `input_rows`; this is the wiring the planner actually runs.
func TestNumGroupsThroughAggregate(t *testing.T) {
	dim := numGroupsScan("dim", 1000, 200, 200)
	filtered := &Filter{Child: dim, Predicate: numGroupsEq(1, 7), LeafLocal: true}
	agg := &Aggregate{Child: filtered, GroupExprs: []Expr{jrCol(0)}}
	if got, want := EstimateRows(agg), int64(5); got != want {
		t.Fatalf("EstimateRows(Aggregate) = %d, want %d", got, want)
	}
	ungrouped := &Aggregate{Child: filtered}
	if got := EstimateRows(ungrouped); got != 1 {
		t.Fatalf("EstimateRows(ungrouped Aggregate) = %d, want 1", got)
	}
}
