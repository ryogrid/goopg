package optimizer

import (
	"math"
	"math/bits"

	"github.com/goopg/goopg/internal/parser"
)

// hashJoinFinalCostInput is the narrow part of final_cost_hashjoin that is
// decided while the join clause and base-relation provenance are still in
// scope.  Its zero value deliberately selects the old non-unique bucket walk.
//
// Goopg covers only the INNER case here.  A LEFT join with a unique inner
// takes the same branch in PG (over its non-pushed-down join quals) and is
// not ported yet.  SEMI and ANTI have their own executor and join-semantics
// work, and remain on their existing paths.
type hashJoinFinalCostInput struct {
	innerUnique    bool
	outerMatchFrac float64
	matchCount     float64
	// hashClauseSel is approx_tuple_count's selectivity: the product of the
	// hash clauses' plain inner-join selectivities.  PG's non-unique arm
	// charges cpu_tuple_cost on sel * outer path rows * inner path rows, not
	// on the joinrel's rows, so a Parallel Hash candidate (whose inner path
	// rows are per worker too) is charged far fewer tuples (M0146-0005e).
	// Zero means unknown and falls back to the join's output rows.
	hashClauseSel float64
}

// hashJoinFinalCostInputFor returns the fail-closed Inner Unique evidence for
// one hash orientation.  The inner-side columns of the hash clauses must
// collectively cover one complete non-partial bare unique index of a sole,
// complete base relation.  `provableKeys` already implements that coverage and
// rejects partial indexes; retaining its `fromFK` distinction is essential:
// an FK can establish selectivity but does not make this build side unique.
//
// The factors are PG's compute_semi_anti_join_factors for an INNER join, over
// the pair's whole restriction list (`clauses`), not just the hash keys.  For
// an inner join PG passes JOIN_SEMI but an INNER SpecialJoinInfo, and eqjoinsel
// switches on sjinfo->jointype, so jselec is the plain inner-join selectivity:
// outer_match_frac is that selectivity (not joinrel rows / outer rows), and
// match_count = nselec * inner rows / jselec is the inner rel's row count.
// PG 18.3's final_cost_hashjoin trace on TPC-DS Q31 (web_sales ⋈ date_dim,
// hashjointuples = 2 of 179956 outer rows) shows it (M0146-0005e).  Serial
// and partial candidates share the factors; hashJoinCost applies them to each
// candidate path's own outer rows.
func (s *searchCtx) hashJoinFinalCostInputFor(joinrel, outer, inner *RelOptInfo,
	jt parser.JoinType, keys, clauses []*restrictInfo) hashJoinFinalCostInput {
	if s == nil || jt == parser.JoinSemi || jt == parser.JoinAnti || len(keys) == 0 {
		return hashJoinFinalCostInput{}
	}
	approx := 1.0
	for _, ri := range keys {
		cs, _ := s.joinClauseSelectivityExt(ri)
		approx *= cs
	}
	base := hashJoinFinalCostInput{hashClauseSel: clampSelectivity(approx)}
	if jt != parser.JoinInner || joinrel == nil || outer == nil || outer.Rows <= 0 ||
		!s.innerRelProvenUnique(outer, inner, keys, false) {
		return base
	}
	base.innerUnique = true
	base.outerMatchFrac, base.matchCount = s.innerUniqueMatchFactors(inner, clauses)
	return base
}

// innerRelProvenUnique is the fail-closed half of PG's innerrel_is_unique
// that goopg can prove: the inner-side columns of `keys` collectively cover
// one complete non-partial bare unique index of a sole, complete base
// relation.  `provableKeys` already implements that coverage and rejects
// partial indexes; retaining its `fromFK` distinction is essential: an FK can
// establish selectivity but does not make the inner side unique.  With
// `skipNonKeys` a clause that is not an outer=inner equi-pair is ignored (a
// nested loop's whole restriction list); otherwise it declines (a hash
// join's key list).
func (s *searchCtx) innerRelProvenUnique(outer, inner *RelOptInfo, keys []*restrictInfo, skipNonKeys bool) bool {
	if s == nil || s.cat == nil || outer == nil || inner == nil || len(keys) == 0 ||
		relLevel(inner.Relids) != 1 ||
		inner.CheapestTotal == nil || inner.CheapestTotal.RequiredOuter != 0 {
		return false
	}
	innerRel := bits.TrailingZeros32(uint32(inner.Relids))
	if innerRel < 0 || innerRel >= len(s.relInfos) || inner.Relids != RelSet(1)<<uint(innerRel) {
		return false
	}
	pairs := make([]joinKeyPair, 0, len(keys))
	for _, ri := range keys {
		pair, ok := s.joinKeyPairOf(ri, outer.Relids, inner.Relids)
		if !ok {
			if skipNonKeys {
				continue
			}
			return false
		}
		pairs = append(pairs, pair)
	}
	if len(pairs) == 0 {
		return false
	}
	for _, key := range s.provableKeys(s.cat, pairs, make([]bool, len(pairs)),
		func(key provenKey) bool { return !key.fromFK && key.keyRel == innerRel }) {
		if !key.fromFK {
			return true
		}
	}
	return false
}

// innerUniqueMatchFactors is compute_semi_anti_join_factors for an INNER
// pair with a unique inner: outer_match_frac is the inner-join selectivity of
// the pair's restriction list (one clause per EC) and match_count is the
// inner rel's row count (nselec * rows / jselec with jselec == nselec).
func (s *searchCtx) innerUniqueMatchFactors(inner *RelOptInfo, clauses []*restrictInfo) (float64, float64) {
	sel := 1.0
	for _, ri := range oneClausePerEquivClass(clauses) {
		if ri == nil {
			continue
		}
		cs, _ := s.joinClauseSelectivityExt(ri)
		sel *= cs
	}
	sel = clampSelectivity(sel)
	matchCount := 1.0
	if sel > 0 {
		matchCount = math.Max(1.0, inner.Rows)
	}
	return sel, matchCount
}
