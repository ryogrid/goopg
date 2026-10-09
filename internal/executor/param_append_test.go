package executor

import (
	"strings"
	"testing"
)

// TestParameterisedAppendOverUnionAll pins M0146-0049b/c against PG 18.3: a
// flattened UNION ALL leaf joined by an indexed key gets PG's parameterised
// Append — a nested loop driving every member's index or bitmap probe with
// the outer key (add_paths_to_append_rel per child parameterisation,
// create_nestloop_plan's NestLoopParams). TPC-DS Q54's `item` x
// Append(catalog_sales, web_sales) is this shape. Values are PG's.
func TestParameterisedAppendOverUnionAll(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE li (id int primary key, cat int)",
		"CREATE TABLE cs1 (item int, ord int, amt int, primary key (item, ord))",
		"CREATE TABLE ws1 (item int, ord int, amt int, primary key (item, ord))",
		"CREATE TABLE ns1 (item int, amt int)",
		"INSERT INTO li SELECT g, g%50 FROM generate_series(1,2000) g",
		"INSERT INTO cs1 SELECT g%2000+1, g, g FROM generate_series(1,100000) g",
		"INSERT INTO ws1 SELECT g%2000+1, g, g FROM generate_series(1,50000) g",
		"INSERT INTO ns1 SELECT g%2000+1, g FROM generate_series(1,20000) g",
		"ANALYZE li", "ANALYZE cs1", "ANALYZE ws1", "ANALYZE ns1",
	} {
		runSQL(t, ctx, q)
	}
	const q = "SELECT li.id, x.amt FROM li, (SELECT item, amt FROM cs1 UNION ALL SELECT item, amt FROM ws1) x WHERE x.item = li.id AND li.cat = 3"
	plan := strings.Join(renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+q)), "\n")
	for _, want := range []string{"Nested Loop", "Append", "Index Cond: (item = li.id)"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	if strings.Count(plan, "Index Cond: (item = li.id)") != 2 {
		t.Errorf("want both members probed by li.id:\n%s", plan)
	}
	// M0146-0098: a member with a WHERE clause is not a safe append member
	// (is_safe_append_member), so PG keeps it a subquery RTE — `Subquery
	// Scan on "*SELECT* 1"` — and a subquery RTE gets no join-parameterised
	// path, which abandons the parameterised Append; PG 18.3 hash-joins it.
	const qWhere = "SELECT li.id, x.amt FROM li, (SELECT item, amt FROM cs1 WHERE amt > 5 UNION ALL SELECT item, amt FROM ws1) x WHERE x.item = li.id AND li.cat = 3"
	planWhere := strings.Join(renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+qWhere)), "\n")
	if !strings.Contains(planWhere, `Subquery Scan on "*SELECT* 1"`) || strings.Contains(planWhere, "Nested Loop") {
		t.Errorf("want PG's Hash Join over Subquery Scan on \"*SELECT* 1\":\n%s", planWhere)
	}

	for _, c := range []struct{ sql, want string }{
		{"SELECT count(*), sum(x.amt) FROM li, (SELECT item, amt FROM cs1 WHERE amt > 5 UNION ALL SELECT item, amt FROM ws1) x WHERE x.item = li.id AND li.cat = 3", "2999|124930998"},
		{"SELECT count(*), sum(x.amt), count(x.amt) FROM li LEFT JOIN (SELECT item, amt FROM cs1 UNION ALL SELECT item, amt FROM ws1) x ON x.item = li.id WHERE li.cat = 3", "3000|124931000|3000"},
		// A member with no index: no parameterised Append (PG declines too).
		{"SELECT count(*), sum(x.amt) FROM li, (SELECT item, amt FROM cs1 UNION ALL SELECT item, amt FROM ns1) x WHERE x.item = li.id AND li.cat = 3", "2400|103944800"},
		{"SELECT count(*), sum(x.amt) FROM li, (SELECT item, amt FROM cs1 WHERE ord % 3 = 0 UNION ALL SELECT item, amt FROM ws1 UNION ALL SELECT item, amt FROM cs1 WHERE amt < 100) x WHERE x.item = li.id AND li.cat < 3", "5005|175075250"},
	} {
		got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";")
		if got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
}

// TestLateralAppendNestedLoopRows pins M0146-0009m against PG 18.3: a nested
// loop over a LATERAL UNION ALL of two bitmap probes keyed by the outer row is
// sized outer × one call's rows. PG: Nested Loop rows=3000 over Seq Scan li
// rows=40 and Append rows=75 (50 + 25). goopg said 1 and 1: EstimateRows had
// no bitmap heap scan arm (0), and the one-relation scope's Filter that
// repeats the probe's index key (`item = li.id`) charged its selectivity a
// second time.
func TestLateralAppendNestedLoopRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE li (id int primary key, cat int)",
		"CREATE TABLE cs1 (item int, ord int, amt int, primary key (item, ord))",
		"CREATE TABLE ws1 (item int, ord int, amt int, primary key (item, ord))",
		"INSERT INTO li SELECT g, g%50 FROM generate_series(1,2000) g",
		"INSERT INTO cs1 SELECT g%2000+1, g, g FROM generate_series(1,100000) g",
		"INSERT INTO ws1 SELECT g%2000+1, g, g FROM generate_series(1,50000) g",
		"ANALYZE li", "ANALYZE cs1", "ANALYZE ws1",
	} {
		runSQL(t, ctx, q)
	}
	lines := renderRows(runSQL(t, ctx, "EXPLAIN SELECT li.id, x.amt FROM li, LATERAL (SELECT amt FROM cs1 WHERE item = li.id UNION ALL SELECT amt FROM ws1 WHERE item = li.id) x WHERE li.cat = 3"))
	plan := strings.Join(lines, "\n")
	for _, want := range []string{"Nested Loop  (cost=", "rows=3000 ", "Append  (cost=", "rows=75 "} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q (PG: Nested Loop rows=3000 over Append rows=75):\n%s", want, plan)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "Nested Loop") || !strings.Contains(lines[0], "rows=3000 ") {
		t.Errorf("top node %q, want PG's Nested Loop rows=3000", lines[0])
	}
}
