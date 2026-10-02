package optimizer

import (
	"reflect"
	"sort"
)

// renumberRTIDsFlatRtableOrder re-stamps every scan's RTID so that RTID
// order is PostgreSQL's flattened range-table order, which is the order
// EXPLAIN's set_rtable_names hands out `_N` suffixes in (M0146-0005df).
//
// RTIDs are allocated in planning first-encounter order (rtableScope.Alloc:
// outer FROM left-to-right, nested as reached), so a FROM subquery's
// relations are numbered in the middle of its parent's. PG builds
// glob->finalrtable differently (setrefs.c):
//
//  1. set_plan_references(root, top_plan) first adds the top query's own
//     range table (add_rtes_to_flat_rtable), every entry in rtindex order —
//     the query's FROM relations together with the relations of the
//     subqueries pull_up_subqueries flattened into it;
//  2. walking the top plan, each SubqueryScan PG kept (a subquery it could
//     not pull up) recurses set_plan_references into its subroot, adding
//     that level's range table at the point of the walk — outer input
//     before inner, so TPC-DS Q83's three grouped CTE subqueries take their
//     `item` / `date_dim` suffixes in join order (sr, wr, cr), not in
//     WITH-clause order;
//  3. the subplans (CTE bodies, sublink plans) follow, each the same way.
//
// Before stripTrivialSubqueryScans runs, a *SubqueryScan wrapper marks
// exactly the subqueries PG keeps as subquery RTEs (wrapped FROM subqueries
// and wrapInlinedCTEScans' inlined CTEs), so the levels are visible here
// and gone after the strip. Within one level the allocation order already
// is rtindex order. Subplans follow glob->subplans' post-order (a nested
// subplan finishes planning, and is appended, before the one holding it);
// siblings go in allocation order (smallest RTID) — CTE bodies are
// allocated before their consumers and sublink bodies as planning reaches
// them. The exact interleaving of a level's CTEs, its sublinks and the
// subplans of its kept subqueries is ledgered.
//
// RTID feeds only EXPLAIN's relation naming (explain_names.go), so the
// renumbering changes no plan and no result.
func renumberRTIDsFlatRtableOrder(root Node) {
	if root == nil {
		return
	}
	var order []int32
	placed := map[int32]bool{}
	place := func(ids []int32) {
		for _, id := range ids {
			if id != 0 && !placed[id] {
				placed[id] = true
				order = append(order, id)
			}
		}
	}
	// collectLevel returns one query level's relations in flat-rtable
	// order — its own entries by allocation (rtindex) order, then each kept
	// subquery level in plan-walk order — and the subplans hanging off it.
	var collectLevel func(top Node) (ids []int32, subs []Node)
	collectLevel = func(top Node) (ids []int32, subs []Node) {
		var own []int32
		var kept []Node
		seen := map[Node]bool{}
		var walk func(n Node)
		walk = func(n Node) {
			if n == nil || seen[n] {
				return
			}
			seen[n] = true
			if id := nodeRTID(n); id != 0 {
				own = append(own, id)
			}
			subs = append(subs, NodeSubplans(n)...)
			switch x := n.(type) {
			case *SubqueryScan:
				if n != top {
					// The subquery RTE itself is this level's entry (an
					// inlined CTE reference carries its RTID); its
					// contents are the kept level's.
					if cs, ok := x.Child.(*CTEScan); ok && cs.RTID != 0 {
						own = append(own, cs.RTID)
					}
					kept = append(kept, x.Child)
					return
				}
			case *CTEScan:
				if x.Inlined() {
					// An inlined single-reference CTE is a subquery RTE
					// in PG: pulled up into this level, or — wrapped by
					// wrapInlinedCTEScans, reached as a kept level — a
					// level of its own (the body is walked below).
					walk(x.Child)
					return
				}
				// A materialized CTE's body is a subplan
				// (SS_process_ctes); its relations are not this level's.
				if x.Child != nil {
					subs = append(subs, x.Child)
				}
				return
			}
			kids, _ := planChildNodes(n)
			for _, k := range kids {
				walk(k)
			}
		}
		if sq, ok := top.(*SubqueryScan); ok {
			walk(sq.Child)
		} else {
			walk(top)
		}
		sort.SliceStable(own, func(i, j int) bool { return own[i] < own[j] })
		ids = own
		for _, k := range kept {
			kids, ksubs := collectLevel(k)
			ids = append(ids, kids...)
			subs = append(subs, ksubs...)
		}
		return ids, subs
	}
	// placeSubplans follows glob->subplans: a subplan is appended once its
	// own planning finishes, so the subplans nested inside it (a sublink in
	// a CTE body — TPC-DS Q23's InitPlan under best_ss_customer) precede it.
	// Siblings go in allocation order.
	done := map[Node]bool{}
	var placeSubplans func(subs []Node)
	placeSubplans = func(subs []Node) {
		sort.SliceStable(subs, func(a, b int) bool { return minRTID(subs[a]) < minRTID(subs[b]) })
		for _, sp := range subs {
			if sp == nil || done[sp] {
				continue
			}
			done[sp] = true
			ids, nested := collectLevel(sp)
			placeSubplans(nested)
			place(ids)
		}
	}
	ids, subs := collectLevel(root)
	place(ids)
	placeSubplans(subs)

	remap := make(map[int32]int32, len(order))
	for i, id := range order {
		remap[id] = int32(i + 1)
	}
	next := int32(len(order) + 1)
	forEachPlanNodeDeep(root, func(n Node) {
		f := rtidField(n)
		if !f.IsValid() || f.Int() == 0 {
			return
		}
		old := int32(f.Int())
		nw, ok := remap[old]
		if !ok {
			// Unreached by the level walk (should not happen): keep it
			// unique and after every placed relation.
			nw = next
			next++
			remap[old] = nw
		}
		f.SetInt(int64(nw))
	})
}

// rtidField returns n's settable RTID field, or the zero Value.
func rtidField(n Node) reflect.Value {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Ptr || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return reflect.Value{}
	}
	f := v.Elem().FieldByName("RTID")
	if !f.IsValid() || f.Kind() != reflect.Int32 || !f.CanSet() {
		return reflect.Value{}
	}
	return f
}

func nodeRTID(n Node) int32 {
	if f := rtidField(n); f.IsValid() {
		return int32(f.Int())
	}
	return 0
}

// minRTID is the smallest RTID anywhere in n's subtree, subplans included
// (0 when none).
func minRTID(n Node) int32 {
	var m int32
	forEachPlanNodeDeep(n, func(x Node) {
		if id := nodeRTID(x); id != 0 && (m == 0 || id < m) {
			m = id
		}
	})
	return m
}

// forEachPlanNodeDeep visits every plan node reachable from root through
// child links and sublink bodies, each once.
func forEachPlanNodeDeep(root Node, fn func(Node)) {
	seen := map[Node]bool{}
	var walk func(Node)
	walk = func(n Node) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		fn(n)
		for _, sp := range NodeSubplans(n) {
			walk(sp)
		}
		kids, _ := planChildNodes(n)
		for _, k := range kids {
			walk(k)
		}
	}
	walk(root)
}
