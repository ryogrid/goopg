package optimizer

// stripTrivialSubqueryScans — M0146-0005w.
//
// PG creates a SubqueryScan plan node for EVERY subquery leaf RTE that
// pull-up refused (set_subquery_pathlist → subqueryscanpath), then DELETES
// the node again at setrefs when it is trivial — setrefs.c
// trivial_subqueryscan / clean_up_removed_plan_level: no qpqual, and the
// scan's targetlist regurgitates the subplan's own output position for
// position. That is why `Subquery Scan on <alias>` survives on TPC-DS
// Q8's `a1` arm (the INTERSECT branch consumes `ca_zip` only, one of the
// arm's two outputs — non-trivial) while a full-width, in-order consumer
// renders the bare child node instead.
//
// goopg plans the label the same way (planSubqueryRangeVar emits the
// wrapper on every non-simple leaf) and replicates the strip decision
// here, at Plan()'s tail — the point in its pipeline that matches
// setrefs' position. For each wrapper the pass computes the positions of
// its output the enclosing scope actually references, in first-reference
// order:
//
//   - consumed = [0..w-1] in order, and no Filter sits directly on the
//     leaf (a leaf-level restriction is what PG keeps as the scan's
//     qpqual — non-trivial)  →  STRIP: unwrap, render the child.
//   - subset, out-of-order, or empty consumption, or a leaf Filter
//     →  KEEP: PG's node survives, `Subquery Scan on <alias>` renders.
//
// Consumption is bounded to the leaf's ENCLOSING scope because sourceIdx
// is per-FROM-clause monotonic: a same-numbered binding inside a nested
// derived table, a set-operation arm, or a CTE body is a different
// binding. The region boundary set is every FROM-subquery subtree
// (rtableScope.derivedSubtrees — recorded wrapped or not), every CTEScan
// body, and every set-operation arm subtree. Scopes the registry cannot
// see (view bodies planned through a re-entrant Plan, expr-level
// sublinks) can only make the used-set larger, which is the over-keep
// direction — a label PG strips, never a wrongly-removed one.

import (
	"reflect"
	"strings"
)

// stripTrivialSubqueryScans unwraps every trivial SubqueryScan in the
// finished plan. derived is the per-statement registry of FROM-subquery
// leaf subtree roots (rtableScope.derivedSubtrees); an empty registry
// leaves the plan untouched.
func stripTrivialSubqueryScans(root Node, derived []Node) Node {
	if root == nil || len(derived) == 0 {
		return root
	}
	isDerived := make(map[Node]bool, len(derived))
	for _, r := range derived {
		isDerived[r] = true
	}

	type leaf struct {
		region Node
		parent Node
		scan   *SubqueryScan
	}
	var leaves []leaf

	// Assign each wrapper its enclosing scope region. planChildNodes
	// (reflection over exported Node fields) supplies children, so new
	// node kinds are traversed automatically; the boundary set is what
	// makes a deeper region, and it is explicit.
	var walk func(n, region, parent Node)
	walk = func(n, region, parent Node) {
		if n == nil {
			return
		}
		inner := region
		if sq, ok := n.(*SubqueryScan); ok {
			leaves = append(leaves, leaf{region: region, parent: parent, scan: sq})
			inner = n
		} else if isDerived[n] {
			inner = n
		} else if _, ok := n.(*CTEScan); ok {
			inner = n
		}
		kids, _ := planChildNodes(n)
		for _, k := range kids {
			r := inner
			if _, isSetOp := n.(*SetOp); isSetOp {
				// Each arm was planned in its own query scope; the arm
				// subtree is the region for everything inside it.
				r = k
			}
			walk(k, r, n)
		}
	}
	walk(root, root, nil)

	if len(leaves) == 0 {
		return root
	}

	// triviality per leaf: the ordered list of consumed leaf-local
	// positions must equal [0..w-1] exactly, every collected ref must
	// resolve back into the leaf schema, and the leaf may not carry a
	// Filter.
	trivial := make(map[*SubqueryScan]bool, len(leaves))
	for _, l := range leaves {
		used, clean := leafUsedPositions(l.region, l.scan, isDerived)
		if !clean {
			continue
		}
		w := len(l.scan.Output())
		identity := len(used) == w
		for i, u := range used {
			if u != i {
				identity = false
				break
			}
		}
		if !identity {
			continue
		}
		if _, isFilter := l.parent.(*Filter); isFilter {
			continue
		}
		trivial[l.scan] = true
	}
	if len(trivial) == 0 {
		return root
	}
	return stripPlanRebuild(root, trivial)
}

// leafUsedPositions returns the leaf-local positions of scan's output
// columns the enclosing region references, in first-reference order,
// plus a clean flag that is false when any same-source ref could not be
// mapped back to a leaf position (name missing or ambiguous) — in that
// case the caller keeps the wrapper.
//
// ColumnRef.Index is NOT leaf-local: scope-level exprs address the
// enclosing row, so `u.a` under a join arrives as the jointree-global
// position. The mapping back is by (Name, SourceTableIdx) against the
// leaf's own output schema — the same Name+source disambiguation
// resolveHostColumnIdx and findColumnIndexByNameAndSource already use.
//
// The walk is bounded by region: any deeper binding scope (a recorded
// derived subtree, a CTE body, or a set-operation arm subtree) is left
// out — its per-FROM-clause sourceIdx numbering belongs to a different
// namespace.
func leafUsedPositions(region Node, scan *SubqueryScan, isDerived map[Node]bool) ([]int, bool) {
	out := scan.Output()
	var used []int
	seen := map[int]bool{}
	clean := true
	var visit func(n Node)
	visit = func(n Node) {
		if n == nil {
			return
		}
		if n != region {
			if isDerived[n] {
				return
			}
			switch n.(type) {
			case *CTEScan, *SubqueryScan:
				return
			}
		}
		eachOwnExpr(n, func(e Expr) {
			cr, ok := e.(*ColumnRef)
			if !ok || cr.SourceTableIdx != scan.src {
				return
			}
			pos, ok := leafLocalPosition(out, cr)
			if !ok {
				clean = false
				return
			}
			if !seen[pos] {
				seen[pos] = true
				used = append(used, pos)
			}
		})
		if _, isSetOp := n.(*SetOp); isSetOp && n != region {
			// The arms are deeper binding scopes; the node's own
			// exprs (merge/dedup keys) still count above.
			return
		}
		kids, _ := planChildNodes(n)
		for _, k := range kids {
			visit(k)
		}
	}
	visit(region)
	return used, clean
}

// leafLocalPosition maps a scope-level reference to a position in the
// leaf's output schema by name. It reports false when the name is absent
// or ambiguous (a leaf selecting the same column twice is exactly the
// non-trivial case PG keeps, so declining the position only ever keeps a
// label).
func leafLocalPosition(out Schema, cr *ColumnRef) (int, bool) {
	pos := -1
	for i, c := range out {
		if !strings.EqualFold(c.Name, cr.Name) {
			continue
		}
		if pos >= 0 {
			return 0, false
		}
		pos = i
	}
	return pos, pos >= 0
}

// eachOwnExpr visits every expression node this plan node carries
// itself — targets, predicates, keys, aggregate args — excluding its
// children. It stubs the child links on a shallow copy and defers the
// per-kind enumeration to walkPlanExprs, the same switch every other
// reader uses, so the expr inventory cannot drift from it (the trick
// nodeOwnExprsHaveEscapingOuterRef already uses).
func eachOwnExpr(n Node, visit func(Expr)) {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Ptr || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return
	}
	cp := reflect.New(v.Elem().Type())
	cp.Elem().Set(v.Elem())
	e := cp.Elem()
	for i := 0; i < e.NumField(); i++ {
		f := e.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Type() {
		case nodeIfaceType:
			f.Set(reflect.Zero(f.Type()))
		case nodeSliceType:
			f.Set(reflect.Zero(f.Type()))
		}
	}
	if stub, ok := cp.Interface().(Node); ok {
		walkPlanExprs(stub, visit)
	}
}

// stripPlanRebuild returns the plan with every trivial SubqueryScan
// replaced by its (already-rebuilt) child. Children are rebuilt first so
// nested wrappers are stripped innermost-out; the coordinate contract is
// unchanged because the wrapper is position-for-position transparent.
func stripPlanRebuild(n Node, trivial map[*SubqueryScan]bool) Node {
	if n == nil {
		return nil
	}
	if sq, ok := n.(*SubqueryScan); ok && trivial[sq] {
		return stripPlanRebuild(sq.Child, trivial)
	}
	return mapPlanChildren(n, func(c Node) Node {
		return stripPlanRebuild(c, trivial)
	})
}

// mapPlanChildren returns a shallow copy of n with every child plan node
// replaced by fn(child) — the rebuild twin of planChildNodes' reflection
// over exported Node / []Node fields, so it covers every node kind
// without a hand-maintained list.
func mapPlanChildren(n Node, fn func(Node) Node) Node {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Ptr || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return n
	}
	cp := reflect.New(v.Elem().Type())
	cp.Elem().Set(v.Elem())
	e := cp.Elem()
	for i := 0; i < e.NumField(); i++ {
		f := e.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Type() {
		case nodeIfaceType:
			if !f.IsNil() {
				f.Set(reflect.ValueOf(fn(f.Interface().(Node))))
			}
		case nodeSliceType:
			if f.Len() > 0 {
				out := reflect.MakeSlice(f.Type(), f.Len(), f.Len())
				for j := 0; j < f.Len(); j++ {
					el := f.Index(j)
					if el.IsNil() {
						continue
					}
					out.Index(j).Set(reflect.ValueOf(fn(el.Interface().(Node))))
				}
				f.Set(out)
			}
		}
	}
	out, _ := cp.Interface().(Node)
	if out == nil {
		return n
	}
	return out
}
