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
		// physical records whether a spine-breaking ancestor sits
		// between the leaf and its region root — see below.
		physical bool
	}
	var leaves []leaf

	// Assign each wrapper its enclosing scope region. planChildNodes
	// (reflection over exported Node fields) supplies children, so new
	// node kinds are traversed automatically; the boundary set is what
	// makes a deeper region, and it is explicit.
	//
	// M0146-0005x: each leaf also tracks the tlist regime upstream picks
	// for it. create_plan hands the top path CP_EXACT_TLIST; Limit,
	// LockRows and Result pass their flags through and Sort, Material
	// and Memoize add CP_SMALL_TLIST — all of which keep a leaf in the
	// pathtarget regime, where the scan's tlist is the needed columns in
	// first-needed order and the consumption-identity test below decides.
	// Joins — including NestedLoopIndexJoin, whose Outer/Inner take the
	// same recurse call — Agg and ProjectSet instead pass children bare
	// CP_LABEL_TLIST/0, so use_physical_tlist fires: the scan's tlist is
	// the subquery's whole output in attno order — identity by
	// construction — and trivial_subqueryscan strips whenever the leaf
	// has no qual, regardless of consumption. (Verified on PG 18.3:
	// `u.a` from a two-column grouped leaf keeps `Subquery Scan` at top
	// level and under Sort/Limit/Unique, but loses it under Merge Join
	// and under a top Aggregate.)
	var walk func(n, region, parent Node, physical bool)
	walk = func(n, region, parent Node, physical bool) {
		if n == nil {
			return
		}
		inner := region
		innerPhysical := physical
		if sq, ok := n.(*SubqueryScan); ok {
			leaves = append(leaves, leaf{region: region, parent: parent, scan: sq, physical: physical})
			inner = n
			innerPhysical = false
		} else if isDerived[n] {
			inner = n
			innerPhysical = false
		} else if _, ok := n.(*CTEScan); ok {
			inner = n
			innerPhysical = false
		}
		// A child's tlist regime is recomputed at EVERY level, the way
		// createplan.c's flags are: a spine breaker hands its children
		// bare 0/LABEL/IGNORE (physical regime), a node demanding an
		// exact or small tlist hands out CP_EXACT_TLIST or adds
		// CP_SMALL_TLIST (pathtarget regime — even under a breaker:
		// `NL -> Sort -> SubqueryScan` still keeps the label on subset
		// consumption), and a passthrough node propagates the incoming
		// regime (Limit, LockRows, Unique/Distinct via flags|LABEL).
		// A new query region restarts in the EXACT regime, matching
		// each subplan's own CP_EXACT_TLIST entry into create_plan.
		childPhysical := innerPhysical
		switch {
		case subqueryStripSpineBreaker(n):
			childPhysical = true
		case subqueryStripTlistReset(n):
			childPhysical = false
		}
		kids, _ := planChildNodes(n)
		for _, k := range kids {
			r := inner
			p := childPhysical
			if r != region {
				// Region boundary — derived body, CTEScan body or
				// SubqueryScan interior each begin their own
				// EXACT-tlist regime.
				p = false
			}
			if _, isSetOp := n.(*SetOp); isSetOp {
				// Each arm was planned in its own query scope; the arm
				// subtree is the region for everything inside it.
				r = k
				p = false
			}
			walk(k, r, n, p)
		}
	}
	walk(root, root, nil, false)

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
		w := len(l.scan.Output())
		identity := clean && len(used) == w
		if identity {
			for i, u := range used {
				if u != i {
					identity = false
					break
				}
			}
		}
		// M0146-0005x — consumption-identity is the pathtarget-regime
		// proxy for PG's structural test; under a spine breaker the
		// physical tlist makes the wrapper trivial regardless of which
		// positions the consumer reads or in what order (createplan.c
		// use_physical_tlist + plancat.c's RTE_SUBQUERY arm). The
		// width guard keeps coordinate honesty: the wrapper may go
		// only when the child's output is position-for-position the
		// leaf's schema.
		if !identity && !(l.physical && len(l.scan.Output()) == len(l.scan.Child.Output())) {
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

// subqueryStripSpineBreaker reports whether this node hands its
// children a bare tlist demand: in createplan.c terms, the child
// create_plan_recurse calls that pass 0, CP_LABEL_TLIST or
// CP_IGNORE_TLIST, letting use_physical_tlist fire on a scan leaf
// below (the flag word carries neither CP_EXACT_TLIST nor
// CP_SMALL_TLIST). create_nestloop_plan covers both goopg join node
// kinds (*Join and *NestedLoopIndexJoin). goopg's *Project is NOT a
// breaker: a SubqueryScan path is projection-capable, so PG folds a
// computed select list into the scan's own tlist (non-identity →
// keep) — verified live (`u.c+1` under Limit keeps the label).
// Everything else either propagates `flags` (Limit, LockRows,
// Unique/Distinct/UpperUnique via flags|LABEL — the added LABEL bit
// does not set EXACT or SMALL) or resets to the pathtarget regime —
// see subqueryStripTlistReset.
func subqueryStripSpineBreaker(n Node) bool {
	switch n.(type) {
	// Joins: create_nestloop_plan passes 0 and a directly-attached
	// merge/hash join child the same (an input needing sort carries a
	// Sort node that resets the regime itself). Aggregate covers
	// create_agg_plan/create_group_plan (CP_LABEL_TLIST).
	case *Join, *NestedLoopIndexJoin, *Aggregate, *ProjectSet:
		return true
	}
	return false
}

// subqueryStripTlistReset reports whether this node hands its children
// an exact or small tlist demand — the (CP_EXACT_TLIST |
// CP_SMALL_TLIST) test that makes use_physical_tlist decline regardless
// of the incoming flags: create_sort_plan/create_incrementalsort_plan,
// create_material_plan/create_memoize_plan and create_windowagg_plan
// add CP_SMALL_TLIST; create_gather_plan/create_gather_merge_plan and
// create_recursiveunion_plan request CP_EXACT_TLIST outright.
func subqueryStripTlistReset(n Node) bool {
	switch n.(type) {
	case *Sort, *IncrementalSort, *Memoize, *WindowAgg,
		*Gather, *GatherMerge, *RecursiveUnion:
		return true
	}
	return false
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
