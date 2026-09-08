package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestInListSAOPIndexScan — B-14 (P2-09a) baseline update of the M0053-0003
// pin (TestInListSeqScanCorrectness).
//
// M0053-0003 recorded that an IN-list with all-constant values went through
// SeqScan (no OR-of-IndexScan rule yet). B-14 lands the ScalarArrayOp index
// path — `match_saopclause_to_indexcol` (indxpath.c:3136) at the pipeline's
// coordinates (trySAOPIndexScan) with multi-descent evaluation in the
// executor — so the same shape now probes the index, one descent per
// element. This test pins the NEW baseline: the plan-shape change from
// SeqScan to IndexScan is deliberate, not a regression.
//
// TPC-H Q12 uses `l_shipmode IN ('MAIL', 'SHIP')` but HammerDB does
// not index l_shipmode, so this path does not fire for Q12 on the bench
// schema (the unmoved-shape pins live in saop_index_test.go).
func TestInListSAOPIndexScan(t *testing.T) {
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
	// B-14: the IN-list now probes the index instead of seq-scanning.
	scan := findIndexScan(node)
	if scan == nil {
		t.Fatalf("B-14 baseline: IN-list over an indexed column should use IndexScan, got plan without one")
	}
	if len(scan.SAOPKeys) != 2 {
		t.Fatalf("IndexScan.SAOPKeys has %d elements, want 2 (one descent per IN element)", len(scan.SAOPKeys))
	}
}
