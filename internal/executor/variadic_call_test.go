package executor

import "testing"

// TestVariadicFunctionCallCollapsesArgs pins the VARIADIC call-site
// argument-matching gap recorded in the 2026-07-15 deferral-ledger row
// (M0119-0004 DU-002 VARIADIC-array signature fix follow-up): a function
// declared with a trailing VARIADIC parameter must accept any number of
// positional call-site arguments — zero, one, or many — collapsing them
// into a single array value, mirroring PostgreSQL's
// parse_func.c/func_match_argtypes VARIADIC calling convention. Previously
// resolveRoutineOverload required an exact ArgTypes/args count match, so
// any call with a different argument count than 1 (the declared VARIADIC
// array parameter) failed "function ... does not exist".
func TestVariadicFunctionCallCollapsesArgs(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION variadic_count(VARIADIC arr integer[]) RETURNS integer LANGUAGE plpgsql AS $$
begin
  return array_length(arr, 1);
end;
$$`); err != nil {
		t.Fatalf("create function: %v", err)
	}

	cases := []struct {
		sql      string
		wantNull bool
		want     int64
	}{
		{`SELECT variadic_count(1, 2, 3)`, false, 3},
		{`SELECT variadic_count(5)`, false, 1},
		{`SELECT variadic_count()`, true, 0},
		{`SELECT variadic_count(1, 2, 3, 4, 5)`, false, 5},
	}
	for _, tc := range cases {
		rows := runQuery(t, ctx, tc.sql)
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: unexpected result shape %v", tc.sql, rows)
		}
		got := rows[0][0]
		if tc.wantNull {
			if !got.IsNull() {
				t.Errorf("%s = %v, want NULL", tc.sql, got)
			}
			continue
		}
		if got.Int != tc.want {
			t.Errorf("%s = %d, want %d", tc.sql, got.Int, tc.want)
		}
	}
}

// TestVariadicFunctionCallSQLLanguage exercises the same VARIADIC
// call-collapsing path through a LANGUAGE sql routine (executeSQLRoutine),
// the sibling dispatch path to executePLpgSQLRoutine exercised above —
// both bind args[i] positionally against r.ArgTypes[i], so both must see
// the bundled array at the VARIADIC position.
func TestVariadicFunctionCallSQLLanguage(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION variadic_len(VARIADIC arr integer[]) RETURNS integer LANGUAGE sql AS $$
SELECT array_length(arr, 1)
$$`); err != nil {
		t.Fatalf("create function: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT variadic_len(10, 20, 30)`)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("unexpected result shape %v", rows)
	}
	if got := rows[0][0].Int; got != 3 {
		t.Errorf("variadic_len(10,20,30) = %d, want 3", got)
	}
}
