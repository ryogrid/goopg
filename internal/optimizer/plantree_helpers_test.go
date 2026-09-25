package optimizer

import (
	"fmt"
)

// Plan-tree helpers shared across the optimizer tests. They lived in
// or_of_ands_test.go and small_dim_buildside_test.go, whose rule-arm tests
// were deleted with GOOPG_PGSHAPED_DP's kill-switch arm (M0145-0008).

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

// findFirstJoin returns the first *Join in the plan tree.
func findFirstJoin(n Node) *Join {
	if n == nil {
		return nil
	}
	switch x := n.(type) {
	case *Join:
		return x
	case *Project:
		return findFirstJoin(x.Child)
	case *Filter:
		return findFirstJoin(x.Child)
	case *Sort:
		return findFirstJoin(x.Child)
	case *Limit:
		return findFirstJoin(x.Child)
	case *Aggregate:
		return findFirstJoin(x.Child)
	}
	return nil
}
