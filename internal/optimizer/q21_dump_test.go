package optimizer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestQ21AntiJoinPredicateSourceTableIdx pins M0071-0009's
// Q21 disambiguation: after planning Q21 against the live
// schema, the AntiJoin's residual predicate `l3.l_suppkey <>
// l1.l_suppkey` MUST have ColumnRef SourceTableIdx values
// that distinguish l3 (right side, inner anti-scope) from
// l1 (left side, outer scope's lineitem alias).
func TestQ21AntiJoinPredicateSourceTableIdx(t *testing.T) {
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
	// Walk and collect AntiJoin + SemiJoin predicates.
	var antiJoin, semiJoin *Join
	visit(node, func(n Node) bool {
		if j, ok := n.(*Join); ok {
			if j.Type == JoinTypeAnti && antiJoin == nil {
				antiJoin = j
			}
			if j.Type == JoinTypeSemi && semiJoin == nil {
				semiJoin = j
			}
		}
		return true
	})
	if semiJoin != nil {
		t.Logf("SemiJoin (EXISTS l2):\n%s", debugJoin(semiJoin))
	}
	if antiJoin == nil {
		t.Fatal("no AntiJoin (NOT EXISTS l3) found in Q21 plan")
	}
	t.Logf("AntiJoin (NOT EXISTS l3):\n%s", debugJoin(antiJoin))
	// Collect predicate ColumnRefs.
	pred := antiJoin.Predicate
	if pred == nil {
		t.Fatal("AntiJoin predicate is nil")
	}
	refs := collectColumnRefs(pred)
	t.Logf("AntiJoin predicate has %d ColumnRefs", len(refs))
	for i, cr := range refs {
		t.Logf("  cr[%d]: Name=%s Index=%d SourceTableIdx=%d", i, cr.Name, cr.Index, cr.SourceTableIdx)
	}
	// The two l_suppkey refs in `l3.l_suppkey <> l1.l_suppkey`
	// must have DIFFERENT SourceTableIdx values (l3 vs l1).
	suppkeyRefs := []*ColumnRef{}
	for _, cr := range refs {
		if cr.Name == "l_suppkey" {
			suppkeyRefs = append(suppkeyRefs, cr)
		}
	}
	if len(suppkeyRefs) < 2 {
		t.Fatalf("expected >= 2 l_suppkey refs in AntiJoin predicate, got %d", len(suppkeyRefs))
	}
	if suppkeyRefs[0].SourceTableIdx == suppkeyRefs[1].SourceTableIdx {
		t.Errorf("Q21 AntiJoin's two l_suppkey refs share SourceTableIdx=%d (should differ): l3 vs l1 self-join must disambiguate",
			suppkeyRefs[0].SourceTableIdx)
	}
}

func debugJoin(j *Join) string {
	var sb strings.Builder
	leftWidth := len(j.Left.Output())
	fmt.Fprintf(&sb, "  Type=%d leftWidth=%d\n", j.Type, leftWidth)
	fmt.Fprintf(&sb, "  Left.Output():\n")
	for i, c := range j.Left.Output() {
		fmt.Fprintf(&sb, "    [%d] Name=%s SourceTableIdx=%d\n", i, c.Name, c.SourceTableIdx)
	}
	fmt.Fprintf(&sb, "  Right.Output():\n")
	for i, c := range j.Right.Output() {
		fmt.Fprintf(&sb, "    [%d] Name=%s SourceTableIdx=%d\n", i+leftWidth, c.Name, c.SourceTableIdx)
	}
	if j.LeftKey != nil {
		fmt.Fprintf(&sb, "  LeftKey: %s\n", debugExpr(j.LeftKey))
	}
	if j.RightKey != nil {
		fmt.Fprintf(&sb, "  RightKey: %s\n", debugExpr(j.RightKey))
	}
	if j.Predicate != nil {
		fmt.Fprintf(&sb, "  Predicate: %s\n", debugExpr(j.Predicate))
	}
	return sb.String()
}

func debugExpr(e Expr) string {
	if e == nil {
		return "<nil>"
	}
	switch x := e.(type) {
	case *ColumnRef:
		return fmt.Sprintf("ColRef[%s/idx=%d/src=%d]", x.Name, x.Index, x.SourceTableIdx)
	case *OuterColumnRef:
		return fmt.Sprintf("OuterColRef[%s/idx=%d/lvl=%d/src=%d]", x.Name, x.Index, x.Level, x.SourceTableIdx)
	case *BinaryOp:
		return fmt.Sprintf("(%s %s %s)", debugExpr(x.Left), x.Op, debugExpr(x.Right))
	case *UnaryOp:
		return fmt.Sprintf("(%s %s)", x.Op, debugExpr(x.Operand))
	case *StringConst:
		return fmt.Sprintf("'%s'", x.Value)
	case *IntegerConst:
		return fmt.Sprintf("%d", x.Value)
	case *NumericConst:
		return x.Value
	case *FuncCall:
		var args []string
		for _, a := range x.Args {
			args = append(args, debugExpr(a))
		}
		return fmt.Sprintf("%s(%s)", x.Name, strings.Join(args, ", "))
	case *BooleanConst:
		if x.Value {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf("<%T>", e)
}

func collectColumnRefs(e Expr) []*ColumnRef {
	var out []*ColumnRef
	walkExprTree(e, func(node Expr) {
		if cr, ok := node.(*ColumnRef); ok {
			out = append(out, cr)
		}
	})
	return out
}
