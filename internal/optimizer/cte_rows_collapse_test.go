package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestInitialRelRowsCTEKeepsCollapsedEstimate pins PG's behaviour for a CTE
// leaf whose filtered estimate collapses: `set_cte_size_estimates` keeps it
// and `clamp_row_est` floors it at 1
// (postgres/src/backend/optimizer/path/costsize.c:5356-5363). goopg's M0129-S1
// arm substituted the body's unfiltered row count instead; M0145-0012 retired
// it for plan parity.
//
// Why a unit witness: the substitution is invisible to every value gate
// (values are identical with and without it — only the plan shape and the
// runtime move, Q74 by ~13x), so reintroducing it would pass the corpus gates.
func TestInitialRelRowsCTEKeepsCollapsedEstimate(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "crf_t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The production shape, in miniature: a CTE body of 8325 rows (Q74's
	// `year_total` at SF0.25) under four equality conjuncts on columns with
	// no statistics. Each defaults to defaultEqSelectivity, so
	// 0.005^4 x 8325 collapses below 1.
	body := &SeqScan{Table: statsTable("year_total_body", 8325, 8325), schema: tableSchema(tbl)}
	cte := &CTEScan{Name: "year_total", Alias: "t", Child: body, schema: tableSchema(tbl)}
	leaf := &Filter{Child: cte, Predicate: combineAnd([]Expr{
		numGroupsEq(0, 1), numGroupsEq(0, 2), numGroupsEq(0, 3), numGroupsEq(0, 4),
	})}

	// filteredRows is meaningless for a derived leaf (it comes from a
	// synthetic catalog.Table), which is exactly why initialRelRows reads the
	// subtree instead — pass a value that would be wrong if it were believed.
	info := baseRelInfo{filteredRows: 7}

	if got := initialRelRows(leaf, info); got != 1 {
		t.Fatalf("initialRelRows = %v, want the collapsed estimate floored at 1 "+
			"(PG's clamp_row_est) — no body-count substitution", got)
	}
}

// TestInitialRelRowsSearchedLeafReadsItsRel pins M0146-0141: a searched join
// tree entering an enclosing search as a leaf (a join_collapse_limit
// sub-problem, or a subquery whose plan is the search's tree) is sized by the
// RelOptInfo the search built for it, as PG's enclosing search reads the
// lower joinrel's own `rows` (make_rel_from_joinlist returns the RelOptInfo
// itself). Re-estimating the built tree bottom-up drifted from that size —
// TPC-DS Q72 SF1's 8-rel sub-problem entered at 1623 rows while its rel held
// 5, which priced the d3 probe loop out.
//
// The descent is searchedJoinInputRelOf's: row-preserving wrappers (here a
// Gather) pass, anything that changes the row count (an Aggregate) stops it
// and the leaf keeps the subtree estimate.
func TestInitialRelRowsSearchedLeafReadsItsRel(t *testing.T) {
	searched := func() Node {
		j := &Join{
			Left:  &SeqScan{Table: statsTable("srl_a", 1000), schema: cpjSchema("a", 1)},
			Right: &SeqScan{Table: statsTable("srl_b", 1000), schema: cpjSchema("b", 1)},
		}
		markSearchedTree(j)
		j.setSearchRel(newRelOptInfo(RelSet(3), 5, 8))
		return j
	}
	info := baseRelInfo{filteredRows: 7}

	if got := initialRelRows(&Gather{Child: searched()}, info); got != 5 {
		t.Errorf("Gather over a searched tree: initialRelRows = %v, want the searched rel's 5", got)
	}
	agg := &Aggregate{Child: searched()}
	if got, est := initialRelRows(agg, info), float64(EstimateRows(agg)); got != est || got == 5 {
		t.Errorf("Aggregate over a searched tree: initialRelRows = %v, want the subtree estimate %v", got, est)
	}
}

// TestSubproblemLeafPathkeysTranslateToBindingCoordinates pins M0146-0148's
// coordinate rule: a sub-problem tree publishes the binding-order window that
// starts at its leaf's binding offset, so the ordering it delivers at published
// position i names coordinate off+i in the enclosing search — the coordinate
// that problem's clauses and the query pathkeys use for that column. A key
// that is not a column of the published row stops the translation.
func TestSubproblemLeafPathkeysTranslateToBindingCoordinates(t *testing.T) {
	child := upperOrderedInput(10) // k int4, v text, w numeric
	sorted := &Sort{Child: child, Keys: []SortKey{
		{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}, Desc: true, NullsFirst: true},
		{Expr: &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}},
	}}
	keys := subproblemLeafPathkeys(7, sorted)
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want 2", keys)
	}
	for i, want := range []struct {
		idx  int
		name string
		asc  bool
	}{{8, "v", false}, {7, "k", true}} {
		cr, ok := keys[i].Expr.(*ColumnRef)
		if !ok || cr.Index != want.idx || cr.Name != want.name || keys[i].SortAsc != want.asc {
			t.Errorf("key %d = %+v (asc=%v), want coordinate %d %q asc=%v", i, keys[i].Expr, keys[i].SortAsc, want.idx, want.name, want.asc)
		}
	}
	unordered := upperOrderedInput(10)
	if got := subproblemLeafPathkeys(7, unordered); len(got) != 0 {
		t.Errorf("an unordered tree claims %v, want none", got)
	}
}
