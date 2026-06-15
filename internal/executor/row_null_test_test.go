package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestRowValuedNullTest pins PostgreSQL's row-valued IS [NOT] NULL semantics
// (evalRowNullTest): `row IS NULL` is true iff EVERY field is null, and
// `row IS NOT NULL` iff EVERY field is non-null — deliberately not inverses, so
// a row mixing null and non-null fields is false for both. A constructed
// RowExpr (a `(a, b, ...)` row constructor or a planner-expanded whole-row
// variable) is never itself a NULL Datum, so the scalar IsNull() path would
// wrongly report `... IS NOT NULL` true; this exercises the row-aware path.
// Regression guard for M0110-0003 (pg_amcheck AC-002 gap #7a:
// `COUNT(*) FILTER (WHERE d IS NOT NULL)` over an outer-joined whole-row ref).
func TestRowValuedNullTest(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()

	cases := []struct {
		sql  string
		want bool
	}{
		{`SELECT (1, 2) IS NOT NULL`, true},
		{`SELECT (1, 2) IS NULL`, false},
		{`SELECT (NULL, NULL) IS NULL`, true},
		{`SELECT (NULL, NULL) IS NOT NULL`, false},
		// Mixed null/non-null: false for BOTH tests (the not-inverse property).
		{`SELECT (1, NULL) IS NOT NULL`, false},
		{`SELECT (1, NULL) IS NULL`, false},
		// Nested row applies the rule recursively.
		{`SELECT (1, (2, 3)) IS NOT NULL`, true},
		{`SELECT (1, (2, NULL)) IS NOT NULL`, false},
		{`SELECT (NULL, (NULL, NULL)) IS NULL`, true},
	}
	for _, tc := range cases {
		rows := runSQL(t, ctx, tc.sql)
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: unexpected shape rows=%v", tc.sql, rows)
		}
		got := rows[0][0]
		if got.IsNull() {
			t.Fatalf("%s: result is NULL, want %v (IS [NOT] NULL must never be null)", tc.sql, tc.want)
		}
		if got.BoolValue() != tc.want {
			t.Errorf("%s = %v, want %v", tc.sql, got.BoolValue(), tc.want)
		}
	}
}
