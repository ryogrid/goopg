package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

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
	sortInnerAndOuter(nil, narrow, scanRel(a, 10000, 100), scanRel(b, 5000, 50), cp, parser.JoinInner, false, keys, nil,
		func([]*restrictInfo) float64 { return joinRows },
		func([]*restrictInfo) (float64, float64) { return 1, 1 }, 0)

	// The merge emits 4x the joinrel's rows before the residual filters them,
	// exactly the Q9 ratio.
	wide := newRelOptInfo(a|b, joinRows, 64)
	sortInnerAndOuter(nil, wide, scanRel(a, 10000, 100), scanRel(b, 5000, 50), cp, parser.JoinInner, false, keys, residual,
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

// TestMergeJoinTuplesIsApproxTupleCount pins the helper against
// approx_tuple_count (costsize.c): the cross product of the input rows times
// each merge clause's selectivity, clamped to at least one row. It is NOT
// recovered from the joinrel's clamped row count — that is what inflated
// TPC-DS Q47's one-row `v1_lag ⋈ v1` merge to 200 emitted tuples
// (M0146-0005bd).
func TestMergeJoinTuplesIsApproxTupleCount(t *testing.T) {
	s := &searchCtx{}
	sel := func(v float64) *restrictInfo {
		return &restrictInfo{relids: 3, ecID: noEquivClass, normSelec: v, normSelecValid: true}
	}
	if got := s.mergeJoinTuples(nil, 50, 40); got != 2000 {
		t.Errorf("no merge clause: got %v, want the 50x40 cross product", got)
	}
	if got := s.mergeJoinTuples([]*restrictInfo{sel(0.1), sel(0.5)}, 100, 40); got != 200 {
		t.Errorf("two clauses at 0.1 and 0.5 over 100x40: got %v, want 200", got)
	}
	// Four independent 1/200 clauses over 3823x2 rows — Q47's lag x v1 —
	// estimate a fraction of a row; clamp_row_est makes it one.
	c := sel(1.0 / 200)
	if got := s.mergeJoinTuples([]*restrictInfo{c, c, c, c}, 3823, 2); got != 1 {
		t.Errorf("Q47-shaped merge: got %v, want the clamped 1", got)
	}
}
