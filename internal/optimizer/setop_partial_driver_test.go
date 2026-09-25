package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestNonStreamingSetOpIsNeverAPartialDriver pins M0145-0008s. Only a
// streaming SetOp (UNION ALL) may be a partial driver: each worker forwarding
// its share of both branches is row-exact. UNION, INTERSECT and EXCEPT compare
// rows across the WHOLE of both inputs, so running one per worker over partial
// inputs drops every match whose two rows land in different workers — regress
// `union` returned 1660 instead of 5000 for `count(*) FROM (a INTERSECT b)`
// under `Gather > Partial Aggregate > HashSetOp > Parallel Seq Scan`.
//
// drivingScan, stampParallelScan and drivingScanCrossesSort are sibling walks
// (hard-won rule 2), so the test drives all three.
func TestNonStreamingSetOpIsNeverAPartialDriver(t *testing.T) {
	tbl := &catalog.Table{Name: "t", Columns: []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}}}
	scan := func() Node { return &SeqScan{Table: tbl, Alias: "t", schema: tableSchema(tbl)} }
	cases := []struct {
		name   string
		op     parser.SetOpType
		all    bool
		driver bool
	}{
		{"union-all", parser.SetOpUnion, true, true},
		{"union", parser.SetOpUnion, false, false},
		{"intersect", parser.SetOpIntersect, false, false},
		{"intersect-all", parser.SetOpIntersect, true, false},
		{"except", parser.SetOpExcept, false, false},
		{"except-all", parser.SetOpExcept, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			so := &SetOp{Left: scan(), Right: scan(), Op: tc.op, All: tc.all}
			if got := drivingScan(so) != nil; got != tc.driver {
				t.Fatalf("drivingScan resolved=%v, want %v", got, tc.driver)
			}
			stamped := stampParallelScan(so)
			if tc.driver {
				if stamped == Node(so) {
					t.Fatal("a UNION ALL driver must have its branch scans stamped")
				}
				return
			}
			if stamped != Node(so) {
				t.Fatal("a non-streaming SetOp must not have scans below it stamped parallel")
			}
			if drivingScanCrossesSort(so) {
				t.Fatal("a non-streaming SetOp has no driving scan to cross a sort to")
			}
		})
	}
}
