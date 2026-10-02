package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// orientECJoinClauses gives every inner-join `a = b` between two leaves
// the operand order PostgreSQL's equivalence-class machinery prints it in
// (M0146-0005de). The order is EXPLAIN text only — equality commutes — and
// shows wherever PG prints the RestrictInfo as it is: a nested loop's Join
// Filter, a parameterised scan's Filter and a bitmap heap scan's Recheck
// Cond. (Hash / Merge Cond are re-ordered outer-first and an Index Cond
// indexed-column-first at plan time, both independently of this.)
//
// PG never plans the written clause. Join clauses are re-derived from the
// equivalence class by create_join_clause, which looks a pair up in
// ec_derives in EITHER orientation (ec_search_derived_clause_for_ems,
// equivclass.c) — so the first code path to create a pair fixes its order:
//
//  1. set_base_rel_pathlists visits the base rels in range-table order.
//     For a rel R, create_index_paths → match_eclass_clauses_to_index →
//     generate_implied_equalities_for_column creates `R.k = other.c` for
//     every btree (non-partial) index key column k of R that is an EC
//     member, against each member of that EC outside R.
//  2. Still under R, the parameterised index paths those clauses yield
//     call get_baserel_parampathinfo(R, {S}), which creates every other EC
//     clause between R and S as `S.c = R.c` (generate_join_implied_
//     equalities_normal puts the outer member first).
//  3. Any pair still uncreated is created by the first join between its
//     two rels — make_join_rel(rel1, rel2) at join level 2, the earlier
//     joinlist entry outer — so it prints in FROM order.
//
// For a clause between rels R1 < R2 (range-table order) that gives: R1 has
// an index key in the clause's EC → R1 first (1); R1 has an index key in an
// EC reaching R2 → R2 first (2); then the same two tests for R2 (R2 first,
// then R1 first); else R1 first (3, joinlist order).
//
// An EC equated to a constant (ec_has_const) yields no join clause in PG
// at all, so its clauses are left alone. Cross-type equalities stay out, as
// in inferTransitiveEqualities (isColumnRefEquality).
//
// The decision is taken here, on the search's conjuncts, but applied to the
// SEARCHED tree (applyECOrientation): the restrict-list machinery reads a
// class's member order off the clauses' operand order
// (buildRestrictInfos' ecMembers → equivClassJoinClause's pair choice), so
// flipping the conjuncts themselves re-chose EC pairs and kept a redundant
// clause on TPC-DS Q82. The result is keyed by each column's (binding id,
// name), which survives the search's coordinate remaps and also names the
// NestLoop-param side (an OuterColumnRef) of a parameterised scan's qual.
func orientECJoinClauses(conjuncts []Expr, scans []Node, spans []leafSpan, cat catalog.Catalog) ecOrientation {
	if len(conjuncts) == 0 || len(spans) != len(scans) {
		return nil
	}
	leafOf := func(idx int) int {
		for i, sp := range spans {
			if idx >= sp.lo && idx < sp.hi {
				return i
			}
		}
		return -1
	}
	// Union-find over flat column offsets: the conjuncts' column
	// equalities, the planner's view of PG's EC membership.
	parent := map[int]int{}
	var find func(int) int
	find = func(x int) int {
		p, ok := parent[x]
		if !ok || p == x {
			parent[x] = x
			return x
		}
		r := find(p)
		parent[x] = r
		return r
	}
	type pair struct {
		bo   *BinaryOp
		l, r int // leaves
	}
	var pairs []pair
	for _, c := range conjuncts {
		l, r, ok := isColumnRefEquality(c)
		if !ok {
			continue
		}
		parent[find(l.Index)] = find(r.Index)
		ll, rl := leafOf(l.Index), leafOf(r.Index)
		if ll >= 0 && rl >= 0 && ll != rl {
			pairs = append(pairs, pair{c.(*BinaryOp), ll, rl})
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	constClass := map[int]bool{}
	for _, c := range conjuncts {
		if cr, _, ok := isColumnRefConstEquality(c); ok {
			constClass[find(cr.Index)] = true
		}
	}
	// Index key columns per leaf, as flat offsets.
	indexed := map[int]bool{}
	rtOrder := make([]int, len(scans))
	for i, n := range scans {
		rtOrder[i] = i
		base := n
		for {
			f, ok := base.(*Filter)
			if !ok {
				break
			}
			base = f.Child
		}
		if id := nodeRTID(base); id != 0 {
			rtOrder[i] = int(id)
		}
		tbl := scanTable(base)
		if tbl == nil || cat == nil {
			continue
		}
		out := n.Output()
		if len(out) != spans[i].hi-spans[i].lo {
			continue
		}
		for _, idx := range cat.IndexesOnTable(tbl) {
			if idx == nil || idx.HasPredicate || idx.DeclaredHash || !strings.EqualFold(idx.Method, "btree") && idx.Method != "" {
				continue
			}
			for _, key := range idx.Columns {
				for k, col := range out {
					if col.Name == key {
						indexed[spans[i].lo+k] = true
					}
				}
			}
		}
	}
	// keyInClass: leaf has an index key column in class.
	keyInClass := func(leaf, class int) bool {
		for o := range indexed {
			if leafOf(o) == leaf && find(o) == class {
				return true
			}
		}
		return false
	}
	// keyReaches: leaf has an index key column in a (non-constant) class
	// with a member in other.
	keyReaches := func(leaf, other int) bool {
		for o := range indexed {
			if leafOf(o) != leaf {
				continue
			}
			cl := find(o)
			if constClass[cl] {
				continue
			}
			for m := range parent {
				if leafOf(m) == other && find(m) == cl {
					return true
				}
			}
		}
		return false
	}
	want := ecOrientation{}
	for _, p := range pairs {
		lc, rc := p.bo.Left.(*ColumnRef), p.bo.Right.(*ColumnRef)
		lk, rk := ecColKey{lc.SourceTableIdx, lc.Name}, ecColKey{rc.SourceTableIdx, rc.Name}
		if lk.src == 0 || rk.src == 0 || lk == rk {
			continue
		}
		class := find(lc.Index)
		if constClass[class] {
			continue
		}
		r1, r2 := p.l, p.r
		if rtOrder[r2] < rtOrder[r1] {
			r1, r2 = r2, r1
		}
		var first int
		switch {
		case keyInClass(r1, class):
			first = r1
		case keyReaches(r1, r2):
			first = r2
		case keyInClass(r2, class):
			first = r2
		case keyReaches(r2, r1):
			first = r1
		default:
			// make_join_rel's outer: the earlier joinlist entry.
			first = p.l
			if p.r < p.l {
				first = p.r
			}
		}
		if first == p.l {
			want[ecPairOf(lk, rk)] = lk
		} else {
			want[ecPairOf(lk, rk)] = rk
		}
	}
	return want
}

// ecColKey names a column by its binding id (SourceTableIdx, per query
// level) and name.
type ecColKey struct {
	src  int16
	name string
}

// ecOrientation maps an unordered column pair to the column PG prints
// first.
type ecOrientation map[[2]ecColKey]ecColKey

func ecPairOf(a, b ecColKey) [2]ecColKey {
	if b.src < a.src || b.src == a.src && b.name < a.name {
		a, b = b, a
	}
	return [2]ecColKey{a, b}
}

// eqOperandKey keys a plain column operand of this level: a ColumnRef, or
// the OuterColumnRef a parameterised scan reads its NestLoop param through.
func eqOperandKey(e Expr) (ecColKey, bool) {
	if x, ok := e.(*ColumnRef); ok {
		return ecColKey{x.SourceTableIdx, x.Name}, x.SourceTableIdx != 0
	}
	if x, ok := e.(*OuterColumnRef); ok {
		return ecColKey{x.SourceTableIdx, x.Name}, x.Level == 1 && x.SourceTableIdx != 0
	}
	return ecColKey{}, false
}

// applyECOrientation orders the equalities of a searched join tree per
// want. It walks only this query level's join, filter and scan nodes —
// a SubqueryScan / CTE / function leaf is another level, whose binding
// ids mean something else — and swaps operands in place, so pointer
// identities the tail passes key on (fillJoinHashKeys' HashKeys) hold.
// Index keys are left alone: an Index Cond prints indexed column first
// whatever the clause says.
func applyECOrientation(n Node, want ecOrientation) {
	if n == nil || len(want) == 0 {
		return
	}
	orient := func(e Expr) { orientECExpr(e, want) }
	switch x := n.(type) {
	case *Join:
		orient(x.Predicate)
		applyECOrientation(x.Left, want)
		applyECOrientation(x.Right, want)
	case *NestedLoopIndexJoin:
		orient(x.Predicate)
		applyECOrientation(x.Outer, want)
		applyECOrientation(x.Inner, want)
	case *Filter:
		orient(x.Predicate)
		applyECOrientation(x.Child, want)
	case *Project:
		applyECOrientation(x.Child, want)
	case *Gather:
		applyECOrientation(x.Child, want)
	case *GatherMerge:
		applyECOrientation(x.Child, want)
	case *Materialize:
		applyECOrientation(x.Child, want)
	case *Memoize:
		applyECOrientation(x.Child, want)
	case *Sort:
		applyECOrientation(x.Child, want)
	case *IndexScan:
		orient(x.Cond)
	case *IndexOnlyScan:
		orient(x.Cond)
	case *BitmapHeapScan:
		for _, q := range x.BitmapQual {
			orient(q)
		}
		orient(x.Cond)
	}
}

// orientECExpr orders the top-level equalities of one qual per want (the
// search's above-root residual is one; applyECOrientation covers the tree).
func orientECExpr(e Expr, want ecOrientation) {
	if e == nil || len(want) == 0 {
		return
	}
	for _, c := range splitAnd(e) {
		bo, ok := c.(*BinaryOp)
		if !ok || bo.Op != parser.OpEq {
			continue
		}
		lk, lok := eqOperandKey(bo.Left)
		rk, rok := eqOperandKey(bo.Right)
		if !lok || !rok || lk == rk {
			continue
		}
		if first, ok := want[ecPairOf(lk, rk)]; ok && first == rk {
			bo.Left, bo.Right = bo.Right, bo.Left
		}
	}
}

// clauseOuterFirst reports whether an equijoin restrict info's clause, as
// it now reads, names the outer side first. leftKey/leftRelids record the
// operand order at build time; orientECJoinClauses may since have swapped
// the clause's operands in place, which shows as Left no longer being
// leftKey.
func clauseOuterFirst(ri *restrictInfo, outer RelSet) bool {
	outerFirst := relsSubset(ri.leftRelids, outer)
	if bo, ok := ri.clause.(*BinaryOp); ok && bo.Left != ri.leftKey && bo.Right == ri.leftKey {
		outerFirst = !outerFirst
	}
	return outerFirst
}
