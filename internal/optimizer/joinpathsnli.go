package optimizer

import (
	"github.com/goopg/goopg/internal/parser"
)

// M0127-P5.4b-ii-b-1 — the NLI arm of `match_unsorted_outer`: the one place in
// the search where a parameterised path is CONSUMED rather than produced.
//
// PG oracle: `match_unsorted_outer`'s inner loop over
// `innerrel->cheapest_parameterized_paths` (joinpath.c:1949-1975), the
// admission test in `try_nestloop_path` (:872-889) with
// `allow_star_schema_join` (:363), and `create_nestloop_path`'s restrict-clause
// drop (pathnode.c:2478-2500). Design: leftdeep-joins 03 §5.2, 03 §9.
//
// This is the third and last of the three separately-falsifiable steps P5.4b
// was split into. P5.4b-i landed the DISCIPLINE (`pathparam.go`), P5.4b-ii-a
// the parameterised base index PATHS (`pathparamindex.go`), and this the arm
// that turns one into a join. The split is why the middle step could be tested
// at all: until an arm existed, a parameterised path could be generated and
// costed but never joined, so "the list is non-empty" was the whole assertion
// available.
//
// It is also what un-breaks the hole P5.4b-i knowingly opened. A pair whose
// inner cheapest-total is parameterised by the outer yields NO path from the
// hash/plain-NL arms — hash cannot bind the parameter at all, and a plain
// nested loop would price the rescan as though the parameter were free, so PG
// drops it from that arm at :1874 precisely so it can be re-costed here with
// the inner's own per-probe cost. Between P5.4b-i and this slice that pair was
// legitimately pathless; from here it is not.
//
// Live since M0127-P5.9 (2026-08-06): `GOOPG_PGSHAPED_DP` defaults ON and
// `planSelect` calls the search, so the paths this arm emits DO compete for
// production plans. Validated by `joinpathsnli_test.go`, no longer in
// isolation.

// paramSourceRelsForProblem is PG's per-joinrel `extra->param_source_rels`
// derivation (joinpath.c:242-276): the relations a join path for THIS
// joinrel is allowed to stay parameterised BY. For each SJI the joinrel
// overlaps on the RHS but not the LHS, every baserel outside the RHS may
// source a parameterisation; FULL constrains symmetrically; the joinrel's
// lateral relids join unconditionally.
//
// FRAME RULE (C-08 design §4 — binding): SJI Min hands are
// statement-leaf-global while this problem numbers its items 1<<i. Since
// the C-04a Q72 fix the hands arrive ALREADY in item space —
// `sjInfosInItemSpace` (relfromjoinlist.go) remaps the whole list once per
// problem, for every consumer — so the shift-by-lo this function used to
// perform is gone; only the mask to n bits remains (it strips the
// outside-window marker bit the remap keeps). Dropped outside bits cannot
// change an overlap verdict for problem-internal joinrelids/req — exact,
// not merely fail-closed (DESIGN §4 proves the identity). The run must
// still be single-leaf-consecutive (`items[i].hi == items[i].lo+1`);
// anything else (multi-leaf items, empty run) returns 0 — today's
// constant, kept deliberately so this fix moves no plan the remap does
// not have to (widening it to nested items is its own, measured step).
// Lateral union is 0 by invariant (no LATERAL shape reaches path
// generation; DESIGN §3 anchors the decline paths).
func paramSourceRelsForProblem(joinrelids RelSet, joinInfoList []*SpecialJoinInfo, items []joinlistRel) RelSet {
	if len(items) == 0 {
		return 0
	}
	lo := items[0].lo
	n := len(items)
	for i, it := range items {
		if it.lo != lo+i || it.hi != lo+i+1 {
			return 0
		}
	}
	var all RelSet
	if n >= 32 {
		all = ^RelSet(0)
	} else {
		all = RelSet(1)<<uint(n) - 1
	}
	mask := all
	var out RelSet
	for _, sj := range joinInfoList {
		if sj == nil {
			continue
		}
		// RIGHT joins never reach PG's loop (reduce_outer_joins flips
		// RIGHT→LEFT in prep_jointree), and since C-04b they never reach
		// this one either: `makeSpecialJoinInfoScoped` performs the same
		// reduction on the SJI's hands (`reduceRightLink`), so a RIGHT
		// link is described by a LEFT SpecialJoinInfo and takes the
		// LEFT-shaped rule below with its nullable side on the right,
		// which is the orientation that rule reads. The skip is kept as
		// a fail-safe for any future producer that builds a RIGHT SJI
		// directly: read backwards, the rule would let a nullable-side
		// joinrel be parameterised by its own preserved side.
		if sj.Jointype == parser.JoinRight {
			continue
		}
		minL := sj.MinLefthand & mask
		minR := sj.MinRighthand & mask
		if relsOverlap(joinrelids, minR) && !relsOverlap(joinrelids, minL) {
			out |= all &^ minR
		}
		if sj.Jointype == parser.JoinFull &&
			relsOverlap(joinrelids, minL) && !relsOverlap(joinrelids, minR) {
			out |= all &^ minL
		}
	}
	return out
}

// allowStarSchemaJoin is `allow_star_schema_join` (joinpath.c:363): a join path
// may stay parameterised even when nothing in `param_source_rels` wants it, IF
// the outer supplies SOME but not all of the inner's parameterisation.
//
// PG's rationale, which is easy to lose: the restriction the empty
// `param_source_rels` expresses is "do not manufacture parameterised join paths
// nobody asked for", and its purpose is to bound the number of
// parameterisations at higher levels. A partially-satisfied inner is the one
// case where refusing costs a plan rather than saving work — the star-schema
// shape, where a fact table is probed by an index whose columns come from two
// different dimension tables and the two dimensions are joined at different
// levels.
func allowStarSchemaJoin(outerRelids, innerParam RelSet) bool {
	return relsOverlap(innerParam, outerRelids) && innerParam&^outerRelids != 0
}

// joinClauseIsMovableInto is `join_clause_is_movable_into` (restrictinfo.c:610):
// can this join clause be pushed down INTO a scan of `currentRelids` that has
// `currentAndOuter` available?
//
// Two conditions in PG survive into goopg:
//
//   - the clause must physically reference the target rel, else pushing it down
//     is not a placement but an invention; and
//   - every relation it references must be available there — the rel itself
//     plus whatever the parameterisation supplies.
//
// PG's other two tests are about outer joins (`rinfo->outer_relids`, and
// `required_relids` exceeding `clause_relids` when an OJ must be below the
// clause). Both are vacuous while 03 §4.4 pins every outer join outside the
// search: goopg's `restrictInfo.relids` IS `required_relids`, and it equals the
// clause's own relids because no clause in the searched shape has an
// outer-join delay. They become expressible with the same event that relaxes
// the pin.
func joinClauseIsMovableInto(ri *restrictInfo, currentRelids, currentAndOuter RelSet) bool {
	if ri == nil {
		return false
	}
	if !relsOverlap(currentRelids, ri.relids) {
		return false
	}
	return relsSubset(ri.relids, currentAndOuter)
}

// nestloopResidualClauses is `create_nestloop_path`'s restrict-clause drop
// (pathnode.c:2478-2500): when the inner is parameterised by the outer, the
// clauses that are movable into the inner are ALREADY being applied down there
// — as index quals on the parameterised scan — so the join must not carry them
// a second time.
//
// This is a correctness statement before it is a costing one. Re-evaluating a
// clause the index already enforced does not change the result, but charging
// for it does, and charging for it on the full `outerRows * innerRows` cross
// product is exactly the mis-costing that would make an NLI look worse than the
// hash join it should beat. PG drops them from the path's `restrict_clauses`,
// so `final_cost_nestloop`'s qpqual charge never sees them; this returns the
// surviving list for the same reason.
//
// `innerAndOuter` is PG's `bms_union(inner_path->parent->relids,
// inner_req_outer)` — what the parameterised inner scan can see.
//
// # goopg drops a NARROWER set than PG, and the difference is a wrong answer
//
// (M0127-P5.5-e-ii-b.) PG may drop on movability ALONE because a PG
// parameterised path really does apply every movable clause: movability is what
// `get_baserel_parampathinfo` (relnode.c:1580) uses to build `ppi_clauses`, the
// index consumes what it can, and `create_indexscan_plan` places the remainder
// into the scan's `qpqual` (createplan.c:3075's `is_redundant_with_indexclauses`
// is the filter for that split). Every dropped clause is therefore enforced
// somewhere below.
//
// goopg's parameterised index path applies only the equalities
// `pickIndexCoveringLeadingPrefix` accepted — `Path.IndexClauses`
// (pathindexclauses.go) — and goopg's `*IndexScan` has NO qual field for a
// remainder to live in. `b.y > a.x` at inner `{b}` under parameterisation `{a}`
// is movable by the test above and enforced by nothing at all: dropping it here
// deletes the restriction from the plan. So the drop is narrowed to the clauses
// the probe demonstrably enforces, and movability survives as the frame it is
// checked inside rather than as the whole test.
//
// Matching is by `restrictInfo` IDENTITY. PG's redundancy test also matches a
// clause DERIVED from the same equivalence class, and that half is deliberately
// not reproduced: `selectivityClauses` (joinrestrict.go:319-348) has already
// reduced each equivalence class to one member before this list is built, so
// there is no same-EC sibling left to match — and were one to appear, keeping it
// costs a redundant qual evaluation while dropping it would lose a restriction.
// The asymmetry decides it. Ledgered.
func nestloopResidualClauses(clauses []*restrictInfo, innerPath *Path, innerRelids, innerParam RelSet) []*restrictInfo {
	innerAndOuter := innerRelids | innerParam
	enforced := probeEnforcedClauses(innerPath)
	var residual []*restrictInfo
	for _, ri := range clauses {
		if ri == nil {
			continue
		}
		if joinClauseIsMovableInto(ri, innerRelids, innerAndOuter) && enforced[ri] {
			continue
		}
		residual = append(residual, ri)
	}
	return residual
}

// probeEnforcedClauses is `is_redundant_with_indexclauses` (createplan.c:3075)
// as a set: the clauses this path's index probe applies itself.
//
// It reads `Path.IndexClauses`, which is exactly the list `createPlan`'s NLI arm
// turns into `IndexScan.Keys` — so "what the probe enforces" has ONE definition
// shared by the costing side and the building side, rather than two that could
// drift into a clause charged twice or enforced never (rule #2).
func probeEnforcedClauses(p *Path) map[*restrictInfo]bool {
	if p == nil || len(p.IndexClauses) == 0 {
		return nil
	}
	enforced := make(map[*restrictInfo]bool, len(p.IndexClauses))
	for _, c := range p.IndexClauses {
		if c.ri != nil {
			enforced[c.ri] = true
		}
	}
	return enforced
}

// addNLIPaths is the NLI arm: for the cheapest-total outer, every
// PARAMETERISED member of `inner.CheapestParameterized` becomes a candidate
// nested loop whose inner is rescanned with the parameter bound.
//
// It skips the list's unparameterised member deliberately. PG's loop covers it
// too — `set_cheapest` prepends the cheapest unparameterised path (pathnode.c:375)
// so that one `foreach` handles both — but goopg reaches that member through
// `addNestLoopPath`, which the caller has already run, and generating it twice
// would put two identical paths through `addPath`'s tournament. The list's
// shape is PG's; only the entry point differs.
//
// Two admission tests, in PG's order:
//
//   - an outer parameterised by anything is not handled here. The
//     parameterised-by-the-inner direction the caller already refused
//     (PATH_PARAM_BY_REL, 03 §9 rule 2); an outer parameterised by a THIRD
//     relation would leave the join parameterised, which is the same deferral
//     as below.
//   - PG's own admission test, unchanged: a still-parameterised result is
//     refused unless something in `param_source_rels` wants that
//     parameterisation or `allow_star_schema_join` vouches for it.
//   - and then, goopg-only, the join's `RequiredOuter` must come out EMPTY.
//     The paths PG's test ADMITS while still parameterised are the
//     star-schema ones, and such a path needs a `ppi_rows` of its own from
//     `get_parameterized_joinrel_size` (costsize.c:5473) — goopg's joinrel
//     sizer is P5.6's, and a stand-in here would be a second cost model
//     inside one comparison, exactly what the `joinRelBuilder` seam exists to
//     prevent. So the two gates are written separately: the first is PG's
//     rule and is complete, the second is a deferral and is ledgered.
//
// The consequence is an invariant worth stating, because the rest of the search
// silently relies on it: every JOIN path EXCEPT an admitted parameterised
// merge (C-08: wanted by its joinrel's param_source_rels) is
// unparameterised, so `Path.Rows == Rel.Rows` holds for every join path but
// that one, and the only other parameterised paths in play are base index
// scans. That is what lets `addNestLoopPath` and `addHashJoinPath` set
// `Rows: joinRel.Rows` unconditionally without a `ppi_rows` of their own —
// both read `CheapestTotal`-only inputs, which a parameterised path can
// never win (03 §9 rule 1), so the merge exception cannot reach them.
func addNLIPaths(s *searchCtx, joinrel, outer, inner *RelOptInfo, cp costParams, jt parser.JoinType, clauses []*restrictInfo, paramSrc RelSet) {
	// R64 (ledger R63-#3): decline when the inner is the preserved side. A
	// RIGHT join preserves its inner child, which in this arm is a
	// parameterized probe — unmatched preserved rows surface from no probe
	// execution, so no driver (fused NLI, decomposed lateral, bitmap) can
	// emit them (Q13 dropped its 50,000 zero-order customers, 34→33 rows).
	// The admitted set mirrors partialHashJoinTypeOK (Inner/Left/Semi/Anti);
	// FULL never reaches here (jointypeForDirection declines it). The Right
	// direction itself stays legal for hash/merge/plain-NL — only this arm
	// declines, and addNestLoopPath must keep admitting Right (a complete
	// inner is sweepable by the generic driver).
	if jt == parser.JoinRight {
		return
	}
	o := outer.CheapestTotal
	if o == nil || o.RequiredOuter != 0 {
		return
	}
	for _, i := range inner.CheapestParameterized {
		if i == nil || i.RequiredOuter == 0 {
			continue
		}
		req := calcNestloopRequiredOuter(outer.Relids, o.RequiredOuter, inner.Relids, i.RequiredOuter)
		// try_nestloop_path's test, verbatim (joinpath.c:882-889),
		// over this joinrel's param_source_rels (C-08 derivation,
		// computed once per addPathsToJoinrel call).
		if req != 0 &&
			!relsOverlap(req, paramSrc) &&
			!allowStarSchemaJoin(outer.Relids, i.RequiredOuter) {
			continue
		}
		// The goopg-only second gate: PG accepts a still-parameterised result
		// here (the star-schema case) and gives it a `ppi_rows` from
		// `get_parameterized_joinrel_size`. goopg has no joinrel sizer until
		// P5.6, so such a path would have to invent its own cardinality.
		// Ledgered against P5.6 rather than approximated.
		if req != 0 {
			continue
		}
		residual := nestloopResidualClauses(clauses, i, inner.Relids, i.RequiredOuter)
		// PG offers the bare inner AND, when `get_memoize_path` returns one,
		// the cache-wrapped inner — both to the same `try_nestloop_path`
		// (joinpath.c:1965-1986), so `add_path` decides whether the cache pays.
		// The residual is computed ONCE, from the bare inner: a Memoize wrapper
		// changes nothing about which clauses the probe below it enforces, and
		// re-deriving it per candidate would invite the two to disagree.
		// M0127-P5.4b-ii-b-2.
		for _, in := range []*Path{i, getMemoizePath(s, outer, o, i, cp)} {
			if in == nil {
				continue
			}
			// `in.Cost` prices ONE execution with the parameter bound and
			// `in.Rows` is its `ppi_rows` (03 §9 rule 3), so the inner's own
			// total IS the per-outer-row rescan cost for an uncached inner —
			// PG's `cost_rescan` default for an index scan, which caches
			// nothing between rescans (costsize.c:4577). This is the whole
			// reason PG re-costs the pair here instead of in the plain-NL arm,
			// where the inner's unparameterised total would be charged per
			// outer row. `pathRescanTotal` is the one place that knows a
			// Memoize wrapper answers differently.
			// take2 P2-06: as the plain nested loop above. An NLI inner is a
			// parameterised index probe, so its "cache" is per-probe and the
			// Memoize arm of nestLoopInnerRescanCost is the one that fires
			// when a Memoize sits between.
			matBuild, matRescan := nestLoopInnerRescanCost(in, cp)
			// The rescan STARTUP rides alongside the run: nestloopCost
			// subtracts it back out of the run part
			// (innerRescanRun = Total − Startup), so pass Total with
			// the startup included — PG's rescan_total, not just its
			// run half.
			rsStart := nestLoopInnerRescanStartup(in)
			cost := nestloopCost(cp, o.Cost, in.Cost, o.Rows, in.Rows, rsStart, matRescan+rsStart)
			cost.Total += matBuild
			cost.Total += qualEvalCost(cp, len(residual), o.Rows*in.Rows)
			addPath(joinrel, &Path{
				Kind:     PathNestLoop,
				Jointype: jt, // C-03b; see addHashJoinPath.
				Rel:      joinrel,
				Rows:     joinrel.Rows,
				Cost:     cost,
				Children: []*Path{o, in},
				// R53 slice 1: the partition, in Children order.
				OuterRelids: outer.Relids,
				InnerRelids: inner.Relids,
				Residual:    residual,
				// Empty by the test above. Carried through the constructor
				// rather than hard-coded so the star-schema case is a one-line
				// relaxation once P5.6's sizer exists.
				RequiredOuter: req,
				// create_nestloop_path (pathnode.c:2590). C-19a.
				ParallelSafe: parallelSafeWith(joinrel, o, in),
				// initial_cost_nestloop (costsize.c:3282-3284): enable_nestloop
				// counts on EVERY nestloop path, parameterised inner or not —
				// PG has no separate index-nestloop switch — plus the inner
				// and outer inputs' own counts. The plain-NL arm (pathgen.go)
				// always had this; the NLI arm did not, which R59's probe
				// repricing exposed: with nestloop disabled the NLI path
				// priced below the merge join and stole a contest the test
				// fixture had closed to every nestloop.
				DisabledNodes: disabledNodesFor(!cp.enableNestLoop, o, in),
			}, "nestloop.index")
		}
	}
}

// addPartialNestLoopPaths is `consider_parallel_nestloop` +
// `try_partial_nestloop_path` (joinpath.c:2107-2214, :945-1010): the last
// missing join arm — a partial path joining a PARTIAL outer to a COMPLETE
// inner, rescanned once per per-worker outer row. R60 (plan-parity-fix
// take2).
//
// It is the NLI arm's sibling deliberately: the residual comes from the
// same `nestloopResidualClauses`, the pair expansion is the same bare +
// `getMemoizePath` shape, and the four costing lines are identical. What
// differs is the outer (every member of `outer.PartialPathlist`, not the
// cheapest total), the inner list (the WHOLE `CheapestParameterized`,
// unparameterised member included — PG iterates the same
// `cheapest_parameterized_paths` with the cheapest total prepended), and
// the filing (`addPartialPath`, not `addPath`).
//
// Two PG admission tests do NOT cross over, one by vacuity and one by
// structure: `try_nestloop_path`'s param_source_rels / star-schema test
// (:882-889) belongs to the serial arm — a partial result must be FULLY
// unparameterised ("Parameterized partial paths are not supported",
// :959), so `req != 0` is a refusal here, not a deferral; and the lateral
// check has nothing to read — no LATERAL shape reaches path generation
// (C-08 invariant, cited the same way the two landed partial producers
// cite it: by comment, with no field to test).
func addPartialNestLoopPaths(s *searchCtx, joinrel, outer, inner *RelOptInfo, cp costParams, jt parser.JoinType, clauses []*restrictInfo) {
	// V0: the reader-only gate, shared with both landed partial producers
	// (`joinpathsparallel.go:82-91`): nothing but `generateUsefulGatherPaths`
	// and the next level's own partial producer reads a partial path.
	if gatherPathsMode == gatherPathsOff {
		tracePVetoCtx(s, "nestloop", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V0", "jt="+traceJoinTypeName(jt))
		return
	}
	// The dispatch gate (joinpath.c:2022-2031), minus the vacuous:
	// UNIQUE_OUTER has no goopg jointype, so the set collapses to exactly
	// `partialHashJoinTypeOK` ({INNER, LEFT, SEMI, ANTI}).
	if !partialHashJoinTypeOK(jt) {
		tracePVetoCtx(s, "nestloop", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V1", "jt="+traceJoinTypeName(jt))
		return
	}
	// R94 (plan-parity-fix-take2): file only the evidenced ordinary INNER
	// shape. LEFT/SEMI/ANTI have no admitted consumer — the path
	// classifier (partialPathDrivingKind) and the node twin
	// (nestedLoopJoinIsPartialCapable) both refuse them — and a refused
	// path filed at PartialPathlist[0] would starve admittable hash/merge
	// siblings, since makeGatherPath reads the head only. Filing them
	// would be costed-but-never-runnable noise at best. Narrowing the
	// filing (rather than the classifier) keeps every filed partial NL
	// runnable end to end.
	if jt != parser.JoinInner {
		tracePVetoCtx(s, "nestloop", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V1-nl-inner", "jt="+traceJoinTypeName(jt))
		return
	}
	// `joinrel->consider_parallel` (already propagated by
	// joinrelConsiderParallel), as the partial-hash producer reads it.
	if s == nil || !s.parallelModeOK || joinrel == nil || !joinrel.ConsiderParallel {
		sub := "cp"
		if s == nil {
			sub = "s-nil"
		} else if !s.parallelModeOK {
			sub = "mode"
		} else if joinrel == nil {
			sub = "nil-joinrel"
		}
		tracePVetoCtx(s, "nestloop", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V2", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	// `outerrel->partial_pathlist != NIL`.
	if len(outer.PartialPathlist) == 0 {
		tracePVetoCtx(s, "nestloop", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V4", "jt="+traceJoinTypeName(jt))
		return
	}
	// PG's `foreach` over the whole partial_pathlist — NOT the head. The
	// orderings that motivate it upstream do not exist yet (NL paths carry
	// no pathkeys, §2.iii), but rows-per-worker differ by outer (a dearer
	// outer on more workers rescans less), so a non-head outer is not
	// strictly dominated. Lists are tiny; faithfulness is cheap.
	for _, o := range outer.PartialPathlist {
		if o == nil || o.ParallelWorkers <= 0 || o.RequiredOuter != 0 {
			// PG *asserts* the outer unparameterised
			// (`bms_is_empty(PATH_REQ_OUTER(outer_path))`, :967) — refused,
			// not asserted, per `addPartialPath`'s fail-closed convention.
			sub := "unsafe"
			if o == nil {
				sub = "head-nil"
			} else if o.ParallelWorkers <= 0 {
				sub = "workers"
			} else {
				sub = "outer-param"
			}
			tracePVetoCtx(s, "nestloop", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V5", "sub="+sub+" jt="+traceJoinTypeName(jt))
			continue
		}
		// `innerrel->cheapest_parameterized_paths`, prepended unparameterised
		// member included (`setCheapest`, path.go:1193) — so the
		// unparameterised inner (partial PLAIN nestloop) and the
		// parameterised probes (partial INDEX nestloop) ride one loop, as
		// they do in PG. UNIQUE_INNER is vacuous (no such jointype, hence no
		// `create_unique_path` to call). The materialised-inner tail is
		// deliberately absent: goopg builds no Material path anywhere
		// (`plannersettings.go:72`), a pre-existing all-join-types gap.
		for _, i := range inner.CheapestParameterized {
			if i == nil || !i.ParallelSafe {
				// "Can't join to an inner path that is not parallel-safe."
				tracePVetoCtx(s, "nestloop", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V7", "jt="+traceJoinTypeName(jt))
				continue
			}
			// The subset test (joinpath.c:968-990): the inner's
			// parameterisation must be fully satisfiable by the outer. The
			// top_parent branch is vacuous (no top parents in goopg);
			// `calcNestloopRequiredOuter` with an unparameterised outer IS
			// the subset test, and a nonzero remainder is refused (V8) —
			// there is no star-schema exception here, because unlike the
			// serial arm the result may not stay parameterised.
			req := calcNestloopRequiredOuter(outer.Relids, o.RequiredOuter, inner.Relids, i.RequiredOuter)
			if req != 0 {
				tracePVetoCtx(s, "nestloop", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V8", "jt="+traceJoinTypeName(jt))
				continue
			}
			residual := nestloopResidualClauses(clauses, i, inner.Relids, i.RequiredOuter)
			// The reparameterizability check
			// (`path_is_reparameterizable_by_child`) has no goopg
			// machinery to mirror — and nothing to check: with `req == 0`
			// and an unparameterised outer there is no parameterisation
			// left to translate. Stated, not skipped silently.
			for _, in := range []*Path{i, getMemoizePath(s, outer, o, i, cp)} {
				if in == nil {
					continue
				}
				// `initial_cost_nestloop` performs no worker division, and
				// none is wanted: the outer input arrives already
				// per-worker-divided (cost AND rows), so the four lines are
				// the NLI arm's verbatim — the partial-ness lives in the
				// inputs, exactly as the partial-hash producer documents.
				// (`add_partial_path_precheck` is CPU-only — bail before
				// creating the path — and the post-costing domination
				// decides identically, so it is not mirrored.)
				matBuild, matRescan := nestLoopInnerRescanCost(in, cp)
				cost := nestloopCost(cp, o.Cost, in.Cost, o.Rows, in.Rows, 0, matRescan)
				cost.Total += matBuild
				cost.Total += qualEvalCost(cp, len(residual), o.Rows*in.Rows)
				// `final_cost_nestloop` (costsize.c:4307-4314-twin): "For
				// partial paths, scale row estimate." One divisor, applied
				// here and undone by `computeGatherRows` — not two.
				divisor := getParallelDivisor(o.ParallelWorkers, cp.parallelLeaderParticipation)
				addPartialPath(joinrel, &Path{
					Kind:     PathNestLoop,
					Jointype: jt, // C-03b; see addHashJoinPath.
					Rel:      joinrel,
					Rows:     clampRowEst(joinrel.Rows / divisor),
					Cost:     cost,
					Children: []*Path{o, in},
					// R53 slice 1: the partition, in Children order.
					OuterRelids: outer.Relids,
					InnerRelids: inner.Relids,
					Residual:    residual,
					// Empty by the V8 refusal above. Carried through the
					// constructor rather than hard-coded, as the NLI arm does.
					RequiredOuter: req,
					// create_nestloop_path gets `outer_path->parallel_workers`
					// ("a foolish way to estimate parallel_workers, but for
					// now…", pathnode.c:2733); parallel_aware stays false —
					// there is no shared NL build.
					ParallelSafe:    parallelSafeWith(joinrel, o, in),
					ParallelWorkers: o.ParallelWorkers,
					ParallelAware:   false,
					// The R59 rule (costsize.c:3282): enable_nestloop counts
					// on EVERY nestloop path, partial ones included.
					DisabledNodes: disabledNodesFor(!cp.enableNestLoop, o, in),
				}, "join.nestloop.partial")
			}
		}
	}
}
