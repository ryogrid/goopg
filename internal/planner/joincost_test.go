package planner

import "testing"

// TestChooseInnerJoinAlgoFallbackWhenStatsMissing pins the M0006
// contract: when either input lacks a row estimate (zero return
// from EstimateRows because no ANALYZE), the cost selector
// declines and the call site keeps its rule-based JoinAlgoHash.
func TestChooseInnerJoinAlgoFallbackWhenStatsMissing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		l, r     int64
	}{
		{"left missing", 0, 100},
		{"right missing", 100, 0},
		{"both missing", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			algo, ok := chooseInnerJoinAlgo(tc.l, tc.r)
			if ok {
				t.Errorf("ok=true want false (no stats)")
			}
			if algo != JoinAlgoHash {
				t.Errorf("algo=%v want JoinAlgoHash (fallback)", algo)
			}
		})
	}
}

// TestChooseInnerJoinAlgoFavorsHashForBalanced: at L = R = 10000,
// hash beats merge and nested-loop by a wide margin under the
// unit-row cost model:
//   hash      = build(10k) + probe(10k) = 20000
//   merge     = 10k*log2(10k) + 10k*log2(10k) + 20k ≈ 286k
//   nestloop  = 10k * 10k = 100M
func TestChooseInnerJoinAlgoFavorsHashForBalanced(t *testing.T) {
	algo, ok := chooseInnerJoinAlgo(10_000, 10_000)
	if !ok {
		t.Fatal("ok=false want true")
	}
	if algo != JoinAlgoHash {
		t.Errorf("algo=%v want JoinAlgoHash for balanced 10k/10k", algo)
	}
}

// TestChooseInnerJoinAlgoFavorsNestLoopForVerySmallInputs: when
// both sides are very small, the absolute cost difference between
// algorithms shrinks, but hash still wins under the unit-row
// model — costHash(2,2)=4, costNL(2,2)=4. Tie on hash by
// construction. The test pins that the cost selector at least
// doesn't regress to merge for tiny inputs.
func TestChooseInnerJoinAlgoFavorsNestLoopForVerySmallInputs(t *testing.T) {
	algo, ok := chooseInnerJoinAlgo(1, 1)
	if !ok {
		t.Fatal("ok=false want true")
	}
	// 1*1 = 1 vs hash 1+1 = 2 → nested-loop wins on cost.
	if algo != JoinAlgoNestedLoop {
		t.Errorf("algo=%v want JoinAlgoNestedLoop for 1/1", algo)
	}
}

// TestChooseInnerJoinAlgoFavorsHashOverMergeAtModestScale:
// merge's sort cost dominates at modest input sizes — hash
// should win. v0 doesn't track input sortedness, so merge will
// rarely win bare INNER joins; pinning that here guards against
// future cost-model regressions that flip merge into the
// happy path.
func TestChooseInnerJoinAlgoFavorsHashOverMergeAtModestScale(t *testing.T) {
	algo, ok := chooseInnerJoinAlgo(100, 100)
	if !ok {
		t.Fatal("ok=false want true")
	}
	if algo == JoinAlgoMerge {
		t.Errorf("algo=JoinAlgoMerge for 100/100; merge sort cost should lose to hash")
	}
}

// M0127-P4.2 (design leftdeep-joins/07 §3). The claim under test is that RIGHT
// and FULL are no longer PINNED to merge — the executor can fill either side
// now, so the algorithm is a price comparison like everywhere else. What must
// NOT change is the stats-blind answer: with no estimate on one side the
// function declines and the caller keeps merge, which is what PG picks for the
// unanalysed inputs the regress suite is built from.
func TestChooseOuterFillJoinAlgo(t *testing.T) {
	defer func(prev bool) { hashOuterJoinEnabled = prev }(hashOuterJoinEnabled)
	hashOuterJoinEnabled = true
	for _, tc := range []struct {
		name     string
		l, r     int64
		wantAlgo JoinAlgo
		wantOK   bool
	}{
		{"left missing", 0, 100, JoinAlgoMerge, false},
		{"right missing", 100, 0, JoinAlgoMerge, false},
		{"both missing", 0, 0, JoinAlgoMerge, false},
		// hash = build + probe = L+R; merge additionally pays both sorts, so
		// hash wins wherever the sorts can be priced at all.
		{"balanced", 10_000, 10_000, JoinAlgoHash, true},
		{"skewed", 1, 1_000_000, JoinAlgoHash, true},
		{"single rows", 1, 1, JoinAlgoHash, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			algo, ok := chooseOuterFillJoinAlgo(tc.l, tc.r)
			if ok != tc.wantOK {
				t.Errorf("ok=%v want %v", ok, tc.wantOK)
			}
			if algo != tc.wantAlgo {
				t.Errorf("algo=%v want %v", algo, tc.wantAlgo)
			}
		})
	}
}

// Nested loop is deliberately NOT a candidate for RIGHT/FULL even where the
// unit-row model would make it cheapest, because runNestedLoop drains both
// inputs into memory and the model does not price that. Guard the exclusion
// directly: at 1x1 the inner chooser picks nested loop, and the outer-fill
// chooser must still not.
func TestChooseOuterFillJoinAlgoNeverPicksNestedLoop(t *testing.T) {
	defer func(prev bool) { hashOuterJoinEnabled = prev }(hashOuterJoinEnabled)
	hashOuterJoinEnabled = true
	if algo, _ := chooseInnerJoinAlgo(1, 1); algo != JoinAlgoNestedLoop {
		t.Skipf("precondition changed: chooseInnerJoinAlgo(1,1) = %v", algo)
	}
	if algo, _ := chooseOuterFillJoinAlgo(1, 1); algo == JoinAlgoNestedLoop {
		t.Errorf("chooseOuterFillJoinAlgo(1,1) = JoinAlgoNestedLoop, which drains both inputs")
	}
}

// The gate itself: with GOOPG_HASH_OUTER_JOIN unset the function declines
// whatever the estimates say, so the planner keeps merge and the default plan
// shape of every RIGHT/FULL query is unchanged by M0127-P4.2.
func TestChooseOuterFillJoinAlgoDeclinesWhileGated(t *testing.T) {
	defer func(prev bool) { hashOuterJoinEnabled = prev }(hashOuterJoinEnabled)
	hashOuterJoinEnabled = false
	algo, ok := chooseOuterFillJoinAlgo(10_000, 10_000)
	if ok {
		t.Errorf("ok=true want false while gated")
	}
	if algo != JoinAlgoMerge {
		t.Errorf("algo=%v want JoinAlgoMerge while gated", algo)
	}
}
