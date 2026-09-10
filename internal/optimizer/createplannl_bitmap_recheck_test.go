package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCreateNestLoopBitmapJoinKeepsProbeBitmapQual is the R49 Slice-B update
// of the review/260831-2 OP1-3 guard. The NLI-bitmap arm used to clear
// `BitmapHeapScan.BitmapQual` and fold the probe clause into the join
// Predicate; Slice B moves the recheck down onto the probe (MOVE, not copy):
// `BitmapQual` carries the probe clause in merged outer++inner coordinates
// (inner-left, PG's `Recheck Cond:` orientation) for per-tuple evaluation
// against the combined row, and the Predicate keeps the residual only. The
// recheck moves — it must not vanish: once the per-probe bitmap exceeds
// work_mem, `tbmLossify` degrades pages to lossy and the heap scan yields
// every tuple on such a page, relying on the recheck qual to filter them
// (PG keeps `bitmapqualorig` for exactly this).
func TestCreateNestLoopBitmapJoinKeepsProbeBitmapQual(t *testing.T) {
	a, b := cpjTwoRel()
	idx := cpiIndex("a0")

	// `a.a0 = b.b1`: binding column 0 on the inner (relid 0), binding column 3
	// on the outer (relid 1). Under the merged layout `b0 b1 b2 a0 a1` that is
	// merged position 1 (outer) and merged position 3 (inner).
	clause := equiClauseOn(a.Relids, b.Relids, 0, 3)
	clause.clause = cpnEq(col(0), col(3))

	inner := &Path{
		Kind: PathBitmapHeapScan, Rel: a, Rows: 1, IndexInfo: idx,
		IndexClauses:  []indexPathClause{{ri: clause, indexCol: 0, key: col(3)}},
		RequiredOuter: b.Relids,
		Children: []*Path{{
			Kind: PathBitmapIndexScan, Rel: a, Rows: 1, IndexInfo: idx,
			IndexClauses:  []indexPathClause{{ri: clause, indexCol: 0, key: col(3)}},
			RequiredOuter: b.Relids,
		}},
	}
	// The residual is empty, exactly as `nestloopResidualClauses` leaves it once
	// the probe-enforced clause has been dropped.
	p := cpnNestLoopPath(cpjLeafPath(b), inner, nil)

	n, _ := createPlanNode(p)
	nli, ok := n.(*NestedLoopIndexJoin)
	if !ok {
		t.Fatalf("createPlan(parameterised bitmap PathNestLoop) = %T, want *NestedLoopIndexJoin", n)
	}
	// The residual is empty, so the Predicate is nil and no join line renders.
	if nli.Predicate != nil {
		t.Fatalf("Predicate = %v, want nil (probe clause moved onto the probe, residual empty)", nli.Predicate)
	}
	// The recheck moved, not vanished: BitmapQual carries the probe equality
	// in merged coordinates, inner-left (PG's `Recheck Cond:` orientation).
	// keyPairs orients outer-on-the-left (b.b1 merged position 1, a.a0
	// position 3); the arm flips to inner-first: col(3) = col(1).
	bhs, ok := nli.Inner.(*BitmapHeapScan)
	if !ok {
		t.Fatalf("Inner = %T, want *BitmapHeapScan", nli.Inner)
	}
	if len(bhs.BitmapQual) != 1 {
		t.Fatalf("len(BitmapQual) = %d, want 1 (the probe clause)", len(bhs.BitmapQual))
	}
	eq, ok := bhs.BitmapQual[0].(*BinaryOp)
	if !ok {
		t.Fatalf("BitmapQual[0] = %T, want the probe equality as a *BinaryOp", bhs.BitmapQual[0])
	}
	l, lok := eq.Left.(*ColumnRef)
	r, rok := eq.Right.(*ColumnRef)
	if !lok || !rok {
		t.Fatalf("BitmapQual[0] = %v, want two column references on the merged row", bhs.BitmapQual[0])
	}
	if l.Index != 3 || r.Index != 1 {
		t.Errorf("BitmapQual[0] = col(%d) = col(%d), want col(3) = col(1) inner-left on the merged row", l.Index, r.Index)
	}
}

// TestCreateNestLoopBitmapJoinSemiProbeQual pins review-note-8(b): a SEMI
// PathNestLoop over a parameterised bitmap inner gets the same treatment as
// INNER — probe clause in merged-coord BitmapQual, residual-only Predicate.
// The natural-shape census exercises only INNER (SEMI picks index-only/hash
// in the corpus), so the arm's type-indifference is pinned here, not there.
func TestCreateNestLoopBitmapJoinSemiProbeQual(t *testing.T) {
	a, b := cpjTwoRel()
	idx := cpiIndex("a0")

	clause := equiClauseOn(a.Relids, b.Relids, 0, 3)
	clause.clause = cpnEq(col(0), col(3))

	inner := &Path{
		Kind: PathBitmapHeapScan, Rel: a, Rows: 1, IndexInfo: idx,
		IndexClauses:  []indexPathClause{{ri: clause, indexCol: 0, key: col(3)}},
		RequiredOuter: b.Relids,
		Children: []*Path{{
			Kind: PathBitmapIndexScan, Rel: a, Rows: 1, IndexInfo: idx,
			IndexClauses:  []indexPathClause{{ri: clause, indexCol: 0, key: col(3)}},
			RequiredOuter: b.Relids,
		}},
	}
	p := cpnNestLoopPath(cpjLeafPath(b), inner, nil)
	p.Jointype = parser.JoinSemi

	n, _ := createPlanNode(p)
	nli, ok := n.(*NestedLoopIndexJoin)
	if !ok {
		t.Fatalf("createPlan(parameterised bitmap PathNestLoop semi) = %T, want *NestedLoopIndexJoin", n)
	}
	if nli.Type != JoinTypeSemi {
		t.Fatalf("Type = %v, want JoinTypeSemi", nli.Type)
	}
	if nli.Predicate != nil {
		t.Fatalf("Predicate = %v, want nil (residual empty)", nli.Predicate)
	}
	bhs, ok := nli.Inner.(*BitmapHeapScan)
	if !ok {
		t.Fatalf("Inner = %T, want *BitmapHeapScan", nli.Inner)
	}
	if len(bhs.BitmapQual) != 1 {
		t.Fatalf("len(BitmapQual) = %d, want 1 (the probe clause)", len(bhs.BitmapQual))
	}
	eq, ok := bhs.BitmapQual[0].(*BinaryOp)
	if !ok {
		t.Fatalf("BitmapQual[0] = %T, want *BinaryOp", bhs.BitmapQual[0])
	}
	l, lok := eq.Left.(*ColumnRef)
	r, rok := eq.Right.(*ColumnRef)
	if !lok || !rok || l.Index != 3 || r.Index != 1 {
		t.Fatalf("BitmapQual[0] = %v, want col(3) = col(1) inner-left", bhs.BitmapQual[0])
	}
}

// TestCreateNestLoopBitmapJoinKeylessProbe pins review-note-8(c): a
// parameterised bitmap path with zero index clauses (no probe keys) yields an
// empty BitmapQual and a residual-only Predicate — a full scan per probe,
// exactly as before Slice B. The planner never emits this shape today
// (parameterisation needs ≥1 key), so it is pinned here, not end-to-end.
func TestCreateNestLoopBitmapJoinKeylessProbe(t *testing.T) {
	a, b := cpjTwoRel()
	idx := cpiIndex("a0")

	inner := &Path{
		Kind: PathBitmapHeapScan, Rel: a, Rows: 1, IndexInfo: idx,
		RequiredOuter: b.Relids,
		Children: []*Path{{
			Kind: PathBitmapIndexScan, Rel: a, Rows: 1, IndexInfo: idx,
			RequiredOuter: b.Relids,
		}},
	}
	p := cpnNestLoopPath(cpjLeafPath(b), inner, nil)

	n, _ := createPlanNode(p)
	nli, ok := n.(*NestedLoopIndexJoin)
	if !ok {
		t.Fatalf("createPlan(keyless bitmap PathNestLoop) = %T, want *NestedLoopIndexJoin", n)
	}
	if nli.Predicate != nil {
		t.Fatalf("Predicate = %v, want nil (no probe clauses, residual empty)", nli.Predicate)
	}
	bhs, ok := nli.Inner.(*BitmapHeapScan)
	if !ok {
		t.Fatalf("Inner = %T, want *BitmapHeapScan", nli.Inner)
	}
	if len(bhs.BitmapQual) != 0 {
		t.Fatalf("len(BitmapQual) = %d, want 0 (no probe keys)", len(bhs.BitmapQual))
	}
}
