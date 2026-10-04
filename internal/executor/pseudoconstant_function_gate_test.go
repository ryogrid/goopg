package executor

import (
	"strings"
	"testing"
)

// TestPseudoconstantFunctionQualGatesScope pins M0146-0007h: PG's
// is_pseudo_constant_clause asks only for no current-level Vars and no
// volatile function, so a WHERE conjunct like `now() = now()` or
// `CURRENT_USER = 'x'` is evaluated once as a Result's One-Time Filter
// (create_gating_plan), not per row. goopg gated only sublink-bearing
// conjuncts (M0145-0008o). The NOT MATERIALIZED pair is regress subselect's:
// once both references are pulled up (M0146-0007f), `x.n = x2.n` reads
// `now() = now()`.
//
// Volatility comes from PG's pg_proc.dat (pgVolatileBuiltins): random() and
// pg_try_advisory_lock() are volatile there and must stay per-row Filters;
// the second one was missing from goopg's hand-written list.
func TestPseudoconstantFunctionQualGatesScope(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE pc_t (f1 int, f2 int)")
	runSQL(t, ctx, "INSERT INTO pc_t SELECT g, g FROM generate_series(1, 5) g")
	explain := func(q string) []string {
		return renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+q))
	}
	gated := []struct{ q, gate, rows string }{
		{"SELECT f1 FROM pc_t WHERE now() = now() ORDER BY f1",
			"One-Time Filter: (now() = now())", "1;2;3;4;5"},
		{"SELECT f1 FROM pc_t WHERE f1 > 2 AND now() > '2000-01-01'::timestamptz ORDER BY f1",
			"One-Time Filter: (now() > ", "3;4;5"},
		{"SELECT count(*) FROM pc_t WHERE now() < '2000-01-01'::timestamptz",
			"One-Time Filter: (now() < ", "0"},
		{`WITH x AS NOT MATERIALIZED (SELECT f1, now() AS n FROM pc_t)
		  SELECT count(*) FROM x, x x2 WHERE x.n = x2.n`,
			"One-Time Filter: (now() = now())", "25"},
	}
	for _, c := range gated {
		plan := explain(c.q)
		joined := strings.Join(plan, "\n")
		if countLinesContaining(plan, c.gate) != 1 || countLinesContaining(plan, "Result") == 0 {
			t.Errorf("want a Result gated by %q (PG 18.3):\n%s", c.gate, joined)
		}
		if countLinesContaining(plan, "Filter: (now()") != 0 && !strings.Contains(joined, "One-Time") {
			t.Errorf("the pseudoconstant must not stay a per-row Filter:\n%s", joined)
		}
		if got := strings.Join(renderRows(runSQL(t, ctx, c.q)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q\nplan:\n%s", c.q, got, c.rows, joined)
		}
	}
	for _, q := range []string{
		"SELECT f1 FROM pc_t WHERE random() > 2",
		"SELECT f1 FROM pc_t WHERE pg_try_advisory_lock(42)",
	} {
		if plan := explain(q); countLinesContaining(plan, "One-Time Filter") != 0 {
			t.Errorf("a volatile qual is evaluated per row, never gated:\n%s", strings.Join(plan, "\n"))
		}
	}
}

// TestRelationIsPublishable pins pg_relation_is_publishable (pg_publication.c
// is_publishable_class) with PG 18.3's values. psql's \d asks it with a
// constant OID beside `pc.oid = '<oid>'`; once that conjunct gates the scope
// (M0146-0007h) it is evaluated even when no pg_class row matches, so a
// missing builtin turned every \d into "function … does not exist".
func TestRelationIsPublishable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE pub_t (a int)",
		"CREATE UNLOGGED TABLE pub_unl (a int)",
		"CREATE TEMP TABLE pub_tmp (a int)",
		"CREATE VIEW pub_v AS SELECT 1 AS a",
	} {
		runSQL(t, ctx, q)
	}
	const q = `SELECT c.relname, pg_relation_is_publishable(c.oid) FROM pg_class c
		WHERE c.relname IN ('pub_t', 'pub_unl', 'pub_tmp', 'pub_v', 'pg_class') ORDER BY 1`
	if got, want := strings.Join(renderRows(runSQL(t, ctx, q)), ";"),
		"pg_class|f;pub_t|t;pub_tmp|f;pub_unl|f;pub_v|f"; got != want {
		t.Errorf("rows %q, want PG's %q", got, want)
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT pg_relation_is_publishable(999999)")), ";"); got != "NULL" {
		t.Errorf("a missing relation is NULL, got %q", got)
	}
	// psql \d's shape: the OID constant matches no row here.
	const d = `SELECT pc.relname FROM pg_catalog.pg_class pc
		WHERE pc.oid = '999999' AND pg_catalog.pg_relation_is_publishable('999999')`
	if _, err := runSQLCtxErr(t, ctx, d); err != nil {
		t.Errorf("%s: %v", d, err)
	}
}
