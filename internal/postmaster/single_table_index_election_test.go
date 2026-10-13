package postmaster

import (
	"strings"
	"testing"
)

// TestSingleTableIndexElectionIsCosted pins M0146-0060 against PG 18.3. The
// single-table rule-based index producer used to replace the costed search's
// Seq Scan with a heuristic Index Scan (cost 0.00..0.99) that never competed;
// it now fires only for a LIKE prefix, the one restriction whose index path the
// search does not build. The search builds the rest itself, including the two
// keys it had relied on the rule for: a client bind parameter ($n) and an IN
// list with a NULL element.
func TestSingleTableIndexElectionIsCosted(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE agg_sort_order (c1 int PRIMARY KEY, c2 int)",
		"CREATE UNIQUE INDEX agg_sort_order_c2_idx ON agg_sort_order(c2)",
		"INSERT INTO agg_sort_order SELECT i, i FROM generate_series(1,100) i",
		"ANALYZE agg_sort_order",
		"CREATE TABLE acc (aid int PRIMARY KEY, abalance int, name text)",
		"INSERT INTO acc SELECT g, 0, 'name' || g FROM generate_series(1,20000) g",
		"CREATE INDEX acc_name ON acc (name text_pattern_ops)",
		"ANALYZE acc",
		"PREPARE s(int) AS SELECT abalance FROM acc WHERE aid = $1",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	for _, c := range []struct{ sql, want string }{
		// PG keeps the Seq Scan (cost 2.25): 99 of 100 rows qualify.
		{"EXPLAIN (COSTS OFF) SELECT * FROM agg_sort_order WHERE c2 < 100", "Seq Scan on agg_sort_order"},
		{"EXPLAIN (COSTS OFF) EXECUTE s(5)", "Index Scan using acc_pkey on acc"},
		{"EXPLAIN (COSTS OFF) SELECT abalance FROM acc WHERE aid IN (7, NULL)", "Index Scan using acc_pkey on acc"},
		{"EXPLAIN (COSTS OFF) SELECT abalance FROM acc WHERE name LIKE 'name123%'", "Index Scan using acc_name on acc"},
	} {
		got, e := allRowsQuery(t, conn, c.sql)
		if e != "" || len(got) == 0 || !strings.Contains(got[0], c.want) {
			t.Errorf("%s: plan %q (err %q), want first line %q", c.sql, got, e, c.want)
		}
	}
	if got, e := allRowsQuery(t, conn, "EXECUTE s(5)"); e != "" || strings.Join(got, "|") != "0" {
		t.Errorf("EXECUTE s(5) = %q (err %q), want 0", got, e)
	}
}
