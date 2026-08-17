package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// horizonsCatalog mirrors the isolation `horizons` spec's table:
//
//	CREATE TABLE horizons_tst (data int unique);
//
// which yields a UNIQUE B-tree index named horizons_tst_data_key on (data).
func horizonsCatalog(t *testing.T) (catalog.Catalog, *catalog.Table) {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "horizons_tst"}, []catalog.Column{
		{Name: "data", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "horizons_tst_data_key"},
		tbl, []string{"data"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
	return c, tbl
}

// withSeqScanDisabled wraps cat so the planner sees `enable_seqscan = off`.
func withSeqScanDisabled(cat catalog.Catalog) catalog.Catalog {
	w := catalog.WithSearchPath(cat, func() []string { return []string{"public"} })
	w.DisableSeqScan = true
	return w
}

// TestOrderedIndexOnlyScanPromotedWhenSeqScanDisabled: with enable_seqscan=off,
// `SELECT * FROM horizons_tst ORDER BY data` must plan to a bare IndexOnlyScan —
// the covering unique index provides the ordering, so the Sort is dropped (the
// shape the horizons spec's `pruner_query_plan` EXPLAIN asserts). Design 0118-0103.
func TestOrderedIndexOnlyScanPromotedWhenSeqScanDisabled(t *testing.T) {
	cat, _ := horizonsCatalog(t)
	stmt := parseOne(t, "SELECT * FROM horizons_tst ORDER BY data")
	node, err := Plan(stmt, withSeqScanDisabled(cat))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ios, ok := node.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("expected top node *IndexOnlyScan (Sort dropped), got %T", node)
	}
	if ios.Index == nil || ios.Index.Name != "horizons_tst_data_key" {
		t.Fatalf("expected index horizons_tst_data_key, got %+v", ios.Index)
	}
	if len(ios.Covered) != 1 || ios.Covered[0].Name != "data" {
		t.Fatalf("expected Covered=[data], got %+v", ios.Covered)
	}
	// A full-range IOS: no equality/range bounds (RangeScan over the whole index).
	if ios.Key != nil || len(ios.Keys) != 0 || ios.LowKey != nil || ios.HighKey != nil {
		t.Fatalf("expected unbounded full-range IOS, got Key=%v Keys=%v Low=%v High=%v",
			ios.Key, ios.Keys, ios.LowKey, ios.HighKey)
	}
}

// TestOrderedIndexOnlyScanNotPromotedByDefault: without the GUC the rule-based
// planner is unchanged — the query keeps its Sort (no ordered-IOS promotion), so
// ordinary queries / TPC-H / pgbench plans never shift. Design 0118-0103.
func TestOrderedIndexOnlyScanNotPromotedByDefault(t *testing.T) {
	cat, _ := horizonsCatalog(t)
	stmt := parseOne(t, "SELECT * FROM horizons_tst ORDER BY data")
	node, err := Plan(stmt, cat) // no seqscan-disabled wrapper
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, ok := node.(*IndexOnlyScan); ok {
		t.Fatalf("unexpected ordered-IOS promotion without enable_seqscan=off: %T", node)
	}
}

// TestOrderedIndexOnlyScanRequiresAscending: a DESC ORDER BY does not match the
// ascending NULLS-LAST order the full-range RangeScan produces, so no promotion.
func TestOrderedIndexOnlyScanRequiresAscending(t *testing.T) {
	cat, _ := horizonsCatalog(t)
	stmt := parseOne(t, "SELECT * FROM horizons_tst ORDER BY data DESC")
	node, err := Plan(stmt, withSeqScanDisabled(cat))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, ok := node.(*IndexOnlyScan); ok {
		t.Fatalf("unexpected ordered-IOS promotion for DESC order: %T", node)
	}
}
