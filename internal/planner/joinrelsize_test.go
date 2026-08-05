package planner

// M0127-P5.6-b — `calcJoinrelSize`, the superkey no-fan-out rule, and the
// concrete `joinRelBuilder` (joinrelsize.go).
//
// Nothing in the repository consults any of this yet (`GOOPG_PGSHAPED_DP` is
// OFF and no production caller reaches `joinSearch`), so these tests are the
// slice's whole falsifiable surface. What they pin, in order: PG's
// `outer × inner × fkselec × jselec` shape; that a covered UNIQUE/FK key
// replaces its clauses with ONE 1/raw-tuples rather than a product of
// marginals; that the divisor is the RAW count and — for a declared FK — the
// PARENT's, not the child's; that a partially equated composite key proves
// nothing; that a clause is consumed at most once; and that the builder
// actually binds sizing and path generation together at `makeJoinRel`.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// jrsCatalog is the two-table TPC-H shape the superkey rule was designed
// against: `partsupp` with a COMPOSITE unique PK — the key PG's own
// `has_unique_index` cannot see, because it requires a single-column index
// (plancat.c:2244) — and `lineitem` with the two columns that equate to it.
//
// Statistics are deliberately present and deliberately WRONG for the composite
// case: the per-column marginals would price the two-clause join at
// 1/200000 · 1/10000, twelve orders of magnitude tighter than the 1/800000 the
// key actually implies. That gap is what the superkey rule exists to close, so
// a test that left the columns unanalysed could not tell the two apart.
func jrsCatalog(t *testing.T) (catalog.Catalog, *catalog.Table, *catalog.Table) {
	t.Helper()
	c := catalog.NewInMemory()
	partsupp := jsTable(t, c, "partsupp", []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int4"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int4"}},
	}, 800000,
		catalog.ColumnStats{NDistinct: 200000},
		catalog.ColumnStats{NDistinct: 10000},
	)
	lineitem := jsTable(t, c, "lineitem", []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000,
		catalog.ColumnStats{NDistinct: 200000},
		catalog.ColumnStats{NDistinct: 10000},
	)
	return c, partsupp, lineitem
}

// jrsCtx wires those two tables into a two-relation search: rel 0 is
// `lineitem` (the probing side) and rel 1 is `partsupp`.
func jrsCtx(t *testing.T, lineitem, partsupp *catalog.Table) *searchCtx {
	t.Helper()
	s, err := newSearchCtx(2, defaultCostParams())
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: lineitem, baseRows: 6000000},
		{table: partsupp, baseRows: 800000},
	}
	return s
}

// jrsEq is one cross-rel equality between rel 0's column and rel 1's, in the
// canonical two-sided form the sizer reads.
func jrsEq(leftName, rightName string, ecID int) *restrictInfo {
	l, r := jsCol(0, leftName), jsCol(9, rightName)
	return &restrictInfo{
		clause:      &BinaryOp{Op: parser.OpEq, Left: l, Right: r},
		relids:      relsetOf(0) | relsetOf(1),
		leftKey:     l,
		rightKey:    r,
		leftRelids:  relsetOf(0),
		rightRelids: relsetOf(1),
		isEquijoin:  true,
		ecID:        ecID,
	}
}

func jrsRels(outerRows, innerRows float64) (*RelOptInfo, *RelOptInfo) {
	return newRelOptInfo(relsetOf(0), outerRows, 40), newRelOptInfo(relsetOf(1), innerRows, 24)
}

func wantRows(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s: rows=%v, want %v", what, got, want)
	}
}

// TestCalcJoinrelSizeEqjoinselShape: with no key to prove, the estimate is PG's
// plain `outer × inner × clauselist_selectivity`, and the per-clause factor is
// P5.6-a's eqjoinsel — 1/max(nd_l, nd_r).
func TestCalcJoinrelSizeEqjoinselShape(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 800000)

	clauses := []*restrictInfo{jrsEq("l_partkey", "ps_partkey", noEquivClass)}
	rows, width := s.calcJoinrelSize(c, outer, inner, clauses)

	// nd is 200000 on both sides, so the divisor is 200000.
	wantRows(t, rows, clampRowEst(6000000.0*800000.0/200000.0), "single non-key equality")
	if width != 64 {
		t.Fatalf("width=%d; want 64 (the sum of the input widths)", width)
	}
}

// TestCalcJoinrelSizeCompositeUniqueNoFanout: the primary Q9 fix. Both columns
// of `partsupp`'s composite PK are equated, so each `lineitem` row matches at
// most one `partsupp` row and the join does not fan out: the two clauses are
// REMOVED and replaced by 1/800000, PG's `get_foreign_key_join_selectivity`
// shape applied to unique-index evidence (cost-model/14 §2).
//
// The assertion is against the per-clause answer as well as against the
// absolute number, because "smaller than the marginal product" is the property
// that actually matters — the marginals here are wrong by a factor of 2.5e6.
func TestCalcJoinrelSizeCompositeUniqueNoFanout(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"}, partsupp,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 800000)

	clauses := []*restrictInfo{
		jrsEq("l_partkey", "ps_partkey", noEquivClass),
		jrsEq("l_suppkey", "ps_suppkey", noEquivClass),
	}
	rows, _ := s.calcJoinrelSize(c, outer, inner, clauses)
	wantRows(t, rows, 6000000, "composite unique key covered")

	marginal := clampRowEst(6000000.0 * 800000.0 / 200000.0 / 10000.0)
	if rows <= marginal {
		t.Fatalf("rows=%v is not above the marginal-product answer %v — the key never fired", rows, marginal)
	}
}

// TestCalcJoinrelSizePartialKeyDoesNotFire: PG's chicken-out (costsize.c:5760).
// Only ONE column of the composite key is equated, which proves nothing about
// fan-out — many `partsupp` rows share a `ps_partkey` — so the clause falls
// back to eqjoinsel.
func TestCalcJoinrelSizePartialKeyDoesNotFire(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"}, partsupp,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 800000)

	clauses := []*restrictInfo{jrsEq("l_partkey", "ps_partkey", noEquivClass)}
	rows, _ := s.calcJoinrelSize(c, outer, inner, clauses)
	wantRows(t, rows, clampRowEst(6000000.0*800000.0/200000.0), "half a composite key")
}

// TestCalcJoinrelSizeSuperkeySubsetLeavesResidual: the ⊆ half of the superkey
// rule. A one-column unique index under a two-clause join covers ONE clause;
// the other stays in the residual and is charged by eqjoinsel on top, exactly
// as PG leaves non-FK clauses to `clauselist_selectivity`.
func TestCalcJoinrelSizeSuperkeySubsetLeavesResidual(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_part_uq"}, partsupp,
		[]string{"ps_partkey"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 800000)

	clauses := []*restrictInfo{
		jrsEq("l_partkey", "ps_partkey", noEquivClass),
		jrsEq("l_suppkey", "ps_suppkey", noEquivClass),
	}
	rows, _ := s.calcJoinrelSize(c, outer, inner, clauses)
	// 1/800000 for the covered key, then 1/10000 for the leftover equality.
	wantRows(t, rows, clampRowEst(6000000.0*800000.0/800000.0/10000.0), "one-column key under a two-clause join")
}

// TestCalcJoinrelSizeDividesByRawNotFilteredCount: PG is explicit that the
// divisor is the raw table count, "not any estimate of its filtered or joined
// size" (costsize.c:5852). Here the key side has been filtered to an eighth of
// itself, and the join must produce that same eighth of the probing side — a
// real match fraction — rather than all 6e6 rows.
func TestCalcJoinrelSizeDividesByRawNotFilteredCount(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"}, partsupp,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 100000) // partsupp filtered 800k -> 100k

	clauses := []*restrictInfo{
		jrsEq("l_partkey", "ps_partkey", noEquivClass),
		jrsEq("l_suppkey", "ps_suppkey", noEquivClass),
	}
	rows, _ := s.calcJoinrelSize(c, outer, inner, clauses)
	wantRows(t, rows, 750000, "filtered key side")
}

// TestCalcJoinrelSizeFKDividesByParentCount: the declared-FK arm, and the one
// asymmetry that is easy to get backwards. An FK on the CHILD says each child
// row matches exactly one PARENT row, so the divisor is the parent's raw count
// (`1.0 / ref_tuples`, costsize.c:5847). Dividing by the child's count instead
// — which is what the legacy `uniqueNoFanoutRawCount` does, since it takes the
// row count of whichever table carried the matching constraint — would divide
// the fact table's own cardinality out of the join and under-estimate by the
// ratio of the two tables.
func TestCalcJoinrelSizeFKDividesByParentCount(t *testing.T) {
	c := catalog.NewInMemory()
	orders := jsTable(t, c, "orders", []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 1500000, catalog.ColumnStats{NDistinct: 100000})
	lineitem := jsTable(t, c, "lineitem", []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000, catalog.ColumnStats{NDistinct: 100000})
	lineitem.ForeignKeys = []catalog.ForeignKey{{
		Name: "lineitem_orderkey_fkey", Columns: []string{"l_orderkey"},
		RefTable: "orders", RefColumns: []string{"o_orderkey"},
	}}

	s, err := newSearchCtx(2, defaultCostParams())
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: lineitem, baseRows: 6000000},
		{table: orders, baseRows: 1500000},
	}
	outer, inner := jrsRels(6000000, 1500000)

	rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{jrsEq("l_orderkey", "o_orderkey", noEquivClass)})
	wantRows(t, rows, 6000000, "FK child joined to its parent")
	if rows == clampRowEst(6000000.0*1500000.0/6000000.0) {
		t.Fatal("the divisor was the CHILD's raw count — an FK bounds the join by the PARENT's")
	}
}

// TestCalcJoinrelSizeInvalidFKIgnored: a NOT VALID / NOT ENFORCED constraint
// proves nothing about the rows already in the table, so it must not license a
// no-fan-out estimate.
func TestCalcJoinrelSizeInvalidFKIgnored(t *testing.T) {
	c := catalog.NewInMemory()
	orders := jsTable(t, c, "orders", []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 1500000, catalog.ColumnStats{NDistinct: 100000})
	lineitem := jsTable(t, c, "lineitem", []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000, catalog.ColumnStats{NDistinct: 100000})
	lineitem.ForeignKeys = []catalog.ForeignKey{{
		Name: "lineitem_orderkey_fkey", Columns: []string{"l_orderkey"},
		RefTable: "orders", RefColumns: []string{"o_orderkey"}, NotValid: true,
	}}

	s, err := newSearchCtx(2, defaultCostParams())
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: lineitem, baseRows: 6000000},
		{table: orders, baseRows: 1500000},
	}
	outer, inner := jrsRels(6000000, 1500000)

	rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{jrsEq("l_orderkey", "o_orderkey", noEquivClass)})
	wantRows(t, rows, clampRowEst(6000000.0*1500000.0/100000.0), "NOT VALID FK")
}

// TestCalcJoinrelSizeClauseConsumedOnce: when BOTH sides can prove a key over
// the same clause, the tighter bound is taken and the clause is charged once.
// Charging both would square the restriction — the double-count PG's
// remove-then-substitute structure exists to prevent.
func TestCalcJoinrelSizeClauseConsumedOnce(t *testing.T) {
	c := catalog.NewInMemory()
	orders := jsTable(t, c, "orders", []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 1500000, catalog.ColumnStats{NDistinct: 1000})
	shipments := jsTable(t, c, "shipments", []catalog.Column{
		{Name: "s_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000, catalog.ColumnStats{NDistinct: 1000})
	for _, spec := range []struct {
		name string
		tbl  *catalog.Table
		col  string
	}{{"orders_pkey", orders, "o_orderkey"}, {"shipments_pkey", shipments, "s_orderkey"}} {
		if _, err := c.CreateIndex(parser.ObjectName{Name: spec.name}, spec.tbl,
			[]string{spec.col}, true, "btree", true); err != nil {
			t.Fatal(err)
		}
	}

	s, err := newSearchCtx(2, defaultCostParams())
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: shipments, baseRows: 6000000},
		{table: orders, baseRows: 1500000},
	}
	outer, inner := jrsRels(6000000, 1500000)

	rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{jrsEq("s_orderkey", "o_orderkey", noEquivClass)})
	// The larger raw count (6e6) is the tighter bound; applying the smaller
	// one as well would give 250000.
	wantRows(t, rows, 1500000, "unique on both sides")
}

// TestCalcJoinrelSizeEquivClassReduction: two clauses of one equivalence class
// are one restriction, so only one may be charged. Without the reduction the
// estimate here would be 200000× tighter than the truth — 04 §5's double-count.
func TestCalcJoinrelSizeEquivClassReduction(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 800000)

	explicit := jrsEq("l_partkey", "ps_partkey", 0)
	inferred := jrsEq("l_partkey", "ps_partkey", 0)
	inferred.inferred = true

	rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{explicit, inferred})
	wantRows(t, rows, clampRowEst(6000000.0*800000.0/200000.0), "two members of one class")
}

// TestOneClausePerEquivClassIsSelectivityClauses is the refactor's tripwire.
// `selectivityClauses` and `calcJoinrelSize` must reduce identically — they are
// the same rule reached from opposite directions, and the failure mode of a
// second copy is that only one of them learns about a future change.
func TestOneClausePerEquivClassIsSelectivityClauses(t *testing.T) {
	explicit := jrsEq("l_partkey", "ps_partkey", 0)
	inferred := jrsEq("l_partkey", "ps_partkey", 0)
	inferred.inferred = true
	loose := jrsEq("l_suppkey", "ps_suppkey", noEquivClass)
	l := &restrictInfoList{all: []*restrictInfo{inferred, explicit, loose}, nclasses: 1}

	viaList := l.selectivityClauses(relsetOf(0), relsetOf(1))
	viaFunc := oneClausePerEquivClass(l.clausesFor(relsetOf(0), relsetOf(1)))
	if len(viaList) != len(viaFunc) {
		t.Fatalf("selectivityClauses gave %d clauses, oneClausePerEquivClass gave %d", len(viaList), len(viaFunc))
	}
	for i := range viaList {
		if viaList[i] != viaFunc[i] {
			t.Fatalf("clause %d differs between the two doors", i)
		}
	}
	if len(viaList) != 2 || viaList[0] != explicit {
		t.Fatalf("reduction picked %v; want [explicit, loose] — explicit beats inferred", viaList)
	}
}

// TestCalcJoinrelSizeNoCatalogFallsBack: a nil catalog means no uniqueness
// evidence, not an error — the estimate degrades to the per-clause answer.
func TestCalcJoinrelSizeNoCatalogFallsBack(t *testing.T) {
	_, partsupp, lineitem := jrsCatalog(t)
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 800000)

	rows, _ := s.calcJoinrelSize(nil, outer, inner, []*restrictInfo{jrsEq("l_partkey", "ps_partkey", noEquivClass)})
	wantRows(t, rows, clampRowEst(6000000.0*800000.0/200000.0), "nil catalog")
}

// TestJoinRelBuilderSizesOnceAndAddsPaths: the seam is closed. `makeJoinRel`
// must take its rows from `calcJoinrelSize` and its paths from
// `addPathsToJoinrel`, through ONE object — and it must size the relset only on
// the create path, so a second pair spanning the same rels cannot revise a
// figure `add_path` has already compared against.
func TestJoinRelBuilderSizesOnceAndAddsPaths(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"}, partsupp,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	s := jrsCtx(t, lineitem, partsupp)
	for i, rows := range []float64{6000000, 800000} {
		rel := newRelOptInfo(relsetOf(i), rows, 32)
		if err := s.addRel(rel); err != nil {
			t.Fatal(err)
		}
		addPath(rel, &Path{Kind: PathPrebuilt, Rel: rel, Rows: rows, Cost: Cost{Total: rows}})
		setCheapest(rel)
	}
	s.clauses = &restrictInfoList{all: []*restrictInfo{
		jrsEq("l_partkey", "ps_partkey", noEquivClass),
		jrsEq("l_suppkey", "ps_suppkey", noEquivClass),
	}}
	s.builder = newJoinRelBuilder(s, c)

	joinrel, err := s.makeJoinRel(s.findRel(relsetOf(0)), s.findRel(relsetOf(1)))
	if err != nil {
		t.Fatalf("makeJoinRel: %v", err)
	}
	wantRows(t, joinrel.Rows, 6000000, "joinrel through the concrete builder")
	if len(joinrel.Pathlist) == 0 {
		t.Fatal("the builder added no paths — sizing and path generation are not bound together")
	}

	// The mirror pair must find the same rel and must not resize it.
	again, err := s.makeJoinRel(s.findRel(relsetOf(1)), s.findRel(relsetOf(0)))
	if err != nil {
		t.Fatalf("makeJoinRel (mirror): %v", err)
	}
	if again != joinrel {
		t.Fatal("the second pair built a second RelOptInfo for one relset")
	}
	wantRows(t, again.Rows, 6000000, "joinrel after the mirror pair")
}

// ---------------------------------------------------------------------------
// M0127-P5.6-c — the clamp discipline (04 §3.3).
//
// Two clamps sit after `outer × inner × fkselec × jselec`, and the tests below
// pin them apart, because their justifications are different in kind: the
// key-implied bound is a counting argument that is always true, and the
// `max(l, r)` cap is a heuristic that must NOT fire on an estimate derived from
// statistics.
// ---------------------------------------------------------------------------

// TestCalcJoinrelSizeKeyBoundClampsStaleStats: the case the structural bound
// exists for. A proven key normally makes the product land exactly ON the bound
// (`|L|·|R_raw|/R_raw`), so the clamp is invisible — until the key side's row
// ESTIMATE and its ANALYZE-time raw count disagree. Here `partsupp` has grown
// 10× since it was analysed, so the divisor is a tenth of the rows the search
// thinks it will read and the product claims 60M rows from a join in which each
// of 6M `lineitem` rows can match at most one `partsupp` row.
//
// 6M is not a tighter guess than 60M; it is the largest number the join can
// possibly produce.
func TestCalcJoinrelSizeKeyBoundClampsStaleStats(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"}, partsupp,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 8000000) // partsupp analysed at 800k, now 8M

	clauses := []*restrictInfo{
		jrsEq("l_partkey", "ps_partkey", noEquivClass),
		jrsEq("l_suppkey", "ps_suppkey", noEquivClass),
	}
	rows, _ := s.calcJoinrelSize(c, outer, inner, clauses)
	wantRows(t, rows, 6000000, "stale key-side statistics")

	if unclamped := clampRowEst(6000000.0 * 8000000.0 / 800000.0); rows >= unclamped {
		t.Fatalf("rows=%v was not clamped below the raw product %v", rows, unclamped)
	}
}

// TestCalcJoinrelSizeKeyBoundNeedsASingleRelKeySide: the soundness restriction,
// and the reason the clamp is not simply "the other side's rows".
//
// This is the previous test with ONE difference — the key relation now sits
// inside a two-relation side. The counting argument no longer holds: a join
// below may already have duplicated `partsupp`'s rows, so an outer row matching
// a single `partsupp` row can match several rows of that side, and the output
// may legitimately exceed the outer's row count. The estimate must be left
// alone rather than truncated to a bound that is not a bound.
func TestCalcJoinrelSizeKeyBoundNeedsASingleRelKeySide(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"}, partsupp,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	supplier := jsTable(t, c, "supplier", []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int4"}},
	}, 10000, catalog.ColumnStats{NDistinct: 10000})

	s, err := newSearchCtx(3, defaultCostParams())
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: lineitem, baseRows: 6000000},
		{table: partsupp, baseRows: 800000},
		{table: supplier, baseRows: 10000},
	}
	outer := newRelOptInfo(relsetOf(0), 6000000, 40)
	inner := newRelOptInfo(relsetOf(1, 2), 8000000, 24) // partsupp ⋈ supplier

	clauses := []*restrictInfo{
		jrsEq("l_partkey", "ps_partkey", noEquivClass),
		jrsEq("l_suppkey", "ps_suppkey", noEquivClass),
	}
	rows, _ := s.calcJoinrelSize(c, outer, inner, clauses)
	wantRows(t, rows, clampRowEst(6000000.0*8000000.0/800000.0), "key relation inside a joinrel")
}

// jrsUnanalysed is two tables with a `RowCount` but no per-column statistics —
// the state a table is in before anyone has run ANALYZE, and the only state in
// which 04 §3.3's fallback cap is allowed to fire.
func jrsUnanalysed(t *testing.T) (catalog.Catalog, *searchCtx) {
	t.Helper()
	c := catalog.NewInMemory()
	orders := jsTable(t, c, "orders", []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 800000)
	lineitem := jsTable(t, c, "lineitem", []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000)
	s, err := newSearchCtx(2, defaultCostParams())
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: lineitem, baseRows: 6000000},
		{table: orders, baseRows: 800000},
	}
	return c, s
}

// TestCalcJoinrelSizeFallbackCapWithoutStats: M0126-0010's cap
// (cardinality.go:400-406), carried into the new sizer for the case it was
// written for. Neither column is analysed, so both ndistincts are
// DEFAULT_NUM_DISTINCT and the whole estimate is a constant from selfuncs.h;
// 24 billion rows is not a measurement of anything, and the cap replaces it
// with the invariant a non-cross equi-join obeys — no more rows than the larger
// input.
func TestCalcJoinrelSizeFallbackCapWithoutStats(t *testing.T) {
	c, s := jrsUnanalysed(t)
	outer, inner := jrsRels(6000000, 800000)

	rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{jrsEq("l_orderkey", "o_orderkey", noEquivClass)})
	wantRows(t, rows, 6000000, "two unanalysed columns")

	if uncapped := clampRowEst(6000000.0 * 800000.0 / defaultNumDistinct); rows >= uncapped {
		t.Fatalf("rows=%v was not capped below the default-selectivity product %v", rows, uncapped)
	}
}

// TestCalcJoinrelSizeInequalityIsCapped: the fallback condition is a property of
// the CLAUSE ARM, not merely of missing statistics. Both columns here are
// analysed, but `scalarltjoinsel` has no model at all (selfuncs.c:2908 returns
// DEFAULT_INEQ_SEL unconditionally), so the statistics never entered the
// answer and the estimate is as much a guess as the unanalysed case above.
func TestCalcJoinrelSizeInequalityIsCapped(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 800000)

	ri := jrsEq("l_partkey", "ps_partkey", noEquivClass)
	ri.clause.(*BinaryOp).Op = parser.OpLt
	ri.isEquijoin = false

	rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{ri})
	wantRows(t, rows, 6000000, "inequality join clause")
}

// TestCalcJoinrelSizeMeasuredBlowUpIsNotCapped: the other half of the cap's
// condition, and the reason it cannot simply always fire. 100 distinct values
// on each side of 6M and 800k rows is a genuine many-to-many join that really
// does produce billions of rows; capping it at 6M would hide a blow-up the
// planner must see in order to avoid it.
func TestCalcJoinrelSizeMeasuredBlowUpIsNotCapped(t *testing.T) {
	c := catalog.NewInMemory()
	orders := jsTable(t, c, "orders", []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 800000, catalog.ColumnStats{NDistinct: 100})
	lineitem := jsTable(t, c, "lineitem", []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000, catalog.ColumnStats{NDistinct: 100})
	s, err := newSearchCtx(2, defaultCostParams())
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: lineitem, baseRows: 6000000},
		{table: orders, baseRows: 800000},
	}
	outer, inner := jrsRels(6000000, 800000)

	rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{jrsEq("l_orderkey", "o_orderkey", noEquivClass)})
	wantRows(t, rows, clampRowEst(6000000.0*800000.0/100.0), "a measured many-to-many join")
}

// TestCalcJoinrelSizeCrossProductIsNotCapped: with no restriction clauses at
// all the product IS the answer, so the cap must not read "no measured clause"
// as "guess". The legacy cap guards the same case by excluding
// `JoinTypeCross` explicitly (cardinality.go:383); here the empty residual is
// what carries it.
func TestCalcJoinrelSizeCrossProductIsNotCapped(t *testing.T) {
	c, s := jrsUnanalysed(t)
	outer, inner := jrsRels(6000000, 800000)

	rows, _ := s.calcJoinrelSize(c, outer, inner, nil)
	wantRows(t, rows, clampRowEst(6000000.0*800000.0), "cross product")
}
