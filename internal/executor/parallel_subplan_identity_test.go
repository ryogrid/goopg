package executor

import (
	"fmt"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestParallelSubPlanIdentity is M0146-0002f's worker-safety gate: a
// parallel-safe SubPlan (max_parallel_hazard_walker's SubPlan arm) runs
// INSIDE each worker, so its sublink caches, its hashed probe set and its
// own scan must be worker-local — and that scan must read the whole
// subquery relation in every worker, never one block-allocator partition of
// it (PG runs a SubPlan's plan in full per participant). Each query runs
// serially and then under a real Gather at 1, 2 and 4 workers; the row sets
// must agree. M0146-0002f ran it with -race (go test -race -run
// TestParallelSubPlanIdentity ./internal/executor).
func TestParallelSubPlanIdentity(t *testing.T) {
	ctx, cleanup := parallelIdentityFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE pq_sub (k int)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 400; i += 3 {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO pq_sub VALUES (%d)", i)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	queries := []string{
		"SELECT id FROM pq_ident WHERE id NOT IN (SELECT k FROM pq_sub)",
		"SELECT id FROM pq_ident WHERE id IN (SELECT k FROM pq_sub)",
		"SELECT id FROM pq_ident WHERE v > (SELECT max(k) FROM pq_sub)",
		// Under OR the sublink stays in the scan's Filter; a bare
		// uncorrelated EXISTS plans as a gating Result, which the planner
		// never places under a Gather.
		"SELECT id FROM pq_ident WHERE id < 5 OR EXISTS (SELECT 1 FROM pq_sub WHERE k = 3)",
		"SELECT id FROM pq_ident WHERE id < 5 OR NOT EXISTS (SELECT 1 FROM pq_sub WHERE k = 4)",
	}
	for _, sql := range queries {
		serial := runForced(t, ctx, sql, false)
		sort.Strings(serial)
		for _, workers := range []int{1, 2, 4} {
			t.Run(fmt.Sprintf("%s/workers=%d", sql, workers), func(t *testing.T) {
				stmts, err := parser.Parse(sql)
				if err != nil {
					t.Fatalf("parse: %v", err)
				}
				node, err := optimizer.Plan(stmts[0], ctx.Catalog)
				if err != nil {
					t.Fatalf("plan: %v", err)
				}
				gathered := optimizer.NewGather(0, optimizer.StripGather(node), workers)
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
					got = append(got, datumTestString(slot.Row()[0]))
				}
				if err := op.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
				sort.Strings(got)
				if len(got) != len(serial) {
					t.Fatalf("got %d rows, serial %d — a SubPlan saw only part of its relation, or a worker re-emitted rows", len(got), len(serial))
				}
				for i := range got {
					if got[i] != serial[i] {
						t.Fatalf("row %d: got %q, serial %q", i, got[i], serial[i])
					}
				}
			})
		}
	}
}
