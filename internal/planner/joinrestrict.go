package planner

// M0127-P5.2 — the join search's clause bookkeeping: a `restrictInfo` list, the
// connectivity predicates the enumerator gates on, and the equivalence-class
// rule that stops an inferred equality from being counted twice.
//
// PG oracle: `RestrictInfo` (pathnodes.h) carries `clause`, `required_relids`,
// and — for a mergeable/hashable equality — `left_relids`/`right_relids` over
// the two operands. `have_relevant_joinclause` (joininfo.c:39) is the
// enumerator's connectivity gate; `build_joinrel_restrictlist` (relnode.c) is
// what a joinrel's own quals are drawn from; `generate_join_implied_equalities`
// (equivclass.c) is where an equivalence class contributes exactly ONE clause
// per (outer, inner) split. Design: leftdeep-joins 03 §3, 04 §5.
//
// This file generalises `joinEdge` (bushy.go:40). A joinEdge is a PAIR of FROM
// positions and is therefore blind to any qual spanning three relations; a
// restrictInfo carries a `RelSet`, so a three-rel qual is one clause with three
// bits rather than something the graph cannot express. The list also keeps
// non-equality join quals, which the edge list drops entirely — P5.4's qual
// placement (03 §5.4) has to put them somewhere, and "somewhere" has to be a
// clause the search knows about.
//
// P5.3's enumerator is the first consumer, and since M0127-P5.9 (2026-08-06)
// `GOOPG_PGSHAPED_DP` is ON by default — so `planSelect` DOES reach this via the
// search; only `=0` opts out. Validated in isolation by `joinrestrict_test.go`.

import (
	"fmt"
	"sort"

	"github.com/goopg/goopg/internal/parser"
)

// noEquivClass is the ecID of a clause that belongs to no equivalence class:
// every such clause contributes its own selectivity, since there is no other
// member that could already have applied it.
const noEquivClass = -1

// restrictInfo is one qual the join search reasons about — PG's RestrictInfo
// reduced to the fields this search reads.
type restrictInfo struct {
	// clause is the predicate itself, carried whole so P5.4 can attach it to
	// the join node it places the qual at.
	clause Expr

	// relids is PG's `required_relids`: every base relation the clause
	// references. Its member count is what makes a clause a join clause
	// (>= 2) rather than a baserel restriction (<= 1); the latter are not in
	// this list at all, because P5.1 already pushed them inside the leaf node
	// (joinsearch.go:174-183) and their selectivity is already inside the
	// initial rel's row count.
	relids RelSet

	// leftKey / rightKey and their relids are PG's operand split for a
	// canonical cross-rel equality — the operands a hash or merge join keys
	// on. Set only when `isEquijoin`; nil/0 otherwise.
	leftKey, rightKey       Expr
	leftRelids, rightRelids RelSet

	// isEquijoin marks the clause as `l = r` with each operand computable at
	// one side and the two sides disjoint — PG's precondition for a
	// hashclause/mergeclause (`clause_sides_match_join`, and the
	// `left_relids`/`right_relids` disjointness `restrict_infos` rely on).
	// A non-equality join qual (`a.x < b.y`) and an equality whose operands
	// share a relation (`a.x = a.y + b.z`) are join clauses but NOT
	// equijoins: neither has a two-sided key split. A three-rel equality
	// `a.x = b.y + c.z` DOES have one — it keys {a} against {b,c} — which is
	// why the split is stored as relsets rather than as two FROM positions.
	isEquijoin bool

	// inferred marks a clause synthesised by `inferAnchoredEqualities`
	// (equiv_class.go:236) rather than written by the user. Per 04 §5 this is
	// NOT an admissibility penalty — an inferred clause connects its two rels
	// exactly as a written one does, and the old ×2.0 `inferredEdgePenalty`
	// (bushy.go:67) has no counterpart here. It matters only as the
	// tie-break when an equivalence class has to choose which of its members
	// carries the selectivity.
	inferred bool

	// ecID is the equivalence class this clause belongs to, or noEquivClass.
	// Two clauses with the same ecID are transitively the same restriction:
	// `a.x = b.x` and (inferred) `a.x = c.x` and `b.x = c.x` are one class,
	// and a join may apply only ONE of them (see selectivityClauses).
	ecID int
}

// restrictInfoList is every join clause in one join problem, in a stable order
// (conjunct order, then the order equalities were pulled out of an OR). PG
// distributes these into per-rel `joininfo` lists; goopg scans the flat list,
// which is the same answer at this size — a join problem the search accepts has
// at most 16 relations (joinsearch.go:38) and a handful of quals per relation.
type restrictInfoList struct {
	all []*restrictInfo

	// nclasses is the number of equivalence classes discovered; ecIDs are
	// dense in [0, nclasses).
	nclasses int
}

// relsOverlap reports whether two relsets share a base relation (bms_overlap).
func relsOverlap(a, b RelSet) bool { return a&b != 0 }

// relsSubset reports whether sub is contained in super (bms_is_subset).
func relsSubset(sub, super RelSet) bool { return sub&^super == 0 }

// buildRestrictInfos turns the DP's conjunct list into the search's clause
// list. `conjuncts` and `inferredCount` are exactly what `buildJoinGraph`
// receives (bushy.go:271): the last `inferredCount` entries are the ones
// `inferAnchoredEqualities` synthesised. `cumOffsets` maps a ColumnRef's schema
// index to its FROM position, the same coordinate space `tableForCol` uses.
//
// A conjunct becomes a clause when it references two or more FROM items.
// Single-rel and constant conjuncts are deliberately absent: they are already
// inside the leaf nodes P5.1 wrapped, so admitting them here would let P5.6
// charge their selectivity a second time.
func buildRestrictInfos(conjuncts []Expr, inferredCount int, cumOffsets []int) *restrictInfoList {
	l := &restrictInfoList{}

	// explicitEnd mirrors buildJoinGraph's split: conjuncts at or after it are
	// the synthesised tail.
	explicitEnd := len(conjuncts) - inferredCount
	if explicitEnd < 0 {
		explicitEnd = 0
	}

	// ec accumulates the equivalence classes across every canonical
	// ColumnRef = ColumnRef equality, explicit and inferred alike — the whole
	// point of 04 §5 is that an inferred member is a full member of its class.
	ec := newEquivClasses()
	// classKey remembers, per clause, the ident to look the class up by after
	// all unions are done (a root chosen mid-build can be superseded).
	classKey := make([]*columnIdent, 0, len(conjuncts))

	add := func(e Expr, inferred bool) {
		relids, ok := relidsOfExpr(e, cumOffsets)
		if !ok || relLevel(relids) < 2 {
			return
		}
		ri := &restrictInfo{clause: e, relids: relids, inferred: inferred, ecID: noEquivClass}
		if bin, isBin := e.(*BinaryOp); isBin && bin.Op == parser.OpEq {
			lr, lok := relidsOfExpr(bin.Left, cumOffsets)
			rr, rok := relidsOfExpr(bin.Right, cumOffsets)
			if lok && rok && lr != 0 && rr != 0 && !relsOverlap(lr, rr) {
				ri.isEquijoin = true
				ri.leftKey, ri.rightKey = bin.Left, bin.Right
				ri.leftRelids, ri.rightRelids = lr, rr
			}
		}
		l.all = append(l.all, ri)

		var key *columnIdent
		if lc, rc, isColEq := isColumnRefEquality(e); isColEq && ri.isEquijoin {
			li, rid := identOf(lc), identOf(rc)
			ec.union(li, rid)
			k := li
			key = &k
		}
		classKey = append(classKey, key)
	}

	for i, c := range conjuncts {
		inferred := i >= explicitEnd
		if bin, ok := c.(*BinaryOp); ok && bin.Op == parser.OpEq {
			add(bin, inferred)
			continue
		}
		// An OR-of-ANDs contributes the equalities common to every branch —
		// the same widening buildJoinGraph does (bushy.go:316-325), so the two
		// clause views agree about Q19-shaped predicates. The full OR is also
		// admitted when it spans rels, because it is a real join qual that has
		// to be placed even though it keys nothing.
		eqs := plannerCommonEquijoinsAcrossOr(c)
		for _, eq := range eqs {
			add(eq, inferred)
		}
		if len(eqs) == 0 {
			add(c, inferred)
		}
	}

	l.assignEquivClasses(ec, classKey)
	return l
}

// assignEquivClasses gives every EC-member clause a dense, deterministic class
// id. Determinism matters directly: the class id decides which member carries
// a join's selectivity, so a map-iteration-ordered id would make the plan
// depend on Go's randomised map order.
func (l *restrictInfoList) assignEquivClasses(ec *equivClasses, classKey []*columnIdent) {
	roots := make([]columnIdent, 0, len(classKey))
	seen := make(map[columnIdent]bool)
	for _, k := range classKey {
		if k == nil {
			continue
		}
		root := ec.find(*k)
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool {
		return compareColumnIdent(roots[i], roots[j]) < 0
	})
	id := make(map[columnIdent]int, len(roots))
	for i, r := range roots {
		id[r] = i
	}
	for i, k := range classKey {
		if k == nil {
			continue
		}
		l.all[i].ecID = id[ec.find(*k)]
	}
	l.nclasses = len(roots)
}

// hasRelevantJoinClause is PG's `have_relevant_joinclause` (joininfo.c:39): is
// there a clause referencing both rels? Note what it does NOT require — that
// the clause be COVERED by the two rels. PG's test is a pair of overlaps
// (membership in rel1's joininfo means the clause references rel1;
// `bms_overlap(other_relids, required_relids)` is the second half), so a
// three-rel qual `a.x = b.y + c.z` makes a⋈b relevant even though the qual
// cannot be evaluated there yet. That is deliberate upstream: it is the
// enumerator's "are these two worth joining" heuristic, not a placement test.
// The placement test — which does require coverage — is `clausesFor` below.
func (l *restrictInfoList) hasRelevantJoinClause(a, b *RelOptInfo) bool {
	if l == nil || a == nil || b == nil {
		return false
	}
	return l.hasRelevantJoinClauseRelids(a.Relids, b.Relids)
}

// hasRelevantJoinClauseRelids is hasRelevantJoinClause over bare relsets, for
// callers that have not built the RelOptInfo yet.
func (l *restrictInfoList) hasRelevantJoinClauseRelids(a, b RelSet) bool {
	if l == nil {
		return false
	}
	for _, ri := range l.all {
		if relsOverlap(ri.relids, a) && relsOverlap(ri.relids, b) {
			return true
		}
	}
	return false
}

// hasNoJoinClauseAtAll reports whether the rel is connected to nothing — PG's
// `old_rel->joininfo == NIL && !old_rel->has_eclass_joins` (joinrels.c:120).
// It is the gate on phase 1's clauseless else-branch: such a rel is crossed in
// eagerly at every level rather than waiting for the last-ditch pass, so a
// 1-row disconnected dimension can join at level 2. P5.3 is its consumer.
func (l *restrictInfoList) hasNoJoinClauseAtAll(rel *RelOptInfo) bool {
	if rel == nil {
		return true
	}
	if l == nil {
		return true
	}
	for _, ri := range l.all {
		if relsOverlap(ri.relids, rel.Relids) {
			return false
		}
	}
	return true
}

// clausesFor returns the clauses that become the joinrel's own restriction
// list when `outer` and `inner` are joined — PG's
// `build_joinrel_restrictlist` (relnode.c) rule: `required_relids` must be a
// SUBSET of the join's relids (the qual is computable here) and must touch
// both sides (it was not already applied below). Order is list order.
//
// Unlike hasRelevantJoinClause this DOES require coverage, because a clause is
// applied at the lowest level that can evaluate it (03 §5.4) and evaluating a
// clause whose third relation is not present is not possible.
func (l *restrictInfoList) clausesFor(outer, inner RelSet) []*restrictInfo {
	if l == nil || outer == 0 || inner == 0 || relsOverlap(outer, inner) {
		return nil
	}
	join := outer | inner
	var out []*restrictInfo
	for _, ri := range l.all {
		if !relsSubset(ri.relids, join) {
			continue
		}
		if !relsOverlap(ri.relids, outer) || !relsOverlap(ri.relids, inner) {
			continue
		}
		out = append(out, ri)
	}
	return out
}

// selectivityClauses is `clausesFor` reduced to the clauses that may
// CONTRIBUTE SELECTIVITY at this join — 04 §5's equivalence-class rule, and
// P5.6's input to `calcJoinrelSize`.
//
// The rule, and why it is not a heuristic: PG's
// `generate_join_implied_equalities_normal` (equivclass.c) splits an EC's
// members into those computable at the outer rel and those computable at the
// inner rel, and emits exactly ONE clause equating one outer member to one
// inner member ("we can equate any one outer member to any one inner member").
// The members within each side are already equal — enforced below. So an EC
// with n members yields n-1 clauses over a whole join tree, never C(n,2). A
// join that applied `a.x = b.x` and then also charged for the inferred
// `a.x = c.x` AND `b.x = c.x` when c arrives would multiply one restriction's
// selectivity twice; that double-count is exactly what the old ×2.0
// `inferredEdgePenalty` (bushy.go:67) was papering over, in the cost dimension
// instead of the cardinality one where the error lives.
//
// Which member survives: PG scores candidate pairs by operand Var-ness and
// hashjoinability (`select_equality_operator`, best_score up to 3). Every EC
// member goopg builds is already Var = Var of one type (isColumnRefEquality
// rejects the rest), so the only dimension that discriminates here is written
// versus synthesised — and the written clause is the one the estimator has
// statistics for and the user's own predicate. Explicit therefore beats
// inferred; ties go to list order, so the answer does not move between runs.
//
// The reduction itself lives in `oneClausePerEquivClass` (joinrelsize.go),
// because `calcJoinrelSize` is handed the joinrel's FULL restriction list —
// what the path generators must evaluate — and has to apply the same rule from
// the other direction.
func (l *restrictInfoList) selectivityClauses(outer, inner RelSet) []*restrictInfo {
	return oneClausePerEquivClass(l.clausesFor(outer, inner))
}

// relidsOfExpr is the set of FROM positions an expression references, in the
// coordinate space `cumOffsets` describes (the same one `tableForCol` reads).
// ok is false when a referenced column falls outside every relation's slice of
// the schema, which means the caller's offsets do not describe this expression
// and any relset derived from it would be a guess. tableForCol collapses that
// case and the multi-rel case into the same -1; a clause list cannot, since
// spanning several relations is the normal state of a join clause.
func relidsOfExpr(e Expr, cumOffsets []int) (RelSet, bool) {
	if e == nil || len(cumOffsets) < 2 {
		return 0, false
	}
	var set RelSet
	ok := true
	visitColumnRefsForTable(e, func(colIdx int) {
		for t := 0; t < len(cumOffsets)-1; t++ {
			if colIdx >= cumOffsets[t] && colIdx < cumOffsets[t+1] {
				if t >= maxSearchRels {
					ok = false
					return
				}
				set |= RelSet(1) << uint(t)
				return
			}
		}
		ok = false
	})
	if !ok {
		return 0, false
	}
	return set, true
}

// ---------------------------------------------------------------------------
// M0127-P6.3 — the three coordinate/edge helpers that outlived `bushy.go`.
//
// They were written for the old subset-bitmask DP's edge extraction and were
// deleted from under it with the rest of that file (08 §4), but each has a
// live consumer in the clause-producing path: `plannerCommonEquijoinsAcrossOr`
// feeds `buildRestrictInfos` below and `pushdown.go`'s cross-join promotion,
// and `tableForCol` / `visitColumnRefsForTable` are how BOTH this file's
// `relSetOf` and `local_filters.go`'s conjunct partition attribute an
// expression to a FROM position. They move here rather than dying because the
// question they answer — "which relation does this conjunct read?" — is this
// file's question, not the enumerator's.

// plannerCommonEquijoinsAcrossOr is the planner-Expr counterpart of
// commonEquijoinsAcrossOr in joinorder.go. Returns equality
// expressions of the form `t1.col = t2.col` that appear in every
// branch of an OR predicate.
func plannerCommonEquijoinsAcrossOr(e Expr) []*BinaryOp {
	bin, ok := e.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return nil
	}
	branches := flattenPlannerOr(bin)
	if len(branches) < 2 {
		return nil
	}
	branchEqs := make([]map[string]*BinaryOp, len(branches))
	for i, br := range branches {
		branchEqs[i] = map[string]*BinaryOp{}
		for _, c := range splitAnd(br) {
			b, ok := c.(*BinaryOp)
			if !ok || b.Op != parser.OpEq {
				continue
			}
			lc, lok := b.Left.(*ColumnRef)
			rc, rok := b.Right.(*ColumnRef)
			if !lok || !rok {
				continue
			}
			branchEqs[i][colRefIndexPairKey(lc, rc)] = b
		}
	}
	var common []*BinaryOp
	for k, v := range branchEqs[0] {
		ok := true
		for j := 1; j < len(branchEqs); j++ {
			if _, present := branchEqs[j][k]; !present {
				ok = false
				break
			}
		}
		if ok {
			common = append(common, v)
		}
	}
	return common
}

func flattenPlannerOr(e Expr) []Expr {
	bin, ok := e.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return []Expr{e}
	}
	out := flattenPlannerOr(bin.Left)
	out = append(out, flattenPlannerOr(bin.Right)...)
	return out
}

// colRefIndexPairKey produces an order-independent key for two
// planner ColumnRef nodes via their resolved column indices.
func colRefIndexPairKey(a, b *ColumnRef) string {
	li, ri := a.Index, b.Index
	if li > ri {
		li, ri = ri, li
	}
	return fmt.Sprintf("%d==%d", li, ri)
}

// tableForCol returns the FROM-table index that all ColumnRef nodes
// in e belong to, or -1 if columns span multiple tables.
func tableForCol(e Expr, cumOffsets []int) int {
	result := -1
	visitColumnRefsForTable(e, func(colIdx int) {
		for t := 0; t < len(cumOffsets)-1; t++ {
			if colIdx >= cumOffsets[t] && colIdx < cumOffsets[t+1] {
				if result == -1 {
					result = t
				} else if result != t {
					result = -2 // spans multiple tables
				}
				return
			}
		}
		result = -2 // column not in any table
	})
	if result < 0 {
		return -1
	}
	return result
}

// visitColumnRefsForTable invokes onIdx with the Index of every
// same-scope *ColumnRef in e. Its one live consumer is tableForCol.
//
// M0125-0002 commit 4: built on walkExprRefs / exprChildSlots instead
// of its own 12-of-32 type switch. Child structure comes from the
// primitive, so a ColumnRef under IS NULL, a cast, a row constructor
// or IS DISTINCT FROM — all silently skipped by the old arms — now
// contributes its index, and tableForCol attributes the conjunct
// correctly instead of answering -1 from a partial reference set.
//
// Scope policy: scopeIgnore. tableForCol's cumOffsets attribution is
// only meaningful for indices in THIS scope's coordinate space: an
// inner plan's ColumnRefs index the subplan's own schema and an
// *OuterColumnRef names a scope above, so neither reaches onIdx (the
// old walker's documented declines, preserved — see
// visit_refs_for_table_arms_test.go). A subquery node's PARAM_EXEC
// Args are same-scope slots and ARE visited now, as is the Operand of
// a Plan-carrying InExpr — the old arm returned before visiting
// anything when Plan != nil, so `col IN (subquery)` read as "no
// table" even though col is an ordinary same-scope reference.
//
// An unenumerated type panics, matching PG's
// expression_tree_walker_impl (nodeFuncs.c:2667); a silent skip means
// tableForCol partitions on an incomplete reference set (RC-1a).
func visitColumnRefsForTable(e Expr, onIdx func(int)) {
	walkExprRefs(e, scopeIgnore, exprVisitor{
		Visit: func(x Expr) bool {
			if cr, ok := x.(*ColumnRef); ok {
				onIdx(cr.Index)
			}
			return true
		},
		OnUnknown: func(x Expr) {
			panic(fmt.Sprintf("visitColumnRefsForTable: unrecognized expression type %T — "+
				"teach exprChildSlots (exprwalk.go) about it; a silent skip makes "+
				"tableForCol partition on an incomplete reference set", x))
		},
	})
}
