package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// M0148-0001c2 arm (1): get_relation_info reads a btree's tree_height off the
// metapage (_bt_getrootheight → btm_fastlevel), and btcostestimate's descent
// cost is (tree_height + 1) * 50 * cpu_operator_cost. estimateIndexGeometry
// used to guess the height from the heap tuple count, so a never-analysed
// two-row table (estimated at 2550 tuples) priced a height-1 descent where PG
// read 0 — the regress minmaxtest members' 0.28 vs PG's 0.15. With the
// storage hook answering, the metapage height wins; without it the estimate
// stays the fallback.
func TestEstimateIndexGeometryReadsMetapageHeight(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "th1"}, []catalog.Column{{Name: "f1", Type: catalog.Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "th1i"}, tbl, []string{"f1"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	saved := catalog.IndexTreeHeightFunc
	t.Cleanup(func() { catalog.IndexTreeHeightFunc = saved })

	catalog.IndexTreeHeightFunc = nil
	if _, _, h := estimateIndexGeometry(c, idx, tbl, 2550); h != 1 {
		t.Fatalf("fallback estimate: height %d, want the tuple-count guess 1", h)
	}
	var asked storage.RelFileNode
	catalog.IndexTreeHeightFunc = func(rel storage.RelFileNode) (int, bool) {
		asked = rel
		return 0, true
	}
	if _, _, h := estimateIndexGeometry(c, idx, tbl, 2550); h != 0 {
		t.Fatalf("metapage height: got %d, want 0 (the single-page root PG reads)", h)
	}
	if asked != c.IndexRelFileNode(idx) {
		t.Fatalf("hook asked for %+v, want the index's own file %+v", asked, c.IndexRelFileNode(idx))
	}
	catalog.IndexTreeHeightFunc = func(storage.RelFileNode) (int, bool) { return 0, false }
	if _, _, h := estimateIndexGeometry(c, idx, tbl, 2550); h != 1 {
		t.Fatalf("hook declined: height %d, want the fallback 1", h)
	}
}
