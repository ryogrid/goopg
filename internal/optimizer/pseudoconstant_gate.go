package optimizer

import (
	"github.com/goopg/goopg/internal/catalog"
)

// gatePseudoconstantQuals is PG's gating plan for pseudoconstant quals
// (create_gating_plan, postgres/src/backend/optimizer/plan/createplan.c): a
// WHERE conjunct that reads nothing from the current query level and calls no
// volatile function has the same value for every row. PG does not evaluate it
// per row; it evaluates it once as a Result node's `resconstantqual`
// (`One-Time Filter:`) placed above the scan or join the qual belonged to, and
// the Result emits nothing when the qual is false or NULL. An uncorrelated
// sublink in such a qual is an InitPlan, run once.
//
//	SELECT a FROM t1 WHERE a > 1 AND EXISTS (SELECT 1 FROM t3)
//
//	Result
//	  One-Time Filter: (InitPlan 1).col1
//	  InitPlan 1
//	    ->  Seq Scan on t3
//	  ->  Seq Scan on t1
//	        Filter: (a > 1)
//
// The pass runs over one scope's FROM/WHERE tree after the join search and the
// post-hoc passes. It lifts pseudoconstant conjuncts out of the Filter chain at
// the top of that tree (the WHERE residual) and gates the tree with one Result
// above it. That is PG's placement for a WHERE-level pseudoconstant: its
// qualscope is the whole jointree. A pseudoconstant names no relation, so the
// join search cannot attribute it to a leaf and it always reaches this
// residual. The pass does not descend into the searched tree: leaf Filters
// there carry path costs and boundary maps, and a pseudoconstant below an
// outer join must stay where it is.
//
// Admission is a fail-closed whitelist (isPseudoconstantConjunct): at least one
// uncorrelated sublink, and otherwise only constants and pure operators.
// References to an enclosing query level are excluded as well. PG treats them
// as Params and re-evaluates the gate on rescan, and goopg's Result evaluates
// its one-time filter at Open, so excluding them is the conservative choice
// (ledgered with M0145-0008o).
func gatePseudoconstantQuals(node Node, cat catalog.Catalog) Node {
	if node == nil {
		return node
	}
	var gates []Expr
	rewritten := liftPseudoconstantConjuncts(node, cat, &gates)
	if len(gates) == 0 {
		return node
	}
	schema := rewritten.Output()
	targets := make([]Expr, len(schema))
	for i, c := range schema {
		targets[i] = &ColumnRef{pos: node.Pos(), Index: i, Name: c.Name, Type: c.Type, SourceTableIdx: c.SourceTableIdx}
	}
	return &Result{
		pos:           node.Pos(),
		Targets:       targets,
		OneTimeFilter: combineAnd(gates),
		Child:         rewritten,
		schema:        schema,
	}
}

// liftPseudoconstantConjuncts removes the pseudoconstant conjuncts of the
// Filter chain at the top of n, appending them to gates, and returns the
// rewritten tree. A Filter left with no conjunct is spliced out.
func liftPseudoconstantConjuncts(n Node, cat catalog.Catalog, gates *[]Expr) Node {
	x, ok := n.(*Filter)
	if !ok {
		return n
	}
	var keep []Expr
	conjuncts := splitAnd(x.Predicate)
	for _, c := range conjuncts {
		if isPseudoconstantConjunct(c, cat) {
			*gates = append(*gates, c)
			continue
		}
		keep = append(keep, c)
	}
	child := liftPseudoconstantConjuncts(x.Child, cat, gates)
	if len(keep) == 0 {
		return child
	}
	if len(keep) == len(conjuncts) && child == x.Child {
		return x
	}
	cp := *x
	cp.Child = child
	cp.Predicate = combineAnd(keep)
	return &cp
}

// isPseudoconstantConjunct reports whether c is PG's pseudoconstant
// (is_pseudo_constant_clause: no Vars of the current level, no volatile
// functions) in the shape this pass admits: it contains at least one sublink,
// every sublink is uncorrelated (sublinkIsUncorrelated, binder-aware), and
// every other node is a constant or a pure operator from a known set. Any
// other node kind (a column, an outer-level reference, CTID, a whole-row
// reference, an unrecognised expression) makes it not pseudoconstant.
func isPseudoconstantConjunct(c Expr, cat catalog.Catalog) bool {
	sublinks := 0
	ok := true
	// scopeIgnore visits every same-scope slot, a sublink's operand included,
	// and does not descend into the sublink bodies, whose correlation is
	// decided by sublinkIsUncorrelated. An unrecognised node aborts the walk
	// (fail-closed).
	walked := walkExprRefs(c, scopeIgnore, exprVisitor{Visit: func(e Expr) bool {
		switch x := e.(type) {
		case *ExistsExpr:
			sublinks++
			if !sublinkIsUncorrelated(x.Plan, x.Args, x.ParParam) {
				ok = false
			}
		case *SubqueryExpr:
			sublinks++
			if !sublinkIsUncorrelated(x.Plan, x.Args, x.ParParam) {
				ok = false
			}
		case *InExpr:
			sublinks++
			if x.Plan == nil || !sublinkIsUncorrelated(x.Plan, x.Args, x.ParParam) {
				ok = false
			}
		case *BinaryOp, *UnaryOp, *BooleanConst, *IntegerConst, *NumericConst,
			*StringConst, *NullConst, *CastExpr, *IsNullExpr, *IsBoolExpr:
		default:
			ok = false
		}
		return ok
	}})
	if !walked || !ok || sublinks == 0 {
		return false
	}
	return !exprListHasVolatileBuiltin([]Expr{c}, cat)
}

// SublinkIsInitPlan reports whether e is a sublink PG plans as an InitPlan: a
// value computed once per statement and read as `(InitPlan N).col1`, rather
// than a SubPlan evaluated per row. PG makes every uncorrelated EXPR or EXISTS
// sublink an initplan param (make_subplan, subselect.c). EXPLAIN uses this for
// the plan name and its subtree label, so the two always agree.
func SublinkIsInitPlan(e Expr) bool {
	switch x := e.(type) {
	case *SubqueryExpr:
		return x.IsNonCorrelated
	case *ExistsExpr:
		return sublinkIsUncorrelated(x.Plan, x.Args, x.ParParam)
	}
	return false
}
