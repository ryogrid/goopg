package postmaster

import (
	"strings"
	"testing"
)

// TestExplainSubPlanParamReferentOnParameterisedInner pins the M0146-0042
// slice for TPC-DS Q6 against PG 18.3. A correlated SubPlan in the Filter of a
// parameterised inner index scan prints its PARAM_EXEC argument as the outer
// Var (find_param_referent, ruleutils.c get_parameter): `i.i_category`, not
// `$0`. The argument reads the inner scan's own relation, so it resolves in
// the context of the node that evaluates the SubPlan, not the loop's outer
// input that a join-residual sublink resolves against.
func TestExplainSubPlanParamReferentOnParameterisedInner(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE item (i_item_sk int PRIMARY KEY, i_category char(50), i_current_price numeric(7,2))",
		"CREATE TABLE ss (ss_item_sk int, ss_customer_sk int)",
		"INSERT INTO item SELECT g, 'cat' || (g % 10), g % 100 FROM generate_series(1, 5000) g",
		"INSERT INTO ss SELECT (g * 7) % 5000 + 1, g FROM generate_series(1, 20000) g",
		"ANALYZE item",
		"ANALYZE ss",
		"SET enable_hashjoin = off",
		"SET enable_mergejoin = off",
		"SET max_parallel_workers_per_gather = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	const corr = "i.i_current_price > 1.2 * (SELECT avg(j.i_current_price) FROM item j WHERE j.i_category = i.i_category)"
	for _, c := range []struct{ name, sql, shape string }{
		// PG: Nested Loop -> Memoize -> Index Scan using item_pkey on item i,
		// whose Filter holds SubPlan 1.
		{"parameterised inner", "EXPLAIN (COSTS OFF) SELECT count(*) FROM ss s, item i WHERE s.ss_item_sk = i.i_item_sk AND " + corr,
			"Index Scan using item_pkey on item i"},
		{"plain scan", "EXPLAIN (COSTS OFF) SELECT count(*) FROM item i WHERE " + corr, "Seq Scan on item i"},
	} {
		got, e := allRowsQuery(t, conn, c.sql)
		plan := strings.Join(got, "\n")
		if e != "" || !strings.Contains(plan, c.shape) {
			t.Fatalf("%s: plan %q (err %q), want a %q node", c.name, plan, e, c.shape)
		}
		if !strings.Contains(plan, "Filter: (i_category = i.i_category)") || strings.Contains(plan, "$0") {
			t.Errorf("%s: SubPlan argument not printed as its referent:\n%s", c.name, plan)
		}
	}
}
