package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0146-0107: an IN sublink over a kept CTE becomes a semi join whose inner
// columns carry the sublink level's binding ids. PG names them by the CTE
// scan's own range-table label (TPC-DS Q14's `cross_items_1.ss_item_sk`,
// Q23's `best_ss_customer.c_customer_sk`); goopg printed them bare.
//
// PG 18.3:
//
//	Hash Semi Join
//	  Hash Cond: (m107s.k = c.k)
//
// and with enable_hashjoin = enable_mergejoin = off:
//
//	Nested Loop
//	  ->  HashAggregate
//	        Group Key: c.k
//	        ->  CTE Scan on c
//	  ->  Index Scan using m107b_pkey on m107b
//	        Index Cond: (k = c.k)
func TestExplainSemiJoinOverKeptCTEKeys(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE m107a (k int, v int)",
		"CREATE TABLE m107b (k int PRIMARY KEY, w int)",
		"INSERT INTO m107a SELECT g % 50, g FROM generate_series(1, 200) g",
		"INSERT INTO m107b SELECT g, g FROM generate_series(1, 5000) g",
		"ANALYZE m107a", "ANALYZE m107b",
	} {
		runSQL(t, ctx, q)
	}
	const q = "EXPLAIN (COSTS OFF) WITH c AS MATERIALIZED (SELECT k, v FROM m107a) SELECT * FROM m107b WHERE m107b.k IN (SELECT k FROM c)"
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	// A semi join keeps one side, yet its keys index both inputs; over a
	// unique-ified inner the key reads through the dedupe.
	for _, s := range []string{
		"CREATE TABLE m107s (k int, w int)",
		"INSERT INTO m107s SELECT g, g % 3 FROM generate_series(1, 50) g",
		"CREATE TABLE m107t (k int, m int)",
		"INSERT INTO m107t SELECT g, g % 5 FROM generate_series(1, 50) g",
		"ANALYZE m107s", "ANALYZE m107t",
	} {
		runSQL(t, ctx, s)
	}
	for _, src := range []string{"m107a", "m107t"} {
		semi := strings.Join(renderRows(runSQLWith(t, ctx,
			"EXPLAIN (COSTS OFF) WITH c AS MATERIALIZED (SELECT k FROM "+src+") SELECT * FROM m107s WHERE m107s.k IN (SELECT k FROM c)", ps)), "\n")
		if !strings.Contains(semi, "Hash Cond: (m107s.k = c.k)") {
			t.Errorf("want PG's `Hash Cond: (m107s.k = c.k)`:\n%s", semi)
		}
	}
	ps.EnableHashJoin, ps.EnableMergeJoin = false, false
	nl := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n")
	for _, want := range []string{"Group Key: c.k", "Index Cond: (k = c.k)"} {
		if !strings.Contains(nl, want) {
			t.Errorf("want %q, as PG prints:\n%s", want, nl)
		}
	}
	// A bitmap probe's keys are plain ColumnRefs into the outer row (TPC-DS
	// Q14's store_sales_pkey probe printed `(ss_item_sk = ss_item_sk)`).
	for _, s := range []string{
		"CREATE TABLE m107p (k int, x int, w int, PRIMARY KEY (k, x))",
		"INSERT INTO m107p SELECT g % 500, g, g FROM generate_series(1, 5000) g",
		"ANALYZE m107p",
	} {
		runSQL(t, ctx, s)
	}
	ps.EnableIndexScan = false
	bm := strings.Join(renderRows(runSQLWith(t, ctx,
		"EXPLAIN (COSTS OFF) WITH c AS MATERIALIZED (SELECT k, v FROM m107a) SELECT * FROM m107p WHERE m107p.k IN (SELECT k FROM c)", ps)), "\n")
	for _, want := range []string{"Recheck Cond: (k = c.k)", "Index Cond: (k = c.k)"} {
		if !strings.Contains(bm, want) {
			t.Errorf("want %q, as PG prints:\n%s", want, bm)
		}
	}
}
