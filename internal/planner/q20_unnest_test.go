package planner

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanQ20DumpsTree builds Q20's planned tree and logs it
// for diagnostic purposes (M0071-0002 follow-up). The Q20 0-rows
// regression is suspected to be a scalar-decorrelation × IN-unnest
// interaction at `unnestNonCorrelatedInExpr` — this test captures
// the plan tree shape so the SemiJoin's RightKey index, the
// scalar HashJoin's keys, and the inner Filter's predicate
// indices can be verified by reading the test log.
func TestPlanQ20DumpsTree(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "s_name", Type: catalog.Type{Name: "varchar"}},
		{Name: "s_address", Type: catalog.Type{Name: "varchar"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "nation"}, []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "n_name", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_availqty", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "p_name", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_quantity", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_shipdate", Type: catalog.Type{Name: "date"}},
	}); err != nil {
		t.Fatal(err)
	}
	q20 := `select s_name, s_address from supplier, nation where s_suppkey in ( select ps_suppkey from partsupp where ps_partkey in ( select p_partkey from part where p_name like 'forest%') and ps_availqty > ( select 0.5 * sum(l_quantity) from lineitem where l_partkey = ps_partkey and l_suppkey = ps_suppkey and l_shipdate >= date '1994-01-01' and l_shipdate < date '1994-01-01' + interval '1 year')) and s_nationkey = n_nationkey and n_name = 'CANADA' order by s_name`
	node, err := Plan(parseOne(t, q20), c)
	if err != nil {
		t.Fatal(err)
	}
	s := planTreeString(node)
	t.Logf("Q20 plan:\n%s", s)

	// Walk the inner-most join (HashJoin from scalar decorrelation
	// inside the IN's subquery) to dump its keys.
	visit(node, func(n Node) bool {
		if j, ok := n.(*Join); ok {
			t.Logf("Join type=%d Algo=%v outerWidth=%d innerWidth=%d schemaWidth=%d Predicate=%s LeftKey=%s RightKey=%s",
				j.Type, j.Algo, len(j.Left.Output()), len(j.Right.Output()), len(j.Output()),
				exprDebugIdx(j.Predicate), exprDebugIdx(j.LeftKey), exprDebugIdx(j.RightKey))
		}
		if f, ok := n.(*Filter); ok {
			t.Logf("Filter Predicate=%s child.Output=%v", exprDebugIdx(f.Predicate), schemaNamesShort(f.Child.Output()))
		}
		return true
	})

	// Sanity: must contain a SemiJoin (Type=5) for the outer IN.
	if !containsJoinType(node, JoinTypeSemi) {
		t.Errorf("Q20: expected outer SemiJoin from IN-unnest, got plan:\n%s", s)
	}
	// M0071-0002-followup: pin that the inner Filter's scalar
	// substitution preserves the Project's `0.5 *` multiplier (i.e.
	// the inner Filter's predicate is `ps_availqty > 0.5 * sum`,
	// not `ps_availqty > sum`).
	foundScalarFilter := false
	visit(node, func(n Node) bool {
		f, ok := n.(*Filter)
		if !ok {
			return true
		}
		s := exprDebugIdx(f.Predicate)
		if strings.Contains(s, "ps_availqty") && strings.Contains(s, "sum") {
			foundScalarFilter = true
			// The fix produces `(... * Col[sum/...])` not
			// just `Col[sum/...]`. Look for the BinaryOp.
			if !strings.Contains(s, "* Col[sum") {
				t.Errorf("Q20: scalar Filter predicate missing multiplier; got: %s", s)
			}
		}
		return true
	})
	if !foundScalarFilter {
		t.Errorf("Q20: scalar Filter (with ps_availqty + sum) not found in plan")
	}
}

// TestPlanQ20OuterInFullyUnnested guards the M-NIGHTLY
// tpch/Q20-timeout root cause: the OUTERMOST `s_suppkey IN (SELECT
// ps_suppkey FROM partsupp WHERE ...)` must be unnested into a
// SemiJoin, not left as a raw `*InExpr` inside a Filter predicate.
//
// `TestPlanQ20DumpsTree`'s `containsJoinType(node, JoinTypeSemi)`
// check does NOT catch a regression here: the MIDDLE-level
// `ps_partkey IN (SELECT p_partkey FROM part WHERE ...)` always
// unnests into its own SemiJoin regardless of whether the outer one
// does, so that assertion passes even when the outer IN is left
// un-unnested. This test walks every Filter predicate in the tree
// and fails if any live `*InExpr` remains — the exact signature
// EXPLAIN rendered as `Filter: (<*planner.InExpr> AND ...)` when the
// bug was live, which forced the executor to re-run the entire
// partsupp+lineitem-aggregate subtree once per supplier row (10000
// rows at TPC-H SF1) instead of building it once as a hash-join
// probe side. The root cause was `planHasOuterRef` (planner.go)
// treating ANY OuterColumnRef found inside a nested subquery as
// "escapes the tested plan", even when its Level (hop count) placed
// it squarely within the tested plan's own scope — see that
// function's doc comment for the corrected depth-aware semantics.
func TestPlanQ20OuterInFullyUnnested(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "s_name", Type: catalog.Type{Name: "varchar"}},
		{Name: "s_address", Type: catalog.Type{Name: "varchar"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "nation"}, []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "n_name", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_availqty", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "p_name", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_quantity", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_shipdate", Type: catalog.Type{Name: "date"}},
	}); err != nil {
		t.Fatal(err)
	}
	q20 := `select s_name, s_address from supplier, nation where s_suppkey in ( select ps_suppkey from partsupp where ps_partkey in ( select p_partkey from part where p_name like 'forest%') and ps_availqty > ( select 0.5 * sum(l_quantity) from lineitem where l_partkey = ps_partkey and l_suppkey = ps_suppkey and l_shipdate >= date '1994-01-01' and l_shipdate < date '1994-01-01' + interval '1 year')) and s_nationkey = n_nationkey and n_name = 'CANADA' order by s_name`
	node, err := Plan(parseOne(t, q20), c)
	if err != nil {
		t.Fatal(err)
	}
	s := planTreeString(node)

	foundRawInExpr := false
	visit(node, func(n Node) bool {
		f, ok := n.(*Filter)
		if !ok {
			return true
		}
		for _, conjunct := range splitAnd(f.Predicate) {
			if _, ok := conjunct.(*InExpr); ok {
				foundRawInExpr = true
			}
		}
		return true
	})
	if foundRawInExpr {
		t.Errorf("Q20: outer `s_suppkey IN (...)` was left as a raw InExpr (not unnested to a SemiJoin) — this forces per-outer-row re-execution of the partsupp+lineitem subtree; got plan:\n%s", s)
	}
}

func schemaNamesShort(s Schema) []string {
	out := make([]string, len(s))
	for i, c := range s {
		out[i] = c.Name
	}
	return out
}

// exprDebugIdx is exprDebug + ColumnRef indices (the q21 helper
// drops the Index, which hides the actual bug).
func exprDebugIdx(e Expr) string {
	if e == nil {
		return "<nil>"
	}
	switch x := e.(type) {
	case *ColumnRef:
		return "Col[" + x.Name + "/" + itoa(x.Index) + "]"
	case *OuterColumnRef:
		return "OuterCol[" + x.Name + "/" + itoa(x.Index) + "]"
	case *BinaryOp:
		return "(" + exprDebugIdx(x.Left) + " " + x.Op.String() + " " + exprDebugIdx(x.Right) + ")"
	case *UnaryOp:
		return "(" + x.Op.String() + exprDebugIdx(x.Operand) + ")"
	case *FuncCall:
		args := make([]string, len(x.Args))
		for i, a := range x.Args {
			args[i] = exprDebugIdx(a)
		}
		return x.Name + "(" + strings.Join(args, ",") + ")"
	case *BooleanConst:
		if x.Value {
			return "true"
		}
		return "false"
	case *NumericConst:
		return "Num{" + x.Value + "}"
	case *IntegerConst:
		return "Int{" + itoa(int(x.Value)) + "}"
	case *StringConst:
		return "Str{" + x.Value + "}"
	case *NullConst:
		return "Null"
	case *TypedStringLit:
		return "TypedStr{" + x.Value + "/" + x.Type + "}"
	case *IntervalLit:
		return "Interval"
	case *SubqueryExpr:
		return "Subq{}"
	case *InExpr:
		return "InExpr{}"
	}
	return "<expr>"
}

func itoa(i int) string {
	if i < 0 {
		return "-" + itoa(-i)
	}
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + itoa(i%10)
}
