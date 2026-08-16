package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// nonConstRHSCatalog wires up `lineitem(l_shipdate)` and a `date_t(ts)`
// table with a B-tree index on the timestamp column.
func nonConstRHSCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_shipdate", Type: catalog.Type{Name: "timestamp"}, NotNull: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "idx_lineitem_ship"}, tbl, []string{"l_shipdate"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestRangeIndexScanWithIntervalRHS_TPCH_Q1: TPC-H Q1 shape — the
// canonical `l_shipdate <= date 'X' - interval 'N days'` form must
// fold to a constant via M0051-0002 and produce an IndexScan.
// M0053-0002 regression test — confirms date arithmetic on the RHS is
// not treated as non-constant.
func TestRangeIndexScanWithIntervalRHS_TPCH_Q1(t *testing.T) {
	cat := nonConstRHSCatalog(t)
	stmt := parseOne(t, "SELECT l_orderkey FROM lineitem WHERE l_shipdate <= date '1998-12-01' - interval '90 day'")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !planContainsIndexScan(node) {
		t.Fatalf("expected IndexScan for date-arithmetic RHS, got: %T", node)
	}
}

// TestRangeIndexScanWithIntervalRHS_TPCH_Q6: TPC-H Q6 shape — two-sided
// range with interval addition on RHS bounds.
func TestRangeIndexScanWithIntervalRHS_TPCH_Q6(t *testing.T) {
	cat := nonConstRHSCatalog(t)
	stmt := parseOne(t,
		"SELECT l_orderkey FROM lineitem "+
			"WHERE l_shipdate >= date '1994-01-01' AND l_shipdate < date '1994-01-01' + interval '1 year'")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !planContainsIndexScan(node) {
		t.Fatalf("expected IndexScan for two-sided interval RHS, got: %T", node)
	}
}

// TestColumnVsColumnComparisonFallsBack: comparisons between two
// columns of the same row (`l_partkey = l_orderkey`) cannot use an
// index probe — the RHS depends on the row being scanned. Must fall
// back to SeqScan. Confirms M0053-0002 did not over-eagerly accept
// non-constant RHS expressions.
func TestColumnVsColumnComparisonFallsBack(t *testing.T) {
	cat := nonConstRHSCatalog(t)
	stmt := parseOne(t, "SELECT l_orderkey FROM lineitem WHERE l_partkey = l_orderkey")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if planContainsIndexScan(node) {
		t.Fatalf("column-vs-column comparison must NOT use IndexScan: %T", node)
	}
}
