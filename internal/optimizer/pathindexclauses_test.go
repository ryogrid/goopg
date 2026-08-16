package optimizer

// M0127-P5.5-b — `IndexPath.indexclauses` on goopg's `Path`
// (pathindexclauses.go).
//
// The consumer is P5.5's `createPlan` arm, which does not exist yet, so no
// existing test can observe a wrong answer here. What these tests pin is the one
// property that arm cannot defend itself against: **the list's ORDER**. PG's
// `indexclauses` is ordered by index column (indxpath.c:1042) and goopg's
// executor binds `IndexScan.Keys[i]` to `Index.Columns[i]` positionally
// (plan.go:665), so a list left in the search's candidate order would make
// `createPlan` emit a probe comparing the wrong pair of columns — a wrong
// answer, not a slow plan. Every test below is a reversed-candidate-order case
// for exactly that reason: an implementation that copied `bound` verbatim would
// pass a same-order test.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// picComposite builds the composite-FK shape the ordering question actually
// arises in: `partsupp(ps_partkey, ps_suppkey)` with a two-column index over
// both, which is TPC-H's own layout and the case
// `consideredParameterizations`' unions exist to reach.
func picComposite(t *testing.T) (catalog.Catalog, *catalog.Table, *catalog.Index) {
	t.Helper()
	c := catalog.NewInMemory()
	ps, err := c.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ps_availqty", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"},
		ps, []string{"ps_partkey", "ps_suppkey"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	return c, ps, idx
}

// picReversedBound returns the two clauses of the composite probe in the order
// that is WRONG for the index: the second index column first. This is not a
// contrived input — `bound` is filled by walking the query's clause list, whose
// order is the order the user wrote the join conditions in.
func picReversedBound() []paramIndexClause {
	outer, inner := relsetOf(0), relsetOf(1)
	suppRI := ppiEquiClause(outer, "l_suppkey", inner, "ps_suppkey")
	partRI := ppiEquiClause(outer, "l_partkey", inner, "ps_partkey")
	return []paramIndexClause{
		{ri: suppRI, innerCol: "ps_suppkey", innerKey: suppRI.rightKey, outerKey: suppRI.leftKey, outerRels: outer},
		{ri: partRI, innerCol: "ps_partkey", innerKey: partRI.rightKey, outerKey: partRI.leftKey, outerRels: outer},
	}
}

// picKeyNames renders the carried probe values in list order, which is the
// sequence `createPlan` will hand the executor as `IndexScan.Keys`.
func picKeyNames(t *testing.T, cls []indexPathClause) []string {
	t.Helper()
	out := make([]string, 0, len(cls))
	for i, c := range cls {
		col, ok := c.key.(*ColumnRef)
		if !ok {
			t.Fatalf("indexclause %d key = %#v; want the clause's outer ColumnRef", i, c.key)
		}
		out = append(out, col.Name)
	}
	return out
}

// TestIndexPathClausesReordersCandidateOrderToIndexOrder is the slice's whole
// point. The input names ps_suppkey first; the index's columns are
// (ps_partkey, ps_suppkey); the output must follow the INDEX.
func TestIndexPathClausesReordersCandidateOrderToIndexOrder(t *testing.T) {
	_, _, idx := picComposite(t)

	got := indexPathClauses(idx, picReversedBound())

	if len(got) != len(idx.Columns) {
		t.Fatalf("carried %d clauses for a %d-column index; want one per column",
			len(got), len(idx.Columns))
	}
	// PG's `indexcol` sequence is nondecreasing (indxpath.c:1042). goopg's is
	// strictly 0,1,…,n-1 because it binds every column exactly once.
	for i, c := range got {
		if c.indexCol != i {
			t.Fatalf("indexclause %d has indexCol %d; want the list ordered by index column", i, c.indexCol)
		}
	}
	// The probe value at position i must be the value equated to
	// Index.Columns[i], since that is the column the executor binds it to.
	if names := picKeyNames(t, got); names[0] != "l_partkey" || names[1] != "l_suppkey" {
		t.Fatalf("probe keys = %v; want [l_partkey l_suppkey] — the values equated to %v",
			names, idx.Columns)
	}
}

// TestIndexPathClausesAgreesWithTheIndexPick is hard-won rule #2 applied to the
// one twin this carrier has. `pickIndexCoveringAllLeadingColumns` (the function
// the NLI constructor shares) independently produces the ordered probe-value
// list, and it is the reason the index was accepted at all. If the carrier's
// keys ever disagreed with it, the path would be COSTED for one probe and BUILT
// as another.
func TestIndexPathClausesAgreesWithTheIndexPick(t *testing.T) {
	cat, ps, _ := picComposite(t)
	bound := picReversedBound()

	innerToOuter := make(map[string]Expr, len(bound))
	for _, c := range bound {
		innerToOuter[c.innerCol] = c.outerKey
	}
	idx, pickKeys := pickIndexCoveringAllLeadingColumns(cat, ps, innerToOuter)
	if idx == nil {
		t.Fatal("the fully-bound composite index was declined; the twin cannot be compared")
	}

	carried := indexPathClauses(idx, bound)
	if len(carried) != len(pickKeys) {
		t.Fatalf("carrier has %d keys, the index pick %d", len(carried), len(pickKeys))
	}
	for i := range carried {
		if carried[i].key != pickKeys[i] {
			t.Fatalf("key %d: carrier has %#v, index pick has %#v — the path would be costed for one probe and built as another",
				i, carried[i].key, pickKeys[i])
		}
	}
}

// TestIndexPathClausesDeclinesOnAnUnboundColumn: PG builds a gapped list
// happily (its btree applies whatever boundary conditions it has), but goopg's
// `IndexScan.Keys` is positional and requires one expression per index column,
// so a SHORTENED list would silently re-index every position after the gap.
// nil — decline — is the only safe answer.
func TestIndexPathClausesDeclinesOnAnUnboundColumn(t *testing.T) {
	_, _, idx := picComposite(t)
	// Only the LEADING column bound: the shape PG's `amoptionalkey` arm accepts
	// and goopg's executor cannot express.
	leadingOnly := picReversedBound()[1:]

	if got := indexPathClauses(idx, leadingOnly); got != nil {
		t.Fatalf("a half-bound composite index carried %d clauses; want nil (decline)", len(got))
	}
	// The trailing-column-only case is the one that would mis-bind most
	// visibly: a one-entry list whose key belongs to column 1.
	if got := indexPathClauses(idx, picReversedBound()[:1]); got != nil {
		t.Fatalf("a trailing-only bound carried %d clauses; want nil (decline)", len(got))
	}
}

// TestIndexPathClausesCarriesRestrictInfoIdentity: the second job PG's list
// does. `is_redundant_with_indexclauses` (createplan.c:3075) drops a scan clause
// that the probe already applies, and it recognises it BY the RestrictInfo. A
// carrier that kept only the probe value would leave `createPlan` unable to tell
// that a clause it is about to place as a filter is the very clause it just
// pushed into the index.
func TestIndexPathClausesCarriesRestrictInfoIdentity(t *testing.T) {
	_, _, idx := picComposite(t)
	bound := picReversedBound()

	got := indexPathClauses(idx, bound)
	if len(got) != 2 {
		t.Fatalf("carried %d clauses; want 2", len(got))
	}
	// Reordered, so position 0 must hold the ps_partkey clause — which is
	// `bound[1]`, not `bound[0]`.
	if got[0].ri != bound[1].ri || got[1].ri != bound[0].ri {
		t.Fatal("the carried restrictInfos are not the originals at their reordered positions")
	}
}

// TestIndexPathClausesEmptyInputs: an index path with nothing pushed into it
// carries an empty list, which is pathnodes.h:1817's "an empty indexclauses list
// implies a full index scan" and not a missing value.
func TestIndexPathClausesEmptyInputs(t *testing.T) {
	_, _, idx := picComposite(t)
	if got := indexPathClauses(idx, nil); got != nil {
		t.Fatalf("no bound clauses carried %d entries; want nil", len(got))
	}
	if got := indexPathClauses(nil, picReversedBound()); got != nil {
		t.Fatalf("a nil index carried %d entries; want nil", len(got))
	}
}

// TestAddParameterizedIndexPathsCarriesOrderedIndexClauses is the producer end
// to end: the search's own clause list in reversed order, through
// `addParameterizedIndexPaths`, must still leave the path's list in index-column
// order. This is the assertion that would fail if a later edit filled the field
// from `bound` directly.
func TestAddParameterizedIndexPathsCarriesOrderedIndexClauses(t *testing.T) {
	cat, ps, _ := picComposite(t)
	ppiSetStats(ps, 800_000,
		catalog.ColumnStats{NDistinct: 200_000},
		catalog.ColumnStats{NDistinct: 10_000},
		catalog.ColumnStats{NDistinct: 9_000})
	outer, inner := relsetOf(0), relsetOf(1)
	// Written suppkey-first, as a user would be free to write it.
	s := ppiCtx(t, ps, 800_000,
		ppiEquiClause(outer, "l_suppkey", inner, "ps_suppkey"),
		ppiEquiClause(outer, "l_partkey", inner, "ps_partkey"))

	s.addParameterizedIndexPaths(cat)

	var probe *Path
	for _, p := range s.findRel(inner).Pathlist {
		if p.Kind == PathIndexScan && p.RequiredOuter != 0 {
			probe = p
			break
		}
	}
	if probe == nil {
		t.Fatal("no parameterised index path was generated for the fully-bound composite index")
	}
	if probe.IndexInfo == nil {
		t.Fatal("the path does not name its index (M0127-P5.5-a)")
	}
	if len(probe.IndexClauses) != len(probe.IndexInfo.Columns) {
		t.Fatalf("path carries %d indexclauses for a %d-column index; want one per column",
			len(probe.IndexClauses), len(probe.IndexInfo.Columns))
	}
	if names := picKeyNames(t, probe.IndexClauses); names[0] != "l_partkey" || names[1] != "l_suppkey" {
		t.Fatalf("path probe keys = %v; want [l_partkey l_suppkey] for index columns %v",
			names, probe.IndexInfo.Columns)
	}
}

// TestAddOrderedIndexPathsCarriesNoIndexClauses: the unparameterised ordered
// path is the full-index-scan case. Its EMPTY list is what tells `createPlan`
// that no qual was pushed in — every clause of the query still has to be
// evaluated above it.
func TestAddOrderedIndexPathsCarriesNoIndexClauses(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))

	s.addOrderedIndexPaths(cat)

	paths := orderedPathsOf(s.findRel(inner))
	if len(paths) != 1 {
		t.Fatalf("got %d unparameterised index paths; want exactly one", len(paths))
	}
	if got := paths[0].IndexClauses; len(got) != 0 {
		t.Fatalf("the ordering-only path carries %d indexclauses; want none — it pushes no qual into the index", len(got))
	}
	if paths[0].IndexInfo == nil {
		t.Fatal("a full index scan must still name its index")
	}
}
