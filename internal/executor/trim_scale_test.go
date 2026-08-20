package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestTrimScale covers trim_scale(numeric) (M0134-0058, bucket B): the
// pg_proc entry existed but had no executor implementation, so it silently
// evaluated to NULL. Mirrors PG's numeric_trim_scale
// (postgres/src/backend/utils/adt/numeric.c:4323), which reduces the value's
// display scale to the minimum needed to represent it exactly by stripping
// trailing zero decimal digits.
func TestTrimScale(t *testing.T) {
	tests := []struct {
		sql  string
		want string
	}{
		{`SELECT trim_scale(1.100)`, "1.1"},
		{`SELECT trim_scale(100)`, "100"},
		{`SELECT trim_scale(100.00)`, "100"},
		{`SELECT trim_scale(-1.500)`, "-1.5"},
		{`SELECT trim_scale(0.00)`, "0"},
	}
	for _, tc := range tests {
		ctx := NewContext()
		ctx.Catalog = catalog.NewInMemory()
		rows := runSQL(t, ctx, tc.sql)
		if len(rows) != 1 {
			t.Fatalf("%s: got %d rows, want 1", tc.sql, len(rows))
		}
		got := rows[0][0].Format()
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.sql, got, tc.want)
		}
	}
}

// TestTrimScaleNull confirms NULL-strict propagation: trim_scale(NULL) and
// min_scale(NULL) both return NULL, matching PG's generic strict-function
// dispatch for these builtins.
func TestTrimScaleNull(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	for _, sql := range []string{
		`SELECT trim_scale(NULL::numeric)`,
		`SELECT min_scale(NULL::numeric)`,
	} {
		rows := runSQL(t, ctx, sql)
		if len(rows) != 1 {
			t.Fatalf("%s: got %d rows, want 1", sql, len(rows))
		}
		if !rows[0][0].IsNull() {
			t.Errorf("%s = %v, want NULL", sql, rows[0][0])
		}
	}
}

// TestMinScale covers min_scale(numeric), the sibling that reports the
// minimum scale as an int without altering the value
// (postgres/src/backend/utils/adt/numeric.c:4302 numeric_min_scale).
func TestMinScale(t *testing.T) {
	tests := []struct {
		sql  string
		want string
	}{
		{`SELECT min_scale(1.100)`, "1"},
		{`SELECT min_scale(100)`, "0"},
		{`SELECT min_scale(100.00)`, "0"},
		{`SELECT min_scale(0.00)`, "0"},
	}
	for _, tc := range tests {
		ctx := NewContext()
		ctx.Catalog = catalog.NewInMemory()
		rows := runSQL(t, ctx, tc.sql)
		if len(rows) != 1 {
			t.Fatalf("%s: got %d rows, want 1", tc.sql, len(rows))
		}
		got := rows[0][0].Format()
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.sql, got, tc.want)
		}
	}
}
