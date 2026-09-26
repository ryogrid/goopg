package executor

// M0146-0025: executor identity for the partial-Group shape —
// `Group -> Gather Merge -> Group(partial) -> Sort`, the plan PG files for
// an aggregate-free GROUP BY (planner.c:7570, 7704).
//
// Unlike the Finalize/Partial split (parallel_agg_split_test.go) there is no
// shared accumulator: each worker runs an ordinary sorted dedup and the
// leader's Group collapses the merged streams. The test pins that the
// cross-worker duplicates each worker's dedup cannot see really do collapse —
// pq_agg's grp has 4 distinct values across 400 rows, so every group spans
// several workers.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// findGroupAgg locates the first *optimizer.Aggregate in a plan tree.
func findGroupAgg(n optimizer.Node) *optimizer.Aggregate {
	if a, ok := n.(*optimizer.Aggregate); ok {
		return a
	}
	for _, c := range optimizer.ParallelChildrenForTest(n) {
		if a := findGroupAgg(c); a != nil {
			return a
		}
	}
	return nil
}

func TestPartialGroupGatherMergeIdentity(t *testing.T) {
	ctx, cleanup := pqAggFixture(t)
	defer cleanup()

	sql := "SELECT grp FROM pq_agg GROUP BY grp ORDER BY grp"
	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := renderRows(serialRows)

	// The serial plan's dedup spec gives us the group key and the input
	// subtree; the emitted M0146-0025 shape is spliced in by hand, exactly
	// as createPlanNode lowers the filed path.
	node := planForTest(t, ctx, "SELECT grp FROM pq_agg GROUP BY grp")
	spec := findGroupAgg(node)
	if spec == nil {
		t.Fatal("serial plan has no aggregate to clone")
	}
	cr, ok := spec.GroupExprs[0].(*optimizer.ColumnRef)
	if !ok {
		t.Fatalf("group key is %T, want *ColumnRef", spec.GroupExprs[0])
	}
	grpType := catalog.Type{Name: "int4"}

	// Per-worker: Sort on the INPUT-coordinate key, then the marked dedup.
	workerInput := spec.Child
	partialSpec := *spec
	partialSpec.PartialGroup = true
	partialSpec.Child = &optimizer.Sort{
		Child: workerInput,
		Keys:  []optimizer.SortKey{{Expr: &optimizer.ColumnRef{Index: cr.Index, Name: cr.Name, Type: grpType}}},
	}

	// Merge keys and the leader-side Group key address the partial output's
	// own positions — the planner's keyRefs rewrite.
	outRef := &optimizer.ColumnRef{Index: 0, Name: cr.Name, Type: grpType}
	finalSpec := *spec
	finalSpec.GroupExprs = []optimizer.Expr{outRef}
	finalSpec.Child = nil // set per workers below

	for _, workers := range []int{1, 2, 4} {
		partial := partialSpec
		gm := optimizer.NewGatherMerge(0, &partial, workers,
			[]optimizer.SortKey{{Expr: outRef}})
		final := finalSpec
		final.Child = gm

		advanceStmtCounter(ctx)
		ctx.MaxParallelWorkers = 8
		ctx.ParallelLeaderParticipation = true
		op, err := Build(&final)
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
				"collapsed cross-worker groups",
				workers, len(got), len(want), got, want, workers+1)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("workers=%d: row %d differs:\n got %q\nwant %q",
					workers, i, got[i], want[i])
			}
		}
	}
}
