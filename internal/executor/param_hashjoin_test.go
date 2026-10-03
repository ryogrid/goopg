package executor

import (
	"strings"
	"testing"
)

// TestParameterisedHashJoinInner pins M0146-0049d3 against PG 18.3: a semi
// join whose RHS is a join gets PG's parameterised hash join on the semi
// inner — `hash_inner_and_outer` pairs the RHS's parameterised probe (keyed
// by the outer's column) with the other RHS relation, `try_hashjoin_path`
// admits it because the semi join's LHS is in the joinrel's
// param_source_rels, and a nested loop binds the parameter into the whole
// hash join, rescanning it per outer row. TPC-DS Q95's
// `ws1.ws_order_number IN (SELECT wr_order_number FROM web_returns, ws_wh …)`
// is this shape.
//
// The outer is estimated at one row (statistics taken before six more rows
// arrive), so PG and goopg both choose the parameterised inner while the
// executor rescans it for seven outer rows. Values are PG's.
func TestParameterisedHashJoinInner(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE qo (id int, k int)",
		"INSERT INTO qo SELECT g, g*37 % 10000 FROM generate_series(1,20) g",
		"CREATE TABLE qbig (ord int, x int)",
		"INSERT INTO qbig SELECT g % 10000, g FROM generate_series(1,40000) g",
		"CREATE TABLE qret (ord int, item int, primary key (ord, item))",
		"INSERT INTO qret SELECT g*3 % 10000, g FROM generate_series(1,3000) g",
		"ANALYZE qo", "ANALYZE qbig", "ANALYZE qret",
		"INSERT INTO qo VALUES (3, 3), (3, 6), (3, 7), (3, 300), (3, 9999), (3, 111)",
	} {
		runSQL(t, ctx, q)
	}
	const semi = "SELECT count(*), sum(k) FROM qo WHERE id = 3 AND k IN (SELECT qret.ord FROM qret JOIN qbig ON qbig.ord = qret.ord)"
	plan := strings.Join(renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+semi)), "\n")
	for _, want := range []string{"Nested Loop Semi Join", "Hash Join", "Hash Cond: (qbig.ord = qret.ord)", "Index Cond: (ord = qo.k)"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	for _, c := range []struct{ sql, want string }{
		{semi, "5|531"},
		{"SELECT count(*), sum(k) FROM qo WHERE id = 3 AND NOT EXISTS (SELECT 1 FROM qret JOIN qbig ON qbig.ord = qret.ord WHERE qret.ord = qo.k)", "2|10006"},
		{"SELECT count(*), sum(k), sum(qbig.x) FROM qo JOIN qret ON qret.ord = qo.k JOIN qbig ON qbig.ord = qret.ord WHERE id = 3", "20|2124|302124"},
	} {
		got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";")
		if got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
}
