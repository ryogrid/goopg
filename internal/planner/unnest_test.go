package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func TestCanUnnestSubqueryBasic(t *testing.T) {
	// Directly construct an unnestable SubqueryExpr.
	outerCol := &OuterColumnRef{pos: 0, Level: 1, Index: 0, Name: "p_partkey", Type: catalog.Type{Name: "int8"}}
	subCol := &ColumnRef{pos: 0, Index: 0, Name: "ps_partkey", Type: catalog.Type{Name: "int8"}}
	eqExpr := &BinaryOp{pos: 0, Op: parser.OpEq, Left: outerCol, Right: subCol}
	// Subquery plan: Aggregate over Filter over SeqScan
	agg := &Aggregate{
		pos: 0,
		Child: &Filter{
			pos:       0,
			Child:     &SeqScan{pos: 0, Table: &catalog.Table{Name: "partsupp"}},
			Predicate: eqExpr,
		},
		GroupExprs: nil,
		Aggs: []AggregateCall{
			{pos: 0, Name: "min", Arg: &ColumnRef{pos: 0, Index: 2, Name: "ps_supplycost", Type: catalog.Type{Name: "numeric"}}, Type: catalog.Type{Name: "numeric"}},
		},
	}
	sub := &SubqueryExpr{pos: 0, Plan: agg}

	if !canUnnestSubquery(sub) {
		t.Error("canUnnestSubquery returned false for unnestable subquery")
	}
	params := collectUnnestParams(agg)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if params[0].OuterRef != outerCol {
		t.Error("outer ref mismatch")
	}
	if params[0].SubCol != subCol {
		t.Error("sub col mismatch")
	}
}

func TestCanUnnestSubqueryWithExtraOuterRef(t *testing.T) {
	// Subquery with an OuterColumnRef in a non-equijoin context.
	outerEq := &OuterColumnRef{pos: 0, Level: 1, Index: 0, Name: "p_partkey", Type: catalog.Type{Name: "int8"}}
	subCol := &ColumnRef{pos: 0, Index: 0, Name: "ps_partkey", Type: catalog.Type{Name: "int8"}}
	outerExtra := &OuterColumnRef{pos: 0, Level: 1, Index: 1, Name: "p_size", Type: catalog.Type{Name: "int8"}}

	eqExpr := &BinaryOp{pos: 0, Op: parser.OpEq, Left: outerEq, Right: subCol}
	extraExpr := &BinaryOp{pos: 0, Op: parser.OpGt, Left: outerExtra, Right: &ColumnRef{pos: 0, Index: 3, Name: "ps_availqty", Type: catalog.Type{Name: "int8"}}}
	pred := &BinaryOp{pos: 0, Op: parser.OpAnd, Left: eqExpr, Right: extraExpr}

	agg := &Aggregate{
		pos: 0,
		Child: &Filter{
			pos:       0,
			Child:     &SeqScan{pos: 0, Table: &catalog.Table{Name: "partsupp"}},
			Predicate: pred,
		},
		Aggs: []AggregateCall{
			{pos: 0, Name: "min", Arg: &ColumnRef{pos: 0, Index: 2, Name: "ps_supplycost", Type: catalog.Type{Name: "numeric"}}, Type: catalog.Type{Name: "numeric"}},
		},
	}
	sub := &SubqueryExpr{pos: 0, Plan: agg}

	// S4a (D3.2): a non-equijoin outer ref is now a liftable residual
	// (the aggregate-above-join rewrite carries it on the join
	// predicate), so this shape is unnestable — the historical bail
	// this test used to pin was the pre-S4a limitation.
	if !canUnnestSubquery(sub) {
		t.Error("canUnnestSubquery returned false for a liftable residual correlation (D3.2)")
	}

	// The unliftable variant — the residual buried in an expression
	// kind the index rewriter does not model (CASE) — must still bail.
	casePred := &BinaryOp{pos: 0, Op: parser.OpAnd, Left: eqExpr, Right: &CaseExpr{
		pos: 0,
		Whens: []CaseWhen{{
			When: &BinaryOp{pos: 0, Op: parser.OpGt, Left: outerExtra2(), Right: &ColumnRef{pos: 0, Index: 3, Name: "ps_availqty", Type: catalog.Type{Name: "int8"}}},
			Then: &BooleanConst{pos: 0, Value: true},
		}},
		Else: &BooleanConst{pos: 0, Value: false},
	}}
	aggCase := &Aggregate{
		pos: 0,
		Child: &Filter{
			pos:       0,
			Child:     &SeqScan{pos: 0, Table: &catalog.Table{Name: "partsupp"}},
			Predicate: casePred,
		},
		Aggs: []AggregateCall{
			{pos: 0, Name: "min", Arg: &ColumnRef{pos: 0, Index: 2, Name: "ps_supplycost", Type: catalog.Type{Name: "numeric"}}, Type: catalog.Type{Name: "numeric"}},
		},
	}
	if canUnnestSubquery(&SubqueryExpr{pos: 0, Plan: aggCase}) {
		t.Error("canUnnestSubquery returned true for a CASE-buried (unliftable) correlation")
	}
}

func TestCanUnnestQ2Subquery(t *testing.T) {
	q2 := `select s_acctbal, s_name, n_name, p_partkey, p_mfgr
from part, supplier, partsupp, nation, region
where p_partkey = ps_partkey
  and s_suppkey = ps_suppkey
  and p_size = 15
  and s_nationkey = n_nationkey
  and n_regionkey = r_regionkey
  and r_name = 'EUROPE'
  and ps_supplycost = (
    select min(ps_supplycost)
    from partsupp, supplier, nation, region
    where p_partkey = ps_partkey
      and s_suppkey = ps_suppkey
      and s_nationkey = n_nationkey
      and n_regionkey = r_regionkey
      and r_name = 'EUROPE'
  )`

	cat := catalog.NewInMemory()
	for _, def := range []struct {
		name string
		cols []catalog.Column
	}{
		{"part", []catalog.Column{
			{Name: "p_partkey", Type: catalog.Type{Name: "int8"}},
			{Name: "p_name", Type: catalog.Type{Name: "text"}},
			{Name: "p_mfgr", Type: catalog.Type{Name: "text"}},
			{Name: "p_size", Type: catalog.Type{Name: "int8"}},
			{Name: "p_type", Type: catalog.Type{Name: "text"}},
		}},
		{"supplier", []catalog.Column{
			{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
			{Name: "s_name", Type: catalog.Type{Name: "text"}},
			{Name: "s_nationkey", Type: catalog.Type{Name: "int8"}},
			{Name: "s_acctbal", Type: catalog.Type{Name: "numeric"}},
		}},
		{"partsupp", []catalog.Column{
			{Name: "ps_partkey", Type: catalog.Type{Name: "int8"}},
			{Name: "ps_suppkey", Type: catalog.Type{Name: "int8"}},
			{Name: "ps_supplycost", Type: catalog.Type{Name: "numeric"}},
		}},
		{"nation", []catalog.Column{
			{Name: "n_nationkey", Type: catalog.Type{Name: "int8"}},
			{Name: "n_name", Type: catalog.Type{Name: "text"}},
			{Name: "n_regionkey", Type: catalog.Type{Name: "int8"}},
		}},
		{"region", []catalog.Column{
			{Name: "r_regionkey", Type: catalog.Type{Name: "int8"}},
			{Name: "r_name", Type: catalog.Type{Name: "text"}},
		}},
	} {
		if _, err := cat.CreateTable(parser.ObjectName{Name: def.name}, def.cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", def.name, err)
		}
	}

	stmts, err := parser.Parse(q2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Verify the plan does NOT contain a SubqueryExpr — it should
	// have been unnested into a HashJoin + Aggregate.
	if hasSubquery := findSubqueryInPlan(plan); hasSubquery {
		t.Error("plan still contains SubqueryExpr after unnesting pass")
	}

	// Verify the plan contains a HashJoin whose right child is an Aggregate.
	if !hasJoinWithAggregateChild(plan) {
		t.Error("plan does not contain a HashJoin + Aggregate (unnesting may not have fired)")
	}
}

func findSubqueryInPlan(node Node) bool {
	if node == nil {
		return false
	}
	switch n := node.(type) {
	case *Filter:
		if containsSubqueryExpr(n.Predicate) {
			return true
		}
		return findSubqueryInPlan(n.Child)
	case *Join:
		return findSubqueryInPlan(n.Left) || findSubqueryInPlan(n.Right)
	case *Project:
		for _, t := range n.Targets {
			if containsSubqueryExpr(t) {
				return true
			}
		}
		return findSubqueryInPlan(n.Child)
	case *Aggregate:
		return findSubqueryInPlan(n.Child)
	case *Sort:
		return findSubqueryInPlan(n.Child)
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			if findSubqueryInPlan(tbl) {
				return true
			}
		}
		return false
	}
	return false
}

func containsSubqueryExpr(e Expr) bool {
	if e == nil {
		return false
	}
	if _, ok := e.(*SubqueryExpr); ok {
		return true
	}
	switch x := e.(type) {
	case *BinaryOp:
		return containsSubqueryExpr(x.Left) || containsSubqueryExpr(x.Right)
	case *UnaryOp:
		return containsSubqueryExpr(x.Operand)
	case *FuncCall:
		for _, a := range x.Args {
			if containsSubqueryExpr(a) {
				return true
			}
		}
	case *CaseExpr:
		if x.Operand != nil && containsSubqueryExpr(x.Operand) {
			return true
		}
		for _, w := range x.Whens {
			if containsSubqueryExpr(w.When) || containsSubqueryExpr(w.Then) {
				return true
			}
		}
		if x.Else != nil && containsSubqueryExpr(x.Else) {
			return true
		}
	case *ExtractExpr:
		return containsSubqueryExpr(x.Source)
	}
	return false
}

func hasJoinWithAggregateChild(node Node) bool {
	if node == nil {
		return false
	}
	switch n := node.(type) {
	case *Join:
		if n.Algo == JoinAlgoHash {
			if _, ok := n.Right.(*Aggregate); ok {
				return true
			}
		}
		return hasJoinWithAggregateChild(n.Left) || hasJoinWithAggregateChild(n.Right)
	case *Filter:
		return hasJoinWithAggregateChild(n.Child)
	case *Project:
		return hasJoinWithAggregateChild(n.Child)
	case *Sort:
		return hasJoinWithAggregateChild(n.Child)
	case *Aggregate:
		return hasJoinWithAggregateChild(n.Child)
	}
	return false
}

func TestCannotUnnestNonEquijoinSubquery(t *testing.T) {
	q := `select s_name from supplier
where s_suppkey = (
  select max(ps_suppkey)
  from partsupp, nation
  where s_nationkey > n_nationkey
)`

	cat := catalog.NewInMemory()
	cat.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_name", Type: catalog.Type{Name: "text"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "int8"}},
	})
	cat.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int8"}},
	})
	cat.CreateTable(parser.ObjectName{Name: "nation"}, []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "int8"}},
	})

	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Non-equijoin correlation should NOT be unnested.
	if hasJoinWithAggregateChild(plan) {
		t.Error("non-equijoin subquery was incorrectly unnested")
	}
}

// TestRecursiveUnnestInsideNonUnnestableIN verifies M0040-0004:
// A correlated scalar subquery inside a non-unnestable IN expression's
// inner plan should be unnested by walkSubqueryPlansInExpr even when the
// outer IN itself cannot be pulled up (no equijoin between outer and
// the IN's inner plan). Q20-like structure: the lineitem aggregate inside
// the partsupp filter should become a HashJoin(b ⋈ Agg(c GROUP BY c_b_key)).
func TestRecursiveUnnestInsideNonUnnestableIN(t *testing.T) {
	// a(a_id int), b(b_id int, b_val numeric, b_key int), c(c_id int, c_b_key int, c_qty numeric)
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "a"}, []catalog.Column{
		{Name: "a_id", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "b"}, []catalog.Column{
		{Name: "b_id", Type: catalog.Type{Name: "int4"}},
		{Name: "b_val", Type: catalog.Type{Name: "numeric"}},
		{Name: "b_key", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "c"}, []catalog.Column{
		{Name: "c_id", Type: catalog.Type{Name: "int4"}},
		{Name: "c_b_key", Type: catalog.Type{Name: "int4"}},
		{Name: "c_qty", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Outer IN's inner plan is correlated with `a` through a
	// CASE-wrapped predicate, which residualExprLiftable refuses (an
	// unmodelled expression kind would evaluate stale indices if
	// lifted), so the outer IN genuinely stays a SubPlan. (The test
	// originally used a plain inequality correlation `b_val > a_id`,
	// but S4a/D3.2's residual lifting made that shape UNNESTABLE —
	// see TestInResidualLiftUnnests.) The inner scalar subquery IS
	// correlated with b (c_b_key = b_key) and must still be unnested
	// in place by walkSubqueryPlansInExpr — the M0040-0004 invariant
	// this test pins.
	sql := `SELECT a_id FROM a WHERE a_id + 1 IN (
		SELECT b_id FROM b
		WHERE CASE WHEN b_val > a_id THEN true ELSE false END
		  AND b_val > (SELECT SUM(c_qty) FROM c WHERE c_b_key = b_key)
	)`
	stmt := parseOne(t, sql)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// Navigate to the InExpr in the top-level Filter predicate.
	proj, ok := plan.(*Project)
	if !ok {
		t.Fatalf("root=%T, want *Project", plan)
	}
	f, ok := proj.Child.(*Filter)
	if !ok {
		t.Fatalf("Project.Child=%T, want *Filter", proj.Child)
	}
	in := findInExprInPred(f.Predicate)
	if in == nil {
		t.Fatal("no InExpr found in top-level Filter predicate")
	}

	// M0040-0004: InExpr.Plan should NOT have a SubqueryExpr —
	// the scalar subquery was unnested to a HashJoin.
	if findSubqueryInPlan(in.Plan) {
		t.Error("InExpr.Plan still contains SubqueryExpr; walkSubqueryPlansInExpr did not recurse")
	}
	// The unnested plan should contain a HashJoin with Aggregate child.
	if !hasJoinWithAggregateChild(in.Plan) {
		t.Error("InExpr.Plan should have HashJoin(b, Agg(c)) after scalar subquery unnesting")
	}
}

// findInExprInPred finds the first InExpr with a Plan != nil in an expression.
func findInExprInPred(e Expr) *InExpr {
	if e == nil {
		return nil
	}
	if in, ok := e.(*InExpr); ok && in.Plan != nil {
		return in
	}
	switch x := e.(type) {
	case *BinaryOp:
		if r := findInExprInPred(x.Left); r != nil {
			return r
		}
		return findInExprInPred(x.Right)
	case *UnaryOp:
		return findInExprInPred(x.Operand)
	}
	return nil
}

func TestCannotUnnestExistsExpr(t *testing.T) {
	q := `select s_name from supplier
where exists (
  select 1 from partsupp
  where s_suppkey = ps_suppkey
)`

	cat := catalog.NewInMemory()
	cat.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_name", Type: catalog.Type{Name: "text"}},
	})
	cat.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int8"}},
	})

	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// EXISTS subqueries are out of scope for v0 unnesting.
	if hasJoinWithAggregateChild(plan) {
		t.Error("EXISTS subquery was incorrectly unnested (v0 scope)")
	}
}

// outerExtra2 builds a fresh non-equijoin outer ref for the CASE
// variant in TestCanUnnestSubqueryWithExtraOuterRef (pointer identity
// matters to the collector's accounting maps).
func outerExtra2() *OuterColumnRef {
	return &OuterColumnRef{pos: 0, Level: 1, Index: 1, Name: "p_size", Type: catalog.Type{Name: "int8"}}
}

// TestInResidualLiftUnnests pins S4a/D3.2 item 2: a non-negated IN
// whose only correlation is a liftable non-equi residual now unnests
// to a semi join. The IN's own operand/projection equality supplies
// the hash key (there is no equijoin param), and the residual is
// AND-ed onto the join predicate — mirroring the EXISTS treatment.
func TestInResidualLiftUnnests(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z > t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in != nil {
		t.Errorf("InExpr survived residual-lift unnesting: %s", planString(node))
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("residual-only IN did not become a semi join: %s", planString(node))
	}
	if j.Algo != JoinAlgoHash {
		t.Errorf("IN semi join algo = %d, want JoinAlgoHash (operand equality is the key)", j.Algo)
	}
	if j.LeftKey == nil || j.RightKey == nil {
		t.Error("IN semi join must keep the operand/projection hash keys")
	}
	if j.Predicate == nil {
		t.Error("lifted residual missing from the IN semi join predicate")
	}
}

// TestNotInResidualStaysSubPlan pins the deliberate S4a non-goal: a
// correlated NOT IN with a lifted residual must stay a SubPlan. NOT IN
// carries three-valued NULL semantics (one NULL in the inner set makes
// every non-matching outer row UNKNOWN, i.e. filtered), which the anti
// join produced by residual lifting does not model. canUnnestInExprDepth
// bails on Negated+residuals; this test keeps that gate honest.
func TestNotInResidualStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x NOT IN (SELECT y FROM t2 WHERE z > t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in == nil {
		t.Fatalf("correlated NOT IN with residual must stay a SubPlan: %s", planString(node))
	}
	if findFirstJoinByType(node, JoinTypeAnti) != nil {
		t.Error("correlated NOT IN with residual was incorrectly converted to an anti join")
	}
}

// TestScalarTwoKeyCorrelationStripsTautology pins the S4a tautology
// strip in clonePlanReplacingOuter: a 2-key correlated scalar (Q20's
// shape) groups the cloned inner by both correlation columns, and the
// correlation conjuncts themselves become `col = col` after the
// outer→inner replacement. Those replacement-formed self-equalities
// must be dropped at clone time — Q20's decorrelated plan carried a
// visible `l_suppkey = l_suppkey` residue before the strip.
func TestScalarTwoKeyCorrelationStripsTautology(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "o"}, []catalog.Column{
		{Name: "o_k1", Type: catalog.Type{Name: "int8"}},
		{Name: "o_k2", Type: catalog.Type{Name: "int8"}},
		{Name: "o_val", Type: catalog.Type{Name: "int8"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "i"}, []catalog.Column{
		{Name: "i_k1", Type: catalog.Type{Name: "int8"}},
		{Name: "i_k2", Type: catalog.Type{Name: "int8"}},
		{Name: "i_qty", Type: catalog.Type{Name: "int8"}},
	}); err != nil {
		t.Fatal(err)
	}
	sql := `SELECT o_val FROM o WHERE o_val > (
		SELECT sum(i_qty) FROM i WHERE i_k1 = o_k1 AND i_k2 = o_k2)`
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if !hasJoinWithAggregateChild(node) {
		t.Fatalf("2-key correlated scalar did not unnest: %s", planString(node))
	}
	var tautology Expr
	walkPlanExprs(node, func(e Expr) {
		bin, ok := e.(*BinaryOp)
		if !ok || bin.Op != parser.OpEq {
			return
		}
		l, lok := bin.Left.(*ColumnRef)
		r, rok := bin.Right.(*ColumnRef)
		if lok && rok && l.Index == r.Index && l.Name == r.Name {
			tautology = e
		}
	})
	if tautology != nil {
		t.Errorf("replacement-formed self-equality survived the strip: %#v in %s",
			tautology, planString(node))
	}
}
