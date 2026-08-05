package planner

// M0127-P5.3 / P5.3a — `joinSearchOneLevel` phases 1, 2 and 3, `makeJoinRel`,
// and the `standard_join_search` driver that runs the levels.
//
// PG oracle: `join_search_one_level` (joinrels.c:73) — phase 1 at :85-137,
// phase 2 (bushy) at :141-198, phase 3 (last-ditch) at :200-256;
// `make_rels_by_clause_joins` (:118/:280),
// `make_rels_by_clauseless_joins` (:300); `make_join_rel` (:696) and
// `populate_joinrel_with_paths` (:786); the level loop and per-level
// `set_cheapest` in `standard_join_search` (allpaths.c:3483-3525). Design:
// leftdeep-joins 03 §1, §4.1, §4.2, §4.3, §4.4.
//
// With phase 2 in place (P5.3a) the enumeration is PG's in full: every
// unordered split of a level's relset into two non-empty parts is reachable —
// the (lev−1, 1) splits from phase 1, the (k, lev−k) splits for
// 2 ≤ k ≤ lev−2 from phase 2 — subject to PG's connectivity filters. That
// completeness is what the pair-count test in `joinsearchlevel_test.go` checks
// against 03 §7's closed form.
//
// Two seams stay open on purpose, because their owners are later tasks and a
// stand-in would be a second cost model to unpick later:
//
//   - `joinRelBuilder.sizeJoinRel` is P5.6's `calcJoinrelSize`
//     (`set_joinrel_size_estimates`, costsize.c);
//   - `joinRelBuilder.addPaths` is P5.4's `add_paths_to_joinrel`
//     (joinpath.c:124).
//
// The enumerator owns which pairs are offered and in which outer/inner order,
// which is exactly what this task is verified on. Nothing here is called from
// `planSelect`; the whole search is gated behind `GOOPG_PGSHAPED_DP` (OFF by
// default, joinsearch.go:44) and P5.9 is what flips it. Validated in isolation
// by `joinsearchlevel_test.go`.

import "fmt"

// joinRelBuilder is the search's sizing-and-costing collaborator: everything
// `makeJoinRel` needs that is not enumeration. Splitting it out is what lets
// P5.3 be verified on the pair sequence alone — a test builder records the
// (outer, inner) calls and the enumeration is checked against PG's, with no
// cost model in the way.
type joinRelBuilder interface {
	// sizeJoinRel returns the joinrel's cardinality and tuple width. Called
	// exactly ONCE per relset, from the first pair that reaches it: PG
	// computes the size inside `build_join_rel` on the create path only
	// (relnode.c), and every later pair spanning the same relset reuses it.
	// That is load-bearing rather than an optimisation — `add_path` compares
	// paths within one rel, and two pairs that disagreed about `rows` would
	// make those comparisons meaningless. `clauses` is the joinrel's own
	// restriction list (PG's `build_joinrel_restrictlist` output).
	sizeJoinRel(outer, inner *RelOptInfo, clauses []*restrictInfo) (rows float64, width int)

	// addPaths is `add_paths_to_joinrel` for ONE direction: it must treat
	// `outer` as the outer (probe/driving) side and `inner` as the inner
	// (build/inner-scan) side, and add every path it finds to `joinrel` via
	// `addPath`. makeJoinRel calls it twice per pair, once per direction.
	addPaths(joinrel, outer, inner *RelOptInfo, clauses []*restrictInfo) error
}

// joinOrderRestricted is PG's `have_join_order_restriction` (joinrels.c:1066),
// reserved and CONSTANT FALSE in v1 (03 §4.4). It is the second disjunct of
// both phase-1 and phase-2 pair gates upstream; goopg keeps the disjunct in the
// code so that admitting special joins later is a change to this function, not
// a change to the enumerator's shape.
//
// Why false is correct today and not a shortcut: v1 pins every outer/semi/anti
// construct as an opaque `PathPrebuilt` initial rel (03 §4.4), so the search
// only ever sees INNER-joinable rels, and PG generates join-order restrictions
// solely from `SpecialJoinInfo` entries and LATERAL references — neither of
// which can reach the search while the pin holds. The pin relaxes when
// `join_is_legal`-equivalent inference lands.
func joinOrderRestricted(a, b *RelOptInfo) bool { return false }

// hasJoinRestriction is PG's `has_join_restriction` (joinrels.c:1160), the
// per-rel form used by phase 1's outer branch. Constant false in v1 for the
// same reason as joinOrderRestricted.
func hasJoinRestriction(rel *RelOptInfo) bool { return false }

// joinSearch is the `standard_join_search` analogue (allpaths.c:3457): run
// every level from 2 to nrels, `set_cheapest` each rel the level produced, and
// hand back the sole rel at the top level. `s` must already carry its initial
// rels (buildInitialRels).
//
// `clauses` may be nil — a one-relation problem, or a FROM list with no join
// qual at all, in which case phase 1's clauseless branch carries the whole
// search. Every restrictInfoList predicate is nil-safe for exactly this case.
//
// On error the caller must fall back to the syntactic join shape rather than
// failing the statement (03 §4.2): an error here means the search could not
// enumerate, not that the query is unplannable.
func (s *searchCtx) joinSearch(clauses *restrictInfoList, b joinRelBuilder) (*RelOptInfo, error) {
	if b == nil {
		return nil, fmt.Errorf("join search: no joinrel builder")
	}
	if got := len(s.levelRels(1)); got != s.nrels {
		return nil, fmt.Errorf("join search: %d initial rels for a %d-relation problem", got, s.nrels)
	}
	s.clauses = clauses
	s.builder = b

	// The provenance block is emitted for a FAILED search too (P5.9-l-ii): a
	// search that could not enumerate is precisely the case where "what was
	// offered" is the evidence, and a deferred emit that only fired on success
	// would go dark exactly then. `trace` is nil unless the gate is on.
	defer s.trace.emit()

	for lev := 2; lev <= s.nrels; lev++ {
		if err := s.joinSearchOneLevel(lev); err != nil {
			s.traceFailed(err)
			return nil, err
		}
		// PG runs set_cheapest per rel only after the whole level is done
		// (allpaths.c:3503-3517), because a joinrel keeps receiving paths from
		// every pair that spans it and the cheapest is not known until the
		// last such pair has been offered.
		for _, rel := range s.levelRels(lev) {
			if len(rel.Pathlist) == 0 {
				err := fmt.Errorf("join search: joinrel %#04x has no paths", uint16(rel.Relids))
				s.traceFailed(err)
				return nil, err
			}
			setCheapest(rel)
		}
	}
	top, err := s.finalRel()
	if err != nil {
		s.traceFailed(err)
		return nil, err
	}
	if s.trace != nil {
		s.trace.top = top.Relids
	}
	return top, nil
}

// traceFailed records why the search gave up, so the emitted block says whether
// the enumeration it lists is the whole enumeration or a truncated one.
func (s *searchCtx) traceFailed(err error) {
	if s.trace == nil || err == nil {
		return
	}
	s.trace.failed = err.Error()
}

// joinSearchOneLevel is `join_search_one_level` (joinrels.c:73) at phases 1 and
// 3. It appends to `s.joinrels[lev]`, which PG requires to be empty on entry
// (its `Assert(joinrels[level] == NIL)`, :79).
func (s *searchCtx) joinSearchOneLevel(lev int) error {
	if lev < 2 || lev > s.nrels {
		return fmt.Errorf("join search: level %d out of range for a %d-relation problem", lev, s.nrels)
	}
	if len(s.levelRels(lev)) != 0 {
		return fmt.Errorf("join search: level %d is already populated", lev)
	}

	prev := s.levelRels(lev - 1)
	initial := s.levelRels(1)

	// Phase 1 — left-sided and right-sided plans: rels of exactly lev-1
	// members joined against initial rels (joinrels.c:85-137).
	//
	// The branch is per OLD REL, not per pair, and that placement is PG's:
	// a rel that participates in no join clause and no restriction is crossed
	// in against EVERY initial rel it does not already contain, at every
	// level, so a disconnected 1-row dimension can join at level 2 instead of
	// waiting for the last-ditch pass. 03 §4.1's pseudocode pushes the branch
	// inside the inner loop; that is the same enumeration (a clauseless old
	// rel can have no relevant clause with any base rel, so it always falls
	// through to the else) EXCEPT for the level-2 `first` offset below, which
	// PG applies to the clause branch only.
	s.tracePhase = tracePhaseLeftRight
	for i, old := range prev {
		if !s.clauses.hasNoJoinClauseAtAll(old) || hasJoinRestriction(old) {
			// At level 2 the pair condition is symmetric and the previous
			// level IS the initial-rel list, so initial rels before this one
			// were already paired with it from the other side; starting after
			// it drops the duplicate (joinrels.c:112-116). At level > 2 the
			// two lists are different and every initial rel must be tried.
			first := 0
			if lev == 2 {
				first = i + 1
			}
			if err := s.makeRelsByClauseJoins(old, initial, first); err != nil {
				return err
			}
			continue
		}
		// PG's own note (joinrels.c:127-136): at level 2 two clauseless
		// initial rels are considered in both directions, redundantly.
		// makeJoinRel's find-or-create absorbs the duplicate into one rel, so
		// the cost is a second addPaths pass over an existing relset, exactly
		// as upstream.
		if err := s.makeRelsByClauselessJoins(old, initial); err != nil {
			return err
		}
	}

	// Phase 2 — bushy plans (joinrels.c:141-198): rels of k initial rels
	// joined to rels of lev−k, for 2 ≤ k ≤ lev−2. It belongs HERE, between
	// phases 1 and 3, because phase 3's "did this level come up empty" test
	// must see the bushy pairs too, or it would force cartesian products for a
	// level a bushy pair had already populated.
	//
	// Unlike phase 1 there is no clauseless branch: a bushy pair is built ONLY
	// when a join clause (or, later, an order restriction) connects the two
	// composites. PG's stated reason is planning time (:144-146) — the
	// unfiltered space is (3ⁿ − 2ⁿ⁺¹ + 1)/2 pairs, ~7M at n=15 — and the
	// filter is what keeps goopg's ceiling-16 no-GEQO policy (03 §7) tenable.
	s.tracePhase = tracePhaseBushy
	for k := 2; ; k++ {
		otherLevel := lev - k
		// make_join_rel(x, y) already handles y,x, so the k-loop only has to
		// reach the halfway point (joinrels.c:148-157). At lev 2 and 3 this
		// breaks on the first iteration — there is no bushy shape below 4.
		if k > otherLevel {
			break
		}
		// Both lists are strictly below `lev`, so neither can grow while it is
		// being iterated: makeJoinRel only ever appends at `lev`.
		kRels := s.levelRels(k)
		otherRels := s.levelRels(otherLevel)
		for i, old := range kRels {
			// A composite with no join clause at all is skipped outright
			// (:165-172). In v1 this changes cost, not results: the pair gate
			// below is clause-only while `joinOrderRestricted` is false, so a
			// clauseless rel could not have produced a pair anyway — which is
			// why no test can observe the skip and this comment stands in for
			// one. It is kept verbatim because the `has_join_restriction`
			// disjunct makes it semantically live the moment restrictions
			// enter the search: then a clauseless rel CAN be forced into a
			// bushy plan, and the skip is what decides which ones are.
			if s.clauses.hasNoJoinClauseAtAll(old) && !hasJoinRestriction(old) {
				// Recorded against the empty relset because the skip is per OLD
				// REL, not per pair: what the trace has to show is that this
				// composite was withheld from the whole bushy pass, not that
				// some particular partner was refused (P5.9-l-ii).
				s.trace.decline(tracePhaseBushy, old.Relids, 0, "clauseless-composite")
				continue
			}
			// At the halfway level the two lists are the SAME list, so every
			// pair before this one has already been considered from the other
			// side; the mirror-image offset drops the duplicate (:174-177).
			first := 0
			if k == otherLevel {
				first = i + 1
			}
			// :182-194 is makeRelsByClauseJoins verbatim — non-overlap, then
			// `have_relevant_joinclause || have_join_order_restriction`.
			if err := s.makeRelsByClauseJoins(old, otherRels, first); err != nil {
				return err
			}
		}
	}

	// Phase 3 — last-ditch (joinrels.c:200-256). A level can come up empty
	// when every rel in the sub-problem has join clauses, but only to rels
	// OUTSIDE the sub-problem, so phase 1's clause branch found nothing and
	// its clauseless branch never fired. PG's answer is to redo phase 1 with
	// the clause requirement dropped; left/right-sided only, no bushy
	// (joinrels.c:215-216).
	if len(s.levelRels(lev)) == 0 {
		s.tracePhase = tracePhaseLastDitch
		for _, old := range prev {
			if err := s.makeRelsByClauselessJoins(old, initial); err != nil {
				return err
			}
		}
		// PG errors only when `join_info_list == NIL && !hasLateralRTEs`
		// (:252-255), because with special joins an empty level can be legal
		// and recoverable one level up. In v1 special joins never enter the
		// search (03 §4.4), so PG's guard condition is unconditionally true
		// here and a still-empty level is a planner bug. It is returned as an
		// error rather than raised: 03 §4.2 requires the caller to fall back
		// to the syntactic shape for the whole search problem, never to fail
		// the statement.
		if len(s.levelRels(lev)) == 0 {
			return fmt.Errorf("join search: failed to build any %d-way joins", lev)
		}
	}
	return nil
}

// makeRelsByClauseJoins is `make_rels_by_clause_joins` (joinrels.c:280): join
// `old` to each rel in `others` from index `first` on that it does not overlap
// and is connected to.
func (s *searchCtx) makeRelsByClauseJoins(old *RelOptInfo, others []*RelOptInfo, first int) error {
	for i := first; i < len(others); i++ {
		other := others[i]
		if relsOverlap(old.Relids, other.Relids) {
			continue
		}
		if !s.clauses.hasRelevantJoinClause(old, other) && !joinOrderRestricted(old, other) {
			// The one gate that can silently withhold a partition PG chose.
			// Recorded (P5.9-l-ii) because "never offered" and "offered and
			// out-costed" are the two readings clause 6 has to choose between,
			// and only the first has a cause worth naming: this line is that
			// cause when it fires.
			s.trace.decline(s.tracePhase, old.Relids, other.Relids, "no-join-clause")
			continue
		}
		if _, err := s.makeJoinRel(old, other); err != nil {
			return err
		}
	}
	return nil
}

// makeRelsByClauselessJoins is `make_rels_by_clauseless_joins`
// (joinrels.c:313): cartesian-join `old` to every non-overlapping rel in
// `others`, connected or not.
func (s *searchCtx) makeRelsByClauselessJoins(old *RelOptInfo, others []*RelOptInfo) error {
	for _, other := range others {
		if relsOverlap(old.Relids, other.Relids) {
			continue
		}
		if _, err := s.makeJoinRel(old, other); err != nil {
			return err
		}
	}
	return nil
}

// makeJoinRel is `make_join_rel` (joinrels.c:696): find-or-create the rel for
// the union relset, compute the restriction list that goes with THIS pairing,
// and offer the pair to the path builder in both outer/inner orders.
//
// The two-call tail is `populate_joinrel_with_paths`' JOIN_INNER arm
// (joinrels.c:809-816) and it is where 03 §4.4's printing convention is
// enforced structurally: `(a ⋈ b)` and `(b ⋈ a)` are ONE RelOptInfo with two
// paths, not two rels. Which input drives is a property of the path — the
// emitted `Join` node's children follow the path's outer/inner order, and an
// input-order variant surfaces as a path attribute (02 §2), never as a
// re-shaped tree.
//
// Steps of PG's function that v1 collapses, each because the pin of 03 §4.4
// keeps special joins out of the search: `join_is_legal` (always legal, plain
// inner join), `add_outer_joins_to_relids` (no outer-join relids to add), the
// `reversed` swap (only a special join can request one), the dummy-sjinfo
// construction (nothing reads it until P5.6), and `is_dummy_rel` /
// `restriction_is_constant_false` (goopg proves no rel empty at plan time).
// Each becomes real work when the pin relaxes; none of them can fire while it
// holds.
func (s *searchCtx) makeJoinRel(rel1, rel2 *RelOptInfo) (*RelOptInfo, error) {
	if rel1 == nil || rel2 == nil {
		return nil, fmt.Errorf("join search: nil input rel")
	}
	// PG asserts this (joinrels.c:706); goopg returns it, because the search
	// is a fallback-on-error path rather than an assertion-checked one.
	if relsOverlap(rel1.Relids, rel2.Relids) {
		return nil, fmt.Errorf("join search: overlapping input relsets %#04x and %#04x",
			uint16(rel1.Relids), uint16(rel2.Relids))
	}
	joinrelids := rel1.Relids | rel2.Relids

	// build_joinrel_restrictlist (relnode.c): the quals this join applies —
	// computable here and not already applied below. 03 §3's coverage rule,
	// which is a different predicate from the connectivity gate above.
	clauses := s.clauses.clausesFor(rel1.Relids, rel2.Relids)

	joinrel := s.findRel(joinrelids)
	// Recorded BEFORE the find-or-create branch, so the `created` bit says
	// which pair of the several spanning this relset was the first to reach it
	// — the pair whose `sizeJoinRel` fixed the relset's cardinality for every
	// later comparison (joinsearchlevel.go:43) (P5.9-l-ii).
	s.trace.offer(s.tracePhase, rel1.Relids, rel2.Relids, joinrel == nil)
	if joinrel == nil {
		rows, width := s.builder.sizeJoinRel(rel1, rel2, clauses)
		// The same floor buildInitialRels applies (joinsearch.go:220-240):
		// a zero-row rel would make every join above it free and the level
		// above would order itself on noise.
		if !(rows >= 1) {
			rows = 1
		}
		joinrel = newRelOptInfo(joinrelids, rows, width)
		// A join row is the concatenation of its two inputs' rows — the
		// executor's Join emits left++right — so the column count adds
		// (M0127-P5.7-a).
		joinrel.NCols = relNCols(rel1) + relNCols(rel2)
		// `joinrel->consider_startup = (root->tuple_fraction > 0)`
		// (relnode.c:707) — the same query-wide fact every base rel copied in
		// `buildInitialRels`, not something inherited from the inputs
		// (M0127-P5.7-b).
		joinrel.ConsiderStartup = s.tupleFraction > 0
		if err := s.addRel(joinrel); err != nil {
			return nil, err
		}
	}

	if err := s.builder.addPaths(joinrel, rel1, rel2, clauses); err != nil {
		return nil, err
	}
	if err := s.builder.addPaths(joinrel, rel2, rel1, clauses); err != nil {
		return nil, err
	}
	return joinrel, nil
}
