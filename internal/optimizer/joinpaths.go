package optimizer

// M0127-P5.4a — `add_paths_to_joinrel`, the unparameterised core: which quals a
// join applies for a given input order, and the hash / plain-nested-loop paths
// that go with them.
//
// PG oracle: `add_paths_to_joinrel` (joinpath.c:124), `hash_inner_and_outer`
// (:2220) with its `clause_sides_match_join` operand test (:2205-2231),
// `match_unsorted_outer`'s plain-nestloop arm and the `nestjoinOK` jointype
// gauntlet (:1833-1852), `build_joinrel_restrictlist` (relnode.c). Design:
// leftdeep-joins 03 §5.1, §5.3, §5.4; 05 §5 (multi-column hash keys).
//
// This is the OTHER half of the `joinRelBuilder` seam `makeJoinRel` calls
// (joinsearchlevel.go:52-57): `addPaths`. The interface's remaining method,
// `sizeJoinRel`, is still P5.6's, and the concrete builder that binds the two
// together arrives with it — a stand-in sizer here would be a second cost model
// to unpick later, which is the exact thing the seam exists to avoid. So
// nothing calls this from `planSelect` yet either, and `GOOPG_PGSHAPED_DP`
// stays OFF; it is validated in isolation by `joinpaths_test.go`.
//
// The arms have arrived one slice at a time and each lives beside its oracle:
// hash and plain nested loop here (P5.4a), the parameterised NLI arm in
// `joinpathsnli.go` (P5.4b-ii-b-1), the explicit-sort merge arm in
// `joinpathsmerge.go` (P5.4c-i). What is still deliberately NOT generated, each
// because it needs a mechanism the search does not yet have, and each carrying a
// deferral-ledger row:
//
//   - Memoize paths — P5.4b-ii-b-2. `get_memoize_path` (joinpath.c:562) wraps
//     an NLI inner in a cache when the outer key's distinct count is low
//     enough that the cache pays for itself; goopg has the executor operator
//     (`operators_memoize.go`) but no path-level eligibility or cost for it.
//     It and the 03 §5.2 constructor binding contract both need a built Node
//     rather than a Path, so they attach to P5.5's `createPlan` arms.
//   - `generate_mergejoin_paths` (joinpath.c:1564) — P5.4c-ii, the merge arm
//     that exploits an ALREADY-ordered outer instead of sorting. Dead until
//     some path carries pathkeys, which no path in the search does yet.
//   - the jointype gauntlet and the FULL-without-usable-clause error contract
//     (03 §5.3). 03 §4.4 pins every outer/semi/anti construct OUTSIDE the
//     search as an opaque `PathPrebuilt` initial rel, so the only jointype that
//     can reach this function is INNER — for which `nestjoinOK` is
//     unconditionally true and a path therefore always exists. The gauntlet
//     becomes reachable code the moment `join_is_legal` inference lands, which
//     is the same event that relaxes the pin.

import (
	"fmt"

	"github.com/goopg/goopg/internal/parser"
)

// splitJoinClauses divides a joinrel's restriction list into the clauses a
// keyed operator can KEY on for this pair of input relsets, and the residual
// the operator must evaluate per tuple.
//
// The test is PG's `clause_sides_match_join` (joinpath.c:2205): an equality is
// usable as a hash clause only when one operand is computable entirely on one
// side and the other operand entirely on the other. goopg's `restrictInfo`
// already carries that operand split (`leftRelids`/`rightRelids`, set only for
// an `isEquijoin`), so the test is a containment check rather than a re-walk of
// the expression.
//
// Why the split is per PAIR and not per clause: the same clause can be a key at
// one join and a residual at another. `a.x = b.y + c.z` keys {a} against {b,c},
// so at the pair ({a}, {b,c}) it is a hash clause; at ({a,b}, {c}) its right
// operand straddles both sides and no hash key can be formed, so it must be
// evaluated as an ordinary qual. Both placements are correct and both are
// reachable in the same search — which is why the key set cannot be computed
// once when the clause is built.
//
// Note what this function does NOT decide: WHETHER the clause applies at this
// join at all. That is `clausesFor`'s coverage rule (joinrestrict.go), applied
// by `makeJoinRel` before it ever calls here, and it is what implements 03
// §5.4's "lowest level whose relids it covers" — a clause fully contained in
// one side does not overlap the other and is therefore already gone. The two
// predicates are distinct and neither substitutes for the other, the same way
// `hasRelevantJoinClause` (the pair gate) is distinct from `clausesFor` (the
// placement test).
//
// Order is preserved from the input list, so the key set is deterministic.
func splitJoinClauses(outer, inner RelSet, clauses []*restrictInfo) (keys, residual []*restrictInfo) {
	for _, ri := range clauses {
		if ri == nil {
			continue
		}
		if isKeyableFor(ri, outer, inner) {
			keys = append(keys, ri)
			continue
		}
		residual = append(residual, ri)
	}
	return keys, residual
}

// isKeyableFor is `clause_sides_match_join` for one clause: the equality's two
// operands must land on opposite sides of this join, in either order.
//
// A non-equality join qual (`a.x < b.y`) and an equality whose operands share a
// relation (`a.x = a.y + b.z`) never set `isEquijoin` and so are never keys —
// correctly, since neither has a two-sided operand split to hash on.
func isKeyableFor(ri *restrictInfo, outer, inner RelSet) bool {
	if !ri.isEquijoin || ri.leftRelids == 0 || ri.rightRelids == 0 {
		return false
	}
	if relsSubset(ri.leftRelids, outer) && relsSubset(ri.rightRelids, inner) {
		return true
	}
	return relsSubset(ri.leftRelids, inner) && relsSubset(ri.rightRelids, outer)
}

// jointypeForDirection is `populate_joinrel_with_paths`' per-jointype switch
// (joinrels.c:906-1029) reduced to the one question `addPathsToJoinrel` must
// answer for a SINGLE (outer, inner) call: which join does this direction
// perform, and may it be performed at all? C-03b
// (docs/design/planner-c03-jointype-search/DESIGN.md §4).
//
// Why ORIENTATION and not jointype alone. PG's switch calls
// `add_paths_to_joinrel` twice per pair and the two calls do not get the same
// jointype: a LEFT sjinfo yields JOIN_LEFT for (rel1, rel2) and JOIN_RIGHT for
// (rel2, rel1) (joinrels.c:932-939); SEMI yields JOIN_SEMI and JOIN_RIGHT_SEMI
// (:983-989); ANTI likewise (:1023-1029). So "is this legal" cannot be decided
// from the jointype — it is decided by whether THIS call's outer is the side
// that covers the SJI's LHS.
//
// `makeJoinRel` has already applied PG's `reversed` swap (joinrels.c:715-717),
// so on the FIRST of its two calls `outer` covers MinLefthand and `inner`
// covers MinRighthand — that is exactly what `joinIsLegal` matched on — and on
// the SECOND the containments fail. The gate is written as the containment test
// rather than as "decline the second call" so it stays correct for any caller,
// including the hand-built fixtures the C-03b/C-03d evidence runs on.
//
// The reversed direction is DECLINED rather than emitted as PG's JOIN_RIGHT /
// JOIN_RIGHT_SEMI / JOIN_RIGHT_ANTI. That is a deliberate narrowing, not an
// oversight: goopg's search would then own two ways to express one join and
// C-04 would have to prove both correct at once. Withholding a path can only
// lose an optimisation, never produce a wrong answer, and nothing selects these
// paths today in any case.
//
// SEMI/ANTI contract. PG runs `hash_inner_and_outer` for JOIN_SEMI
// (joinpath.c:2229) and its executor early-outs on the first inner match.
// goopg declines the keyed operators for SEMI/ANTI and offers only the nested
// loops, for the same fail-closed reason: `hash_inner_and_outer`'s semi
// handling is bound up with `create_unique_path` unique-ification, which goopg
// has no analogue of, and a semi-join hashed as though it were an inner join
// would MULTIPLY rows rather than merely mis-cost them. Declining is the safe
// direction and costs nothing while the paths are unreachable.
//
// FULL is DECLINED outright, in both directions (C-03c). goopg's executor has
// no FULL hash semantics, so `createPlanNode` has no arm that could emit one and
// every arm below would silently drop the unmatched rows a full join exists to
// keep. A FULL joinrel therefore ends the level with an empty pathlist, which
// `joinSearch` reports as an error (joinsearchlevel.go:305) and the planner
// answers by falling back to the syntactic join shape — the same outcome the
// pre-C-03 tree reaches by declining the whole search for FULL, and the reason
// this is inert. Deferral ledger: `C-03c FULL-join-search-decline`.
func jointypeForDirection(sjinfo *SpecialJoinInfo, outer, inner RelSet) (parser.JoinType, bool) {
	// No SpecialJoinInfo: a plain inner join, which is what `joinIsLegal`
	// returns for every pair in a query with no outer/semi/anti join at all
	// (its `len(s.joinInfoList) == 0` fast path). Both directions are legal —
	// PG's JOIN_INNER arm, joinrels.c:908-921.
	if sjinfo == nil {
		return parser.JoinInner, true
	}
	switch sjinfo.Jointype {
	case parser.JoinInner, parser.JoinCross:
		// A SpecialJoinInfo is never built for these, but a caller that
		// synthesises one must not accidentally take the outer-join path.
		return parser.JoinInner, true
	case parser.JoinFull:
		// See the FULL note above: no direction, no path, no plan.
		return parser.JoinFull, false
	case parser.JoinLeft, parser.JoinRight, parser.JoinSemi, parser.JoinAnti:
		if relsSubset(sjinfo.MinLefthand, outer) && relsSubset(sjinfo.MinRighthand, inner) {
			return sjinfo.Jointype, true
		}
		return sjinfo.Jointype, false
	default:
		// An unrecognised jointype is PG's `elog(ERROR)` (joinrels.c:1031).
		// goopg cannot raise from path generation, so it declines the
		// direction — the fail-closed equivalent.
		return sjinfo.Jointype, false
	}
}

// addPathsToJoinrel is `add_paths_to_joinrel` (joinpath.c:124) for ONE input
// order: `outer` drives, `inner` is probed or built. `clauses` is the joinrel's
// own restriction list — what `build_joinrel_restrictlist` produced, which for
// goopg is `clausesFor(outer.Relids, inner.Relids)`.
//
// It generates, for this direction:
//
//   - a hash join building `inner`, whenever the pair has at least one usable
//     equality (the multi-column key set is ALL of them, 05 §5); and
//   - a plain nested loop, always.
//
// The nested loop is not a formality. A pair with no usable equality — a
// cartesian product from phase 1's clauseless branch or phase 3's last-ditch
// pass, or a join whose only qual is `a.x < b.y` — has no hash path at all, and
// `joinSearch` treats a joinrel with an empty pathlist as a hard failure
// (joinsearchlevel.go:110-112). Generating NL unconditionally for the jointypes
// that support it is what makes that failure unreachable, and is exactly why PG
// does the same (:1833-1852). Being usually dominated, it is then pruned by
// `addPath` and costs nothing.
//
// A missing cheapest path on either input is a LOUD error rather than a silent
// skip. Every rel the search offers has been through `setCheapest` — level 1 in
// `buildInitialRels`, every higher level at the end of its own level pass — so
// a nil here means the search's own invariant broke, and swallowing it would
// surface much later as an unexplained empty pathlist at the top level.
// `s` is the search context, and it is threaded here for exactly one reason:
// the Memoize arm (`getMemoizePath`, M0127-P5.4b-ii-b-2) has to read a base
// relation's `stadistinct` to know whether a cache would hit, and `relInfos` is
// the only map from a relset bit to the `catalog.Table` behind it (P5.6-a's
// `examineJoinVar` goes through the same door). A nil `s` is legal and means
// "no statistics reachable" — every cache key then fails to resolve and no
// Memoize path is offered, which is the correct degradation and what the
// path-generation unit tests run with.
func addPathsToJoinrel(s *searchCtx, joinrel, outer, inner *RelOptInfo, clauses []*restrictInfo, cp costParams, sjinfo *SpecialJoinInfo) error {
	if joinrel == nil || outer == nil || inner == nil {
		return fmt.Errorf("join paths: nil input rel")
	}
	if outer.CheapestTotal == nil {
		return fmt.Errorf("join paths: outer rel %#08x has no cheapest path", uint32(outer.Relids))
	}
	if inner.CheapestTotal == nil {
		return fmt.Errorf("join paths: inner rel %#08x has no cheapest path", uint32(inner.Relids))
	}

	// C-03b — which join does THIS direction perform, and may it be performed
	// at all. See jointypeForDirection.
	jt, legal := jointypeForDirection(sjinfo, outer.Relids, inner.Relids)
	if !legal {
		return nil
	}
	// SEMI/ANTI are nestloop-only in goopg. See jointypeForDirection's contract
	// note for why the keyed operators decline rather than being ported.
	nestloopOnly := jt == parser.JoinSemi || jt == parser.JoinAnti

	// 03 §9 rule 2 — PATH_PARAM_BY_REL (joinpath.c:43-47). The two directions
	// are refused for genuinely different reasons, so they are named
	// separately rather than folded into one predicate:
	//
	//   - An OUTER parameterised by the inner is impossible in any join order.
	//     The outer is evaluated first, so the inner has produced nothing that
	//     could bind it. PG refuses it for every method (:1398 merge, :1911
	//     nestloop, :2297 hash), and it is also the precondition
	//     `calc_nestloop_required_outer` asserts rather than corrects.
	//   - An INNER parameterised by the outer is refused only by the methods
	//     that cannot supply the parameter. A hash build is materialised in
	//     full before the probe begins, so there is no per-outer-row binding
	//     available and the hash arm is skipped whole (:2297) — PG does not
	//     look for a substitute input, since the cheapest-total path is
	//     already the least-parameterised one available. A plain nested loop
	//     could bind it but would cost it wrongly, rescanning the inner from
	//     scratch as though the parameter were free, so PG drops it from THIS
	//     arm (:1874) and reconsiders it through `cheapest_parameterized_paths`
	//     in the NLI arm — `addNLIPaths` (joinpathsnli.go), landed in
	//     P5.4b-ii-b-1.
	//
	// The two refusals therefore have DIFFERENT scopes, which is why PG writes
	// them as an early `return` and a nulled-out variable rather than as one
	// condition: an outer parameterised by the inner kills this direction
	// outright, while an inner parameterised by the outer kills only the hash
	// and plain-NL arms and is precisely what the NLI arm is for.
	// C-08: PG's per-joinrel param_source_rels, computed ONCE per
	// add_paths_to_joinrel call (joinpath.c:242-276) and passed down to
	// the NLI + merge arms — see paramSourceRelsForProblem. Nil search
	// context (unit fixtures) carries no joinInfoList: 0, the legacy
	// constant.
	var paramSrc RelSet
	if s != nil {
		paramSrc = paramSourceRelsForProblem(joinrel.Relids, s.joinInfoList, s.problemItems)
	}
	o, i := outer.CheapestTotal, inner.CheapestTotal
	if pathParamByRel(o, inner) {
		return nil
	}
	if !pathParamByRel(i, outer) {
		keys, residual := splitJoinClauses(outer.Relids, inner.Relids, clauses)
		// PG's order inside `add_paths_to_joinrel`: `sort_inner_and_outer`
		// (:180) before `hash_inner_and_outer` (:212). It is followed here
		// because `addPath` keeps the INCUMBENT on an exact cost tie, so the
		// order arms are offered in IS the tie-break, and a tie between a merge
		// and a hash path must resolve the way PG resolves it.
		//
		// C-03b adds the second conjunct: the keyed arms (both merge arms, the
		// serial hash arm and its partial twin) are the ones a SEMI/ANTI join
		// may not use here, so they are gated as a block rather than each
		// re-deriving the rule.
		if len(keys) > 0 && !nestloopOnly {
			// mergejointuples: what the merge operator emits, before the
			// residual filters it to joinrel.Rows. Computed ONCE here, where
			// the searchCtx (and so the selectivity model) is in scope, and
			// threaded into both merge arms as a scalar rather than widening
			// their coupling to the search.
			// A closure, not a scalar: the match_unsorted_outer arm TRIMS its
			// merge-clause list per trial and demotes the dropped clauses into
			// the residual, so its mergejointuples differs per call. Passing
			// the rule lets each site apply it to its own residual, while the
			// searchCtx stays out of the merge helpers' signatures.
			mergeTuplesFor := func(res []*restrictInfo) float64 {
				return s.mergeJoinTuples(joinrel.Rows, res, outer.Rows, inner.Rows)
			}
			scanSelFor := func(mc []*restrictInfo) (float64, float64) {
				return s.mergeJoinScanSel(mc, outer.Relids)
			}
			sortInnerAndOuter(joinrel, outer, inner, cp, jt, keys, residual, mergeTuplesFor, scanSelFor, paramSrc)
			// PG's arm 2, `match_unsorted_outer` (:290), sits between arm 1
			// and arm 4 — so a merge over an already-ordered outer is offered
			// to `addPath` BEFORE the hash path, and wins an exact tie against
			// it exactly as it does in PG. Only the merge half of that arm is
			// here; goopg's nested-loop halves (`addNestLoopPath` /
			// `addNLIPaths`) were landed separately and still run after the
			// hash arm, which can only change a hash-vs-nestloop exact tie.
			matchUnsortedOuterMerge(joinrel, outer, inner, cp, jt, keys, residual, mergeTuplesFor, scanSelFor, paramSrc)
			// take2 P2-11: the inner side is the BUILD side here, so the
			// bucket fraction is measured on its keys. Computed at this site
			// because the searchCtx — and so the statistics — is in scope,
			// exactly as mergeTuplesFor is.
			bucket := s.estimateHashBucketSize(keys, inner.Relids)
			addHashJoinPath(joinrel, outer, inner, cp, jt, keys, residual, bucket)
			// C-19f: `hash_inner_and_outer`'s parallel block (joinpath.c:2418)
			// sits immediately after the serial `try_hashjoin_path` loop
			// (:2398), and is passed the SAME hashclauses — so the partial
			// path and its serial twin are priced from identical inputs and
			// can differ only in the parallel terms. It files into the
			// joinrel's PartialPathlist, which nothing but
			// generateUsefulGatherPaths and the next level's own partial
			// producer reads; with GOOPG_GATHER_PATHS off it produces nothing
			// at all (joinpathsparallel.go).
			addPartialHashJoinPath(s, joinrel, outer, inner, cp, jt, keys, residual, bucket)
		}
		// The nested loop keys on nothing, so the key set rejoins the
		// residual: it evaluates every clause, on every pair. Passing
		// `clauses` whole rather than `append(keys, residual...)` also keeps
		// the input order.
		addNestLoopPath(joinrel, outer, inner, cp, jt, clauses)
	}
	// PG runs this inside the same `outerrel->pathlist` loop as the arms above
	// (joinpath.c:1949), unconditionally for every jointype `nestjoinOK`
	// admits — which under 03 §4.4's INNER-only pin is all of them.
	addNLIPaths(s, joinrel, outer, inner, cp, jt, clauses, paramSrc)
	return nil
}
