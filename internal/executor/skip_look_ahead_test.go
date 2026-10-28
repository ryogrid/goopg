package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestSkipLookAheadMatchesEntryWalk pins M0122-0012 C36: the skip scan's
// within-page look-ahead (skipLookAhead, PG 18's _bt_checkkeys_look_ahead)
// jumps over leaf entries that sort before the current group's probe and so
// returns exactly the rows the entry-by-entry walk returns — and those a
// sequential scan returns. The shapes put long runs of non-matching entries
// before each probe inside one leaf (tens to hundreds of distinct b per
// prefix a), with probes at a run's start, middle and end, a probe value that
// no group holds, groups whose run ends exactly at the page's end, duplicate
// keys (posting tuples) and NULLs in the bound column; both the lazy index
// scan and the eager index-only scan run each, driven by a parameterised
// join so the walk restarts between outer rows.
func TestSkipLookAheadMatchesEntryWalk(t *testing.T) {
	hits := 0
	oldHook := skipLookAheadHit
	skipLookAheadHit = func() { hits++ }
	t.Cleanup(func() { skipLookAheadHit = oldHook; skipLookAheadDisabled = false })
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE sla (a int NOT NULL, b int, c int)",
		"INSERT INTO sla SELECT g % 40, CASE WHEN g % 97 = 0 THEN NULL ELSE (g / 40) % 300 END, g FROM generate_series(1, 36000) g",
		"CREATE INDEX sla_ab ON sla (a, b)",
		"CREATE TABLE slb (a int NOT NULL, b int NOT NULL, c int)",
		"INSERT INTO slb SELECT g % 7, (g / 7) % 1500, g FROM generate_series(1, 42000) g",
		"CREATE INDEX slb_ab ON slb (a, b)",
		"CREATE TABLE sldrv (x int)",
		"INSERT INTO sldrv VALUES (0), (1), (2), (150), (151), (298), (299), (300), (749), (1499), (5000)",
		"ANALYZE sla", "ANALYZE slb", "ANALYZE sldrv", "VACUUM sla", "VACUUM slb",
	} {
		runSQL(t, ctx, q)
	}
	seq := optimizer.DefaultPlannerSettings()
	seq.MaxParallelWorkersPerGather = 0
	seq.EnableIndexScan, seq.EnableBitmapScan = false, false
	probe := optimizer.DefaultPlannerSettings()
	probe.MaxParallelWorkersPerGather = 0
	probe.EnableSeqScan, probe.EnableBitmapScan = false, false
	probe.EnableHashJoin, probe.EnableMergeJoin = false, false
	var queries []string
	for _, tbl := range []string{"sla", "slb"} {
		for _, v := range []int{0, 1, 150, 299, 300, 749, 1499, 5000} {
			queries = append(queries, fmt.Sprintf("SELECT count(*), sum(a), sum(c) FROM %s WHERE b = %d", tbl, v))
		}
		queries = append(queries, "SELECT count(*), sum(t.a), sum(t.b) FROM sldrv JOIN "+tbl+" t ON t.b = sldrv.x")
		queries = append(queries, "SELECT a, c FROM "+tbl+" WHERE b = 150 ORDER BY a, c")
	}
	for _, q := range queries {
		want := strings.Join(renderRows(runSQLWith(t, ctx, q, seq)), ";")
		plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, probe)), "\n")
		if !strings.Contains(plan, "Index") {
			t.Fatalf("%s: want an index skip probe:\n%s", q, plan)
		}
		before := hits
		got := strings.Join(renderRows(runSQLWith(t, ctx, q, probe)), ";")
		n := hits - before
		skipLookAheadDisabled = true
		walk := strings.Join(renderRows(runSQLWith(t, ctx, q, probe)), ";")
		skipLookAheadDisabled = false
		if got != want || walk != want {
			t.Fatalf("%s (look-ahead hits %d)\nlook-ahead %q\nentry walk %q\nseq scan   %q", q, n, got, walk, want)
		}
		t.Logf("hits=%d %s", n, q)
	}
	if hits == 0 {
		t.Fatal("the look-ahead never jumped")
	}
}
