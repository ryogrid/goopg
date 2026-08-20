package executor

import "testing"

// TestPLpgSQLRowExprLowering pins M0134-0055 bucket B: lowerPLpgSQLExpr
// (plpgsql_runtime.go) previously had no case for *parser.RowExpr, so any
// PL/pgSQL expression containing a ROW(...) constructor (e.g.
// `RETURN row(10,'aaa',NULL,30)`) failed with "unsupported PL/pgSQL
// expression *parser.RowExpr". The fix lowers each field expression and
// builds an optimizer.RowExpr — the same node the SQL planner produces for
// row constructors — reusing the existing evalRowExpr composite-text
// builder in expr.go.
func TestPLpgSQLRowExprLowering(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("DDL %q: %v", sql, err)
		}
	}

	mustDDL(`CREATE FUNCTION row_ctor_fn() RETURNS record LANGUAGE plpgsql AS $$
begin
  return row(10,'aaa',NULL,30);
end;
$$`)
	got := runQuery(t, ctx, `SELECT row_ctor_fn()`)[0][0]
	if got.IsNull() {
		t.Fatalf("row_ctor_fn() returned NULL, want a composite row value")
	}
	want := `(10,aaa,,30)`
	if got.Format() != want {
		t.Errorf("row_ctor_fn() = %q, want %q", got.Format(), want)
	}

	// Assignment form: ROW(...) on the RHS of a variable assignment.
	mustDDL(`CREATE FUNCTION row_ctor_assign_fn() RETURNS text LANGUAGE plpgsql AS $$
declare r record;
begin
  r := row(1, 'x');
  return r::text;
end;
$$`)
	got2 := runQuery(t, ctx, `SELECT row_ctor_assign_fn()`)[0][0]
	if got2.IsNull() {
		t.Fatalf("row_ctor_assign_fn() returned NULL")
	}
	if got2.StringValue() != `(1,x)` {
		t.Errorf("row_ctor_assign_fn() = %q, want %q", got2.StringValue(), `(1,x)`)
	}
}
