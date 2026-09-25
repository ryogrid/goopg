package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestNestLoopPathInheritsOuterPathkeys pins M0146-0005l: a nested loop
// streams its outer row by row, so match_unsorted_outer (joinpath.c) gives it
// build_join_pathkeys of the outer path's ordering. Without it an ordered
// outer (TPC-DS Q44's rank merge join) lost its order at the loop and the
// LIMIT's fractional election could not prefer the nested loop PG picks.
// FULL and RIGHT deliver no ordering.
func TestNestLoopPathInheritsOuterPathkeys(t *testing.T) {
	cp := defaultCostParams()
	for _, tc := range []struct {
		jt   parser.JoinType
		want int
	}{
		{parser.JoinInner, 1},
		{parser.JoinLeft, 1},
		{parser.JoinFull, 0},
	} {
		outer := newRelOptInfo(relsetOf(0), 100, 8)
		addPath(outer, &Path{Kind: PathIndexScan, Rel: outer, Rows: 100, Cost: Cost{Total: 10}, Pathkeys: ascKeys(1)}, "test")
		setCheapest(outer)
		inner := newRelOptInfo(relsetOf(1), 10, 8)
		addPath(inner, &Path{Kind: PathSeqScan, Rel: inner, Rows: 10, Cost: Cost{Total: 1}}, "test")
		setCheapest(inner)
		joinrel := newRelOptInfo(relsetOf(0, 1), 1000, 16)

		addNestLoopPath(joinrel, outer, inner, cp, tc.jt, nil, uniqueSideNone, nil, semiAntiJoinFactors{})
		if len(joinrel.Pathlist) != 1 {
			t.Fatalf("jt %v: %d paths, want 1", tc.jt, len(joinrel.Pathlist))
		}
		if got := len(joinrel.Pathlist[0].Pathkeys); got != tc.want {
			t.Errorf("jt %v: nested loop has %d pathkeys, want %d", tc.jt, got, tc.want)
		}
	}
}

// TestNestLoopTriesEveryOuterPath pins M0146-0005m: match_unsorted_outer
// builds nested loops over every path of the outer rel, so an ordered outer
// that is NOT the cheapest-total one still yields an ordered nested loop for
// the LIMIT's fractional election (TPC-DS Q44). JOIN_UNIQUE_OUTER keeps its
// single cheapest-total outer.
func TestNestLoopTriesEveryOuterPath(t *testing.T) {
	cp := defaultCostParams()
	outer := newRelOptInfo(relsetOf(0), 100, 8)
	addPath(outer, &Path{Kind: PathSeqScan, Rel: outer, Rows: 100, Cost: Cost{Total: 10}}, "test")
	addPath(outer, &Path{Kind: PathIndexScan, Rel: outer, Rows: 100, Cost: Cost{Total: 20}, Pathkeys: ascKeys(1)}, "test")
	setCheapest(outer)
	if len(outer.Pathlist) != 2 {
		t.Fatalf("outer pathlist has %d paths, want the cheap and the ordered one", len(outer.Pathlist))
	}
	if got := len(nestLoopOuterPaths(outer, uniqueSideNone, nil, cp)); got != 2 {
		t.Fatalf("nestLoopOuterPaths = %d outers, want 2", got)
	}

	inner := newRelOptInfo(relsetOf(1), 10, 8)
	addPath(inner, &Path{Kind: PathSeqScan, Rel: inner, Rows: 10, Cost: Cost{Total: 1}}, "test")
	setCheapest(inner)
	joinrel := newRelOptInfo(relsetOf(0, 1), 1000, 16)
	addNestLoopPath(joinrel, outer, inner, cp, parser.JoinInner, nil, uniqueSideNone, nil, semiAntiJoinFactors{})
	ordered := false
	for _, p := range joinrel.Pathlist {
		if len(p.Pathkeys) == 1 {
			ordered = true
		}
	}
	if !ordered {
		t.Fatal("no ordered nested loop over the non-cheapest ordered outer")
	}
}
