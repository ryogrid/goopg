package optimizer

// M0125-0002 commit 3 pins for visitColumnRefs — the same-scope visit
// surface, stated per kind.
//
// The defect class is "this specific type was never enumerated"
// (RC-1a): the old 7-of-32 hand switch silently skipped a ColumnRef
// sitting under IS NULL, a cast, a row constructor, an IN-list element
// or a subplan's PARAM_EXEC Args — so the three rebind call sites
// (reresolveExprByName, reresolveJoinByName's predRebind, the NLI
// leftover rebind at nl_index_join.go) left those refs at their
// pre-rewrite Index. EXPLAIN cannot see that: it prints predicates by
// Name over the (possibly stale) Index — the M0125-0042 lesson — which
// is why these pins are per-kind unit tests rather than a plan diff.
//
// Two behaviours are pinned, mirroring remap_arms_test.go:
//
//   - same-scope slots ARE visited (including a subquery node's Args,
//     which are evaluated against the CURRENT outer row and therefore
//     live in the parent's coordinate space);
//   - inner-scope plans are NOT visited (their ColumnRefs resolve in
//     the subplan's own coordinate space; rebinding them against the
//     outer child schema is the mirror-image wrong answer), and an
//     *OuterColumnRef is never handed to fn (it names a scope above).

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// visitRefsCollect runs visitColumnRefs and returns every *ColumnRef
// pointer fn was handed, so tests can assert identity, not just count.
func visitRefsCollect(e Expr) []*ColumnRef {
	var got []*ColumnRef
	visitColumnRefs(e, func(x Expr) {
		if cr, ok := x.(*ColumnRef); ok {
			got = append(got, cr)
		}
	})
	return got
}

func visitRefsSaw(e Expr, want *ColumnRef) bool {
	for _, cr := range visitRefsCollect(e) {
		if cr == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------
// Kinds the old 7-arm switch silently skipped: each must now be
// visited. One case per kind — a test that covers BinaryOp proves
// nothing about CollateExpr (how the original hole survived).
// ---------------------------------------------------------------------

func TestVisitColumnRefs_NewlyVisitedKinds(t *testing.T) {
	mk := func() *ColumnRef { return &ColumnRef{Index: 1, Name: "c"} }

	type tc struct {
		name string
		ref  *ColumnRef
		expr func(*ColumnRef) Expr
	}
	cases := []tc{
		{"IsNullExpr", mk(), func(r *ColumnRef) Expr { return &IsNullExpr{Operand: r} }},
		{"IsBoolExpr", mk(), func(r *ColumnRef) Expr { return &IsBoolExpr{Operand: r} }},
		{"CollateExpr", mk(), func(r *ColumnRef) Expr { return &CollateExpr{Operand: r} }},
		{"CastExpr", mk(), func(r *ColumnRef) Expr { return &CastExpr{Operand: r} }},
		{"IsDistinctFromExpr/Left", mk(), func(r *ColumnRef) Expr {
			return &IsDistinctFromExpr{Left: r, Right: &IntegerConst{}}
		}},
		{"IsDistinctFromExpr/Right", mk(), func(r *ColumnRef) Expr {
			return &IsDistinctFromExpr{Left: &IntegerConst{}, Right: r}
		}},
		{"RowExpr", mk(), func(r *ColumnRef) Expr { return &RowExpr{Elems: []Expr{r}} }},
		{"InExpr/List", mk(), func(r *ColumnRef) Expr {
			return &InExpr{Operand: &ColumnRef{Index: 0, Name: "o"}, List: []Expr{r}}
		}},
		{"InExpr/Args", mk(), func(r *ColumnRef) Expr {
			p, _, _ := innerPlanWithOuterRef(5, 7)
			return &InExpr{Operand: &ColumnRef{Index: 0, Name: "o"}, Plan: p, Args: []Expr{r}}
		}},
		{"ExistsExpr/Args", mk(), func(r *ColumnRef) Expr {
			p, _, _ := innerPlanWithOuterRef(5, 7)
			return &ExistsExpr{Plan: p, Args: []Expr{r}}
		}},
		{"SubqueryExpr/Args", mk(), func(r *ColumnRef) Expr {
			p, _, _ := innerPlanWithOuterRef(5, 7)
			return &SubqueryExpr{Plan: p, Args: []Expr{r}}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !visitRefsSaw(c.expr(c.ref), c.ref) {
				t.Errorf("visitColumnRefs skipped the ColumnRef under %s; the rebind "+
					"call sites leave its Index stale (RC-1a)", c.name)
			}
		})
	}
}

// ---------------------------------------------------------------------
// The old arms must keep working through the conversion.
// ---------------------------------------------------------------------

func TestVisitColumnRefs_PreservedArms(t *testing.T) {
	mk := func() *ColumnRef { return &ColumnRef{Index: 1, Name: "c"} }

	type tc struct {
		name string
		ref  *ColumnRef
		expr func(*ColumnRef) Expr
	}
	cases := []tc{
		{"root ColumnRef", mk(), func(r *ColumnRef) Expr { return r }},
		{"BinaryOp/Left", mk(), func(r *ColumnRef) Expr {
			return &BinaryOp{Op: parser.OpEq, Left: r, Right: &IntegerConst{}}
		}},
		{"BinaryOp/Right", mk(), func(r *ColumnRef) Expr {
			return &BinaryOp{Op: parser.OpEq, Left: &IntegerConst{}, Right: r}
		}},
		{"UnaryOp", mk(), func(r *ColumnRef) Expr { return &UnaryOp{Operand: r} }},
		{"FuncCall", mk(), func(r *ColumnRef) Expr { return &FuncCall{Args: []Expr{r}} }},
		{"ExtractExpr", mk(), func(r *ColumnRef) Expr { return &ExtractExpr{Source: r} }},
		{"CaseExpr/Operand", mk(), func(r *ColumnRef) Expr {
			return &CaseExpr{Operand: r, Whens: []CaseWhen{{When: &IntegerConst{}, Then: &IntegerConst{}}}}
		}},
		{"CaseExpr/When", mk(), func(r *ColumnRef) Expr {
			return &CaseExpr{Whens: []CaseWhen{{When: r, Then: &IntegerConst{}}}}
		}},
		{"CaseExpr/Then", mk(), func(r *ColumnRef) Expr {
			return &CaseExpr{Whens: []CaseWhen{{When: &IntegerConst{}, Then: r}}}
		}},
		{"CaseExpr/Else", mk(), func(r *ColumnRef) Expr {
			return &CaseExpr{Whens: []CaseWhen{{When: &IntegerConst{}, Then: &IntegerConst{}}}, Else: r}
		}},
		{"InExpr/Operand", mk(), func(r *ColumnRef) Expr {
			return &InExpr{Operand: r, List: []Expr{&IntegerConst{}}}
		}},
		{"nested depth", mk(), func(r *ColumnRef) Expr {
			return &BinaryOp{Op: parser.OpAnd,
				Left:  &IsNullExpr{Operand: &CastExpr{Operand: r}},
				Right: &IntegerConst{}}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !visitRefsSaw(c.expr(c.ref), c.ref) {
				t.Errorf("visitColumnRefs no longer visits %s", c.name)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Scope boundaries: inner plans are stepped over, outer refs are never
// handed to fn. These pin the "same-scope refs only" contract that
// reresolveExprByName's doc comment states in prose.
// ---------------------------------------------------------------------

func TestVisitColumnRefs_InnerScopeIsNotVisited(t *testing.T) {
	type tc struct {
		name string
		expr func(Node) Expr
	}
	cases := []tc{
		{"InExpr.Plan", func(p Node) Expr {
			return &InExpr{Operand: &ColumnRef{Index: 0, Name: "o"}, Plan: p}
		}},
		{"ExistsExpr.Plan", func(p Node) Expr { return &ExistsExpr{Plan: p} }},
		{"SubqueryExpr.Plan", func(p Node) Expr { return &SubqueryExpr{Plan: p} }},
		{"ArraySubqueryExpr.Plan", func(p Node) Expr { return &ArraySubqueryExpr{Plan: p} }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, _, innerRef := innerPlanWithOuterRef(5, 7)
			if visitRefsSaw(c.expr(p), innerRef) {
				t.Errorf("visitColumnRefs descended into %s; an inner-scope ColumnRef "+
					"rebound against the outer child schema is the mirror-image of RC-1a", c.name)
			}
		})
	}
}

func TestVisitColumnRefs_OuterColumnRefIsNotVisited(t *testing.T) {
	outer := &OuterColumnRef{Level: 1, Index: 3, Name: "o"}
	e := &BinaryOp{Op: parser.OpEq, Left: outer, Right: &ColumnRef{Index: 1, Name: "c"}}
	var sawOuter bool
	visitColumnRefs(e, func(x Expr) {
		if _, ok := x.(*OuterColumnRef); ok {
			sawOuter = true
		}
	})
	if sawOuter {
		t.Error("visitColumnRefs handed an *OuterColumnRef to fn; it names a scope " +
			"above this one and every caller would rebind it against the wrong schema")
	}
}

// ---------------------------------------------------------------------
// The `default:` this walker never had. An unenumerated type used to be
// a silent skip — a ref under it kept its stale Index (RC-1a). It now
// panics, matching PG's expression_tree_walker_impl, which closes with
// elog(ERROR, "unrecognized node type") (nodeFuncs.c:2667). Unreachable
// while exprwalk_exhaustive_test.go is green; this is the backstop.
// ---------------------------------------------------------------------

func TestVisitColumnRefs_PanicsOnUnenumeratedType(t *testing.T) {
	cases := map[string]Expr{
		"root":   &unknownExpr{},
		"nested": &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 1, Name: "c"}, Right: &unknownExpr{}},
	}

	for name, expr := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("visitColumnRefs silently accepted an unenumerated Expr type; " +
						"skipping it leaves a stale column index behind every rebind site")
				}
				msg, ok := r.(string)
				if !ok || !strings.Contains(msg, "unknownExpr") {
					t.Fatalf("panic value %v does not name the offending type", r)
				}
			}()
			visitColumnRefs(expr, func(Expr) {})
		})
	}
}
