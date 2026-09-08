package executor

import (
	"strings"
	"testing"
)

// TestCoopParallelHashBuildValuesAcrossWorkMem is the regression test for a
// SILENT WRONG ANSWER: a hash join whose build side carries a restriction
// returned the right number of rows with a NULL payload in every column.
//
// Mechanism. `parallelBuildLazyHashTable` rebuilds the build-side subtree from
// the plan in each producer goroutine, and it did so through `BuildWorker`,
// which restarts the EX1-02 deform walk at the ROOT bound. A build side of the
// shape `Filter(dk > 3) -> SeqScan(dk, dname)` therefore looked, to that fresh
// walk, as though `dk` were the only column anyone reads: the scan deformed
// [0,1) and `dname` was never materialised. The rows went into the hash table
// with a NULL payload and the join emitted them faithfully.
//
// The bound cannot be re-derived at build time because it depends on the
// `incoming` bound from everything ABOVE the join, so the tree builders now
// stamp it (`joinOp.deformLeftBound` / `deformRightBound`) and this path
// rebuilds at the recorded bound.
//
// Why this test asserts VALUES and sweeps work_mem, and why both matter:
//
//   - Row counts were CORRECT throughout — 147 of 147, every time. A
//     count-based assertion sails straight past this bug, which is how it
//     survived. This repo has form here: 21 of 21 TPC-H result sets once
//     stayed byte-identical while Q2 went 43x slower.
//   - The failing region is NOT the low-memory edge it first looked like.
//     Measured on this fixture before the fix, 25 of 32 work_mem values
//     produced 147/147 NULL payloads, including 1 GiB — which is goopg's own
//     effective hash budget at the default work_mem of 512 MiB with
//     hash_mem_multiplier 2.0. Only a narrow band around 1 KiB..64 KiB was
//     correct, so a spot check inside that band reads as "fine at realistic
//     settings" and is exactly the wrong conclusion. Hence the sweep.
func TestCoopParallelHashBuildValuesAcrossWorkMem(t *testing.T) {
	// A build side with a restriction over it: the Filter is what makes the
	// root-bound walk believe `dk` is the only interesting column.
	const sql = "SELECT f.fid, d.dname FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk WHERE d.dk > 3"

	// 0 (the zero value of a hand-built Context), then powers of two up past
	// the 1 GiB effective budget the default work_mem produces.
	workMems := []int64{0}
	for e := 0; e <= 30; e++ {
		workMems = append(workMems, int64(1)<<uint(e))
	}

	for _, wm := range workMems {
		wm := wm
		t.Run(workMemName(wm), func(t *testing.T) {
			ctx, cleanup := pqJoinFixture(t)
			defer cleanup()
			ctx.WorkMem = wm

			rows, err := runQueryWithErr(ctx, sql)
			if err != nil {
				t.Fatalf("work_mem=%d: %v", wm, err)
			}
			rendered := renderRows(rows)

			if len(rendered) != 147 {
				t.Errorf("work_mem=%d: %d rows, want 147", wm, len(rendered))
			}
			// The payload assertion is the point. `d.dname` is non-NULL for
			// every dimension row this predicate admits, so ANY NULL here is
			// the bug.
			nulls := 0
			for _, r := range rendered {
				if strings.HasSuffix(r, "|NULL") {
					nulls++
				}
			}
			if nulls != 0 {
				t.Errorf("work_mem=%d: %d of %d rows have a NULL d.dname payload — "+
					"the build side was rebuilt at the wrong deform bound, so its "+
					"payload columns were never materialised. Row COUNT is correct "+
					"in this failure mode, which is why it needs a values assertion",
					wm, nulls, len(rendered))
			}
		})
	}
}

func workMemName(wm int64) string {
	switch {
	case wm == 0:
		return "unset"
	case wm < 1024:
		return "tiny"
	case wm < 1<<20:
		return "kib"
	case wm < 1<<30:
		return "mib"
	default:
		return "gib"
	}
}

// TestCoopParallelHashBuildSpills is E-18 slice 1's "the newly-reachable path
// actually fires" assertion.
//
// Before slice 1, parallelBuildEligible declined any build whose geometry
// predicted more than one batch ("Rule 2"), on the ground that a spilling build
// could not be shared. E-09a/E-09b made spilling builds shareable, and the
// cooperative build spills entirely in its single consumer goroutine (the
// producers only scan and filter), so the rule had nothing left to protect.
//
// Retiring a rule is only half the change: a path that is now reachable but
// never taken is untested. This test drives the fixture at a work_mem small
// enough to force batching and asserts that a cooperative build really did end
// with a batch descriptor — and, because this repo's standing lesson is that
// row counts sail past payload corruption, that the values are still right.
func TestCoopParallelHashBuildSpills(t *testing.T) {
	const sql = "SELECT f.fid, d.dname FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk WHERE d.dk > 3"

	before := CoopSpillingBuildCount()

	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()
	// Small enough that the build geometry predicts more than one batch.
	ctx.WorkMem = 1

	rows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("spilling coop build: %v", err)
	}
	rendered := renderRows(rows)
	if len(rendered) != 147 {
		t.Fatalf("%d rows, want 147", len(rendered))
	}
	for _, r := range rendered {
		if strings.HasSuffix(r, "|NULL") {
			t.Fatalf("NULL payload in %q — the spilling cooperative build lost "+
				"its payload columns", r)
		}
	}

	if got := CoopSpillingBuildCount(); got == before {
		t.Fatalf("cooperative spilling build count did not move (%d): the path "+
			"E-18 slice 1 opened was never taken, so this test proves nothing "+
			"about it. Either the fixture no longer batches at work_mem=1 or "+
			"eligibility declines it for another reason", got)
	}
}
