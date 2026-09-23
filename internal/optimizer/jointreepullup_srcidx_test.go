package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// A pulled-up EXISTS body numbers its tables from 1 like the outer query, so
// a self-correlated EXISTS used to put two SourceTableIdx=1 scans in one plan
// and EXPLAIN (first-wins per SourceTableIdx) printed `t1.a = t1.a`. The
// jointree pull-up now shifts each body into its own SourceTableIdx range
// (assignPulledSourceOffsets), as the legacy unnest does with
// remapSourceTableIdx. M0145-0030.
func TestPulledExistsBodyGetsItsOwnSourceTableIdx(t *testing.T) {
	prev := jointreePipeline
	jointreePipeline = true
	t.Cleanup(func() { jointreePipeline = prev })

	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, "SELECT 1 FROM t t1 WHERE EXISTS (SELECT 1 FROM t t2 WHERE t2.a = t1.a AND t2.b <> t1.b)"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var scanSrcs []int16
	var joinKeySrcs []int16
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		switch x := n.(type) {
		case *SeqScan:
			if out := x.Output(); len(out) > 0 {
				scanSrcs = append(scanSrcs, out[0].SourceTableIdx)
			}
		case *Join:
			for _, e := range []Expr{x.Predicate, x.LeftKey, x.RightKey} {
				if e == nil {
					continue
				}
				walkExprTree(e, func(e Expr) {
					if cr, ok := e.(*ColumnRef); ok && cr.Name == "a" {
						joinKeySrcs = append(joinKeySrcs, cr.SourceTableIdx)
					}
				})
			}
		}
		kids, _ := planChildNodes(n)
		for _, k := range kids {
			walk(k)
		}
	}
	walk(node)
	if len(scanSrcs) != 2 || scanSrcs[0] == scanSrcs[1] {
		t.Fatalf("want two scans of t with distinct SourceTableIdx, got %v", scanSrcs)
	}
	if len(joinKeySrcs) < 2 {
		t.Fatalf("want the join to compare t1.a with t2.a, found refs %v", joinKeySrcs)
	}
	if joinKeySrcs[0] == joinKeySrcs[1] {
		t.Fatalf("the join key compares two refs with the same SourceTableIdx %v — EXPLAIN would print t1.a = t1.a", joinKeySrcs)
	}
}
