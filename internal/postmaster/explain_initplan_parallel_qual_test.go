package postmaster

import (
	"strings"
	"testing"
)

// TestInitPlanQualRunsInsideParallelWorkers pins M0146-0192 against PG 18.3.
//
// An uncorrelated scalar subquery is an initplan. PG lists its output Param in
// is_parallel_safe's safe_param_ids (clauses.c) and evaluates it once in the
// leader before launching workers (ExecInitParallelPlan's initParam), so a qual
// comparing against it runs inside the workers. goopg judged the sublink like a
// SubPlan whose body holds a Gather and kept the whole scan serial (TPC-H Q22).
//
// PG 18.3 plans:
//
//	Finalize Aggregate
//	  InitPlan 1
//	    ->  Finalize Aggregate
//	          ->  Gather
//	                Workers Planned: 2
//	                ->  Partial Aggregate
//	                      ->  Parallel Seq Scan on ip_t ip_t_1
//	  ->  Gather
//	        Workers Planned: 2
//	        ->  Partial Aggregate
//	              ->  Parallel Seq Scan on ip_t
//	                    Filter: ((v)::numeric > (InitPlan 1).col1)
//
// The count (25000) is checked as well. The workers must read the value the
// leader computed; re-running the body inside a worker would nest a Gather.
func TestInitPlanQualRunsInsideParallelWorkers(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE ip_t (k int, v int)",
		"INSERT INTO ip_t SELECT g, g % 1000 FROM generate_series(1, 50000) g",
		"ANALYZE ip_t",
		"SET parallel_setup_cost = 0",
		"SET parallel_tuple_cost = 0",
		"SET min_parallel_table_scan_size = 0",
		"SET max_parallel_workers_per_gather = 2",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	const q = "SELECT count(*) FROM ip_t WHERE v > (SELECT avg(v) FROM ip_t)"
	got, e := allRowsQuery(t, conn, "EXPLAIN (COSTS OFF) "+q)
	if e != "" {
		t.Fatal(e)
	}
	plan := strings.Join(got, "\n")
	const want = `Finalize Aggregate
  InitPlan 1
    ->  Finalize Aggregate
          ->  Gather
                Workers Planned: 2
                ->  Partial Aggregate
                      ->  Parallel Seq Scan on ip_t ip_t_1
  ->  Gather
        Workers Planned: 2
        ->  Partial Aggregate
              ->  Parallel Seq Scan on ip_t
                    Filter: ((v)::numeric > (InitPlan 1).col1)`
	if strings.TrimSpace(plan) != want {
		t.Errorf("PG 18.3 runs the initplan qual inside the workers:\n%s\nwant:\n%s", plan, want)
	}
	rows, e := allRowsQuery(t, conn, q)
	if e != "" {
		t.Fatal(e)
	}
	if len(rows) != 1 || strings.TrimSpace(rows[0]) != "25000" {
		t.Errorf("count = %v, want 25000", rows)
	}
}
