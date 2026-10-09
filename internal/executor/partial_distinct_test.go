package executor

// M0146-0027 slice 2: executor identity for the partial-DISTINCT shape —
// `Unique -> Gather Merge -> Unique(partial) -> Sort`, the plan PG files
// for SELECT DISTINCT over a parallel-capable input
// (create_partial_distinct_paths, planner.c:4852). Each worker runs an
// ordinary sorted dedup (distinctOnOp) over its own partition and the
// leader's Unique collapses the merged streams — the same construction
// TestPartialGroupGatherMergeIdentity pins for the partial-Group shape.
//
// pq_agg's grp has 4 distinct values across 400 rows, so every value spans
// several workers: the leader-side dedup collapsing cross-worker duplicates
// is exactly what the test measures (a marker-less descent would be N+1
// copies; this asserts the partial Unique carries no such loss and no such
// duplication either).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// findDistinctNode locates the first *optimizer.Distinct or
// *optimizer.DistinctOn in a plan tree.
func findDistinctNode(n optimizer.Node) optimizer.Node {
	switch n.(type) {
	case *optimizer.Distinct, *optimizer.DistinctOn:
		return n
	}
	for _, c := range optimizer.ParallelChildrenForTest(n) {
		if d := findDistinctNode(c); d != nil {
			return d
		}
	}
	return nil
}

func TestPartialUniqueGatherMergeIdentity(t *testing.T) {
	ctx, cleanup := pqAggFixture(t)
	defer cleanup()

	sql := "SELECT DISTINCT grp FROM pq_agg"
	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := renderRows(serialRows)

	// The serial plan's DISTINCT node gives the input subtree; the emitted
	// M0146-0027 shape is spliced in by hand, exactly as createPlanNode
	// lowers the filed path: per-worker Sort on the dedup key, marked
	// DistinctOn, GatherMerge, leader DistinctOn.
	node := planForTest(t, ctx, sql)
	spec := findDistinctNode(node)
	if spec == nil {
		t.Fatal("serial plan has no distinct node to clone")
	}
	var input optimizer.Node
	switch s := spec.(type) {
	case *optimizer.Distinct:
		input = s.Child
	case *optimizer.DistinctOn:
		input = s.Child
	}
	if input == nil {
		t.Fatal("distinct node has no child")
	}
	// Resolve grp's position in the INPUT row — a bare SeqScan publishes
	// all five columns (grp at 1), a selecting Project only grp (at 0).
	grpIdx := -1
	for i, c := range input.Output() {
		if c.Name == "grp" {
			grpIdx = i
			break
		}
	}
	if grpIdx < 0 {
		t.Fatal("no grp column in the distinct input schema")
	}
	grpRef := &optimizer.ColumnRef{Index: grpIdx, Name: "grp", Type: catalog.Type{Name: "int4"}}

	for _, workers := range []int{1, 2, 4} {
		worker := &optimizer.DistinctOn{
			Child: &optimizer.Sort{
				Child: input,
				Keys:  []optimizer.SortKey{{Expr: grpRef}},
			},
			KeyCols:       []int{grpIdx},
			PartialUnique: true,
		}
		gm := optimizer.NewGatherMerge(0, worker, workers,
			[]optimizer.SortKey{{Expr: grpRef}})
		final := &optimizer.DistinctOn{Child: gm, KeyCols: []int{grpIdx}}

		advanceStmtCounter(ctx)
		ctx.MaxParallelWorkers = 8
		ctx.ParallelLeaderParticipation = true
		op, err := Build(final)
		if err != nil {
			t.Fatalf("workers=%d build: %v", workers, err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatalf("workers=%d open: %v", workers, err)
		}
		var got []string
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatalf("workers=%d next: %v", workers, err)
			}
			got = append(got, renderRows([]Row{slot.Row()})...)
		}
		if err := op.Close(); err != nil {
			t.Fatalf("workers=%d close: %v", workers, err)
		}

		if len(got) != len(want) {
			t.Fatalf("workers=%d: got %d rows, want %d\n got=%v\nwant=%v\n"+
				"~%d times too many means the leader-side dedup never "+
				"collapsed cross-worker duplicates",
				workers, len(got), len(want), got, want, workers+1)
		}
		// Adjacent-dedup output over the merge is ordered; the serial
		// hashed Distinct is not — compare as SETS, not row order.
		gotSet := map[string]bool{}
		for _, r := range got {
			gotSet[r] = true
		}
		for _, w := range want {
			if !gotSet[w] {
				t.Fatalf("workers=%d: missing row %q (got %v)", workers, w, got)
			}
		}
	}
}
