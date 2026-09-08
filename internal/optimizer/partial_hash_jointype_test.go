package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPartialHashJoinTypeOK (R9, plan-parity-fix-take2) pins the producer's
// direction filter AGAINST the executor's own predicate, for every jointype.
//
// This is the point of the round. K17 was not "a missing if" — it was a
// COMMENT claiming a filter the code did not implement, which made the defect
// invisible to reading for as long as it existed. A second hand-written list
// would reproduce exactly that. So the filter is checked against
// hashJoinIsPartialCapable here: if the executor predicate is ever narrowed,
// this test fails instead of the producer silently over-filing a path that
// panics at plan build (or, without the assertion, silently drops rows).
func TestPartialHashJoinTypeOK(t *testing.T) {
	// The hash arm always builds on the right (createHashJoinPlan: "Outer
	// drives the probe, inner is hashed … BuildLeft stays false"), which is
	// the condition hashJoinIsPartialCapable puts on LEFT.
	optType := map[parser.JoinType]JoinType{
		parser.JoinInner: JoinTypeInner,
		parser.JoinLeft:  JoinTypeLeft,
		parser.JoinRight: JoinTypeRight,
		parser.JoinFull:  JoinTypeFull,
		parser.JoinCross: JoinTypeCross,
		parser.JoinSemi:  JoinTypeSemi,
		parser.JoinAnti:  JoinTypeAnti,
	}
	for jt, ot := range optType {
		producer := partialHashJoinTypeOK(jt)
		executor := hashJoinIsPartialCapable(&Join{Algo: JoinAlgoHash, Type: ot})
		if producer != executor {
			t.Errorf("jointype %v: producer files=%v but executor capable=%v — "+
				"the two have drifted, which is exactly the K17 defect",
				jt, producer, executor)
		}
	}
	// The instance that crashed TPC-DS Q5, named explicitly so a future
	// reader sees the case rather than only the invariant.
	if partialHashJoinTypeOK(parser.JoinRight) {
		t.Error("RIGHT hash joins must never be filed as parallel-aware: the " +
			"per-row verdict is not worker-local, so a partial probe would " +
			"silently drop or duplicate rows")
	}
	if partialHashJoinTypeOK(parser.JoinFull) {
		t.Error("FULL hash joins must never be filed as parallel-aware")
	}
	if !partialHashJoinTypeOK(parser.JoinInner) {
		t.Error("INNER must be filed; declining it would disable the mechanism")
	}
}
