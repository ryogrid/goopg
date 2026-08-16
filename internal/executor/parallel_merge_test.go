package executor

// P7 of docs/design/parallel-query/ — Gather Merge execution.
//
// Plain Gather has one correctness property: the SET of rows must match
// serial. Gather Merge has a strictly stronger one — the SEQUENCE must match.
// That is what these tests check, because the failure mode is silent: a merge
// that loses ordering returns every correct row, in the wrong order, with no
// error anywhere.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// gmFixture builds a table whose sort keys have deliberate ties and NULLs, so
// the merge is exercised on the cases where a naive comparator diverges from
// the serial sort.
func gmFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	if err := runDDL(t, ctx, "CREATE TABLE pq_merge (id int, k int, s text)"); err != nil {
		cleanup()
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 400; i++ {
		// k has ~40 distinct values over 400 rows ⇒ every key value is a tie
		// spanning multiple blocks, so ties necessarily cross worker
		// partitions.
		var k string
		if i%37 == 0 {
			k = "NULL"
		} else {
			k = fmt.Sprintf("%d", i%40)
		}
		stmt := fmt.Sprintf("INSERT INTO pq_merge VALUES (%d, %s, 'r-%d')", i, k, i)
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	return ctx, cleanup
}

// runGatherMerge plans sql, wraps its Sort in a GatherMerge with the given
// worker count, and returns the rendered rows IN ORDER.
func runGatherMerge(t *testing.T, ctx *Context, sql string, workers int) []string {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	gathered, srt := spliceGatherMerge(node, workers)
	if srt == nil {
		t.Fatalf("no Sort in the plan for %q; the test would silently prove nothing", sql)
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
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, renderRows([]Row{slot.Row()})...)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out
}

// spliceGatherMerge replaces the plan's Sort with GatherMerge(Sort), leaving
// every node above it — notably the Project — in place.
//
// Wrapping the bare Sort instead would compare the scan's raw columns against
// the query's projected output and fail on column count, which says nothing
// about ordering. This mirrors what MaybeAddGather actually builds.
func spliceGatherMerge(n optimizer.Node, workers int) (optimizer.Node, *optimizer.Sort) {
	switch x := n.(type) {
	case *optimizer.Sort:
		return optimizer.NewGatherMerge(0, x, workers, x.Keys), x
	case *optimizer.Project:
		child, srt := spliceGatherMerge(x.Child, workers)
		if srt == nil {
			return n, nil
		}
		c := *x
		c.Child = child
		return &c, srt
	case *optimizer.Filter:
		child, srt := spliceGatherMerge(x.Child, workers)
		if srt == nil {
			return n, nil
		}
		c := *x
		c.Child = child
		return &c, srt
	case *optimizer.Limit:
		child, srt := spliceGatherMerge(x.Child, workers)
		if srt == nil {
			return n, nil
		}
		c := *x
		c.Child = child
		return &c, srt
	}
	return n, nil
}

// TestGatherMergePreservesOrder is the gate. The merged output must be the
// serial output, position for position.
func TestGatherMergePreservesOrder(t *testing.T) {
	ctx, cleanup := gmFixture(t)
	defer cleanup()

	for _, sql := range []string{
		"SELECT id, k FROM pq_merge ORDER BY id",
		"SELECT id, k FROM pq_merge ORDER BY id DESC",
		// Ties across partitions: k alone is not a total order, so the merge
		// must at minimum agree with serial on the k sequence.
		"SELECT k FROM pq_merge ORDER BY k",
		"SELECT k FROM pq_merge ORDER BY k DESC",
		// NULL placement differs between ASC and DESC, and a merge that gets
		// the NULL rule wrong only diverges at the stream boundaries.
		"SELECT k, id FROM pq_merge ORDER BY k, id",
		"SELECT k, id FROM pq_merge ORDER BY k DESC, id DESC",
		"SELECT id, s FROM pq_merge WHERE k > 10 ORDER BY id",
	} {
		t.Run(sql, func(t *testing.T) {
			serialRows, err := runQueryWithErr(ctx, sql)
			if err != nil {
				t.Fatalf("serial: %v", err)
			}
			want := renderRows(serialRows)

			for _, workers := range []int{0, 1, 2, 4} {
				got := runGatherMerge(t, ctx, sql, workers)
				if len(got) != len(want) {
					t.Fatalf("workers=%d: got %d rows, want %d", workers, len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("workers=%d: row %d differs: got %q, want %q\n"+
							"the merge diverged from the serial sort — with the "+
							"right rows in the wrong order, nothing else would "+
							"have reported this",
							workers, i, got[i], want[i])
					}
				}
			}
		})
	}
}

// TestGatherMergeNoDuplicates: the same duplicate-rows bug the P6 identity
// test caught applies here, and the merge path has its OWN way of hitting it —
// attachParallelScan has to descend through the sortOp, which plain Gather
// never required.
func TestGatherMergeNoDuplicates(t *testing.T) {
	ctx, cleanup := gmFixture(t)
	defer cleanup()

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			got := runGatherMerge(t, ctx, "SELECT id FROM pq_merge ORDER BY id", workers)
			if len(got) != 400 {
				t.Fatalf("got %d rows, want 400 — a %.2gx multiple means "+
					"attachParallelScan failed to reach the scan under the Sort, "+
					"so every worker sorted the WHOLE relation",
					len(got), float64(len(got))/400.0)
			}
			seen := map[string]bool{}
			for _, r := range got {
				if seen[r] {
					t.Fatalf("row %s appeared twice", r)
				}
				seen[r] = true
			}
		})
	}
}

// TestGatherMergeEarlyClose covers the LIMIT shape: the consumer stops pulling
// while workers still have rows queued. Close must cancel, drain and join
// without deadlocking or leaking a goroutine.
func TestGatherMergeEarlyClose(t *testing.T) {
	ctx, cleanup := gmFixture(t)
	defer cleanup()

	stmts, err := parser.Parse("SELECT id FROM pq_merge ORDER BY id")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	gathered, srt := spliceGatherMerge(node, 4)
	if srt == nil {
		t.Fatal("no Sort")
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
	for i := 0; i < 5; i++ {
		if _, err := op.Next(); err != nil && err != EOF {
			t.Fatalf("next: %v", err)
		}
	}
	// Must not hang. A Close that joins before draining deadlocks against a
	// worker blocked mid-send.
	if err := op.Close(); err != nil {
		t.Fatalf("close after partial consumption: %v", err)
	}
	// Idempotent, like every other operator's Close.
	if err := op.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestGatherMergeEmptyInput: a source that yields nothing must be dropped
// before the heap forms, not left in it holding an invalid row.
func TestGatherMergeEmptyInput(t *testing.T) {
	ctx, cleanup := gmFixture(t)
	defer cleanup()

	got := runGatherMerge(t, ctx,
		"SELECT id FROM pq_merge WHERE id < 0 ORDER BY id", 4)
	if len(got) != 0 {
		t.Fatalf("got %d rows from an empty scan, want 0", len(got))
	}
}
