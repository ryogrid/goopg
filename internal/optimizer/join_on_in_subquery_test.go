package optimizer

// M0134-0011c: IN (subquery) inside a JOIN ... ON clause.
//
// Root cause (docs/design/m0134-0011-join-on-sublink-catalog.md): the
// per-join resolve contexts planFromItem builds (leftCtx/rightCtx/mergedCtx)
// were constructed via newResolveContext, which never sets `.cat`. The ON
// clause resolves against mergedCtx (planJoinPredicate -> resolveExpr), so
// planInExpr's `ctx.cat == nil` guard (planner.go, "IN (subquery) not
// supported in this context", SQLSTATE 0A000) fired for every ON-clause
// sublink even though the identical sublink works fine in WHERE / the
// target list, where the TOP-LEVEL context's `.cat` gets patched up (but
// only AFTER every join's ON clause has already been resolved).
//
// PG oracle: postgres/src/backend/parser/parse_clause.c:365
// transformJoinOnClause resolves the ON clause against the full namespace,
// sublinks included; the semijoin pull-up (pull_up_sublinks) that PG may
// additionally apply is an explicit non-goal here — goopg plans these as a
// SubPlan in the join qual instead.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// joinOnInSubqueryCatalog seeds tenk1(hundred)/tenk2(hundred, odd), the
// smallest shape that reproduces the subselect.sql statements this bug
// blocks (`tenk1 a ... JOIN tenk2 b ON a.hundred IN (SELECT ... FROM tenk2
// c ...)`).
func joinOnInSubqueryCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "tenk1"}, []catalog.Column{
		{Name: "hundred", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "tenk2"}, []catalog.Column{
		{Name: "hundred", Type: catalog.Type{Name: "int4"}},
		{Name: "odd", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// planErrorCode extracts the SQLSTATE from a *PlanError, "" otherwise.
func planErrorCode(err error) string {
	if pe, ok := err.(*PlanError); ok {
		return pe.Code
	}
	return ""
}

// TestJoinOnInSubquery_Uncorrelated: acceptance 2a. The uncorrelated shape
// must plan cleanly now, and the ON clause's predicate must carry the
// sublink as a SubPlan (findInExpr digs an *InExpr with a non-nil Plan out
// of the whole plan tree, join predicate included via walkPlanExprs).
func TestJoinOnInSubquery_Uncorrelated(t *testing.T) {
	cat := joinOnInSubqueryCatalog(t)
	sql := "SELECT * FROM tenk1 a LEFT JOIN tenk2 b ON a.hundred IN (SELECT c.hundred FROM tenk2 c)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatalf("Plan() failed for uncorrelated IN in JOIN...ON: %v", err)
	}
	if in := findInExpr(node); in == nil {
		t.Fatalf("no SubPlan InExpr found in plan:\n%s", planString(node))
	}
}

// TestJoinOnInSubquery_CorrelatedToJoinRightSide: acceptance 2b, the risky
// one. The sublink correlates to `b`, the outer LEFT JOIN's own RIGHT side
// — a reference that only resolves because mergedCtx (which now carries
// .cat) is passed as the subquery's lexical parent. Per the brief, this may
// surface a pre-existing executor bug ("SubPlan parameter $0 read before
// assignment", bucket 4 of the design doc) — this test only exercises
// PLANNING, not execution, so it pins that resolution/planning succeeds;
// it does not claim the plan executes correctly.
func TestJoinOnInSubquery_CorrelatedToJoinRightSide(t *testing.T) {
	cat := joinOnInSubqueryCatalog(t)
	sql := "SELECT * FROM tenk1 a LEFT JOIN tenk2 b " +
		"ON a.hundred IN (SELECT c.hundred FROM tenk2 c WHERE c.odd = b.odd)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatalf("Plan() failed for JOIN...ON IN (subquery) correlated to the join's right side: %v", err)
	}
	if in := findInExpr(node); in == nil {
		t.Fatalf("no SubPlan InExpr found in plan:\n%s", planString(node))
	}
}

// TestJoinOnInSubquery_InnerJoin: acceptance 2c, INNER JOIN variant.
func TestJoinOnInSubquery_InnerJoin(t *testing.T) {
	cat := joinOnInSubqueryCatalog(t)
	sql := "SELECT * FROM tenk1 a JOIN tenk2 b ON a.hundred IN (SELECT c.hundred FROM tenk2 c)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatalf("Plan() failed for INNER JOIN...ON IN (subquery): %v", err)
	}
	if in := findInExpr(node); in == nil {
		t.Fatalf("no SubPlan InExpr found in plan:\n%s", planString(node))
	}
}

// TestJoinOnNotInSubquery: acceptance 2c, NOT IN variant.
func TestJoinOnNotInSubquery(t *testing.T) {
	cat := joinOnInSubqueryCatalog(t)
	sql := "SELECT * FROM tenk1 a LEFT JOIN tenk2 b ON a.hundred NOT IN (SELECT c.hundred FROM tenk2 c)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatalf("Plan() failed for JOIN...ON NOT IN (subquery): %v", err)
	}
	in := findInExpr(node)
	if in == nil {
		t.Fatalf("no SubPlan InExpr found in plan:\n%s", planString(node))
	}
	if !in.Negated {
		t.Error("expected InExpr.Negated=true for NOT IN")
	}
}

// TestPlanInExpr_CatFreeContextStillErrors is the acceptance-2d negative
// control: a resolveContext with no catalog must still hit the 0A000 guard
// in planInExpr. Without this, a future refactor could thread `.cat`
// through so pervasively that the guard silently becomes dead code.
func TestPlanInExpr_CatFreeContextStillErrors(t *testing.T) {
	ctx := &resolveContext{} // deliberately no .cat — mirrors a bare-expression context
	x := &parser.InExpr{
		Operand:  &parser.IntegerConst{Value: 1},
		Subquery: &parser.SelectStmt{ValuesRows: nil, Targets: []parser.ResTarget{{Expr: &parser.IntegerConst{Value: 1}}}},
	}
	_, err := planInExpr(x, ctx)
	if err == nil {
		t.Fatal("expected 0A000 error for a catalog-free resolve context, got nil")
	}
	if code := planErrorCode(err); code != "0A000" {
		t.Fatalf("expected SQLSTATE 0A000, got %q (%v)", code, err)
	}
}
