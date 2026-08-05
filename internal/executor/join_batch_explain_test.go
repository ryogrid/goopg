package executor

// M0127-P3.5 — the spill made observable, and the identity asserted through
// SQL rather than through the operator (design leftdeep-joins/06 §4, 09 §2).
//
// P3.2's tests already assert the spill identity by driving joinOp directly.
// What they cannot assert is that the mechanism is reachable and honest from
// the OUTSIDE: that `SET work_mem` reaches the batch state at all, that a
// query returns the same answer at 64 kB as at the default, and that EXPLAIN
// reports the batching rather than leaving the operator's most consequential
// memory decision invisible. Those are the three things below.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// hashJoinInfoRe matches the two forms of PG's show_hash_info line, capturing
// nbatch so a test can assert the spill really engaged.
var hashJoinInfoRe = regexp.MustCompile(
	`Buckets: (\d+)(?: \(originally (\d+)\))?  Batches: (\d+)(?: \(originally (\d+)\))?  Memory Usage: (\d+)kB`)

// spillFixture creates two int-keyed tables and fills them, returning the
// context. `build` rows go into the table the planner will hash (the right
// side of the join text below), `probe` into the other.
func spillFixture(t *testing.T, probeRows, buildRows, distinct int) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)

	for _, ddl := range []string{
		"CREATE TABLE sp_probe (k int, pad text)",
		"CREATE TABLE sp_build (k int, pad text)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	fill := func(name string, n int) {
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("table %s not found after CREATE", name)
		}
		rel := ctx.Catalog.RelFileNode(tbl)
		pad := strings.Repeat("x", 48)
		for i := 0; i < n; i++ {
			row := Row{
				NewIntDatum(int64(i % distinct)),
				NewStringDatum(fmt.Sprintf("%s%d-%s", name, i, pad)),
			}
			if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
				t.Fatalf("fill %s: %v", name, err)
			}
		}
	}
	fill("sp_probe", probeRows)
	fill("sp_build", buildRows)
	return ctx
}

const spillJoinSQL = `SELECT p.k, p.pad, b.pad FROM sp_probe p, sp_build b WHERE p.k = b.k`

// The identity arm orders its output. A spilling join emits in BATCH order,
// which is a different — and equally legal — permutation of the same rows for
// a query with no ORDER BY, so comparing the raw streams would fail on a
// correct engine. The gate's word is "byte-identical", and an ORDER BY is what
// makes that assertion mean the multiset rather than the emission schedule.
const spillJoinOrderedSQL = spillJoinSQL + ` ORDER BY 1, 2, 3`

// renderSpillRows flattens a result set to one string per row so two runs can be
// compared for byte equality rather than for "the same number of rows" — the
// weaker assertion is exactly the one a mis-routed batch would survive.
func renderSpillRows(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		parts := make([]string, len(r))
		for i, d := range r {
			parts[i] = fmt.Sprint(datumToString(d))
		}
		out = append(out, strings.Join(parts, "\x1f"))
	}
	return out
}

// The S3 exit gate's second clause, in miniature and hermetic: a join run with
// work_mem lowered until it batches returns byte-identical rows to the same
// join run at the default. The TPC-H Q3 form of this test is the bench-scale
// artefact; this one is what keeps the property in the unit gate.
func TestHashJoinForcedSpillMatchesDefaultWorkMemThroughSQL(t *testing.T) {
	ctx := spillFixture(t, 1200, 4000, 400)

	ctx.WorkMem = 0 // unlimited — the geometry is single-batch
	want := renderSpillRows(runQueryRows(t, ctx, spillJoinOrderedSQL))
	if len(want) == 0 {
		t.Fatalf("precondition: the unbounded arm emitted no rows")
	}

	// Each run re-plans, so the second one keys its counters under a NEW
	// *planner.Join. In production that never collides — a statement gets a
	// fresh Context — so the test drops the first run's entry rather than
	// teaching the map to overwrite.
	ctx.HashJoinStats = nil
	ctx.WorkMem = 64 << 10
	got := renderSpillRows(runQueryRows(t, ctx, spillJoinOrderedSQL))

	// The counters the bounded run published are the proof the arm really
	// spilled; without them an identity between two in-memory runs proves
	// nothing (the P3.2 tests make the same point about their fixtures).
	hs := lastHashJoinStats(t, ctx)
	if hs.NBatch < 4 {
		t.Fatalf("precondition: nbatch=%d under work_mem=64kB — the gate wants >= 4, "+
			"so either work_mem is not reaching the join or the geometry is not honouring it", hs.NBatch)
	}

	assertSameRows(t, want, got)
}

// lastHashJoinStats returns the single hash join's published counters,
// failing the test when the map does not hold exactly one.
func lastHashJoinStats(t *testing.T, ctx *Context) *HashJoinStats {
	t.Helper()
	if len(ctx.HashJoinStats) != 1 {
		t.Fatalf("want exactly one instrumented hash join, got %d", len(ctx.HashJoinStats))
	}
	for _, hs := range ctx.HashJoinStats {
		return hs
	}
	return nil
}

// EXPLAIN ANALYZE emits PG's hash-table line, in the no-growth form: the
// geometry was chosen once and never moved, so no "(originally …)" appears.
func TestExplainAnalyzeHashJoinReportsBucketsBatchesMemory(t *testing.T) {
	ctx := spillFixture(t, 50, 200, 40)
	ctx.WorkMem = 0

	joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN ANALYZE "+spillJoinSQL), "\n")
	m := hashJoinInfoRe.FindStringSubmatch(joined)
	if m == nil {
		t.Fatalf("no hash-table line in ANALYZE output:\n%s", joined)
	}
	if m[2] != "" || m[4] != "" {
		t.Errorf("unbounded run reported an (originally …) form — nothing should have grown:\n%s", m[0])
	}
	if m[3] != "1" {
		t.Errorf("Batches: %s at unlimited work_mem, want 1:\n%s", m[3], m[0])
	}
	if !strings.HasSuffix(m[0], "kB") {
		t.Errorf("memory usage not rendered in kB: %q", m[0])
	}
}

// The growth form. work_mem is low enough that the build overruns its budget
// mid-flight, so nbatch doubles away from the geometry's original and PG's
// line switches to showing both — with BOTH originals, buckets included, as
// show_hash_info does.
func TestExplainAnalyzeHashJoinReportsGrownBatches(t *testing.T) {
	ctx := spillFixture(t, 200, 4000, 400)
	ctx.WorkMem = 64 << 10
	// M0127-P5.9: nbatch GROWS only when the plan UNDER-ESTIMATES the build
	// side — a right-sized plan picks its final nbatch up front and never
	// moves it, which is what the operator should do. This fixture used to
	// under-estimate by accident: `newDDLFixture` installed no block-count
	// reader, so every never-ANALYZEd relation was sized at the 1-row floor.
	// P5.9 installed the reader (the server has one, so a fixture that does
	// not plans nothing like production) and the accident went away.
	//
	// So the mis-estimate is now made ON PURPOSE, and only for this test:
	// clearing the sizer after the rows are on disk leaves the planner blind
	// in exactly the way a stale-statistics relation leaves it blind in
	// production, which is the real-world trigger for the growth path under
	// test. Nothing else about the fixture changes.
	ctx.Catalog.(*catalog.InMemory).SetRelationSizer(nil)
	// A blind planner is necessary but not sufficient: the PG-shaped search
	// costs its operator, and at the 1-row floor the cheapest join really is a
	// nested loop, so a blind searched arm produces no hash join to grow. The
	// old enumerator promotes the hash join by RULE and therefore still gives
	// one. Both halves are needed, and both go away together — the searched
	// arm needs a growth fixture that mis-estimates while still costing a hash
	// join. Deferral ledger row 2026-08-06 (M0127-P5.9).
	prevSearch := planner.SetPGShapedJoinSearch(false)
	defer planner.SetPGShapedJoinSearch(prevSearch)

	joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN ANALYZE "+spillJoinSQL), "\n")
	m := hashJoinInfoRe.FindStringSubmatch(joined)
	if m == nil {
		t.Fatalf("no hash-table line in ANALYZE output:\n%s", joined)
	}
	if m[4] == "" {
		t.Fatalf("nbatch never grew under work_mem=64kB; line was %q", m[0])
	}
	if m[3] == m[4] {
		t.Errorf("the (originally …) form appeared with equal counts: %q", m[0])
	}
	if m[2] == "" {
		t.Errorf("PG prints BOTH originals once either moved; buckets original missing: %q", m[0])
	}
}

// A plan with no hash join must not grow a stray hash-table line, and the
// renderer must tolerate a nil stats map (every EXPLAIN before this slice ran
// with one).
func TestExplainAnalyzeNonHashPlanHasNoHashInfoLine(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE hj_none (id int)"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN ANALYZE SELECT * FROM hj_none"), "\n")
	if strings.Contains(joined, "Batches:") {
		t.Errorf("hash-table line on a plan with no hash join:\n%s", joined)
	}
	if formatHashJoinInfoLine(nil) != "" {
		t.Errorf("nil stats must render nothing")
	}
}

// Peak memory is reported, not left at zero — the number is flushed at close
// rather than maintained per row, so it is the one counter a wiring mistake
// would silently zero out.
func TestHashJoinStatsCarryPeakMemory(t *testing.T) {
	ctx := spillFixture(t, 100, 2000, 200)
	ctx.WorkMem = 0
	_ = runQueryRows(t, ctx, spillJoinSQL)

	hs := lastHashJoinStats(t, ctx)
	if hs.SpacePeak <= 0 {
		t.Errorf("SpacePeak=%d after a 2000-row build", hs.SpacePeak)
	}
	if hs.NBuckets <= 0 || hs.NBuckets != hs.OrigNBuckets {
		t.Errorf("buckets: got %d, original %d — goopg never resizes buckets, so they must agree",
			hs.NBuckets, hs.OrigNBuckets)
	}
}
