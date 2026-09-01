package executor

import "testing"

// TestPLpgSQLExpressionCastIsApplied is the review/260831-2 ES-7 guard.
// lowerPLpgSQLExpr's `*parser.CastExpr` arm returned the operand unchanged, so
// a cast written inside a PL/pgSQL expression was silently DISCARDED and the
// expression evaluated at the operand's own type: `7 / 2::numeric` did integer
// division and produced 3. Measured on PG 18.3 at 127.0.0.1:65438:
//
//	create function f_es7b() returns text language plpgsql as $$
//	  begin return (7 / 2::numeric)::text || '|' || (5/2)::text
//	              || '|' || (10::numeric/4)::text; end $$;
//	select f_es7b();   ->  3.5000000000000000|2|2.5000000000000000
//
// The middle term is the control: an uncast integer division must STAY
// integer division.
func TestPLpgSQLExpressionCastIsApplied(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION f_es7b() RETURNS text LANGUAGE plpgsql AS $$
	    BEGIN RETURN (7 / 2::numeric)::text || '|' || (5/2)::text
	                 || '|' || (10::numeric/4)::text; END $$`); err != nil {
		t.Fatal(err)
	}

	rows, err := runQueryErr(t, ctx, "SELECT f_es7b()")
	if err != nil {
		t.Fatalf("SELECT f_es7b(): %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("got %d rows, want 1x1", len(rows))
	}
	got := rows[0][0].StringValue()
	const want = "3.5000000000000000|2|2.5000000000000000"
	if got != want {
		t.Errorf("f_es7b() = %q, want %q (pre-fix, with the casts dropped: %q)", got, want, "3|2|2")
	}
}
