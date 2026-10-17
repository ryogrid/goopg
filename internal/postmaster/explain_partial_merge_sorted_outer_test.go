package postmaster

import (
	"strings"
	"testing"
)

// TestPartialMergeJoinSortsItsOuterPerWorker pins M0146-0193 against PG 18.3.
//
// sort_inner_and_outer's partial arm (joinpath.c) builds a merge join whose
// partial outer is explicitly sorted inside each worker: try_partial_mergejoin_path
// with outersortkeys. goopg declined any partial merge join that needed a sort,
// so the shape never existed. On TPC-DS Q33 that is what let the Parallel Hash
// Semi Join survive add_partial_path, where PG's sorted partial merge join
// fuzz-dominates it.
//
// With hash joins and nested loops off, PG 18.3 plans the join below the
// Gather as:
//
//	->  Partial HashAggregate
//	      Group Key: pm_i.c
//	      ->  Merge Join
//	            Merge Cond: (pm_o.k = pm_i.k)
//	            ->  Sort
//	                  Sort Key: pm_o.k
//	                  ->  Parallel Seq Scan on pm_o
//	            ->  Sort
//	                  Sort Key: pm_i.k
//	                  ->  Seq Scan on pm_i
//
// The values are checked too. Each worker merges its own sorted partition
// against the whole inner, so every outer row joins exactly once.
func TestPartialMergeJoinSortsItsOuterPerWorker(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE pm_o (k int, v int)",
		"CREATE TABLE pm_i (k int, c int)",
		"INSERT INTO pm_o SELECT g % 5000, g FROM generate_series(1, 200000) g",
		"INSERT INTO pm_i SELECT g, g % 3 FROM generate_series(1, 400) g",
		"ANALYZE pm_o", "ANALYZE pm_i",
		"SET parallel_setup_cost = 0",
		"SET parallel_tuple_cost = 0",
		"SET min_parallel_table_scan_size = 0",
		"SET max_parallel_workers_per_gather = 2",
		"SET enable_hashjoin = off",
		"SET enable_nestloop = off",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	const q = "SELECT pm_i.c, count(*), sum(pm_o.v) FROM pm_o JOIN pm_i ON pm_o.k = pm_i.k GROUP BY pm_i.c ORDER BY 1"
	got, e := allRowsQuery(t, conn, "EXPLAIN (COSTS OFF) "+q)
	if e != "" {
		t.Fatal(e)
	}
	var lines []string
	for _, l := range got {
		lines = append(lines, strings.TrimSpace(l))
	}
	plan := strings.Join(lines, "\n")
	const want = `->  Partial HashAggregate
Group Key: pm_i.c
->  Merge Join
Merge Cond: (pm_o.k = pm_i.k)
->  Sort
Sort Key: pm_o.k
->  Parallel Seq Scan on pm_o
->  Sort
Sort Key: pm_i.k
->  Seq Scan on pm_i`
	if !strings.Contains(plan, want) {
		t.Errorf("PG 18.3 sorts the partial outer inside each worker:\n%s\nwant (trimmed lines):\n%s", strings.Join(got, "\n"), want)
	}
	rows, e := allRowsQuery(t, conn, q)
	if e != "" {
		t.Fatal(e)
	}
	if strings.Join(rows, ",") != "0,5320,519769320,1,5360,523674680,2,5320,519764000" {
		t.Errorf("result = %v, want PG 18.3's (0,5320,519769320) (1,5360,523674680) (2,5320,519764000)", rows)
	}
}
