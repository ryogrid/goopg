package executor

// agg_order_by_test.go — M0125-0019, accepted BY VALUE.
//
// PostgreSQL evaluates an aggregate's own ORDER BY *inside* the aggregate:
// nodeAgg.c collects the transition inputs into a tuplesort, sorts them, and
// only then runs the transition function over the sorted stream
// (postgres/src/backend/executor/nodeAgg.c, process_ordered_aggregate_single /
// _multi). The clause is therefore not a display detail — it decides the
// aggregate's VALUE.
//
// goopg parsed `string_agg(x, ',' ORDER BY x)` (the clause survives as
// FuncCall.OrderBy → AggregateCall.OrderBy) and then never looked at it in the
// string_agg branch of applyAgg: the branch concatenated in arrival order. Its
// sibling array_agg in the same switch DID capture the keys and sort in
// finishAgg — the classic "sibling paths must change together" asymmetry, and
// the failure is quiet: the row count is right, only the cell content is wrong.
//
// Every `want` below was captured from PostgreSQL 18.3 (the read-only oracle,
// port 65438) running the identical statement, not derived from goopg.

import (
	"strings"
	"testing"
)

// TestAggregateOrderByValue is the acceptance matrix for an aggregate's own
// ORDER BY. It covers string_agg (the reported gap), the delimiter's placement
// under sorting, bytea mode, and array_agg as the already-working control.
func TestAggregateOrderByValue(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want string
	}{
		// --- the reported defect -----------------------------------------
		{
			// PG: 1,2,3. goopg concatenated in arrival order: 3,1,2.
			name: "asc",
			sql:  "SELECT string_agg(x::text, ',' ORDER BY x) FROM (VALUES (3),(1),(2)) v(x)",
			want: "1,2,3",
		},
		{
			name: "desc",
			sql:  "SELECT string_agg(x::text, ',' ORDER BY x DESC) FROM (VALUES (3),(1),(2)) v(x)",
			want: "3,2,1",
		},
		{
			// Control: no ORDER BY still means arrival order, NOT sorted.
			name: "no_order_by_keeps_arrival_order",
			sql:  "SELECT string_agg(x::text, ',') FROM (VALUES (3),(1),(2)) v(x)",
			want: "3,1,2",
		},
		{
			// The sort key need not be the aggregated expression.
			name: "sort_key_is_another_column",
			sql:  "SELECT string_agg(n, '-' ORDER BY id) FROM (VALUES (3,'c'),(1,'a'),(2,'b')) v(id,n)",
			want: "a-b-c",
		},
		{
			name: "two_keys_mixed_direction",
			sql: "SELECT string_agg(n, ',' ORDER BY g DESC, n) FROM " +
				"(VALUES (1,'a'),(1,'c'),(2,'b'),(2,'d')) v(g,n)",
			want: "b,d,a,c",
		},
		{
			// The key is an arbitrary expression, not just a column ref.
			name: "sort_key_is_an_expression",
			sql:  "SELECT string_agg(x::text, ',' ORDER BY -x) FROM (VALUES (3),(1),(2)) v(x)",
			want: "3,2,1",
		},

		// --- NULL sort keys: ASC→NULLS LAST, DESC→NULLS FIRST ------------
		{
			name: "null_key_default_asc_is_nulls_last",
			sql:  "SELECT string_agg(n, ',' ORDER BY k) FROM (VALUES ('a',2),('b',NULL),('c',1)) v(n,k)",
			want: "c,a,b",
		},
		{
			name: "null_key_explicit_nulls_first",
			sql: "SELECT string_agg(n, ',' ORDER BY k NULLS FIRST) FROM " +
				"(VALUES ('a',2),('b',NULL),('c',1)) v(n,k)",
			want: "b,c,a",
		},
		{
			name: "null_key_default_desc_is_nulls_first",
			sql: "SELECT string_agg(n, ',' ORDER BY k DESC) FROM " +
				"(VALUES ('a',2),('b',NULL),('c',1)) v(n,k)",
			want: "b,a,c",
		},
		{
			// A NULL *value* is still skipped by string_agg; ordering the
			// survivors must not resurrect it or leave a stray delimiter.
			name: "null_values_still_skipped",
			sql:  "SELECT string_agg(x, ',' ORDER BY x) FROM (VALUES ('b'),(NULL),('a')) v(x)",
			want: "a,b",
		},

		// --- the delimiter travels with its row, not with its position ---
		{
			// PG appends row i's OWN delimiter before row i, in SORTED
			// order, and drops the first one. Sorted n = a,b,c carrying
			// delimiters +,*,| ⇒ "a" "*b" "|c".
			name: "per_row_delimiter_follows_sort_order",
			sql: "SELECT string_agg(n, d ORDER BY n) FROM " +
				"(VALUES ('c','|'),('a','+'),('b','*')) v(n,d)",
			want: "a*b|c",
		},

		// --- DISTINCT composes with ORDER BY -----------------------------
		{
			name: "distinct_with_order_by",
			sql: "SELECT string_agg(DISTINCT x, ',' ORDER BY x) FROM " +
				"(VALUES ('b'),('a'),('b')) v(x)",
			want: "a,b",
		},

		// --- empty input still yields NULL, not an empty string ----------
		{
			name: "empty_input_is_null",
			sql: "SELECT coalesce(string_agg(x::text, ',' ORDER BY x), '<none>') FROM " +
				"(VALUES (1)) v(x) WHERE false",
			want: "<none>",
		},

		// --- control: array_agg already honoured its ORDER BY ------------
		{
			name: "array_agg_control",
			sql:  "SELECT array_agg(x ORDER BY x)::text FROM (VALUES (3),(1),(2)) v(x)",
			want: "{1,2,3}",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 || len(rows[0]) != 1 {
				t.Fatalf("%s: want one 1-column row, got %v", c.sql, rows)
			}
			got := rows[0][0].Format()
			if got != c.want {
				t.Errorf("%s\n  got  %q\n  want %q (PostgreSQL 18.3)", c.sql, got, c.want)
			}
		})
	}
}

// TestAggregateOrderByByteaStringAgg covers string_agg over bytea, which is a
// separate branch of the same case.
//
// It asserts the ORDER only, not the exact rendering, because goopg's bytea
// representation diverges from PG independently of this fix: a `'\xaa'::bytea`
// literal is carried as the six-character TEXT `\xaa` (length() returns 6, not
// 2; encode() returns an empty string), so the concatenation reads
// `\xbb\x00\xaa` where PG prints `\xbb00aa`. That divergence is filed as
// M0125-0021 and must not be frozen into a `want` here — but the piece order
// is exactly what M0125-0019 owns, and it is now PG's.
func TestAggregateOrderByByteaStringAgg(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	sql := "SELECT string_agg(b, '\\x00'::bytea ORDER BY o)::text FROM " +
		"(VALUES ('\\xaa'::bytea,2),('\\xbb'::bytea,1)) v(b,o)"
	got := runQuery(t, ctx, sql)[0][0].Format()
	// PG 18.3 for this statement: \xbb00aa — bb (o=1) before aa (o=2).
	ibb, iaa := strings.Index(got, "bb"), strings.Index(got, "aa")
	if ibb < 0 || iaa < 0 || ibb > iaa {
		t.Errorf("%s\n  got %q; want bb (o=1) to precede aa (o=2), as in PG's \\xbb00aa", sql, got)
	}
}

// TestAggregateOrderByPerGroup proves the ordering is computed per GROUP, not
// once over the whole input — each group owns its own sort.
func TestAggregateOrderByPerGroup(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	sql := "SELECT g, string_agg(n, ',' ORDER BY n) FROM " +
		"(VALUES (1,'c'),(2,'z'),(1,'a'),(2,'x'),(1,'b')) v(g,n) GROUP BY g ORDER BY g"
	rows := runQuery(t, ctx, sql)
	if len(rows) != 2 {
		t.Fatalf("%s: want 2 groups, got %d", sql, len(rows))
	}
	want := []string{"a,b,c", "x,z"}
	for i, w := range want {
		if got := rows[i][1].Format(); got != w {
			t.Errorf("group %d: got %q, want %q (PostgreSQL 18.3)", i+1, got, w)
		}
	}
}
