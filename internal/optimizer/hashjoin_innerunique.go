package optimizer

import (
	"math/bits"

	"github.com/goopg/goopg/internal/parser"
)

// hashJoinFinalCostInput is the narrow part of final_cost_hashjoin that is
// decided while the join clause and base-relation provenance are still in
// scope.  Its zero value deliberately selects the old non-unique bucket walk.
//
// Goopg can prove only the INNER case here.  In particular, a LEFT join's
// joinrel.Rows includes unmatched outer rows, so it cannot supply PG's
// semifactors.outer_match_frac by division.  SEMI and ANTI have their own
// executor and join-semantics work, and remain on their existing paths.
type hashJoinFinalCostInput struct {
	innerUnique    bool
	outerMatchFrac float64
}

// hashJoinFinalCostInputFor returns the fail-closed Inner Unique evidence for
// one hash orientation.  The inner-side columns of the hash clauses must
// collectively cover one complete non-partial bare unique index of a sole,
// complete base relation.  `provableKeys` already implements that coverage and
// rejects partial indexes; retaining its `fromFK` distinction is essential:
// an FK can establish selectivity but does not make this build side unique.
//
// The match fraction lives in total relation coordinates, like PG's
// compute_semi_anti_join_factors result.  Serial and partial candidates share
// it; hashJoinCost applies it to each candidate path's own outer rows.
func (s *searchCtx) hashJoinFinalCostInputFor(joinrel, outer, inner *RelOptInfo,
	jt parser.JoinType, keys []*restrictInfo) hashJoinFinalCostInput {
	if s == nil || s.cat == nil || jt != parser.JoinInner || joinrel == nil ||
		outer == nil || inner == nil || len(keys) == 0 || relLevel(inner.Relids) != 1 ||
		inner.CheapestTotal == nil || inner.CheapestTotal.RequiredOuter != 0 ||
		outer.Rows <= 0 {
		return hashJoinFinalCostInput{}
	}

	innerRel := bits.TrailingZeros32(uint32(inner.Relids))
	if innerRel < 0 || innerRel >= len(s.relInfos) || inner.Relids != RelSet(1)<<uint(innerRel) {
		return hashJoinFinalCostInput{}
	}

	pairs := make([]joinKeyPair, len(keys))
	for i, ri := range keys {
		pair, ok := s.joinKeyPairOf(ri, outer.Relids, inner.Relids)
		if !ok {
			return hashJoinFinalCostInput{}
		}
		pairs[i] = pair
	}
	for _, key := range s.provableKeys(s.cat, pairs, make([]bool, len(pairs)),
		func(key provenKey) bool { return !key.fromFK && key.keyRel == innerRel }) {
		if !key.fromFK {
			return hashJoinFinalCostInput{
				innerUnique:    true,
				outerMatchFrac: clampSelectivity(joinrel.Rows / outer.Rows),
			}
		}
	}
	return hashJoinFinalCostInput{}
}
