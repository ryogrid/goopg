package executor

import (
	"strings"
	"testing"
)

// TestJoinFilterResolvesSubqueryColumnToSource pins M0146-0005as: a join
// residual column that a grouped subquery republishes under an alias prints
// as its source column, and a column whose binding id no longer names a
// relation still prints qualified — PG's resolve_special_varno deparses a
// join Var through the child plan that produced it (TPC-DS Q46/Q68:
// `current_addr.ca_city <> customer_address.ca_city`, where goopg printed
// `ca_city <> bought_city`).
func TestJoinFilterResolvesSubqueryColumnToSource(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE jr_addr (a_sk int, a_city text)",
		"CREATE TABLE jr_sales (s_addr int, s_cust int, s_amt int)",
		"CREATE TABLE jr_cust (c_sk int, c_addr int)",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	// dn publishes only the two columns the join reads, so its Subquery
	// Scan is stripped even under the Sort the merge puts on it
	// (M0146-0099) and the residual deparses through to jr_addr — PG 18.3
	// prints `(cur.a_city <> jr_addr.a_city)` for this query.
	const q = "EXPLAIN (COSTS OFF) SELECT c_sk FROM " +
		"(SELECT s_cust, a_city AS bought_city FROM jr_sales, jr_addr WHERE s_addr = a_sk GROUP BY s_cust, a_city) dn, " +
		"jr_cust, jr_addr cur WHERE dn.s_cust = c_sk AND c_addr = cur.a_sk AND cur.a_city <> dn.bought_city"
	plan := strings.Join(runExplainRows(t, ctx, q), "\n")
	filter := residualCityFilter(plan)
	if filter == "" {
		t.Fatalf("no residual filter on the ca_city inequality in plan:\n%s", plan)
	}
	if strings.Contains(filter, "bought_city") {
		t.Errorf("residual still prints the subquery alias: %s", filter)
	}
	if !strings.Contains(filter, "cur.a_city") || !strings.Contains(filter, "jr_addr.a_city") {
		t.Errorf("residual must print both sides qualified at their source, got: %s\nplan:\n%s", filter, plan)
	}

	// With an unread aggregate column dn keeps its Subquery Scan under the
	// merge's Sort (a CP_SMALL_TLIST consumer); the residual's Var then
	// names the subquery RTE, and PG prints it through the alias.
	const qKept = "EXPLAIN (COSTS OFF) SELECT c_sk FROM " +
		"(SELECT s_cust, a_city AS bought_city, sum(s_amt) AS amt FROM jr_sales, jr_addr WHERE s_addr = a_sk GROUP BY s_cust, a_city) dn, " +
		"jr_cust, jr_addr cur WHERE dn.s_cust = c_sk AND c_addr = cur.a_sk AND cur.a_city <> dn.bought_city"
	plan = strings.Join(runExplainRows(t, ctx, qKept), "\n")
	if strings.Contains(plan, "Subquery Scan on dn") {
		if f := residualCityFilter(plan); !strings.Contains(f, "dn.bought_city") {
			t.Errorf("residual over a kept Subquery Scan must print dn.bought_city, got: %s\nplan:\n%s", f, plan)
		}
	}
}

// residualCityFilter returns the plan line carrying the a_city inequality.
func residualCityFilter(plan string) string {
	var filter string
	for _, l := range strings.Split(plan, "\n") {
		if strings.Contains(l, "a_city <>") || strings.Contains(l, "<> ") && strings.Contains(l, "Filter") {
			filter = strings.TrimSpace(l)
		}
	}
	return filter
}
