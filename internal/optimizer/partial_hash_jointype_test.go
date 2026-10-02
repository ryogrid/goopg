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
		parser.JoinInner:     JoinTypeInner,
		parser.JoinLeft:      JoinTypeLeft,
		parser.JoinRight:     JoinTypeRight,
		parser.JoinFull:      JoinTypeFull,
		parser.JoinCross:     JoinTypeCross,
		parser.JoinSemi:      JoinTypeSemi,
		parser.JoinAnti:      JoinTypeAnti,
		parser.JoinRightSemi: JoinTypeRightSemi,
		parser.JoinRightAnti: JoinTypeRightAnti,
	}
	for jt, ot := range optType {
		// The producer files a jointype iff the executor can run it as a
		// Parallel Hash (the most permissive partial form).
		producer := partialHashJoinTypeOK(jt)
		executor := hashJoinIsPartialCapable(&Join{Algo: JoinAlgoHash, Type: ot, ParallelHash: true})
		if producer != executor {
			t.Errorf("jointype %v: producer files=%v but executor capable=%v — "+
				"the two have drifted, which is exactly the K17 defect",
				jt, producer, executor)
		}
		// A shared-table-only jointype must be refused with a per-worker or
		// leader-prebuilt table, and the producer must know it is shared-only.
		perWorker := hashJoinIsPartialCapable(&Join{Algo: JoinAlgoHash, Type: ot})
		if partialHashJoinNeedsSharedTable(jt) == perWorker && producer {
			t.Errorf("jointype %v: shared-only=%v but per-worker capable=%v",
				jt, partialHashJoinNeedsSharedTable(jt), perWorker)
		}
	}
	// The instance that crashed TPC-DS Q5 (K17), named explicitly: a RIGHT
	// hash join with a per-worker table must never run partial — no one
	// participant has all the match bits.
	if hashJoinIsPartialCapable(&Join{Algo: JoinAlgoHash, Type: JoinTypeRight}) {
		t.Error("RIGHT hash joins without a shared table must never be parallel-aware")
	}
	if hashJoinIsPartialCapable(&Join{Algo: JoinAlgoHash, Type: JoinTypeFull}) {
		t.Error("FULL hash joins without a shared table must never be parallel-aware")
	}
	// PG excludes JOIN_RIGHT_SEMI from the parallel block entirely.
	if partialHashJoinTypeOK(parser.JoinRightSemi) {
		t.Error("RIGHT SEMI must never be filed as a partial path")
	}
	if !partialHashJoinTypeOK(parser.JoinInner) {
		t.Error("INNER must be filed; declining it would disable the mechanism")
	}
}
