package optimizer

import "testing"

// TestCreateNestLoopBitmapJoinRechecksProbeClause is the review/260831-2 OP1-3
// guard. The NLI-bitmap arm cleared `BitmapHeapScan.BitmapQual` (it cannot be
// expressed in leaf-local coordinates for a per-outer-row probe) while
// `probeEnforcedClauses` had already dropped the same clause from the join
// residual — so NOTHING re-checked the join key. That is safe for the sibling
// INDEX arm, where the probe enforces its keys exactly, but not for a bitmap
// heap scan: once the per-probe bitmap exceeds work_mem, `tbmLossify` degrades
// pages to lossy and the heap scan yields every tuple on such a page, relying
// on the recheck qual to filter them (PG keeps `bitmapqualorig` for exactly
// this). The join predicate is where the recheck can be expressed, because it
// is evaluated on the merged outer++inner row.
func TestCreateNestLoopBitmapJoinRechecksProbeClause(t *testing.T) {
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
	if nli.Predicate == nil {
		t.Fatal("Predicate is nil: a lossy bitmap page would leak every tuple on it, unfiltered")
	}
	eq, ok := nli.Predicate.(*BinaryOp)
	if !ok {
		t.Fatalf("Predicate = %T, want the probe equality as a *BinaryOp", nli.Predicate)
	}
	l, lok := eq.Left.(*ColumnRef)
	r, rok := eq.Right.(*ColumnRef)
	if !lok || !rok {
		t.Fatalf("Predicate = %v, want two column references on the merged row", nli.Predicate)
	}
	// keyPairs orients outer-on-the-left: b.b1 is merged position 1, a.a0 is 3.
	if l.Index != 1 || r.Index != 3 {
		t.Errorf("Predicate = col(%d) = col(%d), want col(1) = col(3) on the merged row", l.Index, r.Index)
	}
}
