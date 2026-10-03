package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// runSQLWith is runSQL planned under explicit planner settings (the
// harness has no SET): EXPLAIN returns the plan text the same way.
func runSQLWith(t *testing.T, ctx *Context, sql string, ps optimizer.PlannerSettings) []Row {
	t.Helper()
	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)
	stmts, err := parser.Parse(sql)
	if err != nil || len(stmts) != 1 {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatalf("Run(%q): %v", sql, err)
	}
	return rows
}

// TestSkipScanLeafWalk pins M0145-0008af's skip-scan leaf walk
// (btree_skip.go walkLeaf) against a sequential-scan oracle, over the three
// prefix shapes its re-descend rule has to get right:
//
//   - dense: thousands of distinct prefixes, a few entries each — many groups
//     per leaf, walked sequentially (TPC-DS Q95's web_returns_pkey probe,
//     where the per-group descent cost 226 ms against PG's 1 ms);
//   - sparse: three prefixes of 20000 entries each, the matches in the middle
//     of each group — whole-leaf groups, so the walk jumps to a group's probe
//     before reaching it and past the group after leaving it;
//   - long runs: matches that span many leaves inside one group, which must
//     be walked, not jumped over.
//
// Both siblings run each shape: the index scan (lazy, one leaf per batch)
// and the index-only scan (eager), which must enumerate the same groups.
func TestSkipScanLeafWalk(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE swd (a int NOT NULL, b int NOT NULL, c int)",
		"INSERT INTO swd SELECT g % 5000, g % 7, g FROM generate_series(1,20000) g",
		"CREATE INDEX swd_ab ON swd (a, b)",
		"CREATE TABLE sws (a int NOT NULL, b int NOT NULL, c int)",
		"INSERT INTO sws SELECT g % 3, g % 1000, g FROM generate_series(1,60000) g",
		"CREATE INDEX sws_ab ON sws (a, b)",
		"CREATE TABLE swl (a int NOT NULL, b int NOT NULL, c int)",
		"INSERT INTO swl SELECT g % 2, (g / 1000) % 3, g FROM generate_series(1,30000) g",
		"CREATE INDEX swl_ab ON swl (a, b)",
		"CREATE TABLE swdrv (x int NOT NULL)",
		"INSERT INTO swdrv VALUES (0), (1), (2), (3), (500), (999)",
		"ANALYZE swd", "ANALYZE sws", "ANALYZE swl", "ANALYZE swdrv",
		"VACUUM swd", "VACUUM sws", "VACUUM swl",
	} {
		runSQL(t, ctx, q)
	}
	probes := []struct{ table, cond string }{
		{"swd", "b = 3"},
		{"sws", "b = 500"},
		{"sws", "b = 0"},
		{"sws", "b = 999"},
		{"swl", "b = 1"},
		{"swl", "b = 2"},
	}
	seq := optimizer.DefaultPlannerSettings()
	seq.MaxParallelWorkersPerGather = 0
	seq.EnableIndexScan, seq.EnableBitmapScan = false, false
	probe := optimizer.DefaultPlannerSettings()
	probe.MaxParallelWorkersPerGather = 0
	probe.EnableSeqScan, probe.EnableBitmapScan = false, false
	probe.EnableHashJoin, probe.EnableMergeJoin = false, false
	check := func(sql, node, idx string) {
		t.Helper()
		want := strings.Join(renderRows(runSQLWith(t, ctx, sql, seq)), ";")
		plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+sql, probe)), "\n")
		if !strings.Contains(plan, node+" using "+idx) {
			t.Errorf("%s: want a skip probe by %s on %s:\n%s", sql, node, idx, plan)
			return
		}
		if got := strings.Join(renderRows(runSQLWith(t, ctx, sql, probe)), ";"); got != want {
			t.Errorf("%s via %s\n got %q, want %q (seq scan)", sql, node, got, want)
		}
	}
	for _, p := range probes {
		// The heap-fetching index scan, walked lazily one leaf per batch.
		check("SELECT count(*), sum(a), sum(c) FROM "+p.table+" WHERE "+p.cond, "Index Scan", p.table+"_ab")
	}
	for _, tbl := range []string{"swd", "sws", "swl"} {
		// The index-only scan, walked eagerly per rescan: a parameterised
		// probe (TPC-DS Q94's shape) over every driver value, so the walk
		// also restarts cleanly between outer rows.
		check("SELECT count(*), sum(t.a), sum(t.b) FROM swdrv JOIN "+tbl+" t ON t.b = swdrv.x", "Index Only Scan", tbl+"_ab")
	}
}
