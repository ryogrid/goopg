package postmaster

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestExplainOuterJoinInnerUniqueHashCost pins M0146-0183 against PG 18.3.
//
// add_paths_to_joinrel (joinpath.c) asks innerrel_is_unique for an outer
// join too, over the join's own clauses, and final_cost_hashjoin then prices
// the probe by the early-exit branch: an unmatched outer row walks an average
// bucket at a tenth of the qual cost, a matched one stops at its first match.
// goopg proved inner-uniqueness for INNER joins only, so a LEFT join whose
// hashed side is unique on the join keys was priced by the generic bucket
// walk — TPC-DS Q5's `web_returns LEFT JOIN web_sales` Hash Right Join cost
// about 4049 over its inputs where PG adds 945.
//
// PG 18.3 builds the unique iu_small side and prints
// `Hash Right Join  (cost=305.00..2371.01 rows=10013 width=8)`. Before the fix
// goopg priced that orientation so high that it hashed the 100000-row side
// instead (`Hash Left Join  (cost=3041.00..7021.00 ...)`).
func TestExplainOuterJoinInnerUniqueHashCost(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE iu_small (a int, b int, v int, PRIMARY KEY (a, b))",
		"CREATE TABLE iu_big (a int, b int, w int)",
		"INSERT INTO iu_small SELECT g % 100, g / 100, g FROM generate_series(1, 10000) g",
		"INSERT INTO iu_big SELECT g % 100, g / 100, g FROM generate_series(1, 100000) g",
		"ANALYZE iu_small",
		"ANALYZE iu_big",
		"SET enable_mergejoin = off",
		"SET enable_nestloop = off",
		"SET max_parallel_workers_per_gather = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, "EXPLAIN SELECT s.v, b.w FROM iu_small s LEFT JOIN iu_big b ON s.a = b.a AND s.b = b.b")
	if e != "" {
		t.Fatal(e)
	}
	plan := strings.Join(got, "\n")
	m := regexp.MustCompile(`Hash Right Join  \(cost=([0-9.]+)\.\.([0-9.]+) `).FindStringSubmatch(plan)
	if m == nil {
		t.Fatalf("PG 18.3 builds the unique iu_small side (Hash Right Join):\n%s", plan)
	}
	// 305 + 1541 (the probe scan) + 500 (the two hash keys per probe row) + the
	// early-exit bucket terms: PG prints 2371.01. The generic walk adds
	// thousands more.
	if total, _ := strconv.ParseFloat(m[2], 64); total > 2400 {
		t.Errorf("Hash Right Join total %v, PG 18.3 prices it at 2371.01:\n%s", total, plan)
	}
}
