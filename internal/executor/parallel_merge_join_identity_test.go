package executor

// E-20 Cut 3 — partial merge join identity gate.
//
// The executor arm (`attachParallelScan` JoinAlgoMerge: descend the OUTER
// side explicitly) has exactly one load-bearing property: each worker
// merge-joins ITS outer partition against the WHOLE inner. The two failure
// modes announce themselves oppositely and both silently:
//
//   - attach lands on the INNER side (or on a build side by BuildLeft
//     confusion): each worker merges a partition of the inner against the
//     whole outer and the Gather returns N copies of every row;
//   - the outer is not partitioned at all: same N-copies symptom.
//
// So the gate is serial-vs-parallel identity over a merge corpus, with a
// non-vacuity guard that a Merge Join node actually ran under the Gather.
// `SET enable_hashjoin = off` (+ nestloop off) forces the planner onto
// merge joins; without it the small fixtures plan hash and the test would
// compare serial-hash against parallel-hash — valid but not this arm.

import (
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// planMergeForced plans sql with hash and nested-loop joins disabled, so
// the planner must shape equi-joins as merge joins. DefaultPlannerSettings
// plus two flips — everything else identical to production planning.
func planMergeForced(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin = false
	ps.EnableNestLoop = false
	node, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !planTreeHasMergeJoin(node) {
		t.Fatalf("forced plan for %q contains no Merge Join; the identity comparison would not exercise the merge arm", sql)
	}
	return node
}

func drainPlan(t *testing.T, ctx *Context, node optimizer.Node) []string {
	t.Helper()
	op, err := Build(node)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer op.Close()
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
	return out
}

// runMergeGathered mirrors runJoinGathered for a forced-merge plan: wrap the
// plan in a Gather with the given worker count and drain it. Returns the rows,
// whether a Gather was placed, and the gathered plan for structural asserts.
func runMergeGathered(t *testing.T, ctx *Context, sql string, workers int) ([]string, bool, optimizer.Node) {
	t.Helper()
	node := planMergeForced(t, ctx, sql)
	gathered := optimizer.MaybeAddGather(node, optimizer.ParallelSettings{
		MaxWorkersPerGather: workers,
		MinTableScanBlocks:  1,
		DebugParallelQuery:  "on",
		BlocksForTable:      func(*catalog.Table) (int64, bool) { return 4096, true },
	})
	hasGather := planTreeHasParallelNode(gathered)
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	return drainPlan(t, ctx, gathered), hasGather, gathered
}

// pqMergeCorpus is the merge-join shape list: only shapes that actually
// plan a Merge Join with hash/nestloop disabled belong here — the forcing
// guard in planMergeForced refuses anything else, so a shape the planner
// will not shape as merge cannot silently dilute this gate into a
// hash-identity test (which parallel_hash_join_test.go already owns).
// Covered: filtered INNER, LEFT with projection, LEFT count, over the
// NULL-carrying pq fixture (null placement + null padding under workers).
func pqMergeCorpus() []string {
	return []string{
		"SELECT f.fid FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk WHERE f.amt > 200",
		"SELECT f.fid, d.dname FROM pq_fact f LEFT JOIN pq_dim d ON f.fk = d.dk",
		"SELECT count(*) FROM pq_fact f LEFT JOIN pq_dim d ON f.fk = d.dk",
	}
}

func planTreeHasMergeJoin(n optimizer.Node) bool {
	found := false
	var walk func(optimizer.Node)
	walk = func(cur optimizer.Node) {
		if cur == nil || found {
			return
		}
		if j, ok := cur.(*optimizer.Join); ok && j.Algo == optimizer.JoinAlgoMerge {
			found = true
			return
		}
		for _, c := range optimizer.ParallelChildrenForTest(cur) {
			walk(c)
		}
	}
	walk(n)
	return found
}

// planTreeHasMergeUnderGather reports whether a Merge Join node sits inside
// a Gather/GatherMerge subtree. Identity against a plan whose Gather sits
// BELOW the merge (parallel scans feeding a serial merge in the leader)
// would pass while never partitioning the merge's outer — the exact
// vacuity this gate must not have.
func planTreeHasMergeUnderGather(n optimizer.Node) bool {
	found := false
	var walk func(optimizer.Node, bool)
	walk = func(cur optimizer.Node, underGather bool) {
		if cur == nil || found {
			return
		}
		switch cur.(type) {
		case *optimizer.Gather, *optimizer.GatherMerge:
			underGather = true
		}
		if j, ok := cur.(*optimizer.Join); ok && j.Algo == optimizer.JoinAlgoMerge && underGather {
			found = true
			return
		}
		for _, c := range optimizer.ParallelChildrenForTest(cur) {
			walk(c, underGather)
		}
	}
	walk(n, false)
	return found
}

// TestParallelMergeJoinIdentity runs the merge corpus serially and gathered
// (workers 1/2/4) and demands identical rows. Fewer rows means the attach
// landed where a partition goes missing; MORE rows (the N-copies signature)
// means a side was not partitioned at all.
func TestParallelMergeJoinIdentity(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	merged := 0
	for _, sql := range pqMergeCorpus() {
		t.Run(sql, func(t *testing.T) {
			// Serial baseline is itself a FORCED-merge plan (planMergeForced
			// refuses a non-merge plan), so both arms run the same join
			// algorithm and only the partitioning differs.
			want := drainPlan(t, ctx, planMergeForced(t, ctx, sql))
			sort.Strings(want)

			for _, workers := range []int{1, 2, 4} {
				got, hasGather, gathered := runMergeGathered(t, ctx, sql, workers)
				if hasGather {
					merged++
					// Non-vacuity per arm: the merge must run UNDER the
					// Gather (partitioned outer), not above it (parallel
					// scans feeding a serial merge in the leader).
					if !planTreeHasMergeUnderGather(gathered) {
						t.Fatalf("workers=%d: Gather present but no Merge Join under it", workers)
					}
				}
				sort.Strings(got)
				if len(got) != len(want) {
					t.Fatalf("workers=%d: got %d rows, want %d", workers, len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("workers=%d: row %d differs: got %q, want %q",
							workers, i, got[i], want[i])
					}
				}
			}
		})
	}
	if merged == 0 {
		t.Fatal("no query in the corpus produced a Gather; the identity " +
			"comparison was serial against serial and asserts nothing")
	}
}
