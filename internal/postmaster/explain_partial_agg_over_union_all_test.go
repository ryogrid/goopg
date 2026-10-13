package postmaster

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestExplainPartialAggOverUnionAll pins M0146-0180 against PG 18.3.
//
// create_partial_grouping_paths (planner.c) aggregates per worker over the
// input rel's cheapest_partial_path; for a UNION ALL that rel is the appendrel
// and the path is its Parallel Append, priced by cost_append's partial arm.
// goopg's split only read a partial path from a join search's rel, so over a
// set operation it seeded the partial input from the committed serial winner
// with its run cost divided — TPC-DS Q76's Parallel Append printed
// 1000.58..14112 under a first child of 17943, against PG's 0.58..37614 — and
// where that winner was a plain Append there was no split at all.
//
// PG 18.3 plans this exact statement as Finalize GroupAggregate -> Gather
// Merge -> Sort -> Partial HashAggregate -> Parallel Append (0.00..8019.88)
// over three whole-claimed Seq Scans; HEAD before the fix planned a serial
// HashAggregate over a plain Append.
func TestExplainPartialAggOverUnionAll(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE pa1 (c int, x int)",
		"CREATE TABLE pa2 (c int, x int)",
		"CREATE TABLE pa3 (c int, x int)",
		"INSERT INTO pa1 SELECT g % 50, g FROM generate_series(1, 200000) g",
		"INSERT INTO pa2 SELECT g % 50, g FROM generate_series(1, 200000) g",
		"INSERT INTO pa3 SELECT g % 50, g FROM generate_series(1, 200000) g",
		"ANALYZE pa1",
		"ANALYZE pa2",
		"ANALYZE pa3",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN SELECT c, count(*) FROM (
  SELECT c FROM pa1 WHERE x > 10 UNION ALL
  SELECT c FROM pa2 WHERE x > 10 UNION ALL
  SELECT c FROM pa3 WHERE x > 10) s GROUP BY c ORDER BY c`)
	if e != "" {
		t.Fatal(e)
	}
	plan := strings.Join(got, "\n")
	for _, want := range []string{
		"Finalize GroupAggregate",
		"->  Gather Merge",
		"->  Partial HashAggregate",
		"->  Parallel Append  (cost=0.00..",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("missing PG 18.3's %q:\n%s", want, plan)
		}
	}
	// cost_append's partial arm never prices the Append below a child it
	// runs whole.
	m := regexp.MustCompile(`Parallel Append  \(cost=[0-9.]+\.\.([0-9.]+) `).FindStringSubmatch(plan)
	if m == nil {
		return
	}
	appendTotal, _ := strconv.ParseFloat(m[1], 64)
	for _, c := range regexp.MustCompile(`Seq Scan on pa[123]  \(cost=[0-9.]+\.\.([0-9.]+) `).FindAllStringSubmatch(plan, -1) {
		if child, _ := strconv.ParseFloat(c[1], 64); appendTotal < child {
			t.Errorf("Parallel Append total %v is below its child's %v:\n%s", appendTotal, child, plan)
		}
	}
}
