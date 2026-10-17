package postmaster

import (
	"strings"
	"testing"
)

// TestCoalesceCommonType pins M0146-0126: COALESCE / GREATEST / LEAST resolve
// their arguments' common type (select_common_type, parse_coerce.c) and coerce
// every argument to it, as transformCoalesceExpr / transformMinMaxExpr do; an
// argument that fails raises its error instead of being skipped. Every
// expectation is PostgreSQL 18.3's answer to the same statement.
func TestCoalesceCommonType(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, sql := range []string{
		"CREATE TABLE cx(a int, b numeric, c text, d bigint)",
		"INSERT INTO cx VALUES (7, NULL, NULL, NULL), (NULL, 2.5, 'x', 9)",
	} {
		if _, e := setClauseQuery(t, conn, sql); e != "" {
			t.Fatalf("%s: %q", sql, e)
		}
	}
	for _, c := range []struct {
		sql  string
		want []string
	}{
		{"SELECT coalesce(1, 2.5) / 3, pg_typeof(coalesce(1, 2.5))::text", []string{"0.33333333333333333333", "numeric"}},
		{"SELECT pg_typeof(coalesce(1, 2::bigint))::text, pg_typeof(coalesce(1::smallint, 2))::text", []string{"bigint", "integer"}},
		{"SELECT pg_typeof(coalesce('1', 2))::text, coalesce('1', 2) + 1", []string{"integer", "2"}},
		{"SELECT pg_typeof(coalesce(NULL, NULL))::text, pg_typeof(coalesce('a', 'b'))::text", []string{"text", "text"}},
		{"SELECT pg_typeof(greatest(1, 2.5))::text, least(3, 2.5)", []string{"numeric", "2.5"}},
		{"SELECT coalesce(a, 2.5) / 3 FROM cx WHERE a IS NULL", []string{"0.83333333333333333333"}},
		{"SELECT pg_typeof(coalesce(a, b))::text, pg_typeof(coalesce(a, d))::text FROM cx LIMIT 1", []string{"numeric", "bigint"}},
	} {
		got, e := setClauseQuery(t, conn, c.sql)
		if e != "" || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s = %q (err %q), want %q", c.sql, got, e, c.want)
		}
	}
	for _, c := range []struct{ sql, want string }{
		{"SELECT coalesce(a, c) FROM cx", "COALESCE types integer and text cannot be matched"},
		{"SELECT coalesce(a, 'x') FROM cx", `invalid input syntax for type integer: "x"`},
		{"SELECT coalesce(1 / 0, 5)", "division by zero"},
		{"SELECT greatest(1 / 0, 5)", "division by zero"},
	} {
		if _, e := setClauseQuery(t, conn, c.sql); !strings.Contains(e, c.want) {
			t.Errorf("%s: error = %q, want %q", c.sql, e, c.want)
		}
	}
	// CREATE TABLE AS stores the common type, so the numeric value survives.
	if _, e := setClauseQuery(t, conn, "CREATE TABLE cy AS SELECT coalesce(a, b) AS v FROM cx"); e != "" {
		t.Fatal(e)
	}
	if got, e := setClauseQuery(t, conn, "SELECT v FROM cy WHERE v < 3"); e != "" || strings.Join(got, "") != "2.5" {
		t.Errorf("cy.v = %q (err %q), want 2.5", got, e)
	}
}
