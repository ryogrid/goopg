package optimizer

// review/260831-2 X-8 — enable_indexscan / enable_bitmapscan /
// enable_indexonlyscan were accepted and ignored.
//
// They were registered as no-op GUCs ("v0's planner ignores them", defaults.go
// M0097-0069), so a session that turned every scan method off still got an
// index plan where the PG 18.3 oracle falls back. The oracle's matrix for
// `SELECT a FROM x8 WHERE a = 42` over x8(a, b) with a btree index on a:
//
//	all on                            Index Only Scan
//	enable_indexonlyscan=off          Index Scan
//	enable_indexscan=off              Bitmap Heap Scan
//	enable_indexscan+bitmapscan=off   Seq Scan
//
// goopg's scan choice is rule-based, so a disabled shape is DECLINED by its
// producer rather than priced out of the running; the third row therefore lands
// on the Seq Scan directly (no bitmap producer runs for this single-table
// rule-based plan). Rows 1, 2 and 4 — and the invariant that the toggles change
// NOTHING when untouched — are what this test pins.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// scanToggleCatalog: CREATE TABLE x8 (a int, b int); CREATE INDEX x8a ON x8(a).
func scanToggleCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "x8"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "x8a"}, tbl, []string{"a"},
		false, "btree", false); err != nil {
		t.Fatal(err)
	}
	return c
}

// withScanToggles wraps cat with the session GUC state the wire path builds in
// postmaster.sessionPlanCatalog.
func withScanToggles(cat catalog.Catalog, index, bitmap, indexOnly bool) catalog.Catalog {
	w := catalog.WithSearchPath(cat, func() []string { return []string{"public"} })
	w.DisableIndexScan = index
	w.DisableBitmapScan = bitmap
	w.DisableIndexOnlyScan = indexOnly
	return w
}

// scanShapesIn collects the index scan shapes anywhere in the plan, using the
// same explicit child walk the other structural tests in this package use
// (planChildren, q8_subquery_scope_posmap_test.go).
func scanShapesIn(n Node) (indexScan, indexOnly, bitmap bool) {
	var walk func(Node)
	walk = func(x Node) {
		if x == nil {
			return
		}
		switch x.(type) {
		case *IndexScan:
			indexScan = true
		case *IndexOnlyScan:
			indexOnly = true
		case *BitmapHeapScan, *BitmapIndexScan:
			bitmap = true
		}
		for _, c := range planChildren(x) {
			walk(c)
		}
	}
	walk(n)
	return
}

func TestScanMethodTogglesGateTheScanShape(t *testing.T) {
	cat := scanToggleCatalog(t)

	for _, tc := range []struct {
		name                         string
		sql                          string
		index, bitmap, indexOnly     bool
		wantIndexOnly, wantIndexScan bool
	}{
		{name: "defaults keep the index-only plan", sql: "SELECT a FROM x8 WHERE a = 42",
			wantIndexOnly: true},
		{name: "defaults keep the index plan", sql: "SELECT b FROM x8 WHERE a = 42",
			wantIndexScan: true},
		{name: "indexonlyscan off demotes to an index scan", sql: "SELECT a FROM x8 WHERE a = 42",
			indexOnly: true, wantIndexScan: true},
		{name: "indexscan off drops the index-only plan too", sql: "SELECT a FROM x8 WHERE a = 42",
			index: true, bitmap: true},
		{name: "indexscan off drops the index plan", sql: "SELECT b FROM x8 WHERE a = 42",
			index: true, bitmap: true},
		{name: "every toggle off leaves a heap scan", sql: "SELECT a FROM x8 WHERE a = 42",
			index: true, bitmap: true, indexOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stmt := parseOne(t, tc.sql)
			node, err := Plan(stmt, withScanToggles(cat, tc.index, tc.bitmap, tc.indexOnly))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			isIndex, isIndexOnly, isBitmap := scanShapesIn(node)
			switch {
			case tc.wantIndexOnly:
				if !isIndexOnly {
					t.Fatalf("want an IndexOnlyScan, got:\n%s", planShapeString(node))
				}
			case tc.wantIndexScan:
				if !isIndex {
					t.Fatalf("want an IndexScan, got:\n%s", planShapeString(node))
				}
				if isIndexOnly {
					t.Fatalf("an IndexOnlyScan survived enable_indexonlyscan=off:\n%s", planShapeString(node))
				}
			default:
				if isIndex || isIndexOnly || isBitmap {
					t.Fatalf("an index scan shape survived the toggles (index=%v indexonly=%v bitmap=%v):\n%s",
						isIndex, isIndexOnly, isBitmap, planShapeString(node))
				}
			}
		})
	}
}
