package executor

import (
	"strings"
	"testing"
)

// M0146 ON-clause outer-reference pins: a JOIN ... ON clause inside a
// correlated subquery resolves outer-level column references exactly as a
// WHERE clause does — PG's parse_expr walks the parent ParseState chain
// from every jointree level. goopg resolved ON quals against a mergedCtx
// that never received `parent`, so `a.hundred` died 42703 "column ...
// does not exist" while the same reference in WHERE worked (the regress
// `subselect` shape:
//   select sum((select count(e.unique1) from tenk1 d left join tenk1 e
//     on e.unique1 = d.unique2 and e.hundred = a.hundred
//     where d.thousand = a.thousand)) from tenk1 a where a.unique1 < 20).
//
// Expected values below were captured live on PostgreSQL 18.3.
func TestJoinOnClauseOuterReference(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE tk (u1 int4, u2 int4, h int4, t int4)",
		"INSERT INTO tk SELECT i, i, i % 10, i % 5 FROM generate_series(0, 99) i",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	cases := []struct {
		name, q, want string
	}{
		// The filed LEFT JOIN shape: ON carries `e.h = a.h`, WHERE carries
		// `d.t = a.t`. count(e.u1) skips the null-extended rows.
		{"left-join", "SELECT a.u1, (SELECT count(e.u1) FROM tk d LEFT JOIN tk e ON e.u1 = d.u2 AND e.h = a.h WHERE d.t = a.t) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|10,1|10,2|10"},
		// Same correlation on an INNER join.
		{"inner-join", "SELECT a.u1, (SELECT count(*) FROM tk d JOIN tk e ON e.u1 = d.u2 AND e.h = a.h WHERE d.t = a.t) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|10,1|10,2|10"},
		// Unqualified outer ref in ON: `u4` exists only on the outer alias.
		{"unqualified", "SELECT a.u1, (SELECT count(*) FROM tk d JOIN tk e ON e.u1 = d.u2 AND u4 = a.u1) FROM (SELECT u1, u1+1000 AS u4 FROM tk) a WHERE a.u1 < 3 ORDER BY a.u1", "0|0,1|0,2|0"},
		// RIGHT JOIN keeps unmatched left rows; the outer ref lives in ON.
		{"right-join", "SELECT a.u1, (SELECT count(*) FROM tk d RIGHT JOIN tk e ON e.u1 = d.u2 AND e.h = a.h WHERE e.t = a.t) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|20,1|20,2|20"},
		// EXISTS-driven: the ON clause of a join inside a correlated EXISTS.
		{"exists", "SELECT a.u1 FROM tk a WHERE EXISTS (SELECT 1 FROM tk d JOIN tk e ON e.u1 = d.u2 AND e.h = a.h WHERE d.t = a.t) AND a.u1 < 3 ORDER BY a.u1", "0,1,2"},
		// IS NOT DISTINCT FROM is not an equality — the outer-ref conjunct
		// must still resolve (20 non-null-matched rows per outer row).
		{"is-not-distinct", "SELECT a.u1, (SELECT count(*) FROM tk d LEFT JOIN tk e ON e.u1 = d.u2 AND e.h IS NOT DISTINCT FROM a.h WHERE d.t = a.t) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|20,1|20,2|20"},
	}
	for _, tc := range cases {
		rows, err := runQueryWithErr(ctx, tc.q)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		var cells []string
		for _, r := range rows {
			parts := make([]string, 0, len(r))
			for _, d := range r {
				parts = append(parts, d.Format())
			}
			cells = append(cells, strings.Join(parts, "|"))
		}
		if got := strings.Join(cells, ","); got != tc.want {
			t.Errorf("%s\n got  %s\n want %s (PG 18.3)", tc.name, got, tc.want)
		}
	}
}
