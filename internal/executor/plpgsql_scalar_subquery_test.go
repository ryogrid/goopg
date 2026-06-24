package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// TestPlpgSQLScalarSubquery pins the M0118-0008 PL/pgSQL scalar-subquery
// assignment (plpgsql-toast assign2 enabler): a top-level `(SELECT ...)` on the
// right of an assignment or returned in a scalar context is planned and
// executed, yielding the first column of the first row (NULL when no row,
// error when more than one row).
func TestPlpgSQLScalarSubquery(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE test1 (a int, b text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO test1 VALUES (1, 'sixthousand')`)

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("DDL %q: %v", sql, err)
		}
	}

	// Scalar subquery assigned to a variable: x := (select test1.b from test1).
	mustDDL(`CREATE FUNCTION sub_assign() RETURNS text LANGUAGE plpgsql AS $$
declare x text;
begin
  x := (select test1.b from test1);
  return x;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT sub_assign()`)[0][0].StringValue(); got != "sixthousand" {
		t.Errorf("sub_assign() = %q, want %q", got, "sixthousand")
	}

	// Scalar subquery with no matching row → NULL.
	mustDDL(`CREATE FUNCTION sub_norow() RETURNS text LANGUAGE plpgsql AS $$
declare x text;
begin
  x := 'sentinel';
  x := (select b from test1 where a = 999);
  return x;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT sub_norow()`)[0][0]; !got.IsNull() {
		t.Errorf("sub_norow() = %v, want NULL", got.Format())
	}

	// Scalar subquery used directly in a RETURN expression.
	mustDDL(`CREATE FUNCTION sub_return() RETURNS int LANGUAGE plpgsql AS $$
begin
  return (select a from test1 where b = 'sixthousand');
end;
$$`)
	if got := runQuery(t, ctx, `SELECT sub_return()`)[0][0].Format(); got != "1" {
		t.Errorf("sub_return() = %q, want %q", got, "1")
	}

	// More than one row → 21000 (cardinality violation).
	runSQL(t, ctx, `INSERT INTO test1 VALUES (2, 'another')`)
	mustDDL(`CREATE FUNCTION sub_toomany() RETURNS text LANGUAGE plpgsql AS $$
declare x text;
begin
  x := (select b from test1);
  return x;
end;
$$`)
	if err := runQueryExpectErr(ctx, `SELECT sub_toomany()`); err == nil {
		t.Errorf("sub_toomany() expected cardinality error, got none")
	}
}

// runQueryExpectErr runs a query and returns any error from plan/build/open/drain
// without failing the test (used to assert an expected runtime error).
func runQueryExpectErr(ctx *Context, sql string) error {
	stmts, err := parser.Parse(sql)
	if err != nil {
		return err
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		return err
	}
	op, err := Build(plan)
	if err != nil {
		return err
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	defer op.Close()
	_, err = drainScan(op)
	return err
}
