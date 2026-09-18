package executor

// M0140-0006c: the executor claim-set for a partial *setOp under Gather.
//
// gatherOp has each worker build its own FULL copy of the child subtree —
// safe only because parallel_scan.go's attachAll hands each worker a
// DISJOINT claim over the driving scan. Before this task, *setOp had no arm
// in attachAll at all: every worker would replay BOTH entire UNION ALL
// branches, and the Gather would return (workers+1) copies of every row —
// the exact defect TestParallelIdentityWithRealGather's header describes for
// a bare scan, reproduced here for the two-branch shape.
//
// Two DIFFERENT tables (deliberately different row COUNTS) drive the two
// branches, so a claim-state bug that accidentally shares one
// parallelScanState between them — letting one branch's block-count
// boundary leak into the other's (parallelScanState.initOnce publishes
// exactly once) — would show up as a wrong row count or a wrong value, not
// just as a slow query.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// parallelSetOpFixture builds two tables of DIFFERENT sizes so the two
// branches' claim state cannot be silently shared without a row count or
// value tripping.
func parallelSetOpFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	if err := runDDL(t, ctx, "CREATE TABLE pq_setop_a (id int)"); err != nil {
		cleanup()
		t.Fatalf("create a: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE pq_setop_b (id int)"); err != nil {
		cleanup()
		t.Fatalf("create b: %v", err)
	}
	for i := 0; i < 260; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO pq_setop_a VALUES (%d)", i)); err != nil {
			cleanup()
			t.Fatalf("insert a %d: %v", i, err)
		}
	}
	for i := 0; i < 90; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO pq_setop_b VALUES (%d)", 100000+i)); err != nil {
			cleanup()
			t.Fatalf("insert b %d: %v", i, err)
		}
	}
	return ctx, cleanup
}

// planSetOpTestNode builds a bare optimizer.SetOp{UNION ALL} over the two
// fixture tables' plans, so the test drives the executor directly without
// depending on the optimizer ever CHOOSING a partial SetOp path itself
// (M0140-0006b-2's reachability gap means it cannot yet).
func planSetOpTestNode(t *testing.T, ctx *Context) *optimizer.SetOp {
	t.Helper()
	left := planForTest(t, ctx, "SELECT id FROM pq_setop_a")
	right := planForTest(t, ctx, "SELECT id FROM pq_setop_b")
	return &optimizer.SetOp{Left: left, Right: right, Op: parser.SetOpUnion, All: true}
}

// TestGatherOverSetOpIdentity is the M0140-0006c gate: every row from BOTH
// branches exactly once, over a Gather whose partial subtree is a streaming
// UNION ALL SetOp.
//
// Before the claim-set fix this either panicked (no driving scan attached,
// each worker fully replaying both branches) or returned (workers+1)x the
// rows, depending on the shape — the N-copies defect this file's header
// describes.
func TestGatherOverSetOpIdentity(t *testing.T) {
	ctx, cleanup := parallelSetOpFixture(t)
	defer cleanup()

	const wantTotal = 260 + 90

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			advanceStmtCounter(ctx)
			so := planSetOpTestNode(t, ctx)
			gathered := optimizer.NewGather(0, so, workers)

			ctx.MaxParallelWorkers = 8
			ctx.ParallelLeaderParticipation = true
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			seen := map[string]int{}
			n := 0
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("next: %v", err)
				}
				seen[datumTestString(slot.Row()[0])]++
				n++
			}
			if err := op.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			if n != wantTotal {
				t.Fatalf("got %d rows, want %d — a multiple means a worker replayed a "+
					"whole branch instead of its partition (the pre-fix N-copies defect)",
					n, wantTotal)
			}
			for v, c := range seen {
				if c != 1 {
					t.Fatalf("row %s returned %d times; each branch's claim set must hand "+
						"each block to exactly one worker", v, c)
				}
			}
			// Sanity: both branches' id ranges are actually represented, so a bug
			// that dropped one branch entirely (rather than duplicating) would
			// also be caught.
			if _, ok := seen[datumTestString(NewIntDatum(5))]; !ok {
				t.Errorf("missing a row from pq_setop_a (id=5); left branch was not read")
			}
			if _, ok := seen[datumTestString(NewIntDatum(100005))]; !ok {
				t.Errorf("missing a row from pq_setop_b (id=100005); right branch was not read")
			}
		})
	}
}
