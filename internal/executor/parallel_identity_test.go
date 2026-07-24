package executor

// P6 of docs/design/parallel-query/ — the identity gate.
//
// The primary correctness property of the whole feature: a parallel plan must
// return exactly what its serial counterpart returns. These tests run the same
// query twice against the same data, once with Gather insertion forced on and
// once off, and compare.
//
// This is the check that caught the feature's worst bug. When the Gather and
// the block allocator were first connected, nothing wired the allocator INTO
// the child trees — each worker built an ordinary serial scan and read the
// whole relation, so the Gather returned N copies of every row (240298 where
// serial returned 120149, on real TPC-H data). Nothing else in the test suite
// would have noticed: the plan looked right, no assertion failed, no race
// fired. Only comparing against serial did.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// parallelIdentityFixture builds a table big enough to be worth scanning, with
// values chosen so aggregates over it are exact (no float rounding) and so the
// row set is easy to reason about.
func parallelIdentityFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	if err := runDDL(t, ctx, "CREATE TABLE pq_ident (id int, grp int, v int, s text)"); err != nil {
		cleanup()
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 400; i++ {
		stmt := fmt.Sprintf("INSERT INTO pq_ident VALUES (%d, %d, %d, 'row-%d')",
			i, i%7, i*3, i)
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	return ctx, cleanup
}

// runForced runs sql with Gather insertion forced on or off and returns the
// rendered rows.
func runForced(t *testing.T, ctx *Context, sql string, parallel bool) []string {
	t.Helper()
	prev := planner.ParallelEnabled()
	planner.SetParallelEnabled(parallel)
	defer planner.SetParallelEnabled(prev)

	rows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("%s (parallel=%v): %v", sql, parallel, err)
	}
	return renderRows(rows)
}

// TestParallelSerialIdentity is the gate. Every query here is run both ways
// and must agree exactly.
//
// Note what is NOT asserted: that a Gather was actually inserted. These
// fixtures are small and statistics are absent, so the size gate legitimately
// declines — and a test that silently compares serial against serial proves
// nothing. TestParallelIdentityWithRealGather below closes that hole by
// driving the operator directly.
func TestParallelSerialIdentity(t *testing.T) {
	ctx, cleanup := parallelIdentityFixture(t)
	defer cleanup()

	for _, sql := range []string{
		"SELECT count(*) FROM pq_ident",
		"SELECT count(*) FROM pq_ident WHERE v > 600",
		"SELECT sum(v), min(v), max(v) FROM pq_ident",
		"SELECT id, v FROM pq_ident WHERE id < 10 ORDER BY id",
		"SELECT grp, count(*), sum(v) FROM pq_ident GROUP BY grp ORDER BY grp",
		"SELECT s FROM pq_ident WHERE id = 42",
		"SELECT count(*) FROM pq_ident WHERE s LIKE 'row-1%'",
	} {
		t.Run(sql, func(t *testing.T) {
			serial := runForced(t, ctx, sql, false)
			parallel := runForced(t, ctx, sql, true)
			if len(serial) != len(parallel) {
				t.Fatalf("row count differs: serial %d, parallel %d\nserial=%v\nparallel=%v",
					len(serial), len(parallel), serial, parallel)
			}
			for i := range serial {
				if serial[i] != parallel[i] {
					t.Fatalf("row %d differs: serial %q, parallel %q", i, serial[i], parallel[i])
				}
			}
		})
	}
}

// TestParallelIdentityWithRealGather drives the Gather operator directly over
// a real table scan, so a Gather is definitely present — the size gate cannot
// quietly turn this into a serial-vs-serial comparison.
//
// This is the shape that reproduced the duplicate-rows bug.
func TestParallelIdentityWithRealGather(t *testing.T) {
	ctx, cleanup := parallelIdentityFixture(t)
	defer cleanup()

	serialRows, err := runQueryWithErr(ctx, "SELECT id FROM pq_ident")
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := len(serialRows)
	if want != 400 {
		t.Fatalf("fixture: got %d rows, want 400", want)
	}

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			stmts, err := parser.Parse("SELECT id FROM pq_ident")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			node, err := planner.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			gathered := planner.NewGather(0, node, workers)

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

			// The union of the workers' output must be the relation, ONCE.
			if n != want {
				t.Errorf("got %d rows, want %d — a %.2gx multiple means every worker "+
					"scanned the whole relation instead of its partition",
					n, want, float64(n)/float64(want))
			}
			for v, c := range seen {
				if c != 1 {
					t.Errorf("row %s returned %d times; the block allocator must "+
						"hand each block to exactly one worker", v, c)
					break
				}
			}
		})
	}
}
