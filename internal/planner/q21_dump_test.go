package planner

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanQ21FullDump dumps Q21's plan with column indices to
// diagnose the 0-rows residual mis-resolution. M0071-0003-followup.
func TestPlanQ21FullDump(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "s_name", Type: catalog.Type{Name: "varchar"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_commitdate", Type: catalog.Type{Name: "date"}},
		{Name: "l_receiptdate", Type: catalog.Type{Name: "date"}},
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
	t.Logf("Q21 plan tree:\n%s", planTreeString(node))

	visit(node, func(n Node) bool {
		switch x := n.(type) {
		case *Join:
			t.Logf("Join type=%d Algo=%v Left.Output=%v",
				x.Type, x.Algo, schemaNamesShort(x.Left.Output()))
			t.Logf("  Right.Output=%v", schemaNamesShort(x.Right.Output()))
			t.Logf("  Predicate=%s LeftKey=%s RightKey=%s",
				exprDebugIdx(x.Predicate), exprDebugIdx(x.LeftKey), exprDebugIdx(x.RightKey))
		case *NestedLoopIndexJoin:
			t.Logf("NLI type=%d Outer.Output=%v Inner.Output=%v",
				x.Type, schemaNamesShort(x.Outer.Output()), schemaNamesShort(x.Inner.Output()))
			t.Logf("  Predicate=%s Key=%s Keys=%v", exprDebugIdx(x.Predicate), exprDebugIdx(x.Inner.Key), len(x.Inner.Keys))
		case *Filter:
			t.Logf("Filter Predicate=%s child.Output=%v",
				exprDebugIdx(x.Predicate), schemaNamesShort(x.Child.Output()))
		case *MultiHashJoin:
			t.Logf("MHJ output=%v", schemaNamesShort(x.Output()))
		}
		return true
	})

	// Just a smoke test — no assertion failure. Diagnostic only.
	_ = strings.Contains
}
