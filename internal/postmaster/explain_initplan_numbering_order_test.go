package postmaster

import (
	"strings"
	"testing"
)

// TestInitPlanNumbersFollowSubqueryRTEOrder pins M0146-0042's TPC-DS Q58 slice
// against PG 18.3.
//
// PG numbers an InitPlan by its position in glob->subplans, i.e. in planning
// order. set_base_rel_sizes plans the subquery RTEs in range-table (source)
// order, so the first subquery in FROM gets InitPlan 1 even when the join
// prints it second. goopg numbered nested query levels in the chosen plan's
// order, so a hash join that puts the second subquery on its outer side
// swapped the numbers. PG 18.3 prints:
//
//	Hash Join
//	  ->  HashAggregate              (b, the second subquery)
//	        InitPlan 2
//	        ->  Seq Scan on ipn_b
//	              Filter: ((v)::numeric > (InitPlan 2).col1)
//	  ->  Hash
//	        ->  HashAggregate        (a, the first subquery)
//	              InitPlan 1
//	              ->  Seq Scan on ipn_s
//	                    Filter: ((v)::numeric > (InitPlan 1).col1)
func TestInitPlanNumbersFollowSubqueryRTEOrder(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE ipn_s (k int, v int)",
		"CREATE TABLE ipn_b (k int, v int)",
		"INSERT INTO ipn_s SELECT g % 10, g FROM generate_series(1, 100) g",
		"INSERT INTO ipn_b SELECT g % 5000, g FROM generate_series(1, 50000) g",
		"ANALYZE ipn_s", "ANALYZE ipn_b",
		"SET max_parallel_workers_per_gather = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN (COSTS OFF) SELECT a.k, a.n, b.n
  FROM (SELECT k, count(*) n FROM ipn_s WHERE v > (SELECT avg(v) FROM ipn_s) GROUP BY k) a
  JOIN (SELECT k, count(*) n FROM ipn_b WHERE v > (SELECT avg(v) FROM ipn_b) GROUP BY k) b ON a.k = b.k`)
	if e != "" {
		t.Fatal(e)
	}
	var seq []string
	for _, l := range got {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "InitPlan ") || strings.HasPrefix(l, "Filter:") || strings.HasPrefix(l, "->  Seq Scan on ipn_s") || strings.HasPrefix(l, "->  Seq Scan on ipn_b") {
			seq = append(seq, l)
		}
	}
	want := []string{
		"InitPlan 2",
		"->  Seq Scan on ipn_b ipn_b_1",
		"->  Seq Scan on ipn_b",
		"Filter: ((v)::numeric > (InitPlan 2).col1)",
		"InitPlan 1",
		"->  Seq Scan on ipn_s ipn_s_1",
		"->  Seq Scan on ipn_s",
		"Filter: ((v)::numeric > (InitPlan 1).col1)",
	}
	if strings.Join(seq, "\n") != strings.Join(want, "\n") {
		t.Errorf("PG 18.3 numbers the InitPlans by subquery order (ipn_s first):\n%s\nwant:\n%s\nplan:\n%s",
			strings.Join(seq, "\n"), strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
}
