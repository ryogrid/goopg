package executor

import (
	"strings"
	"testing"
)

// TestExplainAnalyzeCTEBodyAndSubPlanActuals pins M0122-0021 slice 3: the
// subtrees goopg builds outside the top-level operator tree report their own
// (actual …), as PG's ExplainNode does for every plan node (explain.c recurses
// into initPlan / subPlan lists and CTE subplans, each with its own
// Instrumentation). Before the fix none of these lines carried actuals:
//
//   - a materialized CTE's body, built by cteScanOp with Build() (no scope).
//     Two references share the body node; the replaying one never opens it,
//     so the body still reports exactly one loop;
//   - an InitPlan's inner plan, built lazily by acquireSubPlanOp during
//     execution, long after the EXPLAIN build — one loop;
//   - a correlated SubPlan's inner plan — one loop per call (3 outer rows).
func TestExplainAnalyzeCTEBodyAndSubPlanActuals(t *testing.T) {
	ctx, cleanup := newMemoizeFixture(t)
	defer cleanup()

	cases := []struct {
		name, sql string
		want      []string
	}{
		{
			name: "materialized CTE referenced twice",
			sql:  "WITH c AS MATERIALIZED (SELECT k FROM mo WHERE pad > 5) SELECT count(*) FROM c a JOIN c b ON a.k = b.k",
			want: []string{"Seq Scan on mo (actual rows=34.00 loops=1)", "Rows Removed by Filter: 6"},
		},
		{
			name: "InitPlan",
			sql:  "SELECT count(*) FROM mo WHERE k > (SELECT avg(id) FROM mi)",
			want: []string{"->  Aggregate (actual rows=1.00 loops=1)", "Seq Scan on mi (actual rows=10.00 loops=1)"},
		},
		{
			// An empty outer never reaches the inner: PG prints
			// ` (never executed)` for a node with nloops == 0 (explain.c
			// ExplainNode), not `(actual rows=0.00 loops=0)`.
			name: "inner never executed",
			sql:  "SELECT count(*), sum(mi.v) FROM mo JOIN mi ON mi.id = mo.k WHERE mo.pad < 0",
			want: []string{"Memoize (never executed)", "Index Scan using mi_pkey on mi (never executed)"},
		},
		{
			name: "correlated SubPlan",
			sql:  "SELECT k, (SELECT v FROM mi WHERE mi.id = mo.k) FROM mo WHERE pad < 3",
			want: []string{"Index Scan using mi_pkey on mi (actual rows=1.00 loops=3)"},
		},
	}
	for _, tc := range cases {
		out := strings.Join(runExplainRows(t, ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) "+tc.sql), "\n")
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q:\n%s", tc.name, w, out)
			}
		}
	}
}

// TestExplainAnalyzeCoopBuildInputActuals pins the cooperative-build half of
// M0122-0021 slice 3: a hash join whose build goopg splits across producer
// goroutines (E-18) reports its build input as PG reports a serial build —
// one loop carrying every row. Before the fold the input printed
// `(actual rows=0.00 loops=0)`: the tree buildNode made for it is never
// opened and the producers built theirs uninstrumented.
func TestExplainAnalyzeCoopBuildInputActuals(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE cb_dim (dk int, dname text)`)
	runSQL(t, ctx, `CREATE TABLE cb_fact (fid int, fk int)`)
	runSQL(t, ctx, `INSERT INTO cb_dim SELECT x, 'name-' || x FROM generate_series(1, 3000) x`)
	runSQL(t, ctx, `INSERT INTO cb_fact SELECT x, (x * 7) % 3300 FROM generate_series(1, 12000) x`)
	runSQL(t, ctx, `ANALYZE cb_dim`)
	runSQL(t, ctx, `ANALYZE cb_fact`)
	ctx.MinParallelTableScanBlocks = 0
	ctx.MaxParallelWorkers = 4

	before := coopBorrowedProducers.Load()
	var lines []string
	for _, r := range runSQL(t, ctx, `EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) SELECT f.fid FROM cb_fact f JOIN cb_dim d ON f.fk = d.dk`) {
		lines = append(lines, r[0].StringValue())
	}
	out := strings.Join(lines, "\n")
	if coopBorrowedProducers.Load() == before {
		t.Skipf("precondition: the join did not take the cooperative build:\n%s", out)
	}
	if !strings.Contains(out, "Seq Scan on cb_dim d (actual rows=3000.00 loops=1)") {
		t.Errorf("cooperative build input does not report one loop of 3000 rows:\n%s", out)
	}
	if strings.Contains(out, "loops=0)") {
		t.Errorf("a node still reports loops=0:\n%s", out)
	}
}
