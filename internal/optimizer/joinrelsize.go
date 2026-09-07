package optimizer

// M0127-P5.6-b — `calcJoinrelSize` and the concrete `joinRelBuilder`: the
// joinrel's row estimate, computed ONCE at find-or-create time, and the object
// that finally binds the search's two open seams together.
//
// PG oracle: `set_joinrel_size_estimates` / `calc_joinrel_size_estimate`
// (optimizer/path/costsize.c:5445, :5499) and
// `get_foreign_key_join_selectivity` (:5651); `get_variable_numdistinct`'s
// `isunique` arm (utils/adt/selfuncs.c:6338). Design: leftdeep-joins
// [04](../../docs/design/leftdeep-joins/04-cost-and-cardinality.md) §2 (rows
// once), §3.1 (the FK/unique-superkey generalisation), and
// [cost-model/14](../../docs/design/cost-model/14-fk-aware-and-mcv-join-selectivity.md)
// §2-§3, which is where the superkey extension was designed and reviewed.
//
// PG's shape, reproduced here in full (the INNER arm; C-05 added the LEFT /
// RIGHT / FULL / SEMI / ANTI arms of the jointype switch — see
// `calcJoinrelSize` and docs/design/planner-c05-join-sizing/DESIGN.md):
//
//	fkselec = get_foreign_key_join_selectivity(&restrictlist)  // removes clauses
//	jselec  = clauselist_selectivity(restrictlist)             // what's left
//	rows    = clamp_row_est(outer_rows * inner_rows * fkselec * jselec)
//
// The two-step structure is the point, not an implementation detail. A join
// whose key columns are covered by a UNIQUE/FK key does not fan out at all, and
// the per-clause `eqjoinsel` of P5.6-a cannot express that: it prices each
// clause independently, so an (a,b) composite key equated column-by-column is
// charged 1/nd_a · 1/nd_b — a product of two marginal distincts that is far
// smaller than the 1/ntuples the key actually implies. Removing the covered
// clauses and substituting ONE 1/ntuples is how upstream avoids exactly that,
// and it is the mechanism 04 §3.1 names as the primary Q9 fix.
//
// WHERE goopg's evidence differs from PG's, and why that is a deliberate
// extension rather than a divergence: PG derives the no-fan-out from
// `root->fkey_list` (declared foreign keys) and — for SINGLE-column keys only
// (`has_unique_index`, plancat.c:2244 requires `nkeycolumns == 1`) — from
// `vardata->isunique`. Neither reaches Q9's two-column `partsupp` PK, and the
// loaded TPC-H/TPC-DS data declares no FKs at all, so PG's own machinery would
// find no evidence here whatsoever. goopg therefore accepts a COMPOSITE unique
// index as the same evidence upstream accepts a composite FK for, which is the
// substitution cost-model/14 §2 argued for and which reproduces
// `get_variable_numdistinct`'s `isunique ⇒ nd = ntuples` on the key as a whole.
// The declared-FK arm is implemented beside it for schemas that do declare
// them.
//
// M0127-P5.6-c added the two CLAMPS that sit after that product (04 §3.3), and
// they are deliberately not one mechanism:
//
//   - the key-implied bound is STRUCTURAL and always sound — a proven key means
//     each row of the other side matches at most one row of the key side, so the
//     output cannot exceed the other side's rows, whatever the selectivities
//     multiplied out to (`keyImpliedRowsBound`);
//   - the `max(l, r)` cap is a HEURISTIC backstop inherited from M0126-0010
//     (cardinality.go:400-406) and fires only where that one does: when nothing
//     was proven AND every surviving clause was priced by a selfuncs.h constant.
//     Applying it to a measured estimate would truncate genuine many-to-many
//     joins, whose blow-up is a fact about the data rather than an artefact of
//     compounding.
//
// Live since M0127-P5.9 (2026-08-06): `GOOPG_PGSHAPED_DP` defaults ON
// (`pgShapedDPFromEnv` is `v != "0"`) and `planSelect` calls the search, so
// this builder sizes production joinrels rather than being reachable only from
// tests. Validated by `joinrelsize_test.go`, and now also by the planner bar.

import (
	"math"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// searchJoinRelBuilder is the concrete `joinRelBuilder` (joinsearchlevel.go:42)
// — the object `makeJoinRel` calls for everything that is not enumeration.
//
// It is deliberately thin: sizing is `calcJoinrelSize` below and path
// generation is `addPathsToJoinrel` (joinpaths.go), both of which already exist
// as free functions. What the type contributes is the BINDING — the search's
// context and the planner's catalog, held together so that the sizer and the
// path generators cannot end up reading two different catalogs (the dbOid
// hazard of cost-model/14 §2: a bare `InMemory` resolves `DefaultDBOid`'s
// indexes whatever database is active, which would let a uniqueness proof fire
// off another database's index).
type searchJoinRelBuilder struct {
	s   *searchCtx
	cat catalog.Catalog
}

// newJoinRelBuilder binds a search context and the planner's catalog into the
// builder `joinSearch` runs with. `cat` may be nil — the sizer then has no
// uniqueness evidence and falls back to the per-clause estimate, which is a
// legitimate (if pessimistic) answer rather than an error.
func newJoinRelBuilder(s *searchCtx, cat catalog.Catalog) *searchJoinRelBuilder {
	return &searchJoinRelBuilder{s: s, cat: cat}
}

func (b *searchJoinRelBuilder) sizeJoinRel(outer, inner *RelOptInfo, clauses []*restrictInfo, sjinfo *SpecialJoinInfo) (float64, int) {
	return b.s.calcJoinrelSize(b.cat, outer, inner, clauses, sjinfo)
}

func (b *searchJoinRelBuilder) addPaths(joinrel, outer, inner *RelOptInfo, clauses []*restrictInfo, sjinfo *SpecialJoinInfo) error {
	return addPathsToJoinrel(b.s, joinrel, outer, inner, clauses, b.s.cp, sjinfo)
}

// sizingJointype is the jointype a joinrel is SIZED as: the SJI's when the
// pair matched one, JOIN_INNER otherwise (`join_is_legal` returns no sjinfo
// for a plain inner join and `make_join_rel` then builds a dummy INNER one,
// joinrels.c:766-780 — goopg reads the nil directly).
func sizingJointype(sjinfo *SpecialJoinInfo) parser.JoinType {
	if sjinfo == nil {
		return parser.JoinInner
	}
	return sjinfo.Jointype
}

// joinPublishesInner reports whether a joinrel built by a pair performing
// this join carries its INNER input's columns — the single meeting point
// (C-05 DESIGN §4.5) of the sizer's `Width` and `makeJoinRel`'s `NCols` /
// `AvgVarBytes` / `ColVarBytes`, so that the rel-level figures cannot
// disagree with each other about what a SEMI or ANTI join emits.
//
// A SEMI or ANTI join emits its LHS alone: the RHS exists only to be probed
// for existence, and `createPlanNode`'s SEMI/ANTI arms publish left-only
// schema and layout accordingly (C-03c `joinInputs.publishedSchema`). Every
// other jointype concatenates.
//
// This is a property of the RELSET and not of the arriving pair, which is
// what C-03c's "arrival-order dependent" objection needed answered before the
// rel could carry it: `joinIsLegal` admits a relset containing any of a
// SEMI/ANTI SJI's `MinRighthand` only when the relset lies inside the RHS or
// contains the SJI's LHS as well (the RHS is never joined outward before the
// semijoin is performed; goopg has no `create_unique_path` exception). So for
// a relset holding both hands the semijoin HAS been performed somewhere inside
// it on every pair that spans it, and the RHS columns are absent from every
// route's publication — `({a,b},{c})` carries cols(a)+cols(c) via a SEMI
// child, `({a,c},{b})` carries cols(a,c) via a SEMI pair, `({a},{b,c})` is
// illegal.
func joinPublishesInner(sjinfo *SpecialJoinInfo) bool {
	switch sizingJointype(sjinfo) {
	case parser.JoinSemi, parser.JoinAnti:
		return false
	}
	return true
}

// pushedDownSelectivity is `calc_joinrel_size_estimate`'s `pselec`
// (costsize.c:5555-5580): the selectivity of the clauses at this joinrel that
// are "pushed down" — `RINFO_IS_PUSHED_DOWN(rinfo, joinrelids)`, a clause that
// did not come from this outer join's ON list, or whose required relids are
// not covered by the joinrel — which an outer join applies AFTER
// null-extension and whose selectivity therefore applies to the floored row
// count in full.
//
// Inside goopg's search the partition is empty BY CONSTRUCTION, and this
// function exists so the switch's arms read as PG's and so the place where
// the partition would be computed is named rather than omitted. Two facts
// hold every clause the sizer sees to the joinqual side:
//
//   - `tryJoinSearch` (joinsearchseam.go, C-04a §3.5) holds every WHERE
//     conjunct whose relids reach an admitted outer link's nullable side
//     ABOVE the searched tree — `check_outerjoin_delay`'s verdict spelled as
//     "not in the clause list at all". What enters `restrictInfoList` is a
//     WHERE conjunct proven to touch no nullable side, an ON conjunct of an
//     admitted link, or a `reconsider_outer_join_clauses` constant that
//     becomes a leaf-local filter — all joinquals under PG's definition;
//   - `buildJoinRelRestrictList` admits only `relids ⊆ joinrelids`, so the
//     macro's second disjunct is structurally false.
//
// Resume condition (ledger `C-05 pselec-held-above`): when a slice distributes
// delayed WHERE quals INTO the search (C-02's `delayedAboveOJ` placement,
// C-04c), `restrictInfo` grows the `is_pushed_down` bit and this function
// partitions on it; the arms below already multiply the result in.
func (s *searchCtx) pushedDownSelectivity(clauses []*restrictInfo, joinrelids RelSet) float64 {
	sel := 1.0
	for _, ri := range clauses {
		if ri == nil || relsSubset(ri.relids, joinrelids) {
			continue
		}
		// Unreachable today (see above); priced as PG would if it were not.
		sel *= s.joinClauseSelectivity(ri)
	}
	return clampSelectivity(sel)
}

// calcJoinrelSize is `calc_joinrel_size_estimate` (costsize.c:5501) — C-05
// ported its jointype switch (:5595-5633), replacing the INNER-only maths the
// search sized every joinrel with since P5.6-b and the C-04a LEFT/RIGHT floor
// that stood in for the outer arms until then.
//
// It is called from `makeJoinRel` on the CREATE path only, which is 04 §2's
// rows-once discipline: every path for a relset shares one output-row figure,
// because `add_path` compares paths within one rel and two pairs that disagreed
// about `rows` would make those comparisons meaningless. PG enforces the same
// thing by computing the size inside `build_join_rel` (relnode.c).
//
// Orientation: `outer` is the SJI's LHS and `inner` its RHS — `makeJoinRel`
// applied `join_is_legal`'s `reversed` swap before calling. goopg keeps RIGHT
// as its own jointype where upstream has commuted it into LEFT, so a RIGHT
// SJI's LHS is its NULLABLE side and the preserved input is `inner`
// (`outerJoinRowFloor`, cardinality.go, records the same convention for the
// plan-node estimator); the switch has six arms where PG's has five.
//
// The two clamps sit on the INNER product, before the outer-join floor. They
// are goopg's stand-ins for `fkselec` on a schema that declares no foreign
// keys (cost-model/14 §2; the key-implied bound is structural, the
// all-default `max(l,r)` cap is M0126-0010's heuristic on a pure guess), and
// PG applies its own floor on top of `fkselec` in exactly that order. SEMI and
// ANTI never form the product: their bound (`rows ≤ outer`) is in the formula.
//
// FULL is sized although `jointypeForDirection` declines both of its
// directions at path generation (C-03c): the rel is built before any path is
// tried, and an honest size for a rel whose pathlist stays empty costs nothing.
//
// The width follows `joinPublishesInner`: the sum of the input widths, or the
// LHS width alone for SEMI/ANTI. PG's `build_joinrel_tlist` instead sums only
// the columns needed above the join, so a wide input whose columns are all
// consumed by the join itself is narrower upstream than here; goopg's search
// has no such projection information — 03 §10's boundary map is built over
// the full concatenation — so the sum is the honest answer at this fidelity
// level. Ledgered.
func (s *searchCtx) calcJoinrelSize(cat catalog.Catalog, outer, inner *RelOptInfo, clauses []*restrictInfo, sjinfo *SpecialJoinInfo) (float64, int) {
	if outer == nil || inner == nil {
		return 1, 0
	}
	jt := sizingJointype(sjinfo)
	width := outer.Width
	if joinPublishesInner(sjinfo) {
		width += inner.Width
	}

	// 04 §5's equivalence-class rule first: an EC with n members contributes
	// ONE clause per (outer, inner) split, so charging every member's
	// selectivity would multiply one restriction in several times. This is the
	// same reduction `selectivityClauses` applies — literally the same
	// function — but it has to be applied HERE too, because `makeJoinRel`
	// hands the sizer the joinrel's full restriction list (what the path
	// generators must evaluate), not the selectivity subset.
	est := s.superkeyJoinSelectivity(cat, outer, inner, oneClausePerEquivClass(clauses), jt)
	fkselec := est.sel
	// `allDefault` is 04 §3.3's fallback condition, and it is the residual
	// clauses' property rather than the join's: an estimate every one of whose
	// factors was a constant from selfuncs.h has not measured this join at all.
	allDefault := len(est.residual) > 0
	jselec := 1.0
	for _, ri := range est.residual {
		clauseSel, isdefault := s.joinClauseSelectivityForJoin(ri, jt, outer, inner)
		jselec *= clauseSel
		if !isdefault {
			allDefault = false
		}
	}
	pselec := s.pushedDownSelectivity(clauses, outer.Relids|inner.Relids)

	// The INNER product with its two clamps — the term every non-semi arm
	// starts from (costsize.c:5601, :5605, :5613).
	product := func() float64 {
		rows := outer.Rows * inner.Rows * fkselec * jselec
		// Clamp 1 — the key-implied bound. `rowsBound` is +Inf unless a
		// proven key makes one side's rows an upper bound on the output, so
		// this is a no-op on every join that proved nothing.
		if rows > est.rowsBound {
			rows = est.rowsBound
		}
		// Clamp 2 — M0126-0010's `max(l,r)` cap (cardinality.go:400-406),
		// kept for the non-key fallback and, as there, fired ONLY when the
		// estimate was a pure guess. The condition is what keeps it from
		// breaking honest many-to-many joins, whose blow-up is real and
		// measured; and `len(residual) > 0` is what keeps it off a CROSS
		// product, where |L|·|R| is not an error but the answer.
		if !est.fired && allDefault {
			if mx := math.Max(outer.Rows, inner.Rows); rows > mx {
				rows = mx
			}
		}
		return rows
	}

	var rows float64
	switch jt {
	case parser.JoinLeft:
		// "the output must be at least as large as the non-nullable input"
		// (costsize.c:5585-5588); pushed-down quals apply after that.
		rows = math.Max(product(), outer.Rows) * pselec
	case parser.JoinRight:
		// RIGHT preserves the RHS, which is `inner` in goopg's un-commuted
		// orientation (see the header).
		rows = math.Max(product(), inner.Rows) * pselec
	case parser.JoinFull:
		rows = math.Max(math.Max(product(), outer.Rows), inner.Rows) * pselec
	case parser.JoinSemi:
		// "the fraction of LHS rows that have matches" — the inner's size
		// enters only through the clause selectivities (costsize.c:5620).
		rows = outer.Rows * fkselec * jselec
	case parser.JoinAnti:
		rows = outer.Rows * (1.0 - fkselec*jselec) * pselec
	default:
		// JOIN_INNER, and — where PG would `elog(ERROR)` on an unrecognised
		// jointype (:5629) — the pre-C-05 behaviour for anything else, which
		// fails closed in the same direction the C-04a floor did.
		rows = product()
	}
	return clampRowEst(rows), width
}

// superkeyEstimate is what the superkey pass tells the sizer. `sel` and
// `residual` are `get_foreign_key_join_selectivity`'s two outputs; `fired` and
// `rowsBound` are P5.6-c's, and both exist because the fact that a key was
// proven cannot be recovered from `sel` afterwards — a 1/6000000 divisor and a
// per-clause eqjoinsel that happens to land on the same number are the same
// float and mean opposite things about how much the estimate can be trusted.
type superkeyEstimate struct {
	sel      float64
	residual []*restrictInfo
	// fired reports that at least one key was proven and its clauses removed.
	fired bool
	// rowsBound is the tightest STRUCTURAL upper bound on the joinrel's output
	// implied by the proven keys, or +Inf when none is provable.
	rowsBound float64
}

// superkeyJoinSelectivity is `get_foreign_key_join_selectivity` (costsize.c:5651)
// over goopg's evidence: it removes from the clause list every clause covered
// by a proven key on one side of the join, and returns 1/(that side's RAW tuple
// count) in their place, multiplied over each key it can prove.
//
// Three properties of upstream's function are reproduced deliberately:
//
//   - **The divisor is the RAW, unfiltered tuple count** ("we should use the
//     raw table tuple count, not any estimate of its filtered or joined size",
//     costsize.c:5852). It is not a pessimism knob: when the key side is
//     filtered, `|L|·|R_filt|/R_raw` is a real match FRACTION, and dividing by
//     the filtered count instead would claim every surviving row still finds a
//     partner.
//   - **The whole key must be covered** ("if we failed to remove all the
//     matching clauses we expected to find, chicken out", :5760). A partially
//     equated composite key proves nothing about fan-out; only a full cover
//     does. What may be partial is the CLAUSE list, not the key — extra
//     equated columns beyond the key stay in the residual and are charged by
//     `eqjoinsel` on top, exactly as PG leaves non-FK clauses to
//     `clauselist_selectivity` (cost-model/14 §2's superkey (⊆) rule).
//   - **A clause is consumed once.** Each removal takes its clauses out of the
//     worklist before the next key is considered, so two keys that overlap on
//     a clause cannot both charge for it — PG's stated reason for the
//     chicken-out branch, and the same double-count the EC rule prevents one
//     level up.
//
// Where several keys are provable at once, the one with the LARGEST raw count
// is applied first. That is the tightest of the available bounds, which is the
// same choice `eqjoinsel` makes when it divides by max(nd_l, nd_r) (P5.6-a): a
// key on either side gives an upper bound on the join's size and the estimate
// is the minimum of the bounds, not their average.
func (s *searchCtx) superkeyJoinSelectivity(cat catalog.Catalog, outer, inner *RelOptInfo, clauses []*restrictInfo, jt parser.JoinType) superkeyEstimate {
	est := superkeyEstimate{sel: 1.0, residual: clauses, rowsBound: math.Inf(1)}
	if cat == nil || len(clauses) == 0 {
		return est
	}

	pairs := make([]joinKeyPair, len(clauses))
	removed := make([]bool, len(clauses))
	live := false
	for i, ri := range clauses {
		p, ok := s.joinKeyPairOf(ri, outer.Relids, inner.Relids)
		if !ok {
			continue
		}
		pairs[i] = p
		live = true
	}
	if !live {
		return est
	}

	// C-05: `get_foreign_key_join_selectivity`'s SEMI/ANTI rule
	// (costsize.c:5694-5697). For those jointypes a key is usable only when
	// it is a DECLARED foreign key whose referenced table is exactly the
	// singleton inner: "if the referenced rel is on the inside, then all
	// outer rows must have matches in the referenced table" — and the
	// selectivity is then the fraction of referenced rows that survive their
	// own restrictions, `ref_rel->rows / ref_tuples` (:5811-5827), not
	// 1/ref_tuples. A referenced rel on the OUTSIDE, or an inner that is a
	// join, punts the key to `eqjoinsel_semi`.
	//
	// The UNIQUE-index evidence goopg accepts for INNER (cost-model/14 §2) is
	// deliberately NOT extended here: a unique key on the inner says each
	// outer row matches AT MOST one inner row, which bounds a product, but
	// says nothing about the FRACTION of outer rows that match at all — that
	// is the FK's referential guarantee and only the FK has it.
	// `eqjoinsel_semi`'s `nd1 <= nd2 → 1 - nullfrac1` already prices a unique
	// inner correctly. Ledger `C-05 semi-anti-unique-not-fk`.
	semi := jt == parser.JoinSemi || jt == parser.JoinAnti
	admit := func(k provenKey) bool {
		if !semi {
			return true
		}
		return k.fromFK && inner.Relids == RelSet(1)<<uint(k.keyRel)
	}

	// Greedy, largest-divisor-first, until no further key can be proven over
	// the clauses that are still available.
	for {
		best, ok := s.bestProvableKey(cat, pairs, removed, admit)
		if !ok {
			break
		}
		for _, i := range best.clauses {
			removed[i] = true
		}
		est.fired = true
		if semi {
			est.sel *= inner.Rows / best.rawTuples
			continue
		}
		est.sel *= 1.0 / best.rawTuples
		if b, ok := keyImpliedRowsBound(outer, inner, best.keyRel); ok && b < est.rowsBound {
			est.rowsBound = b
		}
	}

	residual := make([]*restrictInfo, 0, len(clauses))
	for i, ri := range clauses {
		if !removed[i] {
			residual = append(residual, ri)
		}
	}
	est.sel = clampSelectivity(est.sel)
	est.residual = residual
	return est
}

// keyImpliedRowsBound is 04 §3.3's clamp: the structural upper bound a proven
// key puts on the joinrel's output.
//
// The argument is a counting one, not a statistical one. `keyRel` is the
// relation the key makes unique over the equated columns — the relation
// carrying the UNIQUE index, or, for a declared FK, the PARENT (a child row
// matches exactly one parent row, so the bound is "the referencing side's
// rows", 04 §3.3's own words). Every row of the OTHER side therefore matches at
// most one row of `keyRel`, and an inner join cannot emit more rows than the
// other side brings. The bound is that side's `Rows` — its POST-filter estimate,
// because the rows it will actually bring are the ones that survived its quals,
// and unlike the superkey DIVISOR (which must be the raw count, since it is
// converting to a match fraction) this is a count of probes, not a fraction.
//
// The bound only holds when the key side is that ONE base relation. If `keyRel`
// sits inside a multi-relation side, a join below may already have duplicated
// its rows — an outer row matching a single `keyRel` row can then match several
// rows of the side — and the counting argument gives nothing. Reporting no
// bound there is the difference between a clamp that is always sound and one
// that quietly truncates a correct estimate; the general case (scaling by the
// side's own fan-out) needs a per-relation duplication factor the search does
// not carry, and is ledgered rather than guessed at.
func keyImpliedRowsBound(outer, inner *RelOptInfo, keyRel int) (float64, bool) {
	if keyRel < 0 {
		return 0, false
	}
	// `RelSet(1)<<i` is `buildInitialRels`' relid convention (joinsearch.go:230).
	key := RelSet(1) << uint(keyRel)
	switch {
	case outer.Relids == key:
		return inner.Rows, true
	case inner.Relids == key:
		return outer.Rows, true
	default:
		return 0, false
	}
}

// joinKeyPair is one clause reduced to what the superkey test reads: the two
// base relations it equates and the column of each.
//
// `usable` is an explicit field rather than a sentinel in `rel`, because relid
// index 0 is a perfectly ordinary base relation — the FIRST one in the FROM
// list, and therefore the one a zero-value bug would silently attribute every
// unresolvable clause to.
type joinKeyPair struct {
	rel    [2]int
	col    [2]string
	usable bool
}

// joinKeyPairOf resolves a clause to its (relation, column) pair, or reports
// that it is not usable as key evidence.
//
// Both requirements are load-bearing. The clause must be an equijoin whose two
// operands each resolve to a bare column of ONE base relation — a key column
// equated to an expression (`a.pk = b.x + 1`) constrains nothing about how many
// `a` rows a `b` row matches, since the expression is not the stored value the
// index is unique over. And the two operands must land on OPPOSITE sides of
// THIS join: PG's FK arm makes the same test (`con_relid` in one side and
// `ref_relid` in the other, costsize.c:5675-5686) because a key equated within
// one side was already applied below and cannot restrict this join.
func (s *searchCtx) joinKeyPairOf(ri *restrictInfo, outer, inner RelSet) (joinKeyPair, bool) {
	var p joinKeyPair
	if ri == nil || !ri.isEquijoin {
		return p, false
	}
	lr, lcol, lok, rr, rcol, rok := s.joinKeyOperandCols(ri)
	if !lok || !rok {
		return p, false
	}
	leftOuter := relsSubset(ri.leftRelids, outer) && relsSubset(ri.rightRelids, inner)
	rightOuter := relsSubset(ri.rightRelids, outer) && relsSubset(ri.leftRelids, inner)
	if !leftOuter && !rightOuter {
		return p, false
	}
	p.rel = [2]int{lr, rr}
	p.col = [2]string{lcol, rcol}
	p.usable = true
	return p, true
}

// joinKeyOperandCols is `resolveJoinVarColumn` over an equijoin's two canonical
// operands, memoised on the clause (P6-08, take3 08 §9).
//
// The resolution is a pure function of the clause and the search's base
// relations — `joinKeyPairOf`'s own inputs `outer`/`inner` reach only the SIDE
// test below it, never this — so recomputing it once per (clause, joinrel pair)
// re-derives the same two (relation, column) answers O(2^n) times. Only the
// column NAME is kept: the `*ColumnRef` the resolver also returns is used by
// `examineJoinVar` for the operand's type, which this caller does not read.
func (s *searchCtx) joinKeyOperandCols(ri *restrictInfo) (int, string, bool, int, string, bool) {
	if ri.keyPairValid {
		return ri.keyPairLeftRel, ri.keyPairLeftCol, ri.keyPairLeftOK,
			ri.keyPairRightRel, ri.keyPairRightCol, ri.keyPairRightOK
	}
	lr, lcr, lok := s.resolveJoinVarColumn(ri.leftKey, ri.leftRelids)
	rr, rcr, rok := s.resolveJoinVarColumn(ri.rightKey, ri.rightRelids)
	var lcol, rcol string
	if lcr != nil {
		lcol = lcr.Name
	}
	if rcr != nil {
		rcol = rcr.Name
	}
	if s != nil {
		ri.keyPairLeftRel, ri.keyPairLeftCol, ri.keyPairLeftOK = lr, lcol, lok
		ri.keyPairRightRel, ri.keyPairRightCol, ri.keyPairRightOK = rr, rcol, rok
		ri.keyPairValid = true
	}
	return lr, lcol, lok, rr, rcol, rok
}

// provenKey is one applicable key: which clauses it covers, what to divide by,
// and which base relation the key makes unique.
//
// `keyRel` is NOT always the relation the constraint was declared on — for a
// declared FK it is the referenced PARENT, since that is the side the key makes
// unique — and it is the field P5.6-c's clamp reads, so the same asymmetry
// `rawTuples` encodes has to be encoded here too or the bound would be taken
// against the wrong side.
type provenKey struct {
	clauses   []int
	rawTuples float64
	keyRel    int
	// fromFK marks a key proven from a DECLARED foreign key rather than from
	// a unique index — the only evidence the SEMI/ANTI arm accepts (C-05).
	fromFK bool
}

// bestProvableKey finds the key with the largest divisor over the clauses that
// have not yet been consumed. Relations are scanned in FROM order and each
// relation's candidate keys in catalog order, so the answer does not move
// between runs.
//
// `admit` filters the candidates — C-05's SEMI/ANTI rule lives in the caller,
// where the jointype and the inner relset are known.
func (s *searchCtx) bestProvableKey(cat catalog.Catalog, pairs []joinKeyPair, removed []bool, admit func(provenKey) bool) (provenKey, bool) {
	// equated[r] = the columns of base relation r that a still-available
	// clause equates to something on the other side of this join.
	equated := make(map[int]map[string]bool)
	for i, p := range pairs {
		if removed[i] || !p.usable {
			continue
		}
		for k := 0; k < 2; k++ {
			if equated[p.rel[k]] == nil {
				equated[p.rel[k]] = make(map[string]bool)
			}
			equated[p.rel[k]][p.col[k]] = true
		}
	}

	var best provenKey
	found := false
	for r := 0; r < len(s.relInfos); r++ {
		cols := equated[r]
		if len(cols) == 0 {
			continue
		}
		for _, cand := range s.keysCovering(cat, r, cols, pairs, removed) {
			if admit != nil && !admit(cand) {
				continue
			}
			if !found || cand.rawTuples > best.rawTuples {
				best, found = cand, true
			}
		}
	}
	return best, found
}

// keysCovering enumerates every key of base relation `r` whose columns are
// covered by the equated set — the ⊆ ("superkey") test of cost-model/14 §2,
// which fires an (a,b)-unique index under an (a,b,c)-equated join and is what
// makes the mechanism useful on real composite PKs.
//
// The two evidence sources answer with DIFFERENT divisors, and getting that
// backwards is the trap this function exists to avoid:
//
//   - a UNIQUE index on `r` makes `r` itself the key side: each row of the
//     other side matches at most one row of `r`, so the divisor is `r`'s own
//     raw count;
//   - a FOREIGN KEY declared ON `r` makes `r` the CHILD (referencing) side.
//     Each `r` row matches exactly one row of the PARENT, so the divisor is
//     the PARENT's raw count (`1.0 / ref_tuples`, costsize.c:5847) — dividing
//     by the child's count instead would divide the fact table's cardinality
//     out of the join, which is how the legacy `uniqueNoFanoutRawCount`
//     (bushy.go:1192) reads it and is ledgered as a defect of that path.
func (s *searchCtx) keysCovering(cat catalog.Catalog, r int, cols map[string]bool, pairs []joinKeyPair, removed []bool) []provenKey {
	if r < 0 || r >= len(s.relInfos) {
		return nil
	}
	info := s.relInfos[r]
	if info.table == nil {
		return nil
	}
	var out []provenKey

	rawTuples := float64(info.baseRows)
	if rawTuples >= 1 {
		for _, idx := range cat.IndexesOnTable(info.table) {
			if idx == nil || !idx.Unique || !columnsSubset(idx.Columns, cols) {
				continue
			}
			covered := coveringClauses(pairs, removed, r, idx.Columns)
			if len(covered) == 0 {
				continue
			}
			out = append(out, provenKey{clauses: covered, rawTuples: rawTuples, keyRel: r})
		}
	}

	for _, fk := range info.table.ForeignKeys {
		if fk.NotValid || fk.NotEnforced || !columnsSubset(fk.Columns, cols) {
			continue
		}
		parent, parentRows, ok := s.fkParentRel(fk, pairs, removed, r)
		if !ok || parentRows < 1 {
			continue
		}
		// PG's chicken-out (costsize.c:5760): `coveringClauses` answers nil
		// unless every one of the key's columns has a clause behind it that we
		// can actually remove.
		covered := coveringClauses(pairs, removed, r, fk.Columns)
		if len(covered) == 0 {
			continue
		}
		out = append(out, provenKey{clauses: covered, rawTuples: parentRows, keyRel: parent, fromFK: true})
	}
	return out
}

// coveringClauses returns the indexes of the still-available clauses that
// equate relation `r`'s column set `key` to the other side. A key column with
// no clause behind it yields nothing, which is what makes the caller's
// full-cover check meaningful.
func coveringClauses(pairs []joinKeyPair, removed []bool, r int, key []string) []int {
	want := make(map[string]bool, len(key))
	for _, c := range key {
		want[c] = true
	}
	var out []int
	seen := make(map[string]bool, len(key))
	for i, p := range pairs {
		if removed[i] || !p.usable {
			continue
		}
		for k := 0; k < 2; k++ {
			if p.rel[k] != r || !want[p.col[k]] {
				continue
			}
			out = append(out, i)
			seen[p.col[k]] = true
			break
		}
	}
	if len(seen) != len(want) {
		return nil
	}
	return out
}

// fkParentRel resolves the referenced side of a declared FK to a base relation
// of this search, and returns that relation's index and its RAW tuple count.
//
// The parent must be a relation the still-available clauses actually equate
// this key against: a `ForeignKey` names its parent by table name, and a query
// may join the child to a DIFFERENT table of the same name in another schema,
// or may not join it to the parent at all. Matching through the clause pairs
// rather than through the name alone is what keeps the proof tied to the join
// in front of us.
func (s *searchCtx) fkParentRel(fk catalog.ForeignKey, pairs []joinKeyPair, removed []bool, child int) (int, float64, bool) {
	for i, p := range pairs {
		if removed[i] || !p.usable {
			continue
		}
		for k := 0; k < 2; k++ {
			if p.rel[k] != child {
				continue
			}
			other := p.rel[1-k]
			if other < 0 || other >= len(s.relInfos) {
				continue
			}
			tbl := s.relInfos[other].table
			if tbl == nil || tbl.Name != fk.RefTable {
				continue
			}
			return other, float64(s.relInfos[other].baseRows), true
		}
	}
	return -1, 0, false
}

// oneClausePerEquivClass is 04 §5's equivalence-class reduction over an
// already-applicable clause list: at most one member of each class survives,
// explicit beating inferred and ties going to list order.
//
// It is factored out of `selectivityClauses` (joinrestrict.go) rather than
// copied because the two callers reach it from opposite directions — the
// enumerator asks "which clauses carry selectivity for this split", the sizer
// is HANDED the joinrel's restriction list and has to reduce it — and a second
// copy of the winner rule is precisely the sibling-path shape that goes out of
// sync (hard-won rule #2).
func oneClausePerEquivClass(applicable []*restrictInfo) []*restrictInfo {
	if len(applicable) == 0 {
		return nil
	}
	// winner[ecID] = index into `applicable` of the member that carries the
	// class's selectivity.
	winner := make(map[int]int, len(applicable))
	for i, ri := range applicable {
		if ri == nil || ri.ecID == noEquivClass {
			continue
		}
		prev, seen := winner[ri.ecID]
		if !seen {
			winner[ri.ecID] = i
			continue
		}
		// Explicit beats inferred; otherwise the earlier clause keeps it.
		if applicable[prev].inferred && !ri.inferred {
			winner[ri.ecID] = i
		}
	}
	out := make([]*restrictInfo, 0, len(applicable))
	for i, ri := range applicable {
		if ri == nil {
			continue
		}
		if ri.ecID != noEquivClass && winner[ri.ecID] != i {
			continue
		}
		out = append(out, ri)
	}
	return out
}
