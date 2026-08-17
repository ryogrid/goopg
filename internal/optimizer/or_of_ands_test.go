package optimizer

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanOrOfAndsExtractsJoinKey verifies that a Q19-shaped WHERE
// clause `(t1.k=t2.k AND ...) OR (t1.k=t2.k AND ...) OR ...`
// produces a Hash Join on `t1.k = t2.k` instead of a Cartesian
// `Nested Loop` (a CROSS join, which PG folds to JOIN_INNER).
// (M0058-0004.)
func TestPlanOrOfAndsExtractsJoinKey(t *testing.T) {
	// M0127-P5.9: a legacy-rule assertion; see useLegacyEnumerator.
	useLegacyEnumerator(t)
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int8"}, NotNull: true},
		{Name: "p_brand", Type: catalog.Type{Name: "varchar"}},
		{Name: "p_size", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int8"}, NotNull: true},
		{Name: "l_quantity", Type: catalog.Type{Name: "int4"}},
		{Name: "l_extendedprice", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	sql := `SELECT sum(l_extendedprice) FROM lineitem, part WHERE
		(p_partkey = l_partkey AND p_brand = 'B1' AND p_size = 1)
		OR (p_partkey = l_partkey AND p_brand = 'B2' AND p_size = 2)
		OR (p_partkey = l_partkey AND p_brand = 'B3' AND p_size = 3)`
	node, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatal(err)
	}
	if hasCross := containsCrossJoin(node); hasCross {
		t.Errorf("plan still contains CROSS JOIN; expected hash join on l_partkey=p_partkey\nplan: %s", planTreeString(node))
	}
	if !containsHashJoin(node) {
		t.Errorf("plan does not contain a Hash Join; expected one for Q19 shape\nplan: %s", planTreeString(node))
	}
}

func containsCrossJoin(n Node) bool {
	if n == nil {
		return false
	}
	if j, ok := n.(*Join); ok {
		if j.Type == JoinTypeCross {
			return true
		}
		if j.Algo == JoinAlgoNestedLoop && j.Predicate == nil {
			return true
		}
	}
	switch x := n.(type) {
	case *Join:
		return containsCrossJoin(x.Left) || containsCrossJoin(x.Right)
	case *Filter:
		return containsCrossJoin(x.Child)
	case *Project:
		return containsCrossJoin(x.Child)
	case *Aggregate:
		return containsCrossJoin(x.Child)
	case *Sort:
		return containsCrossJoin(x.Child)
	case *Limit:
		return containsCrossJoin(x.Child)
	}
	return false
}

func containsHashJoin(n Node) bool {
	if n == nil {
		return false
	}
	if j, ok := n.(*Join); ok && j.Algo == JoinAlgoHash {
		return true
	}
	switch x := n.(type) {
	case *Join:
		return containsHashJoin(x.Left) || containsHashJoin(x.Right)
	case *Filter:
		return containsHashJoin(x.Child)
	case *Project:
		return containsHashJoin(x.Child)
	case *Aggregate:
		return containsHashJoin(x.Child)
	case *Sort:
		return containsHashJoin(x.Child)
	case *Limit:
		return containsHashJoin(x.Child)
	}
	return false
}

func planTreeString(n Node) string {
	if n == nil {
		return "<nil>"
	}
	return walkPlanString(n, 0)
}

func walkPlanString(n Node, depth int) string {
	indent := ""
	for i := 0; i < depth; i++ {
		indent += "  "
	}
	switch x := n.(type) {
	case *Join:
		s := indent + fmt.Sprintf("Join(t=%d/a=%d)\n", x.Type, x.Algo)
		s += walkPlanString(x.Left, depth+1)
		s += walkPlanString(x.Right, depth+1)
		return s
	case *Filter:
		return indent + "Filter\n" + walkPlanString(x.Child, depth+1)
	case *Project:
		return indent + "Project\n" + walkPlanString(x.Child, depth+1)
	case *Aggregate:
		return indent + "Aggregate\n" + walkPlanString(x.Child, depth+1)
	case *Sort:
		return indent + "Sort\n" + walkPlanString(x.Child, depth+1)
	case *Limit:
		return indent + "Limit\n" + walkPlanString(x.Child, depth+1)
	case *SeqScan:
		return indent + "SeqScan(" + x.Table.Name + ")\n"
	case *IndexScan:
		return indent + "IndexScan(" + x.Table.Name + ")\n"
	}
	return indent + "<other>\n"
}
