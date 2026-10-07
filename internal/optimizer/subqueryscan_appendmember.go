package optimizer

// M0146-0093 — `Subquery Scan on "*SELECT* n"` over UNION ALL members that
// PG cannot pull up into the appendrel.
//
// pull_up_simple_union_all (prepjointree.c) turns every leaf of a simple
// UNION ALL in FROM into a subquery RTE named "*SELECT* n" (n counts the
// leaves left to right) and tries to pull each one up into the appendrel.
// pull_up_subqueries_recurse pulls a member only when is_simple_subquery
// holds AND is_safe_append_member does: the member's jointree must hold
// exactly one RTE and no WHERE quals (an appendrel has no place to put
// them). A join member, a member with a WHERE clause, or a grouped member
// therefore stays a subquery RTE, planned on its own and scanned by a
// SubqueryScan under the Append. create_append_plan plans its children with
// CP_EXACT_TLIST, so the scan's tlist is the appendrel's needed columns —
// the wrapper survives setrefs unless the parent reads every member column
// in order.
//
// goopg plans the members as the union's branches inside the derived leaf's
// own scope. The fold stamps each UNION ALL link with the member numbers of
// its unsafe sides and a shared appendRelLabel; the leaf's
// planSubqueryRangeVar fills the label with the appendrel's binding (its
// sourceIdx and column names), which is how the parent scope names the
// member's columns. At Plan()'s tail wrapAppendRelMembers adds the wrappers
// to the finished plan — after path selection, so the label never changes a
// path or a cost — and the strip pass decides each one from that consumption
// (stripTrivialSubqueryScans).

import (
	"fmt"

	"github.com/goopg/goopg/internal/parser"
)

// appendRelLabel is shared by every member wrapper of one appendrel. src and
// names are the appendrel leaf's binding in the parent scope: the
// coordinates the parent's ColumnRefs use for the member's columns. src 0
// means the binding was never recorded, and the wrapper is then stripped
// (the pre-M0146-0093 plan).
type appendRelLabel struct {
	src   int16
	names []string
}

// isSafeAppendMember is is_simple_subquery && is_safe_append_member for one
// UNION ALL member: no grouping, aggregation, window, DISTINCT, ORDER BY,
// LIMIT, set-returning target or locking at the member's level, no WHERE,
// and exactly one FROM item that is not a join (or no FROM at all). plan is
// the member's planned branch, read for the aggregate / window / SRF nodes
// the AST does not flag.
func isSafeAppendMember(s *parser.SelectStmt, plan Node) bool {
	if s == nil {
		return false
	}
	if len(s.GroupBy) > 0 || s.GroupingSets != nil || s.Having != nil ||
		len(s.OrderBy) > 0 || s.Distinct || len(s.DistinctOn) > 0 ||
		s.Limit != nil || s.Offset != nil || s.WithTies ||
		s.With != nil || len(s.Locking) > 0 || s.SetOpOperand != nil {
		return false
	}
	if s.Where != nil {
		return false
	}
	switch {
	case len(s.From) == 0 && len(s.FromExprs) == 0:
	case len(s.From) == 1 && (len(s.FromExprs) == 0 || (len(s.FromExprs) == 1 && len(s.FromExprs[0].Joins) == 0)):
	default:
		return false
	}
	return !subqueryPlanContains(plan, func(n Node) bool {
		switch n.(type) {
		case *Aggregate, *WindowAgg, *ProjectSet:
			return true
		}
		return false
	})
}

// wrapAppendRelMembers gives every stamped member arm of an appendrel's
// UNION ALL chain its "*SELECT* n" SubqueryScan, in place, on the finished
// plan (Plan()'s tail). The wrapper is a label only — PG's SubqueryScan over a
// member subquery RTE — so it is added after path selection and never priced:
// the member's own plan, partial or not, is exactly the one the search chose.
// In place, because the strip pass identifies derived subtrees by pointer.
// Each link's stamps are consumed as they are applied, so a tree reached
// again — a shared planned subtree, or a re-entrant Plan()'s output seen
// from its caller's tail — is not wrapped twice.
func wrapAppendRelMembers(root Node) {
	seen := map[Node]bool{}
	var visit func(n Node)
	visit = func(n Node) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		if so, ok := n.(*SetOp); ok && so.appendRel != nil {
			so.Left = wrapAppendRelArm(so.Left, so.appendMemberLeft, so.appendRel)
			so.Right = wrapAppendRelArm(so.Right, so.appendMemberRight, so.appendRel)
			// The stamps are consumed: a re-entrant Plan() (EXPLAIN's inner
			// statement, a view body) reaches this tree again from its
			// caller's tail, after the strip decided the wrappers, and must
			// not wrap the members a second time.
			so.appendMemberLeft, so.appendMemberRight = 0, 0
		}
		kids, _ := planChildNodes(n)
		for _, k := range kids {
			visit(k)
		}
	}
	visit(root)
}

// wrapAppendRelArm wraps one arm when its link stamped a member number for
// it; an arm that already is a member wrapper is left as it is.
func wrapAppendRelArm(arm Node, member int, label *appendRelLabel) Node {
	if arm == nil || member <= 0 {
		return arm
	}
	if sq, ok := arm.(*SubqueryScan); ok && sq.appendRel != nil {
		return arm
	}
	return &SubqueryScan{pos: arm.Pos(), Alias: fmt.Sprintf("*SELECT* %d", member), Child: arm,
		schema: arm.Output(), appendRel: label}
}

// recordAppendRelBinding fills the label of the appendrel leaf `inner` with
// the leaf's binding — its sourceIdx and column names, the coordinates the
// parent's ColumnRefs use. Every link of the chain, and every copy a
// lowering makes of it, shares the one label.
func recordAppendRelBinding(inner Node, src int16, schema Schema) {
	so := carrierSetOpNode(inner)
	if so == nil || so.appendRel == nil {
		return
	}
	names := make([]string, len(schema))
	for i, c := range schema {
		names[i] = c.Name
	}
	so.appendRel.src = src
	so.appendRel.names = names
}

// appendMemberUsedPositions is leafUsedPositions for a member wrapper: the
// member's columns are named by the appendrel binding the label records, so
// the region's references are mapped through it.
//
// It approximates the appendrel's reltarget — the columns its parent needs —
// more tightly than first reference: goopg's plans carry Projects that only
// narrow or reorder (the join search's leaf projection, upper narrowing
// between a join and an aggregate), and they list every column whatever the
// parent reads. The first Project reached from the region root is the level's
// final target list and counts; a deeper Project of bare columns is one of
// those pass-throughs and does not. A Project that computes anything counts
// wherever it sits.
func appendMemberUsedPositions(region Node, scan *SubqueryScan, isDerived map[Node]bool) ([]int, bool) {
	l := scan.appendRel
	if l == nil || l.src == 0 || len(l.names) != len(scan.Output()) {
		return nil, false
	}
	out := make(Schema, len(l.names))
	for i, nm := range l.names {
		out[i] = SchemaColumn{Name: nm}
	}
	var used []int
	seen := map[int]bool{}
	clean := true
	var visit func(n Node, belowProject bool)
	visit = func(n Node, belowProject bool) {
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
		pr, isProject := n.(*Project)
		if !(isProject && belowProject && projectIsBareColumns(pr)) {
			eachOwnExpr(n, func(e Expr) {
				cr, ok := e.(*ColumnRef)
				if !ok || cr.SourceTableIdx != l.src {
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
		}
		if _, isSetOp := n.(*SetOp); isSetOp {
			return
		}
		kids, _ := planChildNodes(n)
		for _, k := range kids {
			visit(k, belowProject || isProject)
		}
	}
	visit(region, false)
	return used, clean
}

// projectIsBareColumns reports whether p only passes columns through: every
// target is a bare column or references no column at all (the NULL
// placeholders a narrowing projection carries).
func projectIsBareColumns(p *Project) bool {
	if len(p.Targets) == 0 {
		return false
	}
	for _, t := range p.Targets {
		if _, ok := t.(*ColumnRef); ok {
			continue
		}
		refs := false
		walkExprTree(t, func(e Expr) {
			if _, ok := e.(*ColumnRef); ok {
				refs = true
			}
		})
		if refs {
			return false
		}
	}
	return true
}

// unionAllChainTypesDiffer reports whether any link of the UNION ALL chain
// rooted at so recorded differing branch types — PG then refuses the
// appendrel (is_simple_union_all_recurse), and the members are ordinary
// set-operation leaves whose trivial wrappers setrefs removes.
func unionAllChainTypesDiffer(so *SetOp) bool {
	for so != nil {
		if so.TlistTypesDiffer {
			return true
		}
		l, ok := so.Left.(*SetOp)
		if r, rok := so.Right.(*SetOp); rok && r.TlistTypesDiffer {
			return true
		}
		if !ok {
			return false
		}
		so = l
	}
	return false
}
