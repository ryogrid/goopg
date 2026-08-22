package executor

import "testing"

// TestPadEmptyFill pins the lpad/rpad empty-fill-value semantics to PG 18.3
// (postgres/src/backend/utils/adt/oracle_compat.c:193-197 lpad, :291-295
// rpad): `if (s2len <= 0) len = s1len;` — an explicitly-empty third argument
// means NO padding, while truncation (`if (s1len > len) s1len = len;`) still
// applies because it runs BEFORE the empty-fill check. goopg previously
// substituted a space for the empty fill, padding with a space — wrong in
// both directions: `lpad("hi", 5, "")` returned "   hi" instead of "hi", and
// `lpad("hi", 1, "")` returned " " instead of "h".
//
// The 2-arg overloads default the fill to ' ' at the call site
// (internal/executor/expr.go `case "lpad":` / `case "rpad":`), matching PG's
// separate lpad(text,int)/rpad(text,int) builtins, and must keep padding.
func TestPadEmptyFill(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want string
	}{
		// Criteria 1-4: empty fill → no padding, truncation still applies.
		{"lpad empty fill", "SELECT lpad('hi', 5, '')", "hi"},
		{"lpad empty fill truncates", "SELECT lpad('hi', 1, '')", "h"},
		{"rpad empty fill", "SELECT rpad('hi', 5, '')", "hi"},
		{"rpad empty fill truncates", "SELECT rpad('hi', 1, '')", "h"},
		// Criterion 5: non-empty fill still pads (no regression).
		{"lpad non-empty fill", "SELECT lpad('hi', 5, 'xy')", "xyxhi"},
		{"rpad non-empty fill", "SELECT rpad('hi', 5, 'xy')", "hixyx"},
		// Criterion 6: 2-arg default fill ' ' still pads (both siblings).
		{"lpad 2-arg default fill", "SELECT lpad('hi', 5)", "   hi"},
		{"rpad 2-arg default fill", "SELECT rpad('hi', 5)", "hi   "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if got := rows[0][0].StringValue(); got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}
