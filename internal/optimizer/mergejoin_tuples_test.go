package optimizer

import "testing"

// TestMergeJoinChargedOnMergeTuplesNotJoinrelRows pins
// impl/FINDING-mergejoin-costed-on-postfilter-rows.md.
//
// joinrel.Rows is what survives EVERY join clause. A merge join that can only
// use a SUBSET of the equi-clauses emits what survives the merge clauses alone
// and lets the residual filter the rest, so it must be charged over the larger
// number — PG's `mergejointuples` (final_cost_mergejoin, costsize.c:4045).
//
// Charging joinrel.Rows under-prices the operator by exactly the residual's
// selectivity. On TPC-H Q9's `lineitem x partsupp` that was a ~4x undercharge
// (24,989,610 tuples emitted, 6,001,255 charged), which made the merge path
// beat a two-key hash join at PostgreSQL's work_mem and lose at goopg's
// inflated default — the reason that default was load-bearing.
//
// The assertion is on COST, not on a plan shape: a merge path whose residual
// filters half the tuples must cost strictly more than the same path with no
// residual at all, by more than the residual's own qual-evaluation charge.
func TestMergeJoinChargedOnMergeTuplesNotJoinrelRows(t *testing.T) {
	const a, b = RelSet(1), RelSet(2)
	cp := defaultCostParams()
	keys := []*restrictInfo{equiClauseOn(a, b, 5, 6)}
	residual := []*restrictInfo{plainClause(a | b)}

	// Same joinrel row count in both arms — only the residual differs, so the
	// emitted-tuple count is the only thing that can move the cost.
	const joinRows = 2000

	narrow := newRelOptInfo(a|b, joinRows, 64)
	sortInnerAndOuter(narrow, scanRel(a, 10000, 100), scanRel(b, 5000, 50), cp, keys, nil,
		func([]*restrictInfo) float64 { return joinRows },
		func([]*restrictInfo) (float64, float64) { return 1, 1 }, 0)

	// The merge emits 4x the joinrel's rows before the residual filters them,
	// exactly the Q9 ratio.
	wide := newRelOptInfo(a|b, joinRows, 64)
	sortInnerAndOuter(wide, scanRel(a, 10000, 100), scanRel(b, 5000, 50), cp, keys, residual,
		func([]*restrictInfo) float64 { return joinRows * 4 },
		func([]*restrictInfo) (float64, float64) { return 1, 1 }, 0)

	np, wp := mergePathsOf(narrow), mergePathsOf(wide)
	if len(np) != 1 || len(wp) != 1 {
		t.Fatalf("expected one merge path per arm, got %d and %d", len(np), len(wp))
	}

	// The per-tuple charge alone is cpu_tuple_cost over the extra 3x rows.
	minExtra := cp.cpuTupleCost * (joinRows*4 - joinRows)
	if got := wp[0].Cost.Total - np[0].Cost.Total; got < minExtra {
		t.Errorf("merge emitting %d tuples cost only %.2f more than one emitting %d; "+
			"cpu_tuple_cost over the extra tuples alone is %.2f — the operator is "+
			"still being charged on the post-filter row count",
			joinRows*4, got, joinRows, minExtra)
	}
}

// TestMergeJoinTuplesRecoversPreFilterCount pins the helper directly: dividing
// the joinrel's rows by the residual's selectivity is what recovers the count
// the operator emits, and it must never report FEWER tuples than the joinrel
// has rows, nor more than the cross product.
func TestMergeJoinTuplesRecoversPreFilterCount(t *testing.T) {
	s := &searchCtx{}
	const rows = 1000.0

	if got := s.mergeJoinTuples(rows, nil, 5000, 4000); got != rows {
		t.Errorf("no residual must leave the count alone: got %v, want %v", got, rows)
	}
	// A residual can only ever raise the emitted count above the joinrel's rows.
	if got := s.mergeJoinTuples(rows, []*restrictInfo{plainClause(3)}, 5000, 4000); got < rows {
		t.Errorf("mergejointuples %v is below the joinrel's %v rows", got, rows)
	}
	// Never more pairs than the cross product — checked with a joinrel small
	// enough for the clamp to be reachable. (When the joinrel's own row count
	// already exceeds the cross product the inputs are degenerate, and the
	// helper deliberately returns the row count rather than a smaller number:
	// under-reporting is the failure mode being fixed.)
	if got := s.mergeJoinTuples(50, []*restrictInfo{plainClause(3)}, 10, 10); got > 100 {
		t.Errorf("mergejointuples %v exceeds the 10x10 cross product", got)
	}
}
