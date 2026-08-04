package planner

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
// PG's shape, reproduced here in full:
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
// Still inert: `GOOPG_PGSHAPED_DP` is OFF (joinsearch.go:44) and nothing calls
// `joinSearch` from `planSelect`, so this builder is reachable only from tests
// until P5.9. Validated by `joinrelsize_test.go`.

import (
	"github.com/goopg/goopg/internal/catalog"
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

func (b *searchJoinRelBuilder) sizeJoinRel(outer, inner *RelOptInfo, clauses []*restrictInfo) (float64, int) {
	return b.s.calcJoinrelSize(b.cat, outer, inner, clauses)
}

func (b *searchJoinRelBuilder) addPaths(joinrel, outer, inner *RelOptInfo, clauses []*restrictInfo) error {
	return addPathsToJoinrel(joinrel, outer, inner, clauses, b.s.cp)
}

// calcJoinrelSize is `calc_joinrel_size_estimate` (costsize.c:5499) for the
// only jointype the search can see (INNER — 03 §4.4 pins every outer/semi/anti
// construct outside it as an opaque initial rel).
//
// It is called from `makeJoinRel` on the CREATE path only, which is 04 §2's
// rows-once discipline: every path for a relset shares one output-row figure,
// because `add_path` compares paths within one rel and two pairs that disagreed
// about `rows` would make those comparisons meaningless. PG enforces the same
// thing by computing the size inside `build_join_rel` (relnode.c).
//
// The width is the sum of the input widths. PG's `build_joinrel_tlist` instead
// sums only the columns needed above the join, so a wide input whose columns
// are all consumed by the join itself is narrower upstream than here; goopg's
// search has no such projection information — 03 §10's boundary map is built
// over the full concatenation — so the sum is the honest answer at this
// fidelity level. Ledgered.
func (s *searchCtx) calcJoinrelSize(cat catalog.Catalog, outer, inner *RelOptInfo, clauses []*restrictInfo) (float64, int) {
	if outer == nil || inner == nil {
		return 1, 0
	}
	width := outer.Width + inner.Width

	// 04 §5's equivalence-class rule first: an EC with n members contributes
	// ONE clause per (outer, inner) split, so charging every member's
	// selectivity would multiply one restriction in several times. This is the
	// same reduction `selectivityClauses` applies — literally the same
	// function — but it has to be applied HERE too, because `makeJoinRel`
	// hands the sizer the joinrel's full restriction list (what the path
	// generators must evaluate), not the selectivity subset.
	sel, residual := s.superkeyJoinSelectivity(cat, outer, inner, oneClausePerEquivClass(clauses))
	for _, ri := range residual {
		sel *= s.joinClauseSelectivity(ri)
	}
	return clampRowEst(outer.Rows * inner.Rows * sel), width
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
func (s *searchCtx) superkeyJoinSelectivity(cat catalog.Catalog, outer, inner *RelOptInfo, clauses []*restrictInfo) (float64, []*restrictInfo) {
	sel := 1.0
	if cat == nil || len(clauses) == 0 {
		return sel, clauses
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
		return sel, clauses
	}

	// Greedy, largest-divisor-first, until no further key can be proven over
	// the clauses that are still available.
	for {
		best, ok := s.bestProvableKey(cat, pairs, removed)
		if !ok {
			break
		}
		for _, i := range best.clauses {
			removed[i] = true
		}
		sel *= 1.0 / best.rawTuples
	}

	residual := make([]*restrictInfo, 0, len(clauses))
	for i, ri := range clauses {
		if !removed[i] {
			residual = append(residual, ri)
		}
	}
	return clampSelectivity(sel), residual
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
	lr, lcr, lok := s.resolveJoinVarColumn(ri.leftKey, ri.leftRelids)
	rr, rcr, rok := s.resolveJoinVarColumn(ri.rightKey, ri.rightRelids)
	if !lok || !rok {
		return p, false
	}
	leftOuter := relsSubset(ri.leftRelids, outer) && relsSubset(ri.rightRelids, inner)
	rightOuter := relsSubset(ri.rightRelids, outer) && relsSubset(ri.leftRelids, inner)
	if !leftOuter && !rightOuter {
		return p, false
	}
	p.rel = [2]int{lr, rr}
	p.col = [2]string{lcr.Name, rcr.Name}
	p.usable = true
	return p, true
}

// provenKey is one applicable key: which clauses it covers and what to divide
// by.
type provenKey struct {
	clauses   []int
	rawTuples float64
}

// bestProvableKey finds the key with the largest divisor over the clauses that
// have not yet been consumed. Relations are scanned in FROM order and each
// relation's candidate keys in catalog order, so the answer does not move
// between runs.
func (s *searchCtx) bestProvableKey(cat catalog.Catalog, pairs []joinKeyPair, removed []bool) (provenKey, bool) {
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
			out = append(out, provenKey{clauses: covered, rawTuples: rawTuples})
		}
	}

	for _, fk := range info.table.ForeignKeys {
		if fk.NotValid || fk.NotEnforced || !columnsSubset(fk.Columns, cols) {
			continue
		}
		parentRows, ok := s.fkParentRel(fk, pairs, removed, r)
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
		out = append(out, provenKey{clauses: covered, rawTuples: parentRows})
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
// of this search, and returns its RAW tuple count.
//
// The parent must be a relation the still-available clauses actually equate
// this key against: a `ForeignKey` names its parent by table name, and a query
// may join the child to a DIFFERENT table of the same name in another schema,
// or may not join it to the parent at all. Matching through the clause pairs
// rather than through the name alone is what keeps the proof tied to the join
// in front of us.
func (s *searchCtx) fkParentRel(fk catalog.ForeignKey, pairs []joinKeyPair, removed []bool, child int) (float64, bool) {
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
			return float64(s.relInfos[other].baseRows), true
		}
	}
	return 0, false
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
