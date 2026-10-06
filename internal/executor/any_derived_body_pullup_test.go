package executor

import (
	"strings"
	"testing"
)

// TestAnyDerivedBodyKeepsWhereAndTarget pins M0146-0058 against PG 18.3. An
// IN sublink whose body is non-simple only to the sublink pull-up (a
// function-call target) takes the ANY-derived arm, which plans
// `(<body>) AS ANY_subquery` as one semi-side leaf. The FROM-subquery pull-up
// used to flatten that wrap, leaving the body's bare scan as the leaf: the
// WHERE was dropped and the link bound the raw column, so the first query
// returned 0. Each want is PG's answer.
func TestAnyDerivedBodyKeepsWhereAndTarget(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE m7one (a text)",
		"INSERT INTO m7one VALUES ('x'), ('y')",
		"CREATE TABLE m7two (a text, b int)",
		"INSERT INTO m7two VALUES ('x', 1), ('y', 2)",
	} {
		runSQL(t, ctx, q)
	}
	for _, c := range []struct{ query, want string }{
		{"SELECT count(*) FROM (VALUES ('X'), ('Y')) v(c) WHERE c IN (SELECT upper(a) FROM m7one WHERE a <> 'x')", "1"},
		{"SELECT count(*) FROM (VALUES ('X'), ('Y'), ('Z')) v(c) WHERE c IN (SELECT upper(a) FROM m7one)", "2"},
		{"SELECT count(*) FROM (VALUES ('X'), ('Y')) v(c) WHERE c IN (SELECT upper(a) FROM m7two WHERE b > 1)", "1"},
		{"SELECT string_agg(c, ',' ORDER BY c) FROM (VALUES ('X'), ('Y'), ('x')) v(c) WHERE c IN (SELECT upper(a) FROM m7one WHERE a <> 'x')", "Y"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, c.query)), ";"); got != c.want {
			t.Errorf("%s: got %q, want PG's %q", c.query, got, c.want)
		}
	}
}
