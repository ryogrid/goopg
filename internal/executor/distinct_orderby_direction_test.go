package executor

// SELECT DISTINCT must honour the query's ORDER BY direction — root-0036.
//
// goopg plans SELECT DISTINCT as a Distinct node whose operator re-sorts its
// input ascending in order to make duplicates adjacent, and then re-applies the
// query's ORDER BY on top of it (planner.go, M0097-0046).  That outer Sort was
// silently dropped whenever the sort key did not resolve against the Distinct
// output schema — and because `resolveOrderBySubstitution` rewrites a bare
// ORDER BY name into the matching target's OWN expression, that was the case
// for every *qualified* (`SELECT DISTINCT p.age … ORDER BY age`) and every
// *computed* (`… p.age+1 … ORDER BY 1`) select-list entry.  The result was a
// silently ascending answer to a DESC query — the shape upstream's
// `select_distinct` regress case catches with
// `SELECT DISTINCT p.age FROM person* p ORDER BY age using >`.
//
// Every `want` below is PostgreSQL 18.3's answer, captured from the reference
// cluster on 2026-07-28; none of them is goopg's pre-fix behaviour.
//
// The cases deliberately span the four ways a sort key reaches the Distinct
// output — bare name, qualified name, output alias, and 1-based position — plus
// a computed target and a star target, because the resolution path differs for
// each and a fix for one proves nothing about the others.

import (
	"testing"
)

func newDistinctOrderByFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE person (name text, age int)",
		"INSERT INTO person VALUES ('a', 8)",
		"INSERT INTO person VALUES ('b', 18)",
		"INSERT INTO person VALUES ('c', 98)",
		"INSERT INTO person VALUES ('d', 18)",
		"INSERT INTO person VALUES ('e', 50)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

func TestDistinctHonoursOrderByDirection(t *testing.T) {
	ctx, cleanup := newDistinctOrderByFixture(t)
	defer cleanup()

	cases := []struct {
		desc string
		sql  string
		want []string
	}{
		{
			desc: "bare target name, DESC (the path that already worked — guards against a regression the other way)",
			sql:  "SELECT DISTINCT age FROM person ORDER BY age DESC",
			want: []string{"98", "50", "18", "8"},
		},
		{
			desc: "qualified target, bare ORDER BY name",
			sql:  "SELECT DISTINCT p.age FROM person p ORDER BY age DESC",
			want: []string{"98", "50", "18", "8"},
		},
		{
			desc: "qualified target, positional ORDER BY",
			sql:  "SELECT DISTINCT p.age FROM person p ORDER BY 1 DESC",
			want: []string{"98", "50", "18", "8"},
		},
		{
			desc: "qualified target under an output alias",
			sql:  "SELECT DISTINCT p.age AS a FROM person p ORDER BY a DESC",
			want: []string{"98", "50", "18", "8"},
		},
		{
			desc: "computed target, positional ORDER BY",
			sql:  "SELECT DISTINCT p.age + 1 FROM person p ORDER BY 1 DESC",
			want: []string{"99", "51", "19", "9"},
		},
		{
			desc: "star target, positional ORDER BY (resolveOrderBySubstitution leaves stars alone)",
			sql:  "SELECT DISTINCT * FROM person ORDER BY 2 DESC, 1 ASC",
			want: []string{"c|98", "e|50", "b|18", "d|18", "a|8"},
		},
		{
			desc: "two qualified keys with opposite directions",
			sql:  "SELECT DISTINCT p.age, p.name FROM person p ORDER BY age DESC, name ASC",
			want: []string{"98|c", "50|e", "18|b", "18|d", "8|a"},
		},
		{
			// The upstream select_distinct shape. ORDER BY ... USING <op> is
			// parsed into the same Desc flag (parser.sortUsingIsDesc), so it
			// rides the identical planner path.
			desc: "ORDER BY ... USING > on a qualified target",
			sql:  "SELECT DISTINCT p.age FROM person p ORDER BY age using >",
			want: []string{"98", "50", "18", "8"},
		},
		{
			desc: "ASC on a qualified target still ascends",
			sql:  "SELECT DISTINCT p.age FROM person p ORDER BY age ASC",
			want: []string{"8", "18", "50", "98"},
		},
		{
			desc: "ORDER BY ... USING < on a qualified target",
			sql:  "SELECT DISTINCT p.age FROM person p ORDER BY age using <",
			want: []string{"8", "18", "50", "98"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			rows, err := runQueryWithErr(ctx, tc.sql)
			if err != nil {
				t.Fatalf("%s: %v", tc.sql, err)
			}
			got := renderRows(rows)
			if !equalStrings(got, tc.want) {
				t.Fatalf("%s\nSQL:  %s\ngot   %v\nwant  %v (PG 18.3)", tc.desc, tc.sql, got, tc.want)
			}
		})
	}
}
