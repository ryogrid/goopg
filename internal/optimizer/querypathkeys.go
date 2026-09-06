package optimizer

// C-07 / P3-06 — `standard_qp_callback` (planner.c:3453): the ordering the
// QUERY wants, derived once, before the search builds its first rel.
//
// PG oracle: `standard_qp_callback` (plan/planner.c:3453) for the derivation
// and its precedence; `has_useful_pathkeys` (path/pathkeys.c:2319) for the
// generation gate it feeds. Design: take3 01 §4.4, take3 08 §6.3, take3 04 §9.
//
// # What this file lands, and what it deliberately does not
//
// C-07 has two halves. The first is this file: `query_pathkeys` on the planner
// context, in upstream's precedence order
//
//	group ?: window ?: longer(distinct, sort) ?: setop ?: NIL
//
// plus `hasUsefulPathkeys`, complete, as `addOrderedIndexPaths`' gate.
//
// The second half — "so ORDER BY / GROUP BY MOTIVATE index paths", i.e.
// widening `addOrderedIndexPaths`' useful-column set from the merge-clause
// columns to the query-pathkey columns — is NOT landed, because goopg has no
// consumer that could collect the ordering:
//
//   - the search commits to ONE path at its root, `finalPath`
//     (joinsearch.go:298) = `get_cheapest_fractional_path`, chosen on cost
//     alone; there is no `create_ordered_paths` asking the final rel for a
//     path that already delivers `query_pathkeys`;
//   - the search boundary publishes a NODE, not a path
//     (`planJoinlistSearch`, relfromjoinlist.go:218), so the chosen path's
//     `Pathkeys` are dropped at the seam and nothing above can read them;
//   - the ORDER BY `*Sort` is wrapped on unconditionally, far above that seam
//     and in a different coordinate space (planner.go:1720 and the SRF twin at
//     :1766, whose keys are resolved post-aggregate / post-window), so even a
//     path that arrived correctly sorted would still be re-sorted.
//
// An ordering-only full index scan that nothing selects FOR its ordering is a
// path that can only lose on total cost — or, worse, win `CheapestStartup`
// under a LIMIT and be picked for a fraction while the redundant Sort above it
// still runs. It would also silently disable the GROUP_AGG producer's
// index-ordered input variant (groupingpaths.go indexOrderedAggInput),
// whose GROUP-BY-ordered index promotion matches
// on the child being a `*SeqScan`. The consumers are filed: C-11 (P4-02 upper
// `RelOptInfo`s, incl. `ORDERED`), C-12 (P4-03 a real upper-rel `PathSort`),
// C-15/C-16 (`create_grouping_paths` / `create_distinct_paths`). The widening
// belongs with them, and is a map union at one line: fold the query-pathkey
// columns into the `colExprs` map `addOrderedIndexPaths` hands
// `buildIndexPathkeys` (pathindexordered.go), which is
// `pathkeys_useful_for_ordering` in the same inside-out form
// `mergeableColumnExprsFor` already gives `pathkeys_useful_for_merging`.
//
// So: derivation live and tested, gate complete, generation unchanged.

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// queryPathkeySets is `PlannerInfo`'s five pathkey lists that
// `standard_qp_callback` fills before it picks one
// (`group_pathkeys`, `window_pathkeys`, `distinct_pathkeys`, `sort_pathkeys`,
// `setop_pathkeys`). They are kept as a struct rather than five returns so the
// precedence rule below can be tested independently of the derivation, which
// is where every goopg-specific decline lives.
type queryPathkeySets struct {
	group    []PathKey
	window   []PathKey
	distinct []PathKey
	sort     []PathKey
	setop    []PathKey
}

// chooseQueryPathkeys is the tail of `standard_qp_callback` (planner.c:3600):
// which of the five lists the query actually asks the scan/join level to
// deliver.
//
// The order is upstream's and each step is load-bearing:
//
//   - GROUP BY first, because a sorted grouping input is the largest single
//     win and upstream's own comment refuses to prefer a superset ORDER BY
//     over it ("that might just leave us failing to exploit an available sort
//     order at all");
//   - then the FIRST (bottom) window, since that is the one whose sort the
//     scan level could remove;
//   - then DISTINCT, but only when it is STRICTLY more rigorous than ORDER BY.
//     The comparison is by length and it is sound rather than approximate: the
//     parser guarantees one of the two lists is a prefix of the other
//     (`transformDistinctClause` rejects anything else), so the longer list is
//     the stronger requirement;
//   - then ORDER BY;
//   - then the set-operation child's grouping order.
func chooseQueryPathkeys(sets queryPathkeySets) []PathKey {
	switch {
	case len(sets.group) > 0:
		return sets.group
	case len(sets.window) > 0:
		return sets.window
	case len(sets.distinct) > len(sets.sort):
		return sets.distinct
	case len(sets.sort) > 0:
		return sets.sort
	default:
		return sets.setop
	}
}

// deriveQueryPathkeys is `standard_qp_callback` as a whole: build the five
// lists from the statement and return the chosen one, in the SEARCH's binding
// coordinates.
//
// `ctx` is the FROM-level resolve context — the same one the WHERE predicate
// the search consumes is resolved against (planner.go:1305) — so a pathkey
// built here names a column with the very `Index`/`SourceTableIdx` a clause
// operand carries, which is what goopg's syntactic pathkeys require
// (pathkeys.go's file header; the P5.4c-ii-a finding).
//
// Called from `planSelect` at the two points that already fix the other
// query-level facts the search reads (`searchTupleFraction`,
// `neededColumnNames`), for the same reason: the ORDER BY / GROUP BY clauses
// are not resolved into `SortKey`s until hundreds of lines below the search.
func deriveQueryPathkeys(s *parser.SelectStmt, ctx *resolveContext) []PathKey {
	return chooseQueryPathkeys(deriveQueryPathkeySets(s, ctx))
}

// deriveQueryPathkeySets fills the five lists. Every goopg-specific decline is
// here, and each one is an EMPTY list rather than a partial one wherever a
// partial answer would claim an ordering the query does not want.
func deriveQueryPathkeySets(s *parser.SelectStmt, ctx *resolveContext) queryPathkeySets {
	if s == nil || ctx == nil {
		return queryPathkeySets{}
	}
	sets := queryPathkeySets{}
	sets.sort = presearchPathkeys(sortClauseItems(s), s, ctx)
	sets.group = presearchPathkeys(groupClauseItems(s), s, ctx)
	sets.window = presearchPathkeys(windowClauseItems(s), s, ctx)
	sets.distinct = presearchPathkeys(distinctClauseItems(s), s, ctx)
	// `setop_pathkeys` is upstream's `qp_extra->setop`: the grouping order a
	// subquery being planned AS A SET-OPERATION CHILD could usefully deliver
	// to its parent's merge/unique step. goopg plans a set-op operand through
	// `planSelectWithSettings(s.SetOpOperand, …)` (planner.go:1114), which
	// hands the operand no notion of the parent it feeds, so there is nothing
	// here to derive it from — the fact lives one frame up and is not passed
	// down. Left empty rather than approximated from the operand's own target
	// list, which would claim an ordering the parent may not want at all.
	// `chooseQueryPathkeys` still implements the arm, so the precedence is
	// complete the day a producer exists.
	return sets
}

// presearchSortItem is one clause item on its way to a pathkey: the written
// expression plus the direction the clause asks for. It is goopg's stand-in
// for a `SortGroupClause`, minus the operator OIDs goopg has no catalogue of.
type presearchSortItem struct {
	expr       parser.Expr
	desc       bool
	nullsFirst bool
}

// defaultSortItem is a clause item with no direction of its own — a GROUP BY
// or PARTITION BY entry, or a DISTINCT target that no ORDER BY names.
//
// Upstream gives such an item the default btree ordering operator (`<`) and
// `nulls_first = false` (`make_group_clause` / `addTargetToGroupList`,
// parse_clause.c), which is ASC NULLS LAST — the same default an index column
// carries when `pg_index.indoption` says nothing (`buildIndexPathkeys`,
// pathkeysindex.go), so the two sides agree by construction rather than by
// coincidence.
func defaultSortItem(e parser.Expr) presearchSortItem {
	return presearchSortItem{expr: e}
}

// sortClauseItems is `parse->sortClause`.
func sortClauseItems(s *parser.SelectStmt) []presearchSortItem {
	items := make([]presearchSortItem, 0, len(s.OrderBy))
	for _, sb := range s.OrderBy {
		if sb.UsingOp != "" {
			// `ORDER BY x USING op` names an ordering operator goopg cannot
			// map to an index's ASC/DESC. STOP rather than guess: a pathkey
			// list is a PREFIX contract, so an unexpressible key truncates it
			// (the same rule `buildIndexPathkeys` applies to an unnameable
			// index column).
			break
		}
		items = append(items, presearchSortItem{
			expr:       sb.Expr,
			desc:       sb.Desc,
			nullsFirst: sortByNullsFirst(sb),
		})
	}
	return items
}

// groupClauseItems is `root->processed_groupClause` reduced to what goopg can
// see: `transformGroupClause` (parse_clause.c) walks the sortClause first,
// emitting every leading sort item that is ALSO a grouping item — so the group
// list inherits both the ORDER BY's order and its directions for that prefix —
// and then appends the remaining grouping items with default ordering.
//
// The prefix reuse matters and is not cosmetic: `GROUP BY a ORDER BY a DESC`
// gives group pathkeys DESC, and an ASC-only derivation would claim the query
// wants an ascending grouping input it will immediately re-sort.
//
// Grouping SETS are excluded entirely. Upstream uses the FIRST rollup's
// groupClause there and explicitly declines to optimise across sets
// (planner.c:3462-3468); goopg carries grouping sets as a flat
// `Aggregate.GroupingSets` with no rollup list at all (C-10a's open scope
// question), so there is no "first rollup" to read and claiming the union's
// ordering would over-state what any one set delivers.
func groupClauseItems(s *parser.SelectStmt) []presearchSortItem {
	if len(s.GroupBy) == 0 || s.GroupingSets != nil {
		return nil
	}
	pending := make([]parser.Expr, len(s.GroupBy))
	copy(pending, s.GroupBy)
	items := make([]presearchSortItem, 0, len(pending))
	for _, sb := range sortClauseItems(s) {
		hit := -1
		for i, g := range pending {
			if g != nil && parserSortExprEqual(g, sb.expr, s) {
				hit = i
				break
			}
		}
		if hit < 0 {
			// The first ORDER BY item that is not a grouping item ends the
			// shared prefix (`transformGroupClause`'s own break).
			break
		}
		pending[hit] = nil
		items = append(items, sb)
	}
	for _, g := range pending {
		if g != nil {
			items = append(items, defaultSortItem(g))
		}
	}
	return items
}

// distinctClauseItems is `parse->distinctClause`.
//
// `DISTINCT ON (…)` is the written list, in order (`transformDistinctOnClause`
// requires the ORDER BY prefix to match it, and goopg enforces that at
// planner.go:2016-2038). Plain `DISTINCT` is `transformDistinctClause`: every
// ORDER BY item first, in ORDER BY order and with ORDER BY's directions, then
// the remaining target-list entries with default ordering — which is why
// `SELECT DISTINCT a, b … ORDER BY b` distinguishes on (b, a).
func distinctClauseItems(s *parser.SelectStmt) []presearchSortItem {
	if len(s.DistinctOn) > 0 {
		sorts := sortClauseItems(s)
		items := make([]presearchSortItem, 0, len(s.DistinctOn))
		for i, e := range s.DistinctOn {
			it := defaultSortItem(e)
			// The validated ORDER BY prefix supplies the direction, exactly as
			// `transformDistinctOnClause` reuses the matching sortClause entry.
			if i < len(sorts) {
				it.desc, it.nullsFirst = sorts[i].desc, sorts[i].nullsFirst
			}
			items = append(items, it)
		}
		return items
	}
	if !s.Distinct {
		return nil
	}
	items := sortClauseItems(s)
	for _, t := range s.Targets {
		if _, isStar := t.Expr.(*parser.StarExpr); isStar {
			// A star target stands for a column list this seam has not
			// expanded. Stop: the pathkey list is a prefix contract.
			break
		}
		dup := false
		for _, it := range items {
			if parserSortExprEqual(it.expr, t.Expr, s) {
				dup = true
				break
			}
		}
		if !dup {
			items = append(items, defaultSortItem(t.Expr))
		}
	}
	return items
}

// windowClauseItems is `make_pathkeys_for_window` for the FIRST (bottom)
// window: its PARTITION BY keys, which the executor needs grouped and which
// therefore carry the default ordering, followed by its ORDER BY keys.
//
// "First" is upstream's `linitial(activeWindows)`. goopg does not reorder
// window specifications — `buildWindowStage` (planner.go:6666-6680) groups the
// calls by specification key in TARGET-LIST order and chains the groups in
// that order — so the bottom window is the specification of the first window
// call the target list mentions, and `collectWindowCalls` returns exactly that
// list in exactly that order.
func windowClauseItems(s *parser.SelectStmt) []presearchSortItem {
	calls, err := collectWindowCalls(s)
	if err != nil || len(calls) == 0 || calls[0].Over == nil {
		return nil
	}
	w := calls[0].Over
	items := make([]presearchSortItem, 0, len(w.PartitionBy)+len(w.OrderBy))
	for _, p := range w.PartitionBy {
		items = append(items, defaultSortItem(p))
	}
	for _, ob := range w.OrderBy {
		if ob.UsingOp != "" {
			break
		}
		items = append(items, presearchSortItem{expr: ob.Expr, desc: ob.Desc, nullsFirst: sortByNullsFirst(ob)})
	}
	return items
}

// presearchPathkeys is `make_pathkeys_for_sortclauses` (pathkeys.c:1381) at a
// seam that has no EquivalenceClasses: turn clause items into PathKeys against
// the FROM-level context, dropping redundant ones and STOPPING at the first
// item that cannot be expressed as a column of the searched relations.
//
// STOP, not skip, for the reason `buildIndexPathkeys` stops
// (pathkeysindex.go): pathkeys are a prefix contract. If a query wants
// `(a, f(b), c)` and `f(b)` cannot be named here, the query still wants `a`
// first — but it does NOT want `(a, c)`, because rows are ordered by `c` only
// within equal `f(b)`. Claiming `(a, c)` would let a consumer skip a sort the
// query genuinely needs.
//
// Only a plain column reference survives. goopg's pathkeys are syntactic, so
// the only thing a pathkey can usefully match is an index key column, and
// `buildIndexPathkeys` compares `*ColumnRef`s by `exprEqual`; an expression
// key would build a pathkey nothing can ever satisfy. Restricting the shape
// BEFORE resolution is also what keeps this derivation free of side effects:
// `resolveColumnRef` is a lexical-scope lookup, while the general
// `resolveExpr` plans subqueries.
func presearchPathkeys(items []presearchSortItem, s *parser.SelectStmt, ctx *resolveContext) []PathKey {
	var keys []PathKey
	for _, it := range items {
		col, ok := resolvePresearchSortExpr(it.expr, s, ctx)
		if !ok {
			break
		}
		pk := PathKey{Expr: col, SortAsc: !it.desc, NullsFirst: it.nullsFirst}
		// `make_pathkeys_for_sortclauses` appends only a non-redundant key.
		// Only case 2 of `pathkey_is_redundant` can fire on a column
		// reference; case 1 (a constant EquivalenceClass) cannot, which is
		// why the shape filter above loses nothing.
		if pathkeyRedundantIn(pk, keys) {
			continue
		}
		keys = append(keys, pk)
	}
	return keys
}

// resolvePresearchSortExpr resolves one clause item to a column of the
// searched relations, or declines.
//
// Alias and positional references are substituted first
// (`resolveOrderBySubstitution`), which is what lets `ORDER BY 1` and
// `GROUP BY item_id` reach the underlying column; a substitution that lands on
// an aggregate, a window call or any other expression then fails the shape
// test and truncates the list, which is correct — the search cannot deliver an
// ordering on a value computed above it.
//
// An `OuterColumnRef` is declined too: it names a column of an ENCLOSING
// query's scope, which no path of this search orders by.
func resolvePresearchSortExpr(e parser.Expr, s *parser.SelectStmt, ctx *resolveContext) (Expr, bool) {
	if e == nil {
		return nil, false
	}
	e = resolveOrderBySubstitution(e, s.Targets)
	cr, ok := e.(*parser.ColumnRef)
	if !ok {
		return nil, false
	}
	resolved, err := resolveColumnRef(cr, ctx)
	if err != nil || resolved == nil {
		return nil, false
	}
	col, ok := resolved.(*ColumnRef)
	if !ok || col.Name == "" {
		return nil, false
	}
	return col, true
}

// parserSortExprEqual reports whether two written clause expressions name the
// same thing, for the prefix-matching `transformGroupClause` and
// `transformDistinctClause` do. Both sides are substituted first so
// `GROUP BY x` matches `ORDER BY 1` when target 1 is `x`.
//
// Comparison is on the written form, not the resolved one, because these
// prefix rules are a PARSE-time notion: upstream matches `SortGroupClause`
// entries by `tleSortGroupRef` into one shared target list, and both of
// goopg's inputs here are that same target list's expressions.
func parserSortExprEqual(a, b parser.Expr, s *parser.SelectStmt) bool {
	if a == nil || b == nil {
		return false
	}
	a = resolveOrderBySubstitution(a, s.Targets)
	b = resolveOrderBySubstitution(b, s.Targets)
	if a == b {
		return true
	}
	ac, aok := a.(*parser.ColumnRef)
	bc, bok := b.(*parser.ColumnRef)
	if !aok || !bok {
		return false
	}
	return strings.EqualFold(ac.Column, bc.Column) &&
		strings.EqualFold(ac.Table, bc.Table) &&
		strings.EqualFold(ac.Schema, bc.Schema)
}

// hasUsefulPathkeys is `has_useful_pathkeys` (pathkeys.c:2319), complete: is
// there any reason at all to build an ordered path for this relation?
//
// Upstream's three arms reduce to two here, and the reduction is a PROOF, not
// a simplification. Its arms are, in order, `rel->joininfo != NIL ||
// rel->has_eclass_joins`, `root->group_pathkeys != NIL`, and
// `root->query_pathkeys != NIL` — but `standard_qp_callback` assigns
// `query_pathkeys = group_pathkeys` whenever the group list is non-empty
// (`chooseQueryPathkeys`' first case), so a non-empty `group_pathkeys` implies
// a non-empty `query_pathkeys` and the middle arm can never be the one that
// answers. Writing it out would suggest goopg had a group list the query list
// does not cover.
//
// The merging arm is per-relation, as upstream's is: `joininfo` is the rel's
// OWN join-clause list, so a relation no join clause mentions gets no ordered
// path on its account even when the query is full of them. goopg keeps the
// clause list flat on the search context (joinrestrict.go:93) rather than
// per-rel, so the membership test is written out here.
//
// It is the GATE, not the answer: a rel that passes still produces nothing
// unless some index's leading columns are usable, which is
// `truncate_useless_pathkeys`' job and is where goopg stops at the merging
// half (see this file's header).
func (s *searchCtx) hasUsefulPathkeys(rel *RelOptInfo) bool {
	if rel == nil {
		return false
	}
	if s.relHasJoinClause(rel.Relids) {
		return true // might be able to use pathkeys for merging
	}
	return len(s.queryPathkeys) > 0 // might be able to use them for ordering
}

// clausesAll is the search's flat join-clause list, nil-safe. The list is
// legitimately absent — a one-relation problem has no join clause at all
// (relfromjoinlist.go:209) — and every reader has to say so; this is the one
// place that says it.
func (s *searchCtx) clausesAll() []*restrictInfo {
	if s == nil || s.clauses == nil {
		return nil
	}
	return s.clauses.all
}

// relHasJoinClause is `rel->joininfo != NIL || rel->has_eclass_joins` read off
// the flat clause list: does any join clause of this search have `relids` as
// one of its two sides?
//
// Both spellings of "mentions the rel" are checked, and they differ. A
// two-sided equijoin records `leftRelids`/`rightRelids`, which is what an
// ordering could serve; a non-equality join clause (`a.x < b.y`) records
// neither and is visible only through `relids`. Upstream's `joininfo` holds
// both kinds, so both count here — the gate asks whether a join clause exists,
// not whether it is mergeable.
func (s *searchCtx) relHasJoinClause(relids RelSet) bool {
	if relids == 0 {
		return false
	}
	for _, ri := range s.clausesAll() {
		if ri == nil {
			continue
		}
		if relsOverlap(ri.relids, relids) {
			return true
		}
	}
	return false
}
