package planner

// M0125-0002 commit 4 pins for visitColumnRefsForTable — the same-scope
// index-collection surface, stated per kind.
//
// visitColumnRefsForTable has exactly one live consumer: tableForCol,
// which attributes a conjunct to the single FROM-table its ColumnRefs
// live in (or -1). tableForCol feeds partitionConjunctsForJoinPlanning
// (which conjuncts become leaf-locals, local_filters.go) and the
// join-edge left/right classification (bushy.go) — so a ColumnRef the
// old 12-of-32 hand switch silently skipped meant a conjunct read as
// "no table" and was stranded at the top Filter instead of being
// pushed. D2 row 4 of the design doc calls this walker a first-order
// shape mover for that reason.
//
// Three behaviours are pinned, mirroring visit_refs_arms_test.go:
//
//   - same-scope slots ARE visited, including two the old switch
//     excluded ENTIRELY: the Operand/Args of a Plan-carrying InExpr
//     (the old arm returned before visiting anything when Plan != nil)
//     and the PARAM_EXEC Args of ExistsExpr/SubqueryExpr (the old arm
//     skipped the whole node);
//   - inner-scope plans are NOT visited (a subplan's ColumnRef indices
//     live in the subplan's own coordinate space — attributing them to
//     an outer FROM-table via cumOffsets would be coordinate-space
//     confusion, RC-1a's mirror image), and an *OuterColumnRef's Index
//     is never handed to onIdx (it names a scope above);
//   - an unenumerated type panics instead of silently contributing
//     nothing (the old behaviour made the conjunct read as
//     single-table when its unknown part referenced another table).

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// visitForTableCollect runs visitColumnRefsForTable and returns every
// index onIdx was handed, in visit order.
func visitForTableCollect(e Expr) []int {
	var got []int
	visitColumnRefsForTable(e, func(idx int) {
		got = append(got, idx)
	})
	return got
}

func visitForTableSaw(e Expr, want int) bool {
	for _, idx := range visitForTableCollect(e) {
		if idx == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------
// Kinds the old 12-arm switch silently skipped: each must now be
// visited. One case per kind — covering BinaryOp proves nothing about
// CollateExpr (how the original hole survived).
// ---------------------------------------------------------------------

func TestVisitColumnRefsForTable_NewlyVisitedKinds(t *testing.T) {
	const refIdx = 41 // distinct from every helper index below
	mk := func() *ColumnRef { return &ColumnRef{Index: refIdx, Name: "c"} }

	type tc struct {
		name string
		expr func(*ColumnRef) Expr
	}
	cases := []tc{
		{"IsNullExpr", func(r *ColumnRef) Expr { return &IsNullExpr{Operand: r} }},
		{"IsBoolExpr", func(r *ColumnRef) Expr { return &IsBoolExpr{Operand: r} }},
		{"CollateExpr", func(r *ColumnRef) Expr { return &CollateExpr{Operand: r} }},
		{"CastExpr", func(r *ColumnRef) Expr { return &CastExpr{Operand: r} }},
		{"IsDistinctFromExpr/Left", func(r *ColumnRef) Expr {
			return &IsDistinctFromExpr{Left: r, Right: &IntegerConst{}}
		}},
		{"IsDistinctFromExpr/Right", func(r *ColumnRef) Expr {
			return &IsDistinctFromExpr{Left: &IntegerConst{}, Right: r}
		}},
		{"RowExpr", func(r *ColumnRef) Expr { return &RowExpr{Elems: []Expr{r}} }},
		// The old InExpr arm returned BEFORE visiting anything when
		// Plan != nil — so `col IN (subquery)` contributed no index and
		// tableForCol answered -1 for the whole conjunct even though
		// col itself is an ordinary same-scope reference.
		{"InExpr/Operand with Plan", func(r *ColumnRef) Expr {
			p, _, _ := innerPlanWithOuterRef(5, 7)
			return &InExpr{Operand: r, Plan: p}
		}},
		{"InExpr/Args", func(r *ColumnRef) Expr {
			p, _, _ := innerPlanWithOuterRef(5, 7)
			return &InExpr{Operand: &ColumnRef{Index: 0, Name: "o"}, Plan: p, Args: []Expr{r}}
		}},
		{"ExistsExpr/Args", func(r *ColumnRef) Expr {
			p, _, _ := innerPlanWithOuterRef(5, 7)
			return &ExistsExpr{Plan: p, Args: []Expr{r}}
		}},
		{"SubqueryExpr/Args", func(r *ColumnRef) Expr {
			p, _, _ := innerPlanWithOuterRef(5, 7)
			return &SubqueryExpr{Plan: p, Args: []Expr{r}}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !visitForTableSaw(c.expr(mk()), refIdx) {
				t.Errorf("visitColumnRefsForTable skipped the ColumnRef under %s; "+
					"tableForCol mis-partitions the conjunct (RC-1a)", c.name)
			}
		})
	}
}

// ---------------------------------------------------------------------
// The old arms must keep working through the conversion.
// ---------------------------------------------------------------------

func TestVisitColumnRefsForTable_PreservedArms(t *testing.T) {
	const refIdx = 41
	mk := func() *ColumnRef { return &ColumnRef{Index: refIdx, Name: "c"} }

	type tc struct {
		name string
		expr func(*ColumnRef) Expr
	}
	cases := []tc{
		{"root ColumnRef", func(r *ColumnRef) Expr { return r }},
		{"BinaryOp/Left", func(r *ColumnRef) Expr {
			return &BinaryOp{Op: parser.OpEq, Left: r, Right: &IntegerConst{}}
		}},
		{"BinaryOp/Right", func(r *ColumnRef) Expr {
			return &BinaryOp{Op: parser.OpEq, Left: &IntegerConst{}, Right: r}
		}},
		{"UnaryOp", func(r *ColumnRef) Expr { return &UnaryOp{Operand: r} }},
		{"FuncCall", func(r *ColumnRef) Expr { return &FuncCall{Args: []Expr{r}} }},
		{"ExtractExpr", func(r *ColumnRef) Expr { return &ExtractExpr{Source: r} }},
		{"CaseExpr/Operand", func(r *ColumnRef) Expr {
			return &CaseExpr{Operand: r, Whens: []CaseWhen{{When: &IntegerConst{}, Then: &IntegerConst{}}}}
		}},
		{"CaseExpr/When", func(r *ColumnRef) Expr {
			return &CaseExpr{Whens: []CaseWhen{{When: r, Then: &IntegerConst{}}}}
		}},
		{"CaseExpr/Then", func(r *ColumnRef) Expr {
			return &CaseExpr{Whens: []CaseWhen{{When: &IntegerConst{}, Then: r}}}
		}},
		{"CaseExpr/Else", func(r *ColumnRef) Expr {
			return &CaseExpr{Whens: []CaseWhen{{When: &IntegerConst{}, Then: &IntegerConst{}}}, Else: r}
		}},
		// The literal-list IN form was the old arm's carefully argued
		// descent (TPC-H Q12's l_shipmode IN ('MAIL','SHIP')) — keep it.
		{"InExpr/Operand no Plan", func(r *ColumnRef) Expr {
			return &InExpr{Operand: r, List: []Expr{&IntegerConst{}}}
		}},
		{"InExpr/List", func(r *ColumnRef) Expr {
			return &InExpr{Operand: &ColumnRef{Index: 0, Name: "o"}, List: []Expr{r}}
		}},
		{"nested depth", func(r *ColumnRef) Expr {
			return &BinaryOp{Op: parser.OpAnd,
				Left:  &IsNullExpr{Operand: &CastExpr{Operand: r}},
				Right: &IntegerConst{}}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !visitForTableSaw(c.expr(mk()), refIdx) {
				t.Errorf("visitColumnRefsForTable no longer visits %s", c.name)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Scope boundaries: inner plans are stepped over, outer refs never
// reach onIdx. cumOffsets attribution is only meaningful for indices
// in THIS scope's coordinate space.
// ---------------------------------------------------------------------

func TestVisitColumnRefsForTable_InnerScopeIsNotVisited(t *testing.T) {
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
			if visitForTableSaw(c.expr(p), innerRef.Index) {
				t.Errorf("visitColumnRefsForTable descended into %s; an inner-scope "+
					"index attributed via the outer cumOffsets is coordinate-space "+
					"confusion", c.name)
			}
		})
	}
}

func TestVisitColumnRefsForTable_OuterColumnRefIsNotVisited(t *testing.T) {
	outer := &OuterColumnRef{Level: 1, Index: 3, Name: "o"}
	if got := visitForTableCollect(&IsNullExpr{Operand: outer}); len(got) != 0 {
		t.Errorf("visitColumnRefsForTable handed an *OuterColumnRef index to onIdx "+
			"(got %v); it names a scope above this one", got)
	}
}

// ---------------------------------------------------------------------
// The semantic pin on the ONE live consumer: `col IN (subquery)` now
// attributes to col's table. Under the old walker the whole node was
// invisible, tableForCol returned -1, and the conjunct could never be
// relation-local even when its only same-scope reference is one
// column. This is commit 4's headline behaviour change — deliberate,
// per D2 row 4 — pinned here so a future "restore the old skip"
// regression is a test failure, not a silent plan move.
// ---------------------------------------------------------------------

func TestTableForCol_InSubqueryAttributesToOperandTable(t *testing.T) {
	p, _, _ := innerPlanWithOuterRef(5, 7)
	e := &InExpr{Operand: &ColumnRef{Index: 1, Name: "c"}, Plan: p}
	// Tables: t0 = cols [0,3), t1 = cols [3,5).
	cum := []int{0, 3, 5}
	if got := tableForCol(e, cum); got != 0 {
		t.Errorf("tableForCol(col-1 IN (subquery)) = %d, want 0 — the operand is "+
			"the conjunct's only same-scope reference", got)
	}
}

func TestTableForCol_SpanningTablesStaysMinusOne(t *testing.T) {
	e := &BinaryOp{Op: parser.OpEq,
		Left:  &ColumnRef{Index: 1, Name: "a"},
		Right: &ColumnRef{Index: 4, Name: "b"}}
	cum := []int{0, 3, 5}
	if got := tableForCol(e, cum); got != -1 {
		t.Errorf("tableForCol(join predicate) = %d, want -1", got)
	}
}

// ---------------------------------------------------------------------
// The `default:` this walker never had. An unenumerated type used to
// contribute nothing — the conjunct then read as belonging to whatever
// table its OTHER refs named, even if the unknown part referenced a
// different one. It now panics, matching PG's
// expression_tree_walker_impl, which closes with elog(ERROR,
// "unrecognized node type") (nodeFuncs.c:2667). Unreachable while
// exprwalk_exhaustive_test.go is green; this is the backstop.
// ---------------------------------------------------------------------

func TestVisitColumnRefsForTable_PanicsOnUnenumeratedType(t *testing.T) {
	cases := map[string]Expr{
		"root":   &unknownExpr{},
		"nested": &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 1, Name: "c"}, Right: &unknownExpr{}},
	}

	for name, expr := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("visitColumnRefsForTable silently accepted an unenumerated " +
						"Expr type; the conjunct's table attribution is then computed " +
						"from a partial reference set")
				}
				msg, ok := r.(string)
				if !ok || !strings.Contains(msg, "unknownExpr") {
					t.Fatalf("panic value %v does not name the offending type", r)
				}
			}()
			visitColumnRefsForTable(expr, func(int) {})
		})
	}
}
