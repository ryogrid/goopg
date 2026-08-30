package optimizer

// M0127-P5.4b-ii-a — parameterised base index paths (pathparamindex.go).
//
// These tests are the falsifiable half of the slice. The paths are LIVE since
// M0127-P5.9 (2026-08-06) — `GOOPG_PGSHAPED_DP` defaults ON and `planSelect`
// calls the search — so a wrong parameterisation, row count or cost IS now
// observable elsewhere in the repository, at planner-bar expense rather than
// here. What they pin, in order: which clauses qualify, which index is
// accepted, what `ppi_rows` comes out as, and — the property the whole 03 §9
// discipline exists for — that a parameterised path never displaces the rel's
// unparameterised representative.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ppiCatalog builds a two-table catalog in the shape the TPC-H NLI candidates
// have: a dimension with a unique single-column PK and a non-unique secondary
// index, plus a fact table with none.
func ppiCatalog(t *testing.T) (catalog.Catalog, *catalog.Table, *catalog.Table) {
	t.Helper()
	c := catalog.NewInMemory()
	orders, err := c.CreateTable(parser.ObjectName{Name: "orders"}, []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "o_custkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "o_shippriority", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "orders_pkey"},
		orders, []string{"o_orderkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "orders_custkey_idx"},
		orders, []string{"o_custkey"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	lineitem, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, orders, lineitem
}

// ppiSetStats attaches ANALYZE statistics positionally over tbl.Columns.
func ppiSetStats(tbl *catalog.Table, rowCount int64, cols ...catalog.ColumnStats) {
	tbl.Stats = &catalog.TableStats{RowCount: rowCount, Columns: cols}
}

// ppiEquiClause is `outerCol = innerCol` as the search sees it: a two-sided
// equijoin whose inner operand is a bare ColumnRef naming the indexed column.
func ppiEquiClause(outerRels RelSet, outerName string, innerRels RelSet, innerName string) *restrictInfo {
	return &restrictInfo{
		relids:      outerRels | innerRels,
		leftKey:     &ColumnRef{Name: outerName},
		rightKey:    &ColumnRef{Name: innerName},
		leftRelids:  outerRels,
		rightRelids: innerRels,
		isEquijoin:  true,
		ecID:        noEquivClass,
	}
}

// ppiCtx assembles a two-rel search whose rel 1 is the indexed table, with the
// given clause list already installed. Rel 0 stands for the outer.
func ppiCtx(t *testing.T, inner *catalog.Table, innerRows float64, clauses ...*restrictInfo) *searchCtx {
	t.Helper()
	s, err := newSearchCtx(2, defaultCostParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, rows := range []float64{100, innerRows} {
		rel := newRelOptInfo(RelSet(1)<<uint(i), rows, 32)
		if err := s.addRel(rel); err != nil {
			t.Fatal(err)
		}
		generateScanPaths(rel, s.cp, estScanPages(rows, 32), 0, 0, true)
		setCheapest(rel)
	}
	s.relInfos = []baseRelInfo{{}, {table: inner}}
	// The rebuildable leaf the producers' eligibility gate requires
	// (M0127-P5.5-c): production rels get theirs from `buildInitialRels`.
	s.levelRels(1)[1].baseLeaf = &SeqScan{Table: inner}
	s.clauses = &restrictInfoList{all: clauses}
	return s
}

// TestIndexableJoinClausesForRejectsNonSingleRelOperand: the inner operand must
// be computable on THIS rel alone. `a.x = b.y + c.z` is a perfectly good join
// clause and a perfectly good hash key at the pair ({a},{b,c}) — but no column
// of `b` is being equated to anything, so there is nothing to probe an index on.
func TestIndexableJoinClausesForRejectsNonSingleRelOperand(t *testing.T) {
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)
	ri := &restrictInfo{
		relids:      a | b | c,
		leftKey:     &ColumnRef{Name: "x"},
		rightKey:    &BinaryOp{},
		leftRelids:  a,
		rightRelids: b | c,
		isEquijoin:  true,
		ecID:        noEquivClass,
	}
	if got := indexableJoinClausesFor(b, []*restrictInfo{ri}); len(got) != 0 {
		t.Fatalf("rel {b} accepted a clause whose operand spans {b,c}: %+v", got)
	}
	// The same clause DOES parameterise {a}: its operand is exactly {a}.
	got := indexableJoinClausesFor(a, []*restrictInfo{ri})
	if len(got) != 1 || got[0].innerCol != "x" || got[0].outerRels != b|c {
		t.Fatalf("rel {a} = %+v; want one clause on x parameterised by {b,c}", got)
	}
}

// TestIndexableJoinClausesForRejectsNonEquijoin: an inequality join qual has no
// two-sided operand split, so it can neither key a hash join nor probe an index.
func TestIndexableJoinClausesForRejectsNonEquijoin(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	if got := indexableJoinClausesFor(b, []*restrictInfo{plainClause(a | b)}); len(got) != 0 {
		t.Fatalf("an inequality join qual produced %d index candidates; want 0", len(got))
	}
}

// TestAddParameterizedIndexPathsUniqueIndexGivesOneRow: the PK case, which is
// both the sharpest estimate and the dominant one in practice. PG's
// `var_eq_non_const` short-circuits on `vardata->isunique` (selfuncs.c) rather
// than dividing by ndistinct, and the path's Rows is PG's `ppi_rows` — the rows
// of ONE execution, not of the relation.
func TestAddParameterizedIndexPathsUniqueIndexGivesOneRow(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := ppiCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))

	s.addParameterizedIndexPaths(cat)

	rel := s.findRel(inner)
	var param *Path
	for _, p := range rel.Pathlist {
		if p.RequiredOuter != 0 {
			param = p
		}
	}
	if param == nil {
		t.Fatalf("no parameterised path was added; pathlist = %d entries", len(rel.Pathlist))
	}
	if param.Kind != PathIndexScan {
		t.Errorf("path kind = %v; want PathIndexScan", param.Kind)
	}
	if param.RequiredOuter != outer {
		t.Errorf("RequiredOuter = %#04b; want the outer rel %#04b", param.RequiredOuter, outer)
	}
	if param.Rows != 1 {
		t.Errorf("ppi_rows = %v; want 1 for a fully-bound unique index", param.Rows)
	}
	// One execution of a bound probe, not a scan of the relation.
	//
	// This used to assert `Cost.Total == indexProbeCost(s.cp)` exactly, which
	// was an artefact of the flat per-probe function that priced parameterised
	// paths separately. They are priced by `cost_index` (costsize.c:520) now,
	// like every other index path, so the number is a model output rather than
	// a constant to pin. The PROPERTY the constant stood for is asserted
	// instead — one-probe shaped, and independent of the relation's size.
	if probe := indexProbeCost(s.cp); param.Cost.Total > 2*probe {
		t.Errorf("cost = %v; a fully-bound unique probe must stay one-probe shaped (~%v)", param.Cost.Total, probe)
	}
	if seq := rel.Pathlist[0]; param.Cost.Total >= seq.Cost.Total {
		t.Errorf("a bound PK probe (%v) is not cheaper than the seq scan (%v)", param.Cost.Total, seq.Cost.Total)
	}

	// The sharp form of "one execution": ten times the relation must not cost
	// ten times the probe. A cost that tracked the relation's size would mean
	// the path had been priced as a scan, which is what the old equality was
	// really guarding against.
	catBig, ordersBig, _ := ppiCatalog(t)
	ppiSetStats(ordersBig, 15_000_000,
		catalog.ColumnStats{NDistinct: 15_000_000},
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 5})
	sBig := ppiCtx(t, ordersBig, 15_000_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))
	sBig.addParameterizedIndexPaths(catBig)
	var big *Path
	for _, p := range sBig.findRel(inner).Pathlist {
		if p.RequiredOuter != 0 {
			big = p
		}
	}
	if big == nil {
		t.Fatal("no parameterised path on the 10x relation")
	}
	if big.Rows != 1 {
		t.Errorf("ppi_rows on the 10x relation = %v; want 1", big.Rows)
	}
	if big.Cost.Total > 1.5*param.Cost.Total {
		t.Errorf("probe cost went %v -> %v for a 10x relation; a bound unique probe must not scale with it",
			param.Cost.Total, big.Cost.Total)
	}
}

// TestAddParameterizedIndexPathsNonUniqueUsesNdistinct: without a uniqueness
// guarantee the estimate is PG's averaged-over-all-values one — non-null
// fraction over ndistinct (`var_eq_non_const`) — applied to the rel's own row
// count, which already carries the local-qual selectivity.
func TestAddParameterizedIndexPathsNonUniqueUsesNdistinct(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 1000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := ppiCtx(t, orders, 1_000_000, ppiEquiClause(outer, "c_custkey", inner, "o_custkey"))

	s.addParameterizedIndexPaths(cat)

	rel := s.findRel(inner)
	if len(rel.CheapestParameterized) != 2 {
		t.Fatalf("CheapestParameterized has %d entries; want the seq scan plus one parameterised path",
			len(rel.CheapestParameterized))
	}
	param := rel.CheapestParameterized[1]
	if want := 1_000_000.0 / 1000.0; param.Rows != want {
		t.Errorf("ppi_rows = %v; want rel.Rows/ndistinct = %v", param.Rows, want)
	}
	if param.Cost.Total <= indexProbeCost(s.cp) {
		t.Errorf("a 1000-row probe (%v) is not dearer than a single-row one (%v)",
			param.Cost.Total, indexProbeCost(s.cp))
	}
}

// TestAddParameterizedIndexPathsNeedsEveryIndexColumnBound is the shared
// eligibility rule, and the first half of 03 §5.2's binding contract: path
// generation calls `pickIndexCoveringAllLeadingColumns`, the SAME function the
// NLI constructor uses, so it cannot cost an index the constructor declines.
// A composite index with only its leading column bound is such an index —
// goopg's executor emits no partial-prefix probe.
func TestAddParameterizedIndexPathsNeedsEveryIndexColumnBound(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int4"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"},
		tbl, []string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	outer, inner := relsetOf(0), relsetOf(1)

	// Leading column only: no path.
	s := ppiCtx(t, tbl, 800_000, ppiEquiClause(outer, "p_partkey", inner, "ps_partkey"))
	s.addParameterizedIndexPaths(c)
	if got := len(s.findRel(inner).Pathlist); got != 1 {
		t.Fatalf("a half-bound composite index produced %d paths; want only the seq scan", got)
	}

	// Both columns bound by the same outer rel: one path.
	s = ppiCtx(t, tbl, 800_000,
		ppiEquiClause(outer, "p_partkey", inner, "ps_partkey"),
		ppiEquiClause(outer, "s_suppkey", inner, "ps_suppkey"))
	s.addParameterizedIndexPaths(c)
	if got := len(s.findRel(inner).Pathlist); got != 2 {
		t.Fatalf("a fully-bound composite index produced %d paths; want the seq scan plus one", got)
	}
}

// TestAddParameterizedIndexPathsKeepUnparameterisedCheapest is the property the
// whole P5.4b-i discipline was landed ahead of these paths to protect
// (`set_cheapest`, pathnode.c:272). A parameterised path is far cheaper than
// the seq scan — it is a single bound probe — and a parameterisation-blind
// minimum would hand it to a join that cannot supply the parameter, producing
// an unbuildable plan rather than a slow one.
func TestAddParameterizedIndexPathsKeepUnparameterisedCheapest(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := ppiCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))
	rel := s.findRel(inner)
	seqPath := rel.CheapestTotal

	s.addParameterizedIndexPaths(cat)

	if rel.CheapestTotal != seqPath {
		t.Errorf("CheapestTotal moved to a parameterised path (RequiredOuter=%#04b)", rel.CheapestTotal.RequiredOuter)
	}
	if rel.CheapestStartup != seqPath {
		t.Error("CheapestStartup moved off the unparameterised path")
	}
	if len(rel.CheapestParameterized) == 0 || rel.CheapestParameterized[0] != seqPath {
		t.Error("CheapestParameterized does not lead with the cheapest unparameterised path (pathnode.c:375)")
	}
	// add_path must have kept BOTH: neither dominates, since the cheaper path
	// is the more constrained one (outerDim, path.go:255).
	if len(rel.Pathlist) != 2 {
		t.Fatalf("pathlist has %d entries; want the seq scan and the parameterised probe", len(rel.Pathlist))
	}
}

// TestAddParameterizedIndexPathsOneParameterizationPerOuterRelset: PG builds
// one path per entry of `considered_relids` (indxpath.c:446), and two clauses
// from two different outer rels are two entries. Each is a genuinely different
// plan shape — probing by A's key needs A available, probing by B's needs B —
// so both must survive add_path.
func TestAddParameterizedIndexPathsOneParameterizationPerOuterRelset(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	s, err := newSearchCtx(3, defaultCostParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, rows := range []float64{100, 1_500_000, 200} {
		rel := newRelOptInfo(RelSet(1)<<uint(i), rows, 32)
		if err := s.addRel(rel); err != nil {
			t.Fatal(err)
		}
		generateScanPaths(rel, s.cp, estScanPages(rows, 32), 0, 0, true)
		setCheapest(rel)
	}
	relA, relO, relC := relsetOf(0), relsetOf(1), relsetOf(2)
	s.relInfos = []baseRelInfo{{}, {table: orders}, {}}
	s.levelRels(1)[1].baseLeaf = &SeqScan{Table: orders}
	s.clauses = &restrictInfoList{all: []*restrictInfo{
		ppiEquiClause(relA, "l_orderkey", relO, "o_orderkey"),
		ppiEquiClause(relC, "c_custkey", relO, "o_custkey"),
	}}

	s.addParameterizedIndexPaths(cat)

	seen := map[RelSet]bool{}
	for _, p := range s.findRel(relO).Pathlist {
		if p.RequiredOuter != 0 {
			seen[p.RequiredOuter] = true
		}
	}
	if !seen[relA] || !seen[relC] {
		t.Fatalf("parameterisations = %v; want one per outer rel (%#04b and %#04b)", seen, relA, relC)
	}
}

// TestAddParameterizedIndexPathsNoCatalogIsANoOp: the three-step protocol's
// middle step is optional exactly where it has nothing to say. Every
// enumerator unit test runs without a catalog and must see an unchanged search.
func TestAddParameterizedIndexPathsNoCatalogIsANoOp(t *testing.T) {
	_, orders, _ := ppiCatalog(t)
	outer, inner := relsetOf(0), relsetOf(1)
	s := ppiCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))
	before := len(s.findRel(inner).Pathlist)

	s.addParameterizedIndexPaths(nil)

	if got := len(s.findRel(inner).Pathlist); got != before {
		t.Fatalf("a nil catalog changed the pathlist (%d -> %d)", before, got)
	}
}

// TestVarEqNonConstSelectivityMCVCrossCheck pins the half of
// `var_eq_non_const` a reimplementation is most likely to drop: the averaged
// estimate can never exceed the most common value's own frequency, because a
// probe value drawn uniformly cannot be commoner than the commonest value.
func TestVarEqNonConstSelectivityMCVCrossCheck(t *testing.T) {
	// 1/ndistinct = 0.1, but the commonest value occurs only 4% of the time —
	// which means the distribution is far flatter than ndistinct suggests.
	stats := &catalog.ColumnStats{
		NDistinct: 10,
		MCV:       []catalog.MCVEntry{{Value: "a", Frequency: 0.04}},
	}
	if got := varEqNonConstSelectivity(stats, 1000); got != 0.04 {
		t.Errorf("selectivity = %v; want it clamped to the MCV[0] frequency 0.04", got)
	}
	// Null fraction comes off the top before the division (selfuncs.c).
	got := varEqNonConstSelectivity(&catalog.ColumnStats{NDistinct: 4, NullFrac: 0.5}, 1000)
	if want := 0.5 / 4; got != want {
		t.Errorf("selectivity with 50%% nulls = %v; want %v", got, want)
	}
	// No statistics: PG's DEFAULT_NUM_DISTINCT.
	if got := varEqNonConstSelectivity(nil, 1000); got != 1.0/200.0 {
		t.Errorf("unanalysed column selectivity = %v; want 1/200", got)
	}
}

// A column whose distinct count is stored ONLY in the fraction form — the
// normal state on a restarted server, where the absolute count's row-count
// input is not restored (ledger pq-P6) — must be read as a real estimate and
// not as "unknown". This is PG's negative `stadistinct`
// (`get_variable_numdistinct`, selfuncs.c: `-stadistinct * ntuples`).
//
// The regression it pins is not subtle: TPC-H `lineitem.l_orderkey` reads
// NDistinctFrac=0.202 over 6,001,255 rows. Read correctly that is 1.2 M
// distinct values; read as "unknown" it is 200, a 6000x error in the row
// estimate of every `l_orderkey = <outer>` probe.
func TestVarEqNonConstSelectivityFractionForm(t *testing.T) {
	stats := &catalog.ColumnStats{NDistinct: 0, NDistinctFrac: 0.202}
	const relRows = 6001255.0
	got := varEqNonConstSelectivity(stats, relRows)
	want := 1.0 / clampRowEst(0.202*relRows)
	if got != want {
		t.Errorf("selectivity from the fraction form = %v; want %v", got, want)
	}
	if got >= 1.0/200.0 {
		t.Errorf("selectivity %v is no sharper than DEFAULT_NUM_DISTINCT; the fraction form was ignored", got)
	}
	// When BOTH forms are present the winner is decided by PG's analyze.c
	// rule, which `ColumnStats.StaDistinct` implements: a fraction above 0.1
	// means "distinctness scales with the relation", so it wins over the
	// absolute count rather than losing to it.
	//
	// An earlier version of this test asserted the opposite (absolute always
	// wins) because the function under test open-coded its own reduction. That
	// assertion was pinning a divergence from `getVariableNumDistinct`
	// (joinselectivity.go), which the join estimator uses on the same columns —
	// two different distinct counts for one column in one plan.
	bigFrac := &catalog.ColumnStats{NDistinct: 50, NDistinctFrac: 0.9}
	if got, want := varEqNonConstSelectivity(bigFrac, relRows), 1.0/clampRowEst(0.9*relRows); got != want {
		t.Errorf("selectivity with frac>0.1 and an absolute count = %v; want the fraction to win (%v)", got, want)
	}
	// Below the 0.1 threshold the absolute count is used.
	smallFrac := &catalog.ColumnStats{NDistinct: 50, NDistinctFrac: 0.05}
	if got := varEqNonConstSelectivity(smallFrac, relRows); got != 1.0/50.0 {
		t.Errorf("selectivity with frac<=0.1 = %v; want the absolute 1/50", got)
	}
	// No relation size to scale by: PG "punts" to the default rather than
	// treating the fraction as an absolute count.
	if got := varEqNonConstSelectivity(stats, 0); got != 1.0/200.0 {
		t.Errorf("selectivity with no relation size = %v; want 1/200", got)
	}
}

// PG's two no-data branches are deliberately asymmetric: a relation smaller
// than DEFAULT_NUM_DISTINCT is assumed to hold one distinct value per row, and
// only a larger one falls back to the constant — "so that the behavior isn't
// discontinuous" (get_variable_numdistinct, selfuncs.c).
func TestVariableNumDistinctNoDataBranches(t *testing.T) {
	empty := &catalog.ColumnStats{}
	if got := variableNumDistinct(empty, 50); got != 50 {
		t.Errorf("numdistinct for a 50-row relation with no stats = %v; want 50", got)
	}
	if got := variableNumDistinct(empty, 5000); got != 200 {
		t.Errorf("numdistinct for a 5000-row relation with no stats = %v; want DEFAULT_NUM_DISTINCT", got)
	}
}
