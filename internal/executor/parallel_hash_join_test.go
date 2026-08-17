package executor

// P8 of docs/design/parallel-query/ — parallel hash join.
//
// The build side runs once, in the leader, and every worker probes the same
// frozen table by pointer. Two failure modes drive these tests, and neither
// announces itself:
//
//   - The parallel scan lands on the BUILD side instead of the probe side.
//     Each worker then hashes a PARTITION of the build input, so its table is
//     missing most of its rows and the join quietly drops matches.
//   - A build-computed SCALAR is not carried into the workers. antiBuildHasNull
//     is the dangerous one: it decides NOT IN's three-valued-NULL result, and a
//     worker defaulting it to false returns rows where SQL says the answer is
//     empty.
//
// Both produce a plausible, smaller result set with no error anywhere, so the
// gate has to be serial-vs-parallel identity over a corpus that includes the
// NULL cases.

import (
	"fmt"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// pqJoinFixture builds a small dimension and a larger fact table, plus NULLs on
// both sides so the NOT IN / anti-join paths are genuinely exercised.
func pqJoinFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	fail := func(err error, what string) {
		cleanup()
		t.Fatalf("%s: %v", what, err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE pq_dim (dk int, dname text)"); err != nil {
		fail(err, "create dim")
	}
	if err := runDDL(t, ctx, "CREATE TABLE pq_fact (fid int, fk int, amt int)"); err != nil {
		fail(err, "create fact")
	}
	// 20 dimension rows, one of which has a NULL key — the row that makes
	// NOT IN return the empty set and that a worker missing antiBuildHasNull
	// would ignore.
	for i := 0; i < 20; i++ {
		k := fmt.Sprintf("%d", i)
		if i == 7 {
			k = "NULL"
		}
		if err := runDDL(t, ctx,
			fmt.Sprintf("INSERT INTO pq_dim VALUES (%s, 'd-%d')", k, i)); err != nil {
			fail(err, "insert dim")
		}
	}
	// 400 fact rows spanning blocks, ~half with no matching dimension key so
	// LEFT-join null-padding and anti-join both have work to do.
	for i := 0; i < 400; i++ {
		fk := fmt.Sprintf("%d", i%40) // keys 20..39 have no dimension row
		if i%53 == 0 {
			fk = "NULL"
		}
		if err := runDDL(t, ctx,
			fmt.Sprintf("INSERT INTO pq_fact VALUES (%d, %s, %d)", i, fk, i*2)); err != nil {
			fail(err, "insert fact")
		}
	}
	return ctx, cleanup
}

// pqJoinCorpus is the shape list. Each must return the same rows serially and
// in parallel.
func pqJoinCorpus() []string {
	return []string{
		// INNER
		"SELECT f.fid, d.dname FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk",
		"SELECT count(*) FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk",
		"SELECT f.fid FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk WHERE f.amt > 200",
		// LEFT, outer on the probe side — the only LEFT shape the lazy-hash
		// runtime implements, and the only one P8 approves.
		"SELECT f.fid, d.dname FROM pq_fact f LEFT JOIN pq_dim d ON f.fk = d.dk",
		"SELECT count(*) FROM pq_fact f LEFT JOIN pq_dim d ON f.fk = d.dk",
		// SEMI
		"SELECT f.fid FROM pq_fact f WHERE EXISTS (SELECT 1 FROM pq_dim d WHERE d.dk = f.fk)",
		// ANTI
		"SELECT f.fid FROM pq_fact f WHERE NOT EXISTS (SELECT 1 FROM pq_dim d WHERE d.dk = f.fk)",
		// NULL-aware ANTI (NOT IN). pq_dim has a NULL key, so the correct
		// answer is the EMPTY set — a worker that lost antiBuildHasNull
		// returns hundreds of rows instead.
		"SELECT f.fid FROM pq_fact f WHERE f.fk NOT IN (SELECT d.dk FROM pq_dim d)",
		// NOT IN over a NULL-free subquery, so the non-empty branch of the
		// same flag is covered too.
		"SELECT f.fid FROM pq_fact f WHERE f.fk NOT IN (SELECT d.dk FROM pq_dim d WHERE d.dk IS NOT NULL)",
	}
}

// runJoinGathered plans sql, wraps the largest partial subtree the planner
// would choose in a Gather with the given worker count, and returns the rows.
//
// It goes through planner.MaybeAddGather with a forced size gate rather than
// hand-placing the Gather, so the placement rule itself is under test — if
// findPartialSubtree picked the build side, this is where it shows.
func runJoinGathered(t *testing.T, ctx *Context, sql string, workers int) ([]string, bool) {
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
	gathered := optimizer.MaybeAddGather(node, optimizer.ParallelSettings{
		MaxWorkersPerGather: workers,
		MinTableScanBlocks:  1,
		DebugParallelQuery:  "on", // force past the size gate; fixtures are small
		BlocksForTable:      func(*catalog.Table) (int64, bool) { return 4096, true },
	})
	hasGather := planTreeHasParallelNode(gathered)

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
	return out, hasGather
}

func planTreeHasParallelNode(n optimizer.Node) bool {
	found := false
	var walk func(optimizer.Node)
	walk = func(cur optimizer.Node) {
		if cur == nil || found {
			return
		}
		switch cur.(type) {
		case *optimizer.Gather, *optimizer.GatherMerge:
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

// TestParallelHashJoinIdentity is the gate.
func TestParallelHashJoinIdentity(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	gathered := 0
	for _, sql := range pqJoinCorpus() {
		t.Run(sql, func(t *testing.T) {
			serialRows, err := runQueryWithErr(ctx, sql)
			if err != nil {
				t.Fatalf("serial: %v", err)
			}
			want := renderRows(serialRows)
			sort.Strings(want)

			for _, workers := range []int{1, 2, 4} {
				got, hasGather := runJoinGathered(t, ctx, sql, workers)
				if hasGather {
					gathered++
				}
				sort.Strings(got)
				if len(got) != len(want) {
					t.Fatalf("workers=%d: got %d rows, want %d\n"+
						"fewer rows usually means the parallel scan landed on the "+
						"BUILD side, so each worker hashed only a partition",
						workers, len(got), len(want))
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
	// Guard against the whole suite silently degrading to serial-vs-serial,
	// which would pass while proving nothing.
	if gathered == 0 {
		t.Fatal("no query in the corpus produced a Gather; the identity " +
			"comparison was serial against serial and asserts nothing")
	}
}

// TestParallelHashJoinNotInNullSemantics isolates the NOT IN case, because it
// is the one where a lost scalar produces a WRONG answer rather than a
// truncated one — and where "identity with serial" and "correct per SQL" are
// worth asserting separately.
func TestParallelHashJoinNotInNullSemantics(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	sql := "SELECT f.fid FROM pq_fact f WHERE f.fk NOT IN (SELECT d.dk FROM pq_dim d)"
	for _, workers := range []int{1, 2, 4} {
		got, _ := runJoinGathered(t, ctx, sql, workers)
		if len(got) != 0 {
			t.Fatalf("workers=%d: NOT IN over a subquery containing NULL must "+
				"return no rows (SQL three-valued logic); got %d. The worker "+
				"almost certainly did not receive antiBuildHasNull",
				workers, len(got))
		}
	}
}

// TestSharedHashBuildRunsOnce pins the design's central claim: the build side
// is executed exactly ONE time regardless of worker count. If each worker built
// its own table the join would still be correct, so no identity test would
// catch the regression — only counting does.
func TestSharedHashBuildRunsOnce(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	sql := "SELECT f.fid, d.dname FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk"
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	gathered := optimizer.MaybeAddGather(node, optimizer.ParallelSettings{
		MaxWorkersPerGather: 4,
		MinTableScanBlocks:  1,
		DebugParallelQuery:  "on",
		BlocksForTable:      func(*catalog.Table) (int64, bool) { return 4096, true },
	})
	if !planTreeHasParallelNode(gathered) {
		t.Skip("planner declined to parallelise this shape; nothing to count")
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
	// After Open the tables are published and frozen. Exactly one entry per
	// hash-join plan node, no matter how many workers are running.
	if n := len(ctx.SharedHashBuilds); n != 1 {
		t.Errorf("SharedHashBuilds has %d entries, want 1", n)
	}
	for {
		if _, err := op.Next(); err == EOF {
			break
		} else if err != nil {
			t.Fatalf("next: %v", err)
		}
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Close must retract the publication, or the next serial statement on this
	// session would adopt a stale table for the same plan node.
	if ctx.SharedHashBuilds != nil {
		t.Error("Close left SharedHashBuilds published on the session context")
	}
}
