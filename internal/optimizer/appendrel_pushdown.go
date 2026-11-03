package optimizer

import "strings"

// M0148-0001k — restrictions on an inheritance / partition appendrel are
// pushed into every member scan, as set_append_rel_size does.
//
// planScanRangeVar expands an inherited or partitioned table into a UNION
// ALL chain of member scans (each wrapped in a tableoid Project, plus a
// remap Project when the member's column order differs). A restriction on
// the table is then planned as one Filter over that chain — EXPLAIN printed
// `Append  Filter: (minmaxtest.f1 > 1)` — while PG gives every child rel
// its own copy of the parent's baserestrictinfo with the parent's columns
// replaced by the child's (adjust_appendrel_attrs), so each member scan
// prints `Filter: (f1 > 1)` and the Append has no filter.
//
// The pass runs on the finished plan. For each Filter directly over an
// inheritance / partition appendrel it translates every conjunct through
// each member's Projects down to the member's SeqScan and attaches it there
// as a leaf-local Filter. The conjunct is MOVED, not copied — each row is
// still evaluated once, in its member, as in PG — so a fully pushed Filter
// is left with a TRUE predicate, which EXPLAIN and the executor treat as
// absent (R48). A conjunct that reads an outer reference or holds a sublink
// stays on the Append, as does every conjunct when a member does not have
// the expected shape.

// pushAppendRelRestrictions applies the push throughout the plan, sublink
// bodies included.
func pushAppendRelRestrictions(root Node) {
	if planTakesRowMarksOrModifies(root) {
		return
	}
	seen := map[Node]bool{}
	var walk func(Node) Node
	walk = func(n Node) Node {
		if n == nil || seen[n] {
			return n
		}
		seen[n] = true
		if f, ok := n.(*Filter); ok {
			if top, ok := f.Child.(*SetOp); ok && isInheritanceAppendRel(top) {
				pushFilterIntoAppendRelMembers(f, top)
			}
		}
		for _, sp := range NodeSubplans(n) {
			walk(sp)
		}
		setPlanChildrenInPlace(n, walk)
		return n
	}
	walk(root)
}

// planTakesRowMarksOrModifies reports whether the plan locks or modifies
// rows (FOR UPDATE/SHARE, UPDATE, DELETE, MERGE). Their EvalPlanQual recheck
// (lockRowsOp.findFilterPred and the DML twins) re-tests the latest row
// version against the qual it finds ABOVE the scan chain, so a qual moved
// into the members would make the recheck test nothing and return a row the
// concurrent update took out of the result (isolation eval-plan-qual,
// readp2). Such plans keep the Append-level Filter until the recheck reads
// member quals (deferral ledger 2026-11-03).
func planTakesRowMarksOrModifies(root Node) bool {
	found := false
	seen := map[Node]bool{}
	var walk func(Node) Node
	walk = func(n Node) Node {
		if n == nil || found || seen[n] {
			return n
		}
		seen[n] = true
		switch n.(type) {
		case *LockRows, *Update, *Delete, *Merge:
			found = true
			return n
		}
		for _, sp := range NodeSubplans(n) {
			walk(sp)
		}
		setPlanChildrenInPlace(n, walk)
		return n
	}
	walk(root)
	return found
}

// isInheritanceAppendRel reports whether s is the top of an inheritance or
// partition expansion: a UNION ALL that carries the parent RTE (RTID and
// AppendRelName, set by planScanRangeVar) and is not a FROM-clause UNION
// ALL appendrel (whose quals M0146-0094 pushes on the AST).
func isInheritanceAppendRel(s *SetOp) bool {
	return s != nil && s.All && s.RTID > 0 && s.AppendRelName != "" && !s.IsUnionAllAppendRel()
}

// appendRelMember is one member of the chain: the Project whose Child is
// the member's SeqScan (where the Filter is attached) and the Projects the
// conjunct is translated through, outermost first.
type appendRelMember struct {
	attach *Project
	scan   *SeqScan
	path   []*Project
}

// appendRelMembers flattens the left-deep chain below top. ok is false when
// a member is not `Project(tableoid) [→ Project(remap)] → SeqScan`.
func appendRelMembers(top *SetOp) ([]appendRelMember, bool) {
	var out []appendRelMember
	var visit func(Node) bool
	visit = func(n Node) bool {
		if s, ok := n.(*SetOp); ok && (s == top || (s.All && s.RTID == 0 && s.AppendRelName == "")) {
			return visit(s.Left) && visit(s.Right)
		}
		var path []*Project
		cur := n
		for len(path) < 2 {
			p, ok := cur.(*Project)
			if !ok {
				break
			}
			path = append(path, p)
			cur = p.Child
		}
		scan, ok := cur.(*SeqScan)
		if !ok || len(path) == 0 {
			return false
		}
		out = append(out, appendRelMember{attach: path[len(path)-1], scan: scan, path: path})
		return true
	}
	if !visit(top) {
		return nil, false
	}
	return out, true
}

// pushFilterIntoAppendRelMembers moves every translatable conjunct of f
// into each member of top.
func pushFilterIntoAppendRelMembers(f *Filter, top *SetOp) {
	if f.Predicate == nil {
		return
	}
	members, ok := appendRelMembers(top)
	if !ok || len(members) == 0 {
		return
	}
	conjuncts := splitAnd(f.Predicate)
	perMember := make([][]Expr, len(members))
	var kept []Expr
	for _, c := range conjuncts {
		if !appendRelConjunctPushable(c, top.Output()) {
			kept = append(kept, c)
			continue
		}
		translated := make([]Expr, len(members))
		ok := true
		for i, m := range members {
			e := c
			for _, p := range m.path {
				var tok bool
				if e, tok = substituteProjectTargets(e, p.Targets); !tok {
					ok = false
					break
				}
			}
			if !ok {
				break
			}
			translated[i] = e
		}
		if !ok {
			kept = append(kept, c)
			continue
		}
		for i := range members {
			perMember[i] = append(perMember[i], translated[i])
		}
	}
	if len(kept) == len(conjuncts) {
		return
	}
	for i, m := range members {
		if len(perMember[i]) == 0 {
			continue
		}
		m.attach.Child = &Filter{pos: f.pos, Child: m.scan, Predicate: combineAnd(perMember[i]), LeafLocal: true}
	}
	if len(kept) == 0 {
		f.Predicate = &BooleanConst{Value: true}
	} else {
		f.Predicate = combineAnd(kept)
	}
}

// appendRelConjunctPushable: only same-scope column references into the
// appendrel's output, no outer references, no sublinks. A reference must
// also name the output column it indexes: a Filter's references are not
// always local to its child (FROM-cumulative coordinates in a join tree),
// and a mismatch declines the conjunct rather than translate it wrongly.
func appendRelConjunctPushable(c Expr, out Schema) bool {
	if exprHasSublinkPlan(c) {
		return false
	}
	bad := false
	// The generic driver enumerates the children; only the two leaf kinds
	// that decide pushability are classified here.
	walkExprTree(c, func(x Expr) {
		if _, outer := x.(*OuterColumnRef); outer {
			bad = true
			return
		}
		if r, isRef := x.(*ColumnRef); isRef {
			if r.Index < 0 || r.Index >= len(out) ||
				(r.Name != "" && !strings.EqualFold(out[r.Index].Name, r.Name)) {
				bad = true
			}
		}
	})
	return !bad
}

// substituteProjectTargets rewrites e, an expression over a Project's
// output, into one over the Project's input by replacing each ColumnRef
// with a copy of the target it names (adjust_appendrel_attrs).
func substituteProjectTargets(e Expr, targets []Expr) (Expr, bool) {
	failed := false
	out, ok := cloneExprRefs(e, scopeVeto, exprRewriter{
		Rewrite: func(x Expr) Expr {
			cr, isRef := x.(*ColumnRef)
			if !isRef {
				return x
			}
			if cr.Index < 0 || cr.Index >= len(targets) {
				failed = true
				return x
			}
			t, tok := cloneExprRefs(targets[cr.Index], scopeVeto, exprRewriter{})
			if !tok {
				failed = true
				return x
			}
			return t
		},
	})
	if !ok || failed {
		return nil, false
	}
	return out, true
}
