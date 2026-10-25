package optimizer

import (
	"testing"
)

// M0122-0012 slice 91: the lowering walk treats a CTE scan as a leaf when the
// CTE body is self-contained (selfContainedPlan), so a correlated sublink
// over a CTE scan — TPC-DS Q1/Q30/Q81's `ctr1.k = ctr2.k` filter — is lowered
// to PARAM_EXEC slots and its result cache keys on the bound values instead
// of the whole outer row. The body itself is never rewritten.

// findCTEScan returns the first CTEScan under n (plan children only).
func findCTEScan(n Node) *CTEScan {
	if n == nil {
		return nil
	}
	if c, ok := n.(*CTEScan); ok {
		return c
	}
	for _, ch := range lowerNodeChildren(n) {
		if c := findCTEScan(ch); c != nil {
			return c
		}
	}
	switch x := n.(type) {
	case *Filter:
		return findCTEScan(x.Child)
	case *Aggregate:
		return findCTEScan(x.Child)
	case *Project:
		return findCTEScan(x.Child)
	}
	return nil
}

func TestLowerScalarSublinkOverCTEScan(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "WITH c AS MATERIALIZED (SELECT y, z FROM t2) " +
		"SELECT x FROM t1 WHERE x = -1 OR x > (SELECT max(z) FROM c WHERE c.y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	sq := findSubqueryExprIn(node)
	if sq == nil {
		t.Fatalf("probe lost its SubqueryExpr:\n%s", planString(node))
	}
	scan := findCTEScan(sq.Plan)
	if scan == nil {
		t.Fatalf("the sublink plan reads no CTE scan; the fixture does not exercise the arm:\n%s", planString(sq.Plan))
	}
	if len(sq.ParParam) != 1 || len(sq.Args) != 1 {
		t.Fatalf("ParParam/Args = %v/%v, want one slot: a sublink over a self-contained CTE scan is lowered", sq.ParParam, sq.Args)
	}
	if arg, ok := sq.Args[0].(*ColumnRef); !ok || arg.Name != "x" {
		t.Fatalf("Args[0] = %#v, want the host's x", sq.Args[0])
	}
	// No outer ref survives outside the body, and the body is untouched:
	// no ExecParamRef was written into it.
	var outer, params int
	walkPlanExprs(scan.Child, func(e Expr) {
		switch e.(type) {
		case *OuterColumnRef:
			outer++
		case *ExecParamRef:
			params++
		}
	})
	if params != 0 {
		t.Fatalf("the CTE body (shared by every reference) was rewritten: %d ExecParamRefs", params)
	}
	if outer != 0 {
		t.Fatalf("the CTE body grew %d outer refs", outer)
	}
}

func TestSublinkOverCorrelatedCTEStaysOnStack(t *testing.T) {
	cat := threeTablesCatalog(t)
	// The CTE is defined inside the sublink and its body reads the outer
	// row: the body is not self-contained, so the sublink is not lowered.
	sql := "SELECT x FROM t1 WHERE x = -1 OR x > (WITH c AS MATERIALIZED (SELECT z FROM t2 WHERE t2.y = t1.x) " +
		"SELECT max(z) FROM c)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Skipf("planner does not accept a correlated CTE body here: %v", err)
	}
	sq := findSubqueryExprIn(node)
	if sq == nil || findCTEScan(sq.Plan) == nil {
		t.Skipf("fixture planned without a sublink over a CTE scan:\n%s", planString(node))
	}
	// Lowered, it would carry no params (its refs are all in the body),
	// and the pass would then mark it non-correlated — one cached result
	// for every outer row.
	if len(sq.ParParam) != 0 || sq.IsNonCorrelated {
		t.Fatalf("a sublink over a CTE whose body reads the outer row was lowered (ParParam %v, non-correlated %v)", sq.ParParam, sq.IsNonCorrelated)
	}
}

func TestSelfContainedPlan(t *testing.T) {
	scan := func() Node { return &SeqScan{Table: nil} }
	outer := func(level int) Expr { return &OuterColumnRef{Level: level, Index: 0, Name: "k"} }
	filter := func(pred Expr, child Node) Node { return &Filter{Predicate: pred, Child: child} }
	for name, c := range map[string]struct {
		plan   Node
		frames int
		want   bool
	}{
		"no refs":                    {filter(&BinaryOp{Left: &ColumnRef{Index: 0}, Right: &IntegerConst{Value: 1}}, scan()), 0, true},
		"outer ref past the body":    {filter(outer(1), scan()), 0, false},
		"outer ref within frames":    {filter(outer(1), scan()), 1, true},
		"lateral right, local param": {&Join{Lateral: true, Left: scan(), Right: filter(outer(1), scan())}, 0, true},
		"lateral right, escaping":    {&Join{Lateral: true, Left: scan(), Right: filter(outer(2), scan())}, 0, false},
		"plain join right ref":       {&Join{Left: scan(), Right: filter(outer(1), scan())}, 0, false},
		"exec param":                 {filter(&ExecParamRef{ID: 0}, scan()), 0, false},
		"nested sublink, local ref":  {filter(&SubqueryExpr{Plan: filter(outer(1), scan())}, scan()), 0, true},
		"nested sublink, escaping":   {filter(&SubqueryExpr{Plan: filter(outer(2), scan())}, scan()), 0, false},
		"unmodelled node":            {&WindowAgg{Child: scan()}, 0, false},
	} {
		if got := selfContainedPlan(c.plan, c.frames); got != c.want {
			t.Fatalf("%s: selfContainedPlan = %v, want %v", name, got, c.want)
		}
	}
}
