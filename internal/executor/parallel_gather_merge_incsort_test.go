package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestGatherMergeOverIncrementalSortIdentity pins M0146-0214's executor half:
// generate_useful_gather_paths' incremental-sort arm puts an Incremental Sort
// in each worker under a Gather Merge, so every parallel attach walk must
// descend an incrementalSortOp exactly as it descends a sortOp. A walk that
// stopped there would leave the scan unclaimed and every worker would read
// the whole table — (workers+1)x the rows, in the right order.
//
// The heap is loaded in id order, so each worker's block range arrives
// ordered by id: the Incremental Sort's presorted-prefix contract holds per
// worker, and the merged output must equal the serial ORDER BY id, k.
func TestGatherMergeOverIncrementalSortIdentity(t *testing.T) {
	ctx, cleanup := gmFixture(t)
	defer cleanup()
	const sql = "SELECT id, k FROM pq_merge ORDER BY id, k"
	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := renderRows(serialRows)

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			advanceStmtCounter(ctx)
			node := planWithSettings(t, ctx, sql, optimizer.DefaultPlannerSettings())
			gathered, srt := spliceGatherMergeIncSort(node, workers)
			if srt == nil {
				t.Fatalf("no Sort in the plan for %q; the test would prove nothing", sql)
			}
			ctx.MaxParallelWorkers = 8
			ctx.ParallelLeaderParticipation = true
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			var got []string
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("next: %v", err)
				}
				got = append(got, renderRows([]Row{slot.Row()})...)
			}
			if err := op.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %d rows, want %d — a parallel walk did not descend the incrementalSortOp", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("row %d: got %q, want %q", i, got[i], want[i])
				}
			}
		})
	}
}

// spliceGatherMergeIncSort replaces the plan's Sort on (id, k) with an
// Incremental Sort presorted on id and gathers it with a Gather Merge.
func spliceGatherMergeIncSort(n optimizer.Node, workers int) (optimizer.Node, *optimizer.Sort) {
	switch x := n.(type) {
	case *optimizer.Sort:
		inc := &optimizer.IncrementalSort{Child: x.Child, Keys: x.Keys, PresortedCount: 1}
		return optimizer.NewGatherMerge(0, inc, workers, x.Keys), x
	case *optimizer.Project:
		child, srt := spliceGatherMergeIncSort(x.Child, workers)
		if srt == nil {
			return n, nil
		}
		c := *x
		c.Child = child
		return &c, srt
	}
	return n, nil
}
