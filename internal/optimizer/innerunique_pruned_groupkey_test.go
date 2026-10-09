package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestMergeInnerUniqueNeedsPrunedGroupKeys pins M0146-0101. A grouped
// subquery is distinct only for a clause set that equates every item of its
// ORIGINAL GROUP BY: PG's rel_is_distinct_for reads the range table's
// subquery, whose groupClause still holds the keys that
// remove_useless_groupby_columns / processed_groupClause dropped. With
// `y1 = 1998` the year join clause becomes a constant restriction, and the
// planner prunes yr from the inner's group keys; the inner is then NOT
// proven unique, so final_cost_mergejoin elects materialize_inner and PG
// 18.3 prints `Materialize -> GroupAggregate` over the merge inner. Joined on
// all three keys, the inner is unique and stays bare.
func TestMergeInnerUniqueNeedsPrunedGroupKeys(t *testing.T) {
	cat := catalog.NewInMemory()
	int4 := catalog.Type{Name: "int4"}
	for _, n := range []string{"ft", "gt"} {
		if _, err := cat.CreateTable(parser.ObjectName{Name: n}, []catalog.Column{
			{Name: "yr", Type: int4}, {Name: "item", Type: int4}, {Name: "cust", Type: int4}, {Name: "qty", Type: int4},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ps := DefaultPlannerSettings()
	ps.EnableHashJoin = false
	ps.EnableNestLoop = false
	ps.EnableHashAgg = false
	const body = "SELECT * FROM (SELECT yr y1, item i1, cust c1, sum(qty) q1 FROM ft GROUP BY yr, item, cust) a " +
		"LEFT JOIN (SELECT yr y2, item i2, cust c2, sum(qty) q2 FROM gt GROUP BY yr, item, cust) b " +
		"ON (y2 = y1 AND i2 = i1 AND c2 = c1)"
	for _, tc := range []struct {
		name string
		sql  string
		want bool
	}{
		{"year pinned by a constant", body + " WHERE y1 = 1998", true},
		{"every group key joined", body, false},
	} {
		plan, err := PlanWithSettings(parseOne(t, tc.sql), cat, ps)
		if err != nil {
			t.Fatalf("%s: plan: %v", tc.name, err)
		}
		var mj *Join
		walkPlanNodes(plan, func(n Node) {
			if j, ok := n.(*Join); ok && j.Algo == JoinAlgoMerge && mj == nil {
				mj = j
			}
		})
		if mj == nil {
			t.Fatalf("%s: no merge join in the plan", tc.name)
		}
		_, got := mj.Right.(*Materialize)
		if got != tc.want {
			t.Errorf("%s: Materialize over the merge inner = %v, want %v (inner is %T)", tc.name, got, tc.want, mj.Right)
		}
	}
}
