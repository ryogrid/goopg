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
