package optimizer

// M0142-0008a-3i-plumbing-c14 (design doc §48/§49): chainCarriesLateral had no
// arm for a Semi/Anti join, so a Lateral join buried under one's Left child —
// the exact shape predp.go's Phase A splice produces when its own winning
// tree contains a parameterized index probe — fell through to the
// "escaping outer ref" fallback instead of this function's own coarse "any
// Lateral reachable" rule, and the decline never fired. This pins the fix:
// chainCarriesLateral must descend a Semi/Anti join's Left exactly like
// extractSearchLeaves does.

import "testing"

func TestChainCarriesLateralDescendsSemiAntiLeft(t *testing.T) {
	outer := &SeqScan{schema: Schema{{Name: "a"}}}
	inner := &SeqScan{schema: Schema{{Name: "b"}}}
	rhs := &SeqScan{schema: Schema{{Name: "c"}}}

	for _, sjType := range []JoinType{JoinTypeSemi, JoinTypeAnti} {
		lateral := &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Lateral: true, Left: outer, Right: inner}
		sj := &Join{Algo: JoinAlgoNestedLoop, Type: sjType, Left: lateral, Right: rhs}
		if !chainCarriesLateral(sj) {
			t.Fatalf("%v: a Lateral join under a Semi/Anti join's Left must be found", sjType)
		}
	}
}

func TestChainCarriesLateralAdmitsNonLateralUnderSemiAnti(t *testing.T) {
	outer := &SeqScan{schema: Schema{{Name: "a"}}}
	inner := &SeqScan{schema: Schema{{Name: "b"}}}
	rhs := &SeqScan{schema: Schema{{Name: "c"}}}

	plain := &Join{Algo: JoinAlgoHash, Type: JoinTypeInner, Left: outer, Right: inner}
	sj := &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeSemi, Left: plain, Right: rhs}
	if chainCarriesLateral(sj) {
		t.Fatal("a non-lateral chain under a Semi join's Left must not be declined")
	}
}
