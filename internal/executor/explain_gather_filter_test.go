package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestExplainGatherPrintsItsFilter pins M0146-0005ai: a Filter folded onto a
// Gather / Gather Merge is that node's own qual, and explain.c's T_Gather /
// T_GatherMerge arms print it as `Filter:` BEFORE `Workers Planned:`. The
// text renderer used to drop it, so an executed qual (TPC-H Q20's
// `ps_availqty > (SubPlan 1)`) appeared only in JSON.
func TestExplainGatherPrintsItsFilter(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	pred := &optimizer.BinaryOp{Op: parser.OpGt, Left: &optimizer.ColumnRef{Index: 0, Name: "a"}, Right: &optimizer.IntegerConst{Value: 1}}
	for _, g := range []optimizer.Node{
		optimizer.NewGather(0, &optimizer.SeqScan{Table: tbl}, 2),
		optimizer.NewGatherMerge(0, &optimizer.SeqScan{Table: tbl}, 2, nil),
	} {
		text := renderPlanText(&optimizer.Filter{Child: g, Predicate: pred})
		fi := strings.Index(text, "Filter: (a > 1)")
		wi := strings.Index(text, "Workers Planned: 2")
		if fi < 0 {
			t.Fatalf("%T: filter line missing:\n%s", g, text)
		}
		if wi < 0 || fi > wi {
			t.Errorf("%T: Filter must precede Workers Planned:\n%s", g, text)
		}
	}
}

// TestMoveBeforeLastDetail pins the ANALYZE-side reorder: the appended
// `Rows Removed by Filter` row moves in front of the node's `Workers
// Planned` row, as explain.c orders them.
func TestMoveBeforeLastDetail(t *testing.T) {
	rows := []Row{
		{NewStringDatum("  Filter: (a > 1)")},
		{NewStringDatum("  Workers Planned: 2")},
		{NewStringDatum("  Rows Removed by Filter: 5")},
	}
	moveBeforeLastDetail(rows, "  Workers Planned: ")
	got := []string{rows[0][0].StringValue(), rows[1][0].StringValue(), rows[2][0].StringValue()}
	want := []string{"  Filter: (a > 1)", "  Rows Removed by Filter: 5", "  Workers Planned: 2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row order = %q, want %q", got, want)
		}
	}
}
