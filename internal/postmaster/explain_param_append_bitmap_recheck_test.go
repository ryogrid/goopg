package postmaster

import (
	"strings"
	"testing"
)

// TestExplainParamAppendBitmapRecheck pins M0146-0187 against PG 18.3.
//
// A parameterised Append member probed by a bitmap scan (bindParamProbe) keeps
// its recheck qual on the heap scan's Cond, because a lateral join has no
// merged outer++inner row for BitmapQual's coordinates. EXPLAIN printed it as
// `Filter:`; PG prints the probe as `Recheck Cond:` (bitmapqualorig) and only
// the member's own restriction as `Filter:`. TPC-DS Q54's catalog_sales member
// was its last SF0.25 divergence.
func TestExplainParamAppendBitmapRecheck(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE pa_o (k int, tag int)",
		"CREATE TABLE pa_s1 (k int, v int)",
		"CREATE TABLE pa_s2 (k int, v int)",
		"INSERT INTO pa_o SELECT g, g % 100 FROM generate_series(1, 1000) g",
		"INSERT INTO pa_s1 SELECT g % 1000, g FROM generate_series(1, 100000) g",
		"INSERT INTO pa_s2 SELECT g % 1000, g FROM generate_series(1, 100000) g",
		"CREATE INDEX pa_s1_k ON pa_s1 (k)",
		"CREATE INDEX pa_s2_k ON pa_s2 (k)",
		"ANALYZE pa_o",
		"ANALYZE pa_s1",
		"ANALYZE pa_s2",
		"SET max_parallel_workers_per_gather = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN (COSTS OFF) SELECT o.k, u.v FROM pa_o o,
  (SELECT k, v FROM pa_s1 UNION ALL SELECT k, v FROM pa_s2) u
 WHERE u.k = o.k AND o.tag = 7 AND u.v % 3 = 0`)
	if e != "" {
		t.Fatal(e)
	}
	plan := strings.Join(got, "\n")
	// PG 18.3's plan, verbatim.
	const want = ` Nested Loop
   ->  Seq Scan on pa_o o
         Filter: (tag = 7)
   ->  Append
         ->  Bitmap Heap Scan on pa_s1
               Recheck Cond: (k = o.k)
               Filter: ((v % 3) = 0)
               ->  Bitmap Index Scan on pa_s1_k
                     Index Cond: (k = o.k)
         ->  Bitmap Heap Scan on pa_s2
               Recheck Cond: (k = o.k)
               Filter: ((v % 3) = 0)
               ->  Bitmap Index Scan on pa_s2_k
                     Index Cond: (k = o.k)`
	// psql prints one leading space per row; allRowsQuery returns the bare row.
	norm := func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			out = append(out, strings.TrimSpace(l))
		}
		return strings.Join(out, "\n")
	}
	if !strings.Contains(norm(plan), norm(want)) {
		t.Errorf("plan differs from PG 18.3's:\n%s\nwant:\n%s", plan, want)
	}
}
