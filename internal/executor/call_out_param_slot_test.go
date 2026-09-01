package executor

import "testing"

// TestCallArgAfterOutParamBindsOwnSlot is the review/260831-2 EO1-1 guard.
// callOp.Next() walked the caller's argument list with a cursor that advanced
// only on IN/INOUT parameters while the argument list itself is
// POSITION-aligned (a CALL supplies a placeholder for every parameter, OUT
// ones included). Every IN parameter after an OUT parameter therefore read the
// slot before its own: on `p(a int, OUT b int, c int)`, `CALL p(1, NULL, 2)`
// bound c to the OUT placeholder NULL instead of 2.
//
// PG 18.3 oracle for the same procedure:
//
//	CREATE PROCEDURE p(a int, OUT b int, c int) LANGUAGE plpgsql
//	  AS $$ BEGIN b := a * 100 + c; END $$;
//	CALL p(1, NULL, 2);   -- b = 102
func TestCallArgAfterOutParamBindsOwnSlot(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE PROCEDURE p(a int, OUT b int, c int) LANGUAGE plpgsql AS $$ BEGIN b := a * 100 + c; END $$"); err != nil {
		t.Fatal(err)
	}
	rows, err := runQueryErr(t, ctx, "CALL p(1, NULL, 2)")
	if err != nil {
		t.Fatalf("CALL p(1, NULL, 2): %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("CALL returned %v, want one row of one OUT column", rows)
	}
	if rows[0][0].IsNull() {
		t.Fatal("OUT parameter b came back NULL: c was bound to the OUT placeholder, not to 2")
	}
	if rows[0][0].Int != 102 {
		t.Errorf("b = %d, want 102 (a=1, c=2)", rows[0][0].Int)
	}

	// Two OUT parameters ahead of the last IN one: the shift compounded.
	if err := runDDL(t, ctx, "CREATE PROCEDURE q(OUT x int, a int, OUT y int, b int) LANGUAGE plpgsql AS $$ BEGIN x := a; y := b; END $$"); err != nil {
		t.Fatal(err)
	}
	rows, err = runQueryErr(t, ctx, "CALL q(NULL, 3, NULL, 4)")
	if err != nil {
		t.Fatalf("CALL q(NULL, 3, NULL, 4): %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 2 {
		t.Fatalf("CALL q returned %v, want one row of two OUT columns", rows)
	}
	if rows[0][0].IsNull() || rows[0][0].Int != 3 {
		t.Errorf("x = %v, want 3", rows[0][0])
	}
	if rows[0][1].IsNull() || rows[0][1].Int != 4 {
		t.Errorf("y = %v, want 4", rows[0][1])
	}
}
