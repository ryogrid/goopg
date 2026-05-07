package planner

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanQ21LiveSQL pins M0062-0005's Q21 unnesting end-to-end
// against the live HammerDB schema. The query has BOTH an EXISTS
// and a NOT EXISTS, each with mixed equi-/non-equi correlation.
// Expect at least one Semi/Anti join to appear in the plan tree.
func TestPlanQ21LiveSQL(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_commitdate", Type: catalog.Type{Name: "date"}},
		{Name: "l_receiptdate", Type: catalog.Type{Name: "date"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "s_name", Type: catalog.Type{Name: "varchar"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "orders"}, []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "o_orderstatus", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "nation"}, []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "n_name", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	q21 := `select s_name, count(*) as numwait from supplier, lineitem l1, orders, nation
where s_suppkey = l1.l_suppkey and o_orderkey = l1.l_orderkey
  and o_orderstatus = 'F' and l1.l_receiptdate > l1.l_commitdate
  and exists (select * from lineitem l2 where l2.l_orderkey = l1.l_orderkey and l2.l_suppkey <> l1.l_suppkey)
  and not exists (select * from lineitem l3 where l3.l_orderkey = l1.l_orderkey and l3.l_suppkey <> l1.l_suppkey and l3.l_receiptdate > l3.l_commitdate)
  and s_nationkey = n_nationkey and n_name = 'SAUDI ARABIA'
group by s_name order by numwait desc, s_name`
	node, err := Plan(parseOne(t, q21), c)
	if err != nil {
		t.Fatal(err)
	}
	s := planTreeString(node)
	t.Logf("plan:\n%s", s)
	if !strings.Contains(s, "SemiJoin") && !strings.Contains(s, "AntiJoin") {
		// At minimum the Semi (EXISTS) should fire. Use type-based check too.
		hasSemi := containsJoinType(node, JoinTypeSemi)
		hasAnti := containsJoinType(node, JoinTypeAnti)
		if !hasSemi && !hasAnti {
			t.Errorf("Q21: expected at least one Semi or Anti join, got none:\n%s", s)
		}
	}
	// Dump Semi/Anti predicates so we can verify Index rewriting.
	dumpPredicate(t, node, 0)
}

func dumpPredicate(t *testing.T, n Node, depth int) {
	if n == nil {
		return
	}
	indent := strings.Repeat("  ", depth)
	if j, ok := n.(*Join); ok && (j.Type == JoinTypeSemi || j.Type == JoinTypeAnti) {
		t.Logf("%sJoin(t=%d) leftWidth=%d Predicate=%s LeftKey=%s RightKey=%s",
			indent, j.Type, len(j.Left.Output()), exprDebug(j.Predicate), exprDebug(j.LeftKey), exprDebug(j.RightKey))
	}
	switch x := n.(type) {
	case *Join:
		dumpPredicate(t, x.Left, depth+1)
		dumpPredicate(t, x.Right, depth+1)
	case *Filter:
		t.Logf("%sFilter Predicate=%s", indent, exprDebug(x.Predicate))
		dumpPredicate(t, x.Child, depth+1)
	case *Project:
		dumpPredicate(t, x.Child, depth+1)
	case *Aggregate:
		dumpPredicate(t, x.Child, depth+1)
	case *Sort:
		dumpPredicate(t, x.Child, depth+1)
	}
}

func exprDebug(e Expr) string {
	if e == nil {
		return "<nil>"
	}
	switch x := e.(type) {
	case *ColumnRef:
		return strings.Join([]string{"ColRef[", x.Name, "/", "[", "]"}, "")
	case *BinaryOp:
		return "(" + exprDebug(x.Left) + " " + x.Op + " " + exprDebug(x.Right) + ")"
	case *BooleanConst:
		if x.Value {
			return "true"
		}
		return "false"
	}
	return "<expr>"
}

func containsJoinType(node Node, want JoinType) bool {
	if node == nil {
		return false
	}
	if j, ok := node.(*Join); ok && j.Type == want {
		return true
	}
	switch n := node.(type) {
	case *Join:
		return containsJoinType(n.Left, want) || containsJoinType(n.Right, want)
	case *Filter:
		return containsJoinType(n.Child, want)
	case *Project:
		return containsJoinType(n.Child, want)
	case *Aggregate:
		return containsJoinType(n.Child, want)
	case *Sort:
		return containsJoinType(n.Child, want)
	case *Limit:
		return containsJoinType(n.Child, want)
	case *MultiHashJoin:
		for _, t := range n.Tables {
			if containsJoinType(t, want) {
				return true
			}
		}
	}
	return false
}
