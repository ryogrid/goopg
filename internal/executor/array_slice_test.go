package executor

import "testing"

// TestArraySlice covers `expr[lower:upper]` array-slice syntax (M0134-0079).
// Upstream's grammar (`indirection_el`, gram.y) treats a bare `:` inside a
// subscript as the slice separator; goopg's lexer previously rejected any
// bare `:` as a lex error, so every array-slice expression failed to parse
// at all — this surfaced while sizing regress-sql tuplesort.sql
// (`(array_agg(...))[0:5]`).
//
// PG's array_ref (arrayfuncs.c) never errors or returns NULL for an
// out-of-range or reversed bound: it clamps to the array's actual bounds
// and returns an empty array `{}` when lower > upper. Each `want` below
// was captured from the PG 18.3 reference cluster (port 65432) on
// 2026-08-23.
func TestArraySlice(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT ARRAY[1,2,3,4,5][2:4]", "{2,3,4}"},
		{"SELECT ARRAY[1,2,3,4,5][:3]", "{1,2,3}"},
		{"SELECT ARRAY[1,2,3,4,5][3:]", "{3,4,5}"},
		{"SELECT ARRAY[1,2,3,4,5][:]", "{1,2,3,4,5}"},
		// Reversed bound: empty array, not NULL and not an error.
		{"SELECT ARRAY[1,2,3,4,5][4:2]", "{}"},
		// Out-of-range bounds clamp to the array's actual bounds rather
		// than erroring.
		{"SELECT ARRAY[1,2,3,4,5][10:20]", "{}"},
		{"SELECT ARRAY[1,2,3,4,5][-5:2]", "{1,2}"},
		{"SELECT ARRAY[1,2,3,4,5][0:100]", "{1,2,3,4,5}"},
		// A single-element slice is still an array, not a scalar.
		{"SELECT ARRAY[1,2,3,4,5][3:3]", "{3}"},
		// Nesting: subscripting a slice result works like any other array.
		{"SELECT ARRAY[1,2,3,4,5][2:4][1:1]", "{2}"},
		// Slicing the result of a function call — the exact shape that
		// broke regress-sql tuplesort.sql, `(array_agg(...))[0:5]`.
		{"SELECT (ARRAY[10,20,30])[1:2]", "{10,20}"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("%s = %q, want %q (PG 18.3)", c.sql, got, c.want)
			}
		})
	}

	t.Run("null array", func(t *testing.T) {
		rows := runQuery(t, ctx, "SELECT (NULL::int4[])[1:2]")
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(rows))
		}
		if !rows[0][0].IsNull() {
			t.Errorf("(NULL::int4[])[1:2] = %q, want NULL", rows[0][0].Format())
		}
	})
}
