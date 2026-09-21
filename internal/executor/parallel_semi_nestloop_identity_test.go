package executor

// The parallel SEMI nested-loop identity pin (2026-09-21).
//
// This closes a LIVE WRONG-ANSWER divergence found while doing M0145-0010's
// scope (d) "executor-capability check FIRST". M0137-0019b admitted SEMI on
// the planner side — `nestedLoopJoinIsPartialCapable` and
// `partialPathDrivingKind`'s PathNestLoop arm — and left the executor twin
// `ordinaryInnerNestedLoopPartial` at INNER.
//
// That is not a safe decline. The attach walk returning false leaves the
// driving scan UNATTACHED, and `attachAll`'s result is ignored by `gatherOp`
// (ledger `e10-attachall-precondition-unenforced`), so every worker scanned
// the WHOLE outer and the node emitted N copies. Measured at HEAD on the SF1
// clone: `count(*)` over a plain `Nested Loop Semi Join` returned 450000
// against a serial and ground-truth 150000 — exactly 3x with 3 workers.
//
// It is the same failure mode this package's parallel_identity_test.go header
// describes from the feature's first days ("the Gather returned N copies of
// every row"), one gate later — which is why the pin is an IDENTITY test and
// not a predicate test: it catches the divergence whichever of the four gates
// drifts, rather than re-asserting one side's truth table.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// semiNLFixture builds an outer worth partitioning and a tiny inner. The
// correlation is a NON-equality (`>`), which is what keeps the planner on a
// plain nested loop instead of electing a hash semi join, and the inner column
// is unindexed so no parameterized probe (`*NestedLoopIndexJoin`, a different
// node that never reaches the walk under test) is available.
func semiNLFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{
		"CREATE TABLE snl_outer (id int, seg text)",
		"CREATE TABLE snl_inner (nm text)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	for i := 0; i < 400; i++ {
		if err := runDDL(t, ctx,
			fmt.Sprintf("INSERT INTO snl_outer VALUES (%d, 'AAA')", i)); err != nil {
			cleanup()
			t.Fatalf("insert outer %d: %v", i, err)
		}
	}
	// Every outer row qualifies, so the correct answer is exactly the outer
	// row count — which makes an N-copy regression unmistakable rather than
	// merely "different".
	for _, v := range []string{"MMM", "ZZZ"} {
		if err := runDDL(t, ctx,
			fmt.Sprintf("INSERT INTO snl_inner VALUES ('%s')", v)); err != nil {
			cleanup()
			t.Fatalf("insert inner: %v", err)
		}
	}
	return ctx, cleanup
}

// planHasPlainSemiNestedLoop reports whether the tree contains an ordinary
// (non-lateral, non-parameterized) SEMI nested-loop `*optimizer.Join` — the
// exact node `ordinaryInnerNestedLoopPartial` gates. Asserting this is what
// keeps the identity comparison from being vacuous: without it the test could
// pass by planning some other shape entirely.
func planHasPlainSemiNestedLoop(n optimizer.Node) bool {
	if n == nil {
		return false
	}
	switch x := n.(type) {
	case *optimizer.Join:
		if x.Algo == optimizer.JoinAlgoNestedLoop && x.Type == optimizer.JoinTypeSemi && !x.Lateral {
			return true
		}
		return planHasPlainSemiNestedLoop(x.Left) || planHasPlainSemiNestedLoop(x.Right)
	case *optimizer.Project:
		return planHasPlainSemiNestedLoop(x.Child)
	case *optimizer.Filter:
		return planHasPlainSemiNestedLoop(x.Child)
	case *optimizer.Sort:
		return planHasPlainSemiNestedLoop(x.Child)
	case *optimizer.Limit:
		return planHasPlainSemiNestedLoop(x.Child)
	case *optimizer.Aggregate:
		return planHasPlainSemiNestedLoop(x.Child)
	case *optimizer.Gather:
		return planHasPlainSemiNestedLoop(x.Child)
	}
	return false
}

func TestParallelSemiNestedLoopIdentity(t *testing.T) {
	ctx, cleanup := semiNLFixture(t)
	defer cleanup()

	const sql = "SELECT id FROM snl_outer o WHERE EXISTS (SELECT 1 FROM snl_inner i WHERE i.nm > o.seg)"

	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := len(serialRows)
	if want != 400 {
		t.Fatalf("fixture: serial returned %d rows, want 400 — every outer row should qualify exactly once", want)
	}

	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Fail rather than skip. A pin that silently skips when the shape moves
	// proves nothing, and this shape is the whole point of the test — if a
	// planner change stops electing it, that is a decision for a human to
	// make deliberately, not something to pass over quietly.
	if !planHasPlainSemiNestedLoop(node) {
		t.Fatalf("planner no longer elects a plain SEMI nested loop for this fixture; "+
			"the identity pin would be vacuous. Re-shape the fixture or retire the pin deliberately.\nplan: %#v", node)
	}

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			stmts, err := parser.Parse(sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			gathered := optimizer.NewGather(0, plan, workers)
			ctx.MaxParallelWorkers = 8
			ctx.ParallelLeaderParticipation = true
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			defer op.Close()
			n := 0
			for {
				_, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("next: %v", err)
				}
				n++
			}
			if n != want {
				t.Fatalf("parallel SEMI nested loop returned %d rows, want %d (serial). "+
					"%d workers each scanning the whole outer is the N-copy signature of an "+
					"unattached driving scan — the executor/planner partial-capability gates have diverged again",
					n, want, workers)
			}
		})
	}
}
