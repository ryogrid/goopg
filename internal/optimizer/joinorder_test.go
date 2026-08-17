package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestReorderCommaFromByCardinality pins the greedy nearest-neighbour
// rewrite. Setup mirrors a 6-table TPC-H Q5-shaped FROM list:
// customer (1500), orders (15000), lineitem (60000), supplier (100),
// nation (25), region (5). With a fully-connected equality graph,
// greedy NN seeded at region (smallest) walks
// region → nation → supplier → lineitem → orders → customer.
func TestReorderCommaFromByCardinality(t *testing.T) {
	c := tpchStatsCatalog(t)
	sql := `select 1 from customer, orders, lineitem, supplier, nation, region
	         where c_custkey = o_custkey
	           and l_orderkey = o_orderkey
	           and l_suppkey = s_suppkey
	           and s_nationkey = n_nationkey
	           and n_regionkey = r_regionkey`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	newFE, newFR, rewrote := reorderCommaFromByCardinality(stmt, c)
	if !rewrote {
		t.Fatalf("expected rewrite to apply; got identity")
	}
	if len(newFE) != 6 || len(newFR) != 6 {
		t.Fatalf("expected 6-entry permutations; got %d/%d", len(newFE), len(newFR))
	}
	want := []string{"region", "nation", "supplier", "lineitem", "orders", "customer"}
	for i, name := range want {
		if newFR[i].Name != name {
			t.Errorf("position %d: got %q, want %q", i, newFR[i].Name, name)
		}
	}
}

// TestReorderCommaFromByCardinalityNoStats: without ANALYZE,
// reorder is a no-op so the planner falls back to source order.
func TestReorderCommaFromByCardinalityNoStats(t *testing.T) {
	c := tpchUnstatsCatalog(t)
	sql := `select 1 from customer, orders, lineitem where c_custkey = o_custkey and l_orderkey = o_orderkey`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	if _, _, rewrote := reorderCommaFromByCardinality(stmt, c); rewrote {
		t.Fatalf("expected no rewrite without stats")
	}
}

// TestReorderCommaFromByCardinalitySkipsExplicitJoin: explicit
// JOIN ... ON syntax must preserve the user's stated order.
func TestReorderCommaFromByCardinalitySkipsExplicitJoin(t *testing.T) {
	c := tpchStatsCatalog(t)
	sql := `select 1 from customer c
	         join orders o on c.c_custkey = o.o_custkey
	         join lineitem l on l.l_orderkey = o.o_orderkey`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	if _, _, rewrote := reorderCommaFromByCardinality(stmt, c); rewrote {
		t.Fatalf("explicit JOIN-ON must not be reordered")
	}
}

// tpchStatsCatalog builds a catalog identical to the shared
// internal/testutil/tpch.Catalog() but with row-count statistics
// populated. Plan-time tests in this package can't import the
// shared fixture (testutil/tpch lives outside the planner-test
// world for cycle-avoidance reasons) so the helper duplicates
// the table list locally with only the two tables each query
// uses populated.
func tpchStatsCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, rows int64, cols []catalog.Column) {
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatalf("CreateTable %s: %v", name, err)
		}
		c.SetTableStats(tbl, &catalog.TableStats{RowCount: rows})
	}
	mk("region", 5, []catalog.Column{
		{Name: "r_regionkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "r_name", Type: catalog.Type{Name: "char", Args: []int64{25}}},
	})
	mk("nation", 25, []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "n_name", Type: catalog.Type{Name: "char", Args: []int64{25}}},
		{Name: "n_regionkey", Type: catalog.Type{Name: "numeric"}},
	})
	mk("supplier", 100, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "numeric"}},
	})
	mk("customer", 1500, []catalog.Column{
		{Name: "c_custkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "c_nationkey", Type: catalog.Type{Name: "numeric"}},
	})
	mk("orders", 15000, []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "o_custkey", Type: catalog.Type{Name: "numeric"}},
	})
	mk("lineitem", 60000, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
	})
	return c
}

func tpchUnstatsCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, cols []catalog.Column) {
		if _, err := c.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable %s: %v", name, err)
		}
	}
	mk("customer", []catalog.Column{
		{Name: "c_custkey", Type: catalog.Type{Name: "numeric"}},
	})
	mk("orders", []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "o_custkey", Type: catalog.Type{Name: "numeric"}},
	})
	mk("lineitem", []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "numeric"}},
	})
	return c
}
