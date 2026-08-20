package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestPlpgSQLSublinkExprSQLFallback pins M0134-0014c: PL/pgSQL expressions
// containing a sublink (EXISTS, IN (subquery), a non-root scalar subquery)
// are lowered to real SQL via evalExprViaSQL instead of erroring outright.
// docs/design/m0134-0014-plpgsql-sublink-sql-fallback.md is the ruling
// design doc — this reproduces the mvcc.sql regress-case root cause:
//
//	IF EXISTS(SELECT * FROM clean_aborted_self WHERE key > 0 AND key < 100) THEN
//
// Before this change, each of the sub-tests below failed with one of:
//
//	EXISTS is not supported in PL/pgSQL expressions in v0
//	IN (subquery) is not supported in PL/pgSQL expressions in v0
//	subqueries are not supported in PL/pgSQL expressions in v0
//
// (all SQLSTATE 0A000), aborting the enclosing transaction.
func TestPlpgSQLSublinkExprSQLFallback(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE sub_t (k int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO sub_t VALUES (5), (10), (15)`)

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("DDL %q: %v", sql, err)
		}
	}

	// evalExprViaSQL plans the raw parser expression with no PL/pgSQL
	// frame-variable substitution (§Known limitation), so these use literal
	// bounds baked into the function body rather than plpgsql parameters —
	// exactly like mvcc.sql's `IF EXISTS(SELECT * FROM clean_aborted_self
	// WHERE key > 0 AND key < 100)`, which references no plpgsql variable.
	t.Run("exists_true_false", func(t *testing.T) {
		// (a) IF EXISTS(...) true and false branches.
		mustDDL(`CREATE FUNCTION sub_exists_true() RETURNS text LANGUAGE plpgsql AS $$
begin
  if exists(select * from sub_t where k > 0 and k < 100) then
    return 'yes';
  else
    return 'no';
  end if;
end;
$$`)
		mustDDL(`CREATE FUNCTION sub_exists_false() RETURNS text LANGUAGE plpgsql AS $$
begin
  if exists(select * from sub_t where k > 1000 and k < 2000) then
    return 'yes';
  else
    return 'no';
  end if;
end;
$$`)
		if got := runQuery(t, ctx, `SELECT sub_exists_true()`)[0][0].StringValue(); got != "yes" {
			t.Errorf("sub_exists_true() = %q, want %q", got, "yes")
		}
		if got := runQuery(t, ctx, `SELECT sub_exists_false()`)[0][0].StringValue(); got != "no" {
			t.Errorf("sub_exists_false() = %q, want %q", got, "no")
		}
	})

	t.Run("not_exists", func(t *testing.T) {
		// (b) IF NOT EXISTS(...).
		mustDDL(`CREATE FUNCTION sub_not_exists_empty() RETURNS text LANGUAGE plpgsql AS $$
begin
  if not exists(select * from sub_t where k > 1000 and k < 2000) then
    return 'empty';
  else
    return 'nonempty';
  end if;
end;
$$`)
		mustDDL(`CREATE FUNCTION sub_not_exists_nonempty() RETURNS text LANGUAGE plpgsql AS $$
begin
  if not exists(select * from sub_t where k > 0 and k < 100) then
    return 'empty';
  else
    return 'nonempty';
  end if;
end;
$$`)
		if got := runQuery(t, ctx, `SELECT sub_not_exists_empty()`)[0][0].StringValue(); got != "empty" {
			t.Errorf("sub_not_exists_empty() = %q, want %q", got, "empty")
		}
		if got := runQuery(t, ctx, `SELECT sub_not_exists_nonempty()`)[0][0].StringValue(); got != "nonempty" {
			t.Errorf("sub_not_exists_nonempty() = %q, want %q", got, "nonempty")
		}
	})

	t.Run("nested_scalar_subquery", func(t *testing.T) {
		// (c) A NESTED (non-root) scalar subquery: x := (SELECT max(k) FROM
		// sub_t) + 1. The existing root-SubqueryExpr hatch in
		// evalPLpgSQLExpr never fires here because the SubqueryExpr is a
		// child of a BinaryOp, not the expression root.
		mustDDL(`CREATE FUNCTION sub_nested() RETURNS int LANGUAGE plpgsql AS $$
declare x int;
begin
  x := (select max(k) from sub_t) + 1;
  return x;
end;
$$`)
		if got := runQuery(t, ctx, `SELECT sub_nested()`)[0][0].Format(); got != "16" {
			t.Errorf("sub_nested() = %q, want %q", got, "16")
		}
	})

	t.Run("in_subquery", func(t *testing.T) {
		// (d) IF <literal> IN (SELECT ...). Like EXISTS, the whole InExpr
		// (including its Operand) is planned as raw SQL by evalExprViaSQL,
		// so the operand is a literal here rather than a plpgsql parameter —
		// see the frame-variable-substitution note above.
		mustDDL(`CREATE FUNCTION sub_in_member() RETURNS text LANGUAGE plpgsql AS $$
begin
  if 10 in (select k from sub_t) then
    return 'member';
  else
    return 'nonmember';
  end if;
end;
$$`)
		mustDDL(`CREATE FUNCTION sub_in_nonmember() RETURNS text LANGUAGE plpgsql AS $$
begin
  if 11 in (select k from sub_t) then
    return 'member';
  else
    return 'nonmember';
  end if;
end;
$$`)
		if got := runQuery(t, ctx, `SELECT sub_in_member()`)[0][0].StringValue(); got != "member" {
			t.Errorf("sub_in_member() = %q, want %q", got, "member")
		}
		if got := runQuery(t, ctx, `SELECT sub_in_nonmember()`)[0][0].StringValue(); got != "nonmember" {
			t.Errorf("sub_in_nonmember() = %q, want %q", got, "nonmember")
		}
	})
}

// TestPlpgSQLSublinkExprFrameVariableDeferred pins the DEFERRED limitation
// documented in docs/design/m0134-0014-plpgsql-sublink-sql-fallback.md
// §"Known limitation": evalExprViaSQL (like the pre-existing
// evalScalarSubquery) plans the raw parser expression with NO PL/pgSQL
// frame-variable substitution, so a plpgsql variable referenced inside the
// sublink is not visible to the planner and fails with 42703 ("column ...
// does not exist") rather than resolving from the calling frame. A future
// loop implementing frame-variable binding (threading frame values into
// optimizer.Plan as bound parameters, mirroring PG's SPI paramLI) must
// update this test deliberately when it lands.
func TestPlpgSQLSublinkExprFrameVariableDeferred(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE sub_fv (k int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO sub_fv VALUES (5), (10)`)

	if err := runDDL(t, ctx, `CREATE FUNCTION sub_fv_ref() RETURNS text LANGUAGE plpgsql AS $$
declare i int := 5;
begin
  if exists(select * from sub_fv where k > i) then
    return 'yes';
  else
    return 'no';
  end if;
end;
$$`); err != nil {
		t.Fatalf("create function: %v", err)
	}

	err := runQueryExpectErr(ctx, `SELECT sub_fv_ref()`)
	if err == nil {
		t.Fatalf("sub_fv_ref() expected an error (frame-variable binding into a sublink is deferred), got none")
	}
	// evalExprViaSQL plans the sublink expression via optimizer.Plan directly
	// (no frame-variable substitution), so the unresolved reference to the
	// plpgsql variable "i" surfaces as a planner error, not the interpreter's
	// *ExecError.
	pe, ok := err.(*optimizer.PlanError)
	if !ok {
		t.Fatalf("sub_fv_ref() error = %v (%T), want *optimizer.PlanError", err, err)
	}
	if pe.Code != "42703" {
		t.Errorf("sub_fv_ref() SQLSTATE = %q, want 42703 (column %q does not exist)", pe.Code, "i")
	}
	if !strings.Contains(pe.Message, "\"i\"") {
		t.Errorf("sub_fv_ref() message = %q, want it to reference column \"i\"", pe.Message)
	}
}
