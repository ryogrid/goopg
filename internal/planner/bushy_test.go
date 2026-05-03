package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func TestJoinGraphQ2(t *testing.T) {
	cat := catalog.NewInMemory()
	create := func(name string, cols []catalog.Column) {
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	create("part", []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int8"}},
		{Name: "p_name", Type: catalog.Type{Name: "text"}},
		{Name: "p_mfgr", Type: catalog.Type{Name: "text"}},
	})
	create("supplier", []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_name", Type: catalog.Type{Name: "text"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "int8"}},
	})
	create("partsupp", []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int8"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int8"}},
	})
	create("nation", []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "int8"}},
		{Name: "n_name", Type: catalog.Type{Name: "text"}},
		{Name: "n_regionkey", Type: catalog.Type{Name: "int8"}},
	})
	create("region", []catalog.Column{
		{Name: "r_regionkey", Type: catalog.Type{Name: "int8"}},
		{Name: "r_name", Type: catalog.Type{Name: "text"}},
	})

	q2Simplified := `select s_name from part, supplier, partsupp, nation, region
where p_partkey = ps_partkey
  and s_suppkey = ps_suppkey
  and s_nationkey = n_nationkey
  and n_regionkey = r_regionkey
  and r_name = 'EUROPE'`

	stmts, err := parser.Parse(q2Simplified)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Walk the plan and verify no CROSS joins in the outer query.
	// Since ANALYZE hasn't been run, bushy DP won't be active.
	// This test verifies the plan builds without error.
	if plan == nil {
		t.Fatal("plan is nil")
	}
}

func TestBushyDPWithStats(t *testing.T) {
	cat := catalog.NewInMemory()
	create := func(name string, cols []catalog.Column, rows int64) {
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows}
		tbl.Stats.Columns = make([]catalog.ColumnStats, len(cols))
		for i := range cols {
			tbl.Stats.Columns[i] = catalog.ColumnStats{NDistinct: rows}
		}
	}
	create("part", []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int8"}},
		{Name: "p_name", Type: catalog.Type{Name: "text"}},
		{Name: "p_mfgr", Type: catalog.Type{Name: "text"}},
	}, 10)
	create("supplier", []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_name", Type: catalog.Type{Name: "text"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_acctbal", Type: catalog.Type{Name: "text"}},
	}, 5)
	create("partsupp", []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int8"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int8"}},
	}, 20)
	create("nation", []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "int8"}},
		{Name: "n_name", Type: catalog.Type{Name: "text"}},
		{Name: "n_regionkey", Type: catalog.Type{Name: "int8"}},
	}, 3)
	create("region", []catalog.Column{
		{Name: "r_regionkey", Type: catalog.Type{Name: "int8"}},
		{Name: "r_name", Type: catalog.Type{Name: "text"}},
	}, 1)

	q2 := `select s_name from part, supplier, partsupp, nation, region
where p_partkey = ps_partkey
  and s_suppkey = ps_suppkey
  and s_nationkey = n_nationkey
  and n_regionkey = r_regionkey
  and r_name = 'EUROPE'`

	stmts, err := parser.Parse(q2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// With ANALYZE stats, the bushy DP should fire and eliminate
	// all CROSS joins in the connected component.
	if hasCrossJoin(plan) {
		t.Error("plan still contains CROSS joins — bushy DP did not eliminate them")
	}
}

func hasCrossJoin(node Node) bool {
	if node == nil {
		return false
	}
	if j, ok := node.(*Join); ok && j.Type == JoinTypeCross {
		return true
	}
	switch n := node.(type) {
	case *Join:
		return hasCrossJoin(n.Left) || hasCrossJoin(n.Right)
	case *Filter:
		return hasCrossJoin(n.Child)
	case *Project:
		return hasCrossJoin(n.Child)
	case *Sort:
		return hasCrossJoin(n.Child)
	case *Aggregate:
		return hasCrossJoin(n.Child)
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			if hasCrossJoin(tbl) { return true }
		}
	}
	return false
}

func TestBushyDPTwoComponents(t *testing.T) {
	// Two disconnected groups should have one CROSS join between them.
	cat := catalog.NewInMemory()
	create := func(name string, cols []catalog.Column, rows int64) {
		tbl, _ := cat.CreateTable(parser.ObjectName{Name: name}, cols)
		tbl.Stats = &catalog.TableStats{RowCount: rows}
	}
	create("a", []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int8"}},
		{Name: "val", Type: catalog.Type{Name: "text"}},
	}, 10)
	create("b", []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int8"}},
		{Name: "val", Type: catalog.Type{Name: "text"}},
	}, 5)
	create("c", []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int8"}},
		{Name: "val", Type: catalog.Type{Name: "text"}},
	}, 3)
	create("d", []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int8"}},
		{Name: "val", Type: catalog.Type{Name: "text"}},
	}, 1)

	// a and b are connected by a.id=b.id, c and d by c.id=d.id.
	// No edge between {a,b} and {c,d}.
	q := `select a.val from a, b, c, d where a.id = b.id and c.id = d.id`

	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// With stats on all 4 tables, the bushy DP should fire.
	// The two components {a,b} and {c,d} each get bushy plans,
	// and are CROSS-joined.
	// This verifies the fallback doesn't crash.
	_ = plan
}

func TestBushyPlanWithUnnest(t *testing.T) {
	// Verify that bushy DP (M0034) and subquery unnesting (M0033)
	// work together: the final Q2 plan has zero CROSS joins AND
	// zero SubqueryExpr nodes.
	cat := catalog.NewInMemory()
	create := func(name string, cols []catalog.Column, rows int64) {
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows}
		tbl.Stats.Columns = make([]catalog.ColumnStats, len(cols))
		for i := range cols {
			tbl.Stats.Columns[i] = catalog.ColumnStats{NDistinct: rows}
		}
	}
	create("part", []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int8"}},
		{Name: "p_name", Type: catalog.Type{Name: "text"}},
		{Name: "p_mfgr", Type: catalog.Type{Name: "text"}},
		{Name: "p_size", Type: catalog.Type{Name: "int8"}},
		{Name: "p_type", Type: catalog.Type{Name: "text"}},
	}, 10)
	create("supplier", []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_name", Type: catalog.Type{Name: "text"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_acctbal", Type: catalog.Type{Name: "text"}},
		{Name: "s_phone", Type: catalog.Type{Name: "text"}},
	}, 5)
	create("partsupp", []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int8"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "ps_supplycost", Type: catalog.Type{Name: "numeric"}},
	}, 20)
	create("nation", []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "int8"}},
		{Name: "n_name", Type: catalog.Type{Name: "text"}},
		{Name: "n_regionkey", Type: catalog.Type{Name: "int8"}},
	}, 3)
	create("region", []catalog.Column{
		{Name: "r_regionkey", Type: catalog.Type{Name: "int8"}},
		{Name: "r_name", Type: catalog.Type{Name: "text"}},
	}, 1)

	// Q2 with the correlated subquery.
	q2 := `select s_name from part, supplier, partsupp, nation, region
where p_partkey = ps_partkey
  and s_suppkey = ps_suppkey
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

	stmts, err := parser.Parse(q2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan == nil {
		t.Fatal("plan is nil")
	}

	// Verify no CROSS joins (bushy DP did its job).
	if hasCrossJoin(plan) {
		t.Error("plan still contains CROSS joins after bushy DP + unnest")
	}
	// Verify no SubqueryExpr remains (unnest did its job).
	if hasSubqueryExpr(plan) {
		t.Error("plan still contains SubqueryExpr after unnesting on bushy tree")
	}
	// Verify the aggregate from unnest is present.
	if !hasHashJoinWithAggregateChild(plan) {
		t.Error("plan should contain HashJoin with Aggregate child from unnest")
	}
}

func hasSubqueryExpr(node Node) bool {
	return findSubqueryInPlanRecursive(node)
}

func findSubqueryInPlanRecursive(node Node) bool {
	if node == nil {
		return false
	}
	if mh, ok := node.(*MultiHashJoin); ok {
		for _, tbl := range mh.Tables {
			if findSubqueryInPlanRecursive(tbl) {
				return true
			}
		}
		return false
	}
	return findSubqueryInPlan(node)
}

func hasHashJoinWithAggregateChild(node Node) bool {
	if node == nil {
		return false
	}
	if j, ok := node.(*Join); ok && j.Algo == JoinAlgoHash {
		if _, ok := j.Right.(*Aggregate); ok {
			return true
		}
		if _, ok := j.Left.(*Aggregate); ok {
			return true
		}
	}
	switch n := node.(type) {
	case *Join:
		return hasHashJoinWithAggregateChild(n.Left) || hasHashJoinWithAggregateChild(n.Right)
	case *Filter:
		return hasHashJoinWithAggregateChild(n.Child)
	case *Project:
		return hasHashJoinWithAggregateChild(n.Child)
	case *Sort:
		return hasHashJoinWithAggregateChild(n.Child)
	case *Aggregate:
		return hasHashJoinWithAggregateChild(n.Child)
	case *MultiHashJoin:
		return hasHashJoinWithAggregateChildInNode(n)
	}
	return false
}

func hasHashJoinWithAggregateChildInNode(n *MultiHashJoin) bool {
	for _, tbl := range n.Tables {
		if hasHashJoinWithAggregateChild(tbl) {
			return true
		}
	}
	return false
}

func TestBushyDPFallbackWithoutStats(t *testing.T) {
	cat := catalog.NewInMemory()
	create := func(name string, cols []catalog.Column) {
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	create("x", []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int8"}},
	})
	create("y", []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int8"}},
	})
	create("z", []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int8"}},
	})

	q := `select x.id from x, y, z where x.id = y.id and y.id = z.id`
	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan == nil {
		t.Fatal("plan is nil")
	}
}
