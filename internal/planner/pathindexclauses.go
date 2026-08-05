package planner

// M0127-P5.5-b — `IndexPath.indexclauses`: the quals pushed INTO an index probe,
// carried on the path in INDEX-COLUMN order.
//
// PG oracle: `IndexClause` and the `indexclauses` field of `IndexPath`
// (postgres/src/include/nodes/pathnodes.h:1815-1818, :1846, :1865-1875),
// `build_index_paths`' accumulation loop (indxpath.c:1029-1076),
// `create_indexscan_plan` (createplan.c:3006) with `fix_indexqual_references`
// (createplan.c:5121) and `is_redundant_with_indexclauses` (createplan.c:3075).
// Design: leftdeep-joins 03 §10, 04 §1.1.
//
// This completes the index carrier P5.5-a opened. That slice landed the two
// facts a chosen index path must name — WHICH index and in WHICH direction —
// and ledgered the third as the larger question: a parameterised index path is
// built from `bound []paramIndexClause` (the clauses
// `pickIndexCoveringAllLeadingColumns` accepted) and then DISCARDS them once
// the cost and rows are computed. PG keeps them, and `createPlan` needs them
// for two separate jobs:
//
//   - `fix_indexqual_references` reads the list to build the scan's keys, in
//     order, one per index column. goopg's analogue is `IndexScan.Keys[i]`.
//   - `is_redundant_with_indexclauses` reads it to REMOVE those same clauses
//     from the node's filter quals. A clause pushed into the probe that is also
//     evaluated above it is not wrong, but a clause pushed into the probe whose
//     `restrictInfo` identity was lost cannot be removed at all — so the
//     carrier must hold the `*restrictInfo`, not just the probe value.
//
// **The order fidelity point is the whole reason this is not a two-line copy.**
// PG's `indexclauses` list is in index-column order: `build_index_paths`' outer
// loop runs over `indexcol`, inner loop over that column's matched clauses, so
// "the resulting list of clauses is ordered by index column number ... (This
// order is depended on by btree and possibly other places)" (indxpath.c:1042).
// goopg's `bound` is in the search's CANDIDATE order — whichever order the
// query's clause list happened to present them — and goopg's executor binds
// `Keys[i]` to `Index.Columns[i]` POSITIONALLY (plan.go:665). Carrying `bound`
// verbatim would therefore bind the wrong value to the wrong column on any
// composite index whose clauses arrived out of order: a wrong answer, not a
// slow plan.
//
// The order is held STRUCTURALLY rather than by a sort, and the difference
// matters. `indexPathClauses` below loops over `idx.Columns` and looks the
// clause up per column, which is PG's own loop shape; a sort would be a second
// statement of the same fact, one that a later edit could contradict (rule #2).
//
// Two narrowings against PG, both ledgered because each is a shape goopg's
// executor cannot represent rather than one the planner chose to skip:
//
//   - **One clause per column, never several.** PG's list is *nondecreasing* in
//     `indexcol`, not strictly increasing: `x > 1 AND x < 5` contributes two
//     `IndexClause`s at the same column, and a btree scan applies both as
//     boundary conditions. goopg's `paramIndexClause` collection is first-wins
//     per column (`addOneParameterizedIndexPath`) and `IndexScan.Keys` is an
//     equality probe with one expression per column, so the extra clause has
//     nowhere to go and stays a residual qual.
//   - **No gaps.** PG happily builds a probe binding columns 0 and 2 of a
//     three-column index and leaving column 1 unbound; goopg's
//     `IndexScan.Keys` requires `len(Keys) == len(Index.Columns)` (a shorter
//     prefix is rejected to keep the executor probe purely equality-shaped),
//     so a gapped list is unrepresentable. `indexPathClauses` DECLINES — it
//     returns nil rather than a shortened list — because a shortened list
//     silently re-indexes every position after the gap, which is exactly the
//     mis-binding the whole file exists to prevent.
//
// The second narrowing is defensive as things stand: the only producer runs
// after `pickIndexCoveringAllLeadingColumns` has already established that every
// column of `idx` is bound. It is written anyway because the decline is the
// safe answer and the alternative is silent corruption if a later producer
// (PG's `amoptionalkey`/prefix-probe arm) forgets the precondition.
//
// The UNPARAMETERISED ordered path (pathindexordered.go) carries an EMPTY list,
// and that is not an omission: "An empty list implies a full index scan"
// (pathnodes.h:1817) is precisely what that path is — a scan of the whole
// relation through the index for its ordering alone, with no qual pushed in.
//
// Still inert. `GOOPG_PGSHAPED_DP` is OFF and `createPlan` handles only
// `PathPrebuilt`, so nothing reads the list yet; `pathindexclauses_test.go` is
// where its invariants are falsifiable.

import "github.com/goopg/goopg/internal/catalog"

// indexPathClause is one entry of PG's `indexclauses` list (`IndexClause`,
// pathnodes.h:1865): the restriction that became an index qual, the index column
// it was matched to, and the value the probe binds there.
//
// PG's `indexquals` (the derived, commuted, executor-shaped quals) has no
// analogue here and needs none: goopg's index probe is always
// `indexcol = <outer expression>` with the column on the left by construction —
// `indexableJoinClausesFor` accepts a clause only when one whole operand IS a
// bare `*ColumnRef` of the indexed rel — so the derivation PG performs
// (`match_clause_to_indexcol`'s commutation, lossy operators, RowCompare
// expansion) collapses to picking the other operand. `lossy` is likewise absent:
// a lossy index qual is one the index only approximates, which no B-tree
// equality is.
type indexPathClause struct {
	// ri is `IndexClause.rinfo`: the ORIGINAL clause, kept by identity so
	// `createPlan` can tell that a clause it is about to place as a filter is
	// already being applied by the probe (`is_redundant_with_indexclauses`).
	// PG's version of that test also matches a clause derived from the same
	// equivalence class rather than the same node; goopg's `restrictInfo.ecID`
	// is what the P5.5 arm will use for that half.
	ri *restrictInfo
	// indexCol is `IndexClause.indexcol` (pathnodes.h:1873): ZERO-based, PG's
	// convention here and not the 1-based `AttrNumber` used elsewhere in PG.
	// It is recorded rather than left implicit in the slice position because
	// PG's list may skip and repeat columns; goopg's cannot today (see the
	// header), and an explicit column number is what makes that agreement
	// checkable instead of assumed.
	indexCol int
	// key is the value the probe binds at `indexCol` — the clause's OUTER
	// operand, supplied by the parameterising relations. This is what becomes
	// `IndexScan.Keys[indexCol]`.
	key Expr
}

// indexPathClauses is `build_index_paths`' `index_clauses` accumulation
// (indxpath.c:1029-1076) for goopg's one-equality-per-column probe: the clauses
// of `bound` re-presented in index-column order, or nil when the index is not
// fully bound.
//
// The loop is over the INDEX's columns, not over the clauses, and that is the
// entire mechanism by which the result is ordered — same as PG.
//
// `bound` is scanned by column NAME rather than consulted through a map because
// `addOneParameterizedIndexPath` builds `bound` and its `innerToOuter` map in
// one pass under one first-wins test: the clause this scan finds for column X is
// necessarily the clause whose `outerKey` is `innerToOuter[X]`, which is the
// value `pickIndexCoveringAllLeadingColumns` accepted the index on. The two
// derivations of "the probe value at column X" therefore agree by construction
// rather than by coincidence (rule #2: sibling paths change together).
func indexPathClauses(idx *catalog.Index, bound []paramIndexClause) []indexPathClause {
	if idx == nil || len(idx.Columns) == 0 || len(bound) == 0 {
		return nil
	}
	out := make([]indexPathClause, 0, len(idx.Columns))
	for pos, col := range idx.Columns {
		c, ok := boundClauseForColumn(bound, col)
		if !ok {
			// An unbound column. Declining is the safe answer — see the
			// header's "No gaps".
			return nil
		}
		out = append(out, indexPathClause{ri: c.ri, indexCol: pos, key: c.outerKey})
	}
	return out
}

// boundClauseForColumn finds the bound clause that probes `col`. `bound` holds
// at most one clause per column (first-wins), so the first match is the only
// match and a linear scan over a list of at most `len(idx.Columns)` entries is
// the whole lookup.
func boundClauseForColumn(bound []paramIndexClause, col string) (paramIndexClause, bool) {
	for _, c := range bound {
		if c.innerCol == col {
			return c, true
		}
	}
	return paramIndexClause{}, false
}
