package executor

// The FUSED nested-loop-index-join parallel identity pin (M0145-0010).
//
// This family is the parameterized-probe twin of the ordinary partial nested
// loop: `*optimizer.NestedLoopIndexJoin`, driven by `nestedLoopIndexJoinOp`,
// partial through its OUTER only while the inner probe re-opens per outer row.
//
// It is structured better than the ordinary family and that is worth stating,
// because it is why the 2026-09-21 SEMI wrong answer could not happen here: the
// executor arm calls the PLANNER's exported predicate directly
// (`optimizer.NestedLoopIndexJoinIsPartialCapable`) rather than keeping its own
// copy — "literal agreement, no twin to drift", as its comment says. Widening
// that one predicate therefore widens both sides at once.
//
// What a shared predicate cannot tell you is whether the OPERATOR can drive a
// newly admitted jointype per worker. That is what this test measures, and
// M0145-0010 scope (d) requires it BEFORE admission rather than after.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// nliJointypeFixture gives the probe an INDEXED correlation, which is what
// makes the planner fuse the join into a parameterized index probe instead of
// electing an ordinary nested loop or a hash join.
func nliJointypeFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{
		"CREATE TABLE nlij_outer (id int, k int)",
		"CREATE TABLE nlij_inner (k int, v int)",
		"CREATE INDEX nlij_inner_k ON nlij_inner (k)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	for i := 0; i < 400; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO nlij_outer VALUES (%d, %d)", i, i)); err != nil {
			cleanup()
			t.Fatalf("insert outer: %v", err)
		}
	}
	// Only the first 50 outer keys have a match, so SEMI/ANTI/LEFT each give a
	// different, easily-checked row count and an N-copy regression cannot hide
	// behind a coincidence.
	for i := 0; i < 50; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO nlij_inner VALUES (%d, %d)", i, i*2)); err != nil {
			cleanup()
			t.Fatalf("insert inner: %v", err)
		}
	}
	return ctx, cleanup
}

// topNLIJointype reports the jointype of the fused NLI below the root, so a
// fixture can assert WHICH shape it exercises. A plain `*Join` here means the
// planner did not fuse and the test would be measuring the wrong family.
func topNLIJointype(n optimizer.Node) (optimizer.JoinType, bool) {
	switch x := n.(type) {
	case *optimizer.NestedLoopIndexJoin:
		return x.Type, true
	case *optimizer.Project:
		return topNLIJointype(x.Child)
	case *optimizer.Filter:
		return topNLIJointype(x.Child)
	}
	return 0, false
}

func TestParallelNLIJointypeIdentity(t *testing.T) {
	ctx, cleanup := nliJointypeFixture(t)
	defer cleanup()

	for _, tc := range []struct {
		name string
		sql  string
		want optimizer.JoinType
	}{
		{"semi", "SELECT id FROM nlij_outer o WHERE EXISTS (SELECT 1 FROM nlij_inner i WHERE i.k = o.k)", optimizer.JoinTypeSemi},
		{"anti", "SELECT id FROM nlij_outer o WHERE NOT EXISTS (SELECT 1 FROM nlij_inner i WHERE i.k = o.k)", optimizer.JoinTypeAnti},
		// The `AND o.id > 0` keeps the planner off the LEFT->RIGHT
		// commutation; without it this plans as a hash join and the fixture
		// would silently stop exercising the fused family.
		{"left", "SELECT o.id FROM nlij_outer o LEFT JOIN nlij_inner i ON i.k = o.k AND o.id > 0", optimizer.JoinTypeLeft},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serialRows, err := runQueryWithErr(ctx, tc.sql)
			if err != nil {
				t.Fatalf("serial: %v", err)
			}
			want := len(serialRows)
			if want == 0 {
				t.Fatal("fixture produced no rows; the comparison would be vacuous")
			}

			stmts0, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			plan0, err := optimizer.Plan(stmts0[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			got, isNLI := topNLIJointype(plan0)
			if !isNLI || got != tc.want {
				t.Fatalf("%s: planner produced fused=%v jointype=%v, want a fused NLI of type %v — "+
					"the fixture no longer exercises this family", tc.name, isNLI, got, tc.want)
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
						"the N-copy signature of an unattached driving scan on the FUSED NLI path",
						tc.name, workers, n, want)
				}
			}
		})
	}
}
