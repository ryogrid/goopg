package optimizer

// M0127-P5.6-e-ii — the two class-(a) causes the 2026-08-04 estimate-audit
// baseline isolated (design leftdeep-joins/09 §5.2), plus the coordinate
// substrate both of them needed.
//
// The baseline (analysis/leftdeep-joins/2026-08-04-p56e-baseline.txt) put five
// joinrels over the 10³ tripwire. Three of them share a single root cause that
// these tests pin:
//
//   - a SEMI/ANTI joinrel was sized with the INNER formula `l·r/nd`, so it
//     could exceed its own outer input — Q18's final SEMI carried
//     1 756 987 324 rows against 70 actual. Upstream scales the OUTER by the
//     match fraction (costsize.c `calc_joinrel_size_estimate`, JOIN_SEMI /
//     JOIN_ANTI) and never multiplies by the inner's size.
//   - a joinrel's non-equi restriction contributed no selectivity at all —
//     Q19's whole WHERE is one three-branch OR and the estimate credited it
//     nothing (4.3 × 10⁷ vs 131 actual).
//   - `columnStatsForChild` could not resolve a column THROUGH a join, so a
//     join-level restriction had no stats to price with — and its Project arm
//     did not remap through `Targets`, so what it did resolve could be the
//     wrong column. Its ndistinct twin deliberately still stops at a join:
//     that arm feeds the uncapped `l·r/nd` formula and compounds (P5.6-e-iii,
//     and TestColumnNDistinctDeliberatelyStopsAtAJoin below).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// scanWithStats builds a stats-bearing SeqScan whose Output() is the table's
// own column list, so join coordinate arithmetic in the tests below is the
// same arithmetic the planner does.
func scanWithStats(name string, rows int64, ndistinct ...int64) *SeqScan {
	tbl := statsTable(name, rows, ndistinct...)
	return &SeqScan{Table: tbl, schema: tableSchema(tbl)}
}

// mergedJoin wires a Join the way the planner does: schema = left‖right, which
// is the coordinate space `Predicate` is written in.
func mergedJoin(t JoinType, left, right Node) *Join {
	j := &Join{Type: t, Algo: JoinAlgoHash, Left: left, Right: right}
	j.schema = append(append(Schema(nil), left.Output()...), right.Output()...)
	return j
}

func jrCol(idx int) *ColumnRef {
	return &ColumnRef{Index: idx, Type: catalog.Type{Name: "int4"}}
}

// keyed attaches the single equi-pair the way fillJoinHashKeys does: both
// operands in the MERGED left‖right coordinate space (`splitEqualityForHash`
// classifies them by `Index < leftWidth`), and the same pair published in
// HashKeys. rightIdx is a column offset within the RIGHT input.
func keyed(j *Join, leftIdx, rightIdx int) *Join {
	j.LeftKey = jrCol(leftIdx)
	j.RightKey = jrCol(len(j.Left.Output()) + rightIdx)
	j.HashKeys = []JoinKeyPair{{Left: j.LeftKey, Right: j.RightKey}}
	return j
}

// --- (1) SEMI / ANTI are sized from the outer, not from l·r/nd -------------

func TestEstimateJoinSemiScalesOuterByMatchFraction(t *testing.T) {
	// nd1 = 1000 distinct outer keys, nd2 = 100 distinct inner keys →
	// eqjoinsel_semi's `nd2/nd1` = 0.1 of the outer's 1000 rows.
	outer := scanWithStats("o", 1000, 1000)
	inner := scanWithStats("i", 500, 100)
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	if got, want := EstimateRows(j), int64(100); got != want {
		t.Fatalf("semi join estimate = %d, want %d (outer 1000 × nd2/nd1)", got, want)
	}
	// The property the Q18 miss violated: a semi join cannot emit more rows
	// than its outer input. The pre-fix formula returned l·r/nd = 5000 here.
	if got := EstimateRows(j); got > EstimateRows(outer) {
		t.Fatalf("semi join estimate %d exceeds outer input %d", got, EstimateRows(outer))
	}
}

func TestEstimateJoinSemiAllOuterRowsMatchWhenNd1LEQNd2(t *testing.T) {
	// nd1 <= nd2 is upstream's "assume every non-null outer row has a
	// partner" case — selectivity 1.0, not nd2/nd1 > 1.
	outer := scanWithStats("o", 1000, 100)
	inner := scanWithStats("i", 500, 400)
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)
	if got, want := EstimateRows(j), int64(1000); got != want {
		t.Fatalf("semi join estimate = %d, want %d (nd1 <= nd2 → all outer rows)", got, want)
	}
}

func TestEstimateJoinSemiPuntsToHalfWithoutInnerStats(t *testing.T) {
	// Inner ndistinct unknown and the inner relation is far larger than
	// DEFAULT_NUM_DISTINCT, so the clamp does not rescue it: upstream's
	// eqjoinsel_semi punts to 0.5 rather than guessing.
	outer := scanWithStats("o", 1000, 1000)
	inner := &SeqScan{Table: statsTable("i", 5000, 0)}
	inner.schema = tableSchema(inner.Table)
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)
	if got, want := EstimateRows(j), int64(500); got != want {
		t.Fatalf("semi join estimate = %d, want %d (isdefault → 0.5)", got, want)
	}
}

func TestEstimateJoinSemiClampsInnerNDistinctToInnerRows(t *testing.T) {
	// The asymmetric clamp of eqjoinsel_semi: nd2 unknown defaults to 200,
	// but the inner relation only has 10 rows, so it cannot hold more than
	// 10 distinct values — and once clamped the estimate stops being a
	// default. 1000 outer × 10/1000 = 10.
	outer := scanWithStats("o", 1000, 1000)
	inner := &SeqScan{Table: statsTable("i", 10, 0)}
	inner.schema = tableSchema(inner.Table)
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)
	if got, want := EstimateRows(j), int64(10); got != want {
		t.Fatalf("semi join estimate = %d, want %d (nd2 clamped to inner rows)", got, want)
	}
}

func TestEstimateJoinAntiIsTheComplementOfTheMatchFraction(t *testing.T) {
	// costsize.c: JOIN_ANTI is outer_rows * (1 - jselec). Same inputs as
	// the first SEMI case, so 1000 × (1 - 0.1).
	outer := scanWithStats("o", 1000, 1000)
	inner := scanWithStats("i", 500, 100)
	j := keyed(mergedJoin(JoinTypeAnti, outer, inner), 0, 0)
	if got, want := EstimateRows(j), int64(900); got != want {
		t.Fatalf("anti join estimate = %d, want %d (outer × (1 - nd2/nd1))", got, want)
	}
}

// --- (2) the non-equi restriction is priced --------------------------------

func TestEstimateJoinPricesTwoSidedResidual(t *testing.T) {
	left := scanWithStats("l", 1000, 100)
	right := scanWithStats("r", 900, 100)
	j := keyed(mergedJoin(JoinTypeInner, left, right), 0, 0)
	// Equi-only baseline: 1000·900/100.
	base := EstimateRows(j)
	if base != 9000 {
		t.Fatalf("equi-only estimate = %d, want 9000", base)
	}
	// Add a genuinely two-sided residual the hash key does not enforce.
	// `left.c0 < right.c0` has no upstream join-selectivity model, so it
	// prices at DEFAULT_INEQ_SEL (1/3) — the point of the test is that it
	// prices at all, where before it contributed exactly nothing.
	j.Predicate = &BinaryOp{
		Op:    parser.OpLt,
		Left:  jrCol(0),
		Right: jrCol(len(left.Output())),
	}
	if got, want := EstimateRows(j), int64(3000); got != want {
		t.Fatalf("estimate with two-sided residual = %d, want %d (9000 × 1/3)", got, want)
	}
}

func TestEstimateJoinDoesNotRePriceSingleSidedResidual(t *testing.T) {
	// A one-sided conjunct is a baserestrictinfo in upstream's model and is
	// already priced into the component rel's size — goopg pushes it down as
	// a *Filter and ALSO leaves a copy on the join for the executor to
	// re-apply (Q3's three-conjunct `Filter:`). Counting it here would
	// double-count it.
	left := scanWithStats("l", 1000, 100)
	right := scanWithStats("r", 900, 100)
	j := keyed(mergedJoin(JoinTypeInner, left, right), 0, 0)
	j.Predicate = &BinaryOp{
		Op:    parser.OpLt,
		Left:  jrCol(0),
		Right: &IntegerConst{Value: 5},
	}
	if got, want := EstimateRows(j), int64(9000); got != want {
		t.Fatalf("estimate with one-sided residual = %d, want %d (must not re-price)", got, want)
	}
}

func TestEstimateJoinDoesNotRePriceTheEquiKeyItself(t *testing.T) {
	// The published key pair IS `l·r/nd`; charging it again through
	// clauseSelectivity would apply DEFAULT_EQ_SEL on top of its own
	// answer. Join.Residual() performs the subtraction the executor already
	// relies on, so the two cannot drift.
	left := scanWithStats("l", 1000, 100)
	right := scanWithStats("r", 900, 100)
	j := keyed(mergedJoin(JoinTypeInner, left, right), 0, 0)
	j.Predicate = &BinaryOp{
		Op:    parser.OpEq,
		Left:  jrCol(0),
		Right: jrCol(len(left.Output())),
	}
	if got, want := EstimateRows(j), int64(9000); got != want {
		t.Fatalf("estimate = %d, want %d (key pair must not be priced twice)", got, want)
	}
}

// --- (3) the coordinate substrate ------------------------------------------

func TestSemiJoinResolvesTheRightKeyInMergedCoordinates(t *testing.T) {
	// RightKey's Index counts from the start of the MERGED schema, so
	// resolving it against j.Right without shifting reads past the right
	// child's own columns. The right key here is the right input's LAST
	// column — merged index 2+2 = 4, which a 3-column child cannot hold.
	// The semi path shifts it down and reads 900.
	outer := scanWithStats("o", 9000, 9000, 9000)
	inner := scanWithStats("i", 5000, 5, 7, 900)
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 2)
	// 9000 outer × nd2/nd1 = 9000 × 900/9000.
	if got, want := EstimateRows(j), int64(900); got != want {
		t.Fatalf("semi estimate = %d, want %d (nd2 = right input's 3rd column)", got, want)
	}
}

func TestEstimateJoinResolvesTheRightKeyInMergedCoordinates(t *testing.T) {
	// M0127-P5.6-e-iii closed the gap the former
	// TestEstimateJoinLeavesTheEquiKeyNDistinctGapOpen tripwire pinned.
	// The right key names the right input's column 2, which `keyed` writes
	// as the MERGED index 2+2 = 4 — a coordinate a 3-column child cannot
	// hold. The old lookup handed 4 straight to `j.Right`, read out of
	// range, and took the "nd unavailable" branch, so the right side never
	// entered max(nd) and the join divided by nd_l = 100 alone:
	// 1000*900/100 = 9000.
	//
	// Shifted down by the left's width it resolves to nd_r = 900, which
	// wins max(nd): 1000*900/900 = 1000. That is the PK-side ndistinct an
	// FK-PK join must divide by, and reading it is what stops a join chain
	// from compounding.
	left := scanWithStats("l", 1000, 100, 100)
	right := scanWithStats("r", 900, 5, 7, 900)
	j := keyed(mergedJoin(JoinTypeInner, left, right), 0, 2)
	if got, want := EstimateRows(j), int64(1000); got != want {
		t.Fatalf("estimate = %d, want %d (nd_r=900 must win max(nd))", got, want)
	}

	// The other direction: the left key's ndistinct still wins when it is
	// the larger, so the shift did not simply replace one side with the
	// other. Right column 0 has nd 5; max(100, 5) = 100 → 1000*900/100.
	left2 := scanWithStats("l", 1000, 100, 100)
	right2 := scanWithStats("r", 900, 5, 7, 900)
	j2 := keyed(mergedJoin(JoinTypeInner, left2, right2), 0, 0)
	if got, want := EstimateRows(j2), int64(9000); got != want {
		t.Fatalf("estimate = %d, want %d (nd_l=100 still wins max(nd))", got, want)
	}
}

func TestColumnNDistinctResolvesThroughJoin(t *testing.T) {
	// M0127-P5.6-e-iii: the two column lookups no longer diverge. The
	// ndistinct twin was withheld from descending through a *Join while
	// ANALYZE stored a SAMPLE-saturated ndistinct, because it feeds the
	// uncapped `l·r/nd` formula and a saturated nd compounds up a join
	// chain (Q9: 124.7× → 176 424× over). Haas-Stokes scaling landed with
	// this arm, so both twins now resolve a merged coordinate to the base
	// relation's stats — upstream's `examine_variable` behaviour.
	left := scanWithStats("l", 1000, 11, 12)
	right := scanWithStats("r", 900, 21, 22)
	j := mergedJoin(JoinTypeInner, left, right)
	if got := columnNDistinctForChild(1, j); got != 12 {
		t.Fatalf("columnNDistinctForChild(1, join) = %d, want 12 (left col 1)", got)
	}
	if st := columnStatsForChild(1, j); st == nil || st.NDistinct != 12 {
		t.Fatalf("columnStatsForChild(1, join) did not resolve through the join: %+v", st)
	}
	// Right side, merged coordinate: left is 2 wide, so index 3 is the
	// right's column 1. Both twins must agree.
	lw := len(left.Output())
	if got := columnNDistinctForChild(lw+1, j); got != 22 {
		t.Fatalf("columnNDistinctForChild(right.c1, join) = %d, want 22", got)
	}
	if st := columnStatsForChild(lw+1, j); st == nil || st.NDistinct != 22 {
		t.Fatalf("stats/ndistinct siblings disagree through a join: %+v", st)
	}
}



func TestColumnStatsResolveThroughJoin(t *testing.T) {
	left := scanWithStats("l", 1000, 11, 12)
	right := scanWithStats("r", 900, 21, 22)
	j := mergedJoin(JoinTypeInner, left, right)
	lw := len(left.Output())
	st := columnStatsForChild(lw+1, j)
	if st == nil {
		t.Fatal("columnStatsForChild through a join returned nil for a right-side column")
	}
	if st.NDistinct != 22 {
		t.Fatalf("columnStatsForChild(right.c1) NDistinct = %d, want 22", st.NDistinct)
	}
}

func TestColumnStatsThroughProjectRemapsTargets(t *testing.T) {
	// The sibling divergence this loop closed: the ndistinct twin has
	// remapped through Targets since M0125-0038; the stats lookup passed
	// `idx` straight through and therefore answered with ANOTHER column's
	// MCV list and histogram whenever the Project was not the identity.
	scan := scanWithStats("t", 1000, 11, 12)
	proj := &Project{Child: scan, Targets: []Expr{jrCol(1), jrCol(0)}}
	st := columnStatsForChild(0, proj)
	if st == nil {
		t.Fatal("columnStatsForChild through a Project returned nil")
	}
	if st.NDistinct != 12 {
		t.Fatalf("columnStatsForChild(0, project[c1, c0]) NDistinct = %d, want 12 (target remap)", st.NDistinct)
	}
	if got := columnNDistinctForChild(0, proj); got != st.NDistinct {
		t.Fatalf("stats/ndistinct siblings disagree through a Project: %d vs %d", st.NDistinct, got)
	}
}
