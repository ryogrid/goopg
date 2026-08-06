package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0125-0008 structural guard.
//
// A Semi/Anti join emits the OUTER (Left) row only, so the layout it
// publishes MUST equal `Left.Output()`. Historically that was a cached
// copy taken at construction and refreshed by hand in
// `runJoinSearchBelowPinned`; the later `rewriteMultiWayChain` pass
// re-sorted the subtree below the pinned spine IN PLACE and left the
// copy a stale permutation, which made every ancestor resolve column
// names against a layout the executor never produces (TPC-DS Q16/Q94
// returned MORE rows with two conjuncts than with one).
//
// `Join.Output` now derives the layout, so this test asserts the
// invariant on the FINAL plan — after join search, MHJ packing and both
// remap passes have run. It is deliberately a whole-tree walk rather
// than a check of one node: any future pass that rewrites a Left child
// in place is caught here regardless of where it lives.
func TestSemiAntiJoinPublishesLeftOutput(t *testing.T) {
	cat := semiAntiInvariantCatalog(t)
//
// M0127-P6.2 note: the MultiHashJoin node named below was deleted, so this
// shape now plans as the left-deep binary hash cascade PG builds. The test is
// kept unchanged — its assertions are about the RESULT, so it now guards the
// cascade against the same defect the packed node once carried.

	// Both conjunct orders, and base widths on either side of the
	// >= 3-table threshold where MHJ packing re-sorts the layout.
	queries := []string{
		`SELECT count(*) FROM o o1, d
		  WHERE o1.dsk = d.dsk AND d.dt = 10
		    AND EXISTS (SELECT 1 FROM o o2 WHERE o1.ord = o2.ord AND o1.wh <> o2.wh)
		    AND NOT EXISTS (SELECT 1 FROM r wr1 WHERE o1.ord = wr1.ord)`,
		`SELECT count(*) FROM o o1, d, ca
		  WHERE o1.dsk = d.dsk AND d.dt = 10 AND o1.ask = ca.ask AND ca.st = 1
		    AND EXISTS (SELECT 1 FROM o o2 WHERE o1.ord = o2.ord AND o1.wh <> o2.wh)
		    AND NOT EXISTS (SELECT 1 FROM r wr1 WHERE o1.ord = wr1.ord)`,
		`SELECT count(*) FROM o o1, d, ca, ws
		  WHERE o1.dsk = d.dsk AND d.dt = 10 AND o1.ask = ca.ask AND ca.st = 1
		    AND o1.ssk = ws.ssk AND ws.comp = 1
		    AND EXISTS (SELECT 1 FROM o o2 WHERE o1.ord = o2.ord AND o1.wh <> o2.wh)
		    AND NOT EXISTS (SELECT 1 FROM r wr1 WHERE o1.ord = wr1.ord)`,
		// Reversed conjunct order: puts the SEMI join on top instead.
		`SELECT count(*) FROM o o1, d, ca, ws
		  WHERE o1.dsk = d.dsk AND d.dt = 10 AND o1.ask = ca.ask AND ca.st = 1
		    AND o1.ssk = ws.ssk AND ws.comp = 1
		    AND NOT EXISTS (SELECT 1 FROM r wr1 WHERE o1.ord = wr1.ord)
		    AND EXISTS (SELECT 1 FROM o o2 WHERE o1.ord = o2.ord AND o1.wh <> o2.wh)`,
	}

	for _, sql := range queries {
		node, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan(%s): %v", sql, err)
		}
		seen := 0
		walkPlanJoins(node, func(j *Join) {
			if j.Type != JoinTypeSemi && j.Type != JoinTypeAnti {
				return
			}
			seen++
			left := j.Left.Output()
			out := j.Output()
			if len(out) != len(left) {
				t.Errorf("semi/anti join width %d != Left width %d\n%s",
					len(out), len(left), sql)
				return
			}
			for i := range out {
				if out[i].Name != left[i].Name || out[i].SourceTableIdx != left[i].SourceTableIdx {
					t.Errorf("semi/anti join publishes a layout its Left never produces:\n"+
						"  pos %d: published %s/src%d, Left has %s/src%d\n  %s",
						i, out[i].Name, out[i].SourceTableIdx,
						left[i].Name, left[i].SourceTableIdx, sql)
					return
				}
			}
		})
		if seen != 2 {
			t.Errorf("expected 2 semi/anti joins in the plan, found %d\n%s", seen, sql)
		}
	}
}

// walkPlanJoins invokes fn on every *Join in the plan tree, descending
// through the wrappers the semi/anti spine can sit under. It walks a
// Semi/Anti join's Right child too — the cloned inner plan can itself
// contain a decorrelated join, and the invariant holds there as well.
func walkPlanJoins(n Node, fn func(*Join)) {
	switch x := n.(type) {
	case nil:
		return
	case *Join:
		fn(x)
		walkPlanJoins(x.Left, fn)
		walkPlanJoins(x.Right, fn)
	case *Filter:
		walkPlanJoins(x.Child, fn)
	case *Project:
		walkPlanJoins(x.Child, fn)
	case *Aggregate:
		walkPlanJoins(x.Child, fn)
	case *Sort:
		walkPlanJoins(x.Child, fn)
	case *Limit:
		walkPlanJoins(x.Child, fn)
	case *NestedLoopIndexJoin:
		walkPlanJoins(x.Outer, fn)
	}
}

func semiAntiInvariantCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, cols ...string) {
		var cs []catalog.Column
		for _, n := range cols {
			cs = append(cs, catalog.Column{Name: n, Type: catalog.Type{Name: "int4"}})
		}
		if _, err := c.CreateTable(parser.ObjectName{Name: name}, cs); err != nil {
			t.Fatal(err)
		}
	}
	mk("o", "ord", "wh", "dsk", "ask", "ssk", "cost", "profit")
	mk("d", "dsk", "dt")
	mk("ca", "ask", "st")
	mk("ws", "ssk", "comp")
	mk("r", "ord")
	return c
}
