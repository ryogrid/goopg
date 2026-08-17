package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestInListSeqScanCorrectness — M0053-0003 scope-reduction baseline.
//
// IN-list with all-constant values currently goes through SeqScan
// (no OR-of-IndexScan plan node yet). This test pins the expected
// SeqScan behaviour so a future M0054 OR-IndexScan implementation
// is recognized as a deliberate plan-shape change rather than a
// regression.
//
// TPC-H Q12 uses `l_shipmode IN ('MAIL', 'SHIP')` but HammerDB does
// not index l_shipmode, so this gap does not affect M0053-0006
// run-011 results.
func TestInListSeqScanCorrectness(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_shipmode", Type: catalog.Type{Name: "varchar"}, NotNull: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "idx_lineitem_shipmode"},
		tbl, []string{"l_shipmode"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	stmt := parseOne(t, "SELECT l_orderkey FROM lineitem WHERE l_shipmode IN ('MAIL', 'SHIP')")
	node, err := Plan(stmt, c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Currently the planner routes IN-list to SeqScan (no OR-IndexScan
	// rule yet). Future M0054 may change this; update the assertion
	// when that lands.
	if planContainsIndexScan(node) {
		t.Fatalf("M0053-0003 baseline: IN-list should currently use SeqScan, got plan with IndexScan")
	}
}
