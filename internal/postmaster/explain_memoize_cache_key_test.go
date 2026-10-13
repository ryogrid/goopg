package postmaster

import (
	"strings"
	"testing"
)

// TestExplainMemoizeCacheKeyThroughKeptSubqueryScan pins the M0146-0042 slice
// for TPC-DS Q44 against PG 18.3 (lines from a scratch PG on this fixture). A
// Memoize's cache key is the parameterising loop's outer column; through a
// kept Subquery Scan PG prints it with the scan's alias (`v11.item_sk`), as
// the Index Cond beneath it already did in goopg.
func TestExplainMemoizeCacheKeyThroughKeptSubqueryScan(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"create table it (i_sk int primary key, i_name text)",
		"create table ss (ss_item int, ss_p int)",
		"insert into it select g, 'n' || g from generate_series(1, 2000) g",
		"insert into ss select g % 20 + 1, g % 97 from generate_series(1, 40000) g",
		"analyze it",
		"analyze ss",
		"set enable_hashjoin = off",
		"set enable_mergejoin = off",
		"set max_parallel_workers_per_gather = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN (COSTS OFF) select v11.item_sk, i1.i_name from (select ss_item item_sk, ss_p + 1 as p1 from ss offset 0) v11, it i1 where v11.p1 > 3 and i1.i_sk = v11.item_sk`)
	plan := strings.Join(got, "\n")
	if e != "" || !strings.Contains(plan, "Memoize") || !strings.Contains(plan, "Subquery Scan on v11") {
		t.Fatalf("fixture did not plan PG's Memoize over a kept Subquery Scan (err %q):\n%s", e, plan)
	}
	for _, want := range []string{"Cache Key: v11.item_sk", "Index Cond: (i_sk = v11.item_sk)"} {
		if !strings.Contains(plan, want) {
			t.Errorf("missing PG's line %s:\n%s", want, plan)
		}
	}
}
