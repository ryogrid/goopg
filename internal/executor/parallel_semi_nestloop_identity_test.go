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

// TestParallelLeftAntiNestedLoopIdentity is the executor-capability check
// M0145-0010 scope (d) demands BEFORE a shape is admitted, for the two
// jointypes goopg still refuses that PG admits: LEFT and ANTI
// (`joinpath.c:1842-1846` admits {INNER, LEFT, SEMI, ANTI}).
//
// Unlike the SEMI case these are NOT reachable today — all four gates refuse
// them, so no Gather is ever built over one and there is no live defect. This
// test forces the Gather directly, which is what lets it answer the question
// the whitelist cannot: CAN the executor drive them per-worker correctly?
//
// The structural argument says yes — `fillInner`, the cross-worker inner-match
// reduction, is set only for RIGHT/FULL (`join_nl_stream.go`), and `markInner`
// is called only under it, so LEFT's null-extension and ANTI's no-match
// verdict are both per-outer-row and worker-local. This test is the
// measurement behind that argument.
func TestParallelLeftAntiNestedLoopIdentity(t *testing.T) {
	ctx, cleanup := semiNLFixture(t)
	defer cleanup()

	for _, tc := range []struct {
		name string
		sql  string
		want optimizer.JoinType
	}{
		{
			// ANTI: no inner row satisfies `nm < 'AAA'`, so every outer row
			// survives exactly once.
			name: "anti",
			sql:  "SELECT id FROM snl_outer o WHERE NOT EXISTS (SELECT 1 FROM snl_inner i WHERE i.nm < o.seg)",
			want: optimizer.JoinTypeAnti,
		},
		{
			// LEFT: a non-equality lateral-free left join, one outer row in,
			// at least one row out, never duplicated across workers.
			//
			// The extra `o.id > 0` matters and is not decoration. Without it
			// the planner COMMUTES this into a RIGHT join (jointype 2, not 1)
			// — which is correctly refused by every gate, because RIGHT needs
			// the cross-worker inner-match reduction. A fixture that yields
			// the commuted shape would test the refusal, not the admission,
			// and would look like a LEFT failure while the code was right.
			name: "left",
			sql:  "SELECT o.id FROM snl_outer o LEFT JOIN snl_inner i ON i.nm < o.seg AND o.id > 0",
			want: optimizer.JoinTypeLeft,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serialRows, err := runQueryWithErr(ctx, tc.sql)
			if err != nil {
				t.Fatalf("serial: %v", err)
			}
			want := len(serialRows)
			if want == 0 {
				t.Fatalf("fixture produced no rows; the comparison would be vacuous")
			}
			// Pin the jointype actually planned. A commuted shape would make
			// this test assert the wrong thing silently.
			stmts0, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			plan0, err := optimizer.Plan(stmts0[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if got := topNestedLoopJointype(plan0); got != tc.want {
				t.Fatalf("%s: planner produced jointype %v, want %v — the fixture no longer exercises this shape",
					tc.name, got, tc.want)
			}
			for _, workers := range []int{1, 2, 4} {
				stmts, err := parser.Parse(tc.sql)
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
				n := 0
				for {
					_, err := op.Next()
					if err == EOF {
						break
					}
					if err != nil {
						op.Close()
						t.Fatalf("next: %v", err)
					}
					n++
				}
				op.Close()
				if n != want {
					t.Fatalf("%s workers=%d: parallel returned %d rows, want %d (serial) — "+
						"the N-copy signature of an unattached driving scan",
						tc.name, workers, n, want)
				}
			}
		})
	}
}

// topNestedLoopJointype reports the jointype of the first nested-loop join
// below the root, so a fixture can assert WHICH shape it exercises.
func topNestedLoopJointype(n optimizer.Node) optimizer.JoinType {
	switch x := n.(type) {
	case *optimizer.Join:
		return x.Type
	case *optimizer.Project:
		return topNestedLoopJointype(x.Child)
	case *optimizer.Filter:
		return topNestedLoopJointype(x.Child)
	}
	return optimizer.JoinTypeCross // a value none of these fixtures expect
}
