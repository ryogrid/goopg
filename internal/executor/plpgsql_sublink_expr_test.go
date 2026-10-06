package executor

import (
	"strings"
	"testing"
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

// TestPlpgSQLSublinkExprFrameVariableBound pins the frame-variable binding
// that docs/design/m0134-0014-plpgsql-sublink-sql-fallback.md listed as a
// known limitation: a plpgsql variable referenced inside a sublink resolves
// from the calling frame, as PG binds it as a parameter (pl_exec.c
// plpgsql_param_ref). Until M0146-0072 the SQL fallback planned the raw tree
// and failed 42703 `column "i" does not exist`; PG 18.3 returns 'yes'.
func TestPlpgSQLSublinkExprFrameVariableBound(t *testing.T) {
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

	rows, err := runQueryWithErr(ctx, `SELECT sub_fv_ref()`)
	if err != nil {
		t.Fatalf("sub_fv_ref(): %v (the variable inside EXISTS must bind to the frame)", err)
	}
	if got := strings.Join(renderRows(rows), ";"); got != "yes" {
		t.Errorf("sub_fv_ref() = %s, want PG's yes", got)
	}
}
