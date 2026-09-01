package executor

import "testing"

// TestPLpgSQLForParenthesisedIntegerBound is the review/260831-2 NP-5 guard.
// parseFor decided between an integer-range FOR and a query FOR by peeking at
// ONE token: a leading '(' was taken as "query FOR", so a parenthesised lower
// bound (`FOR i IN (1+1)..4 LOOP`) was fed to the SQL parser and failed with
// 42601 "FOR query parse error". PG resolves the same ambiguity by which
// terminator the bound expression stops at, so both forms work. Measured on
// PG 18.3 at 127.0.0.1:65438:
//
//	for i in (1+1)..4 loop s := s + i; end loop;  -> 9
func TestPLpgSQLForParenthesisedIntegerBound(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION np5_range() RETURNS int LANGUAGE plpgsql AS $$
	    DECLARE i int; s int := 0;
	    BEGIN FOR i IN (1+1)..4 LOOP s := s + i; END LOOP; RETURN s; END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION np5_range: %v", err)
	}
	rows, err := runQueryErr(t, ctx, "SELECT np5_range()")
	if err != nil {
		t.Fatalf("SELECT np5_range(): %v (pre-fix: 42601 FOR query parse error)", err)
	}
	if got := rows[0][0].Int; got != 9 {
		t.Errorf("np5_range() = %d, want 9", got)
	}

	// Control: a genuine parenthesised sub-SELECT must stay a query FOR.
	if err := runDDL(t, ctx, `CREATE FUNCTION np5_query() RETURNS int LANGUAGE plpgsql AS $$
	    DECLARE r record; s int := 0;
	    BEGIN FOR r IN (SELECT g FROM generate_series(2,4) g) LOOP s := s + r.g; END LOOP;
	    RETURN s; END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION np5_query: %v", err)
	}
	rows, err = runQueryErr(t, ctx, "SELECT np5_query()")
	if err != nil {
		t.Fatalf("SELECT np5_query(): %v", err)
	}
	if got := rows[0][0].Int; got != 9 {
		t.Errorf("np5_query() = %d, want 9", got)
	}
}
