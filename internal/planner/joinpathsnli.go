package planner

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
// Still inert: `GOOPG_PGSHAPED_DP` is OFF and nothing calls the search from
// `planSelect`, so no plan can move. Validated in isolation by
// `joinpathsnli_test.go`.

// paramSourceRels is PG's `extra->param_source_rels` (joinpath.c:227-263): the
// relations a join path is allowed to stay parameterised BY, which PG derives
// entirely from `SpecialJoinInfo` entries and LATERAL references.
//
// It is constant-empty in v1 and that is a derivation, not a placeholder: 03
// §4.4 pins every outer/semi/anti construct outside the search as an opaque
// `PathPrebuilt` initial rel, so `join_info_list` has no entry the search can
// see, and goopg has no LATERAL in the searched shape either. PG's own loop
// over an empty `join_info_list` produces exactly this. The named constant
// keeps the admission test below shaped like PG's, so admitting special joins
// later is a change to this function rather than to the test.
func paramSourceRels() RelSet { return 0 }

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
// `pickIndexCoveringAllLeadingColumns` accepted — `Path.IndexClauses`
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
// silently relies on it: every JOIN path in the search is unparameterised, so
// `Path.Rows == Rel.Rows` holds for every join path and the only parameterised
// paths in play are base index scans. That is what lets `addNestLoopPath` and
// `addHashJoinPath` set `Rows: joinRel.Rows` unconditionally without a
// `ppi_rows` of their own.
func addNLIPaths(joinrel, outer, inner *RelOptInfo, cp costParams, clauses []*restrictInfo) {
	o := outer.CheapestTotal
	if o == nil || o.RequiredOuter != 0 {
		return
	}
	for _, i := range inner.CheapestParameterized {
		if i == nil || i.RequiredOuter == 0 {
			continue
		}
		req := calcNestloopRequiredOuter(outer.Relids, o.RequiredOuter, inner.Relids, i.RequiredOuter)
		// try_nestloop_path's test, verbatim (joinpath.c:882-889).
		if req != 0 &&
			!relsOverlap(req, paramSourceRels()) &&
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
		// `i.Cost` prices ONE execution with the parameter bound and `i.Rows`
		// is its `ppi_rows` (03 §9 rule 3), so the inner's own total IS the
		// per-outer-row rescan cost — PG's `cost_rescan` default for an index
		// scan, which caches nothing between rescans (costsize.c:4577). This
		// is the whole reason PG re-costs the pair here instead of in the
		// plain-NL arm, where the inner's unparameterised total would be
		// charged per outer row.
		cost := nestloopCost(cp, o.Cost, i.Cost, o.Rows, i.Rows, i.Cost.Total)
		cost.Total += qualEvalCost(cp, len(residual), o.Rows*i.Rows)
		addPath(joinrel, &Path{
			Kind:     PathNestLoop,
			Rel:      joinrel,
			Rows:     joinrel.Rows,
			Cost:     cost,
			Children: []*Path{o, i},
			Residual: residual,
			// Empty by the test above. Carried through the constructor rather
			// than hard-coded so the star-schema case is a one-line relaxation
			// once P5.6's sizer exists.
			RequiredOuter: req,
		})
	}
}
