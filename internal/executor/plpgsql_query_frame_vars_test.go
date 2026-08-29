package executor

import (
	"testing"
)

// TestPlpgSQLQueryStmtsSubstituteFrameVars pins M0134-0172: the two PL/pgSQL
// statements that plan a *captured SQL string* — `RETURN QUERY <query>` and
// `FOR rec IN <query> LOOP` — must substitute frame variables into that string
// before planning, exactly as their siblings `SELECT ... INTO`
// (plpgsql.SelectIntoStmt) and execPLpgSQLEmbeddedSQL already did.
//
// Before this change neither of them called substitutePlpgsqlFrameVarsInSQL at
// all, so *no* PL/pgSQL variable was visible to those queries — not a declared
// local, not even a function parameter. Every one of the sub-tests below failed
// with SQLSTATE 42703 `column "<var>" does not exist`, because the planner
// resolved the variable name as a column of the queried relation.
//
// docs/design/m0134-0172-plpgsql-query-stmt-frame-var-substitution.md is the
// ruling design doc. The regress-case symptom that surfaced this was
// stats_ext.sql's `check_estimated_rows()` helper, whose body is
//
//	return query select tmp[1]::int, tmp[2]::int;
//
// and which failed 381 times in one run with `column "tmp" does not exist`.
func TestPlpgSQLQueryStmtsSubstituteFrameVars(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE qfv_t (a int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO qfv_t VALUES (1), (2), (3)`)

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("DDL %q: %v", sql, err)
		}
	}

	t.Run("return_query_param_and_local", func(t *testing.T) {
		// (a) RETURN QUERY over a function parameter AND a declared local.
		// This is the minimal shape: neither `v` nor `n` is a column of any
		// relation, so pre-fix this raised 42703 on `v`.
		mustDDL(`CREATE FUNCTION qfv_ret(n int) RETURNS TABLE (a int) LANGUAGE plpgsql AS $$
declare v int := 7;
begin
  return query select v + n;
end;
$$`)
		if got := runQuery(t, ctx, `SELECT * FROM qfv_ret(3)`)[0][0].Format(); got != "10" {
			t.Errorf("qfv_ret(3) = %q, want %q", got, "10")
		}
	})

	t.Run("return_query_var_in_where", func(t *testing.T) {
		// (b) The variable is used as a WHERE bound against a real table, so
		// the query genuinely plans a relation — this is the shape that makes
		// RETURN QUERY useful and the one PG users actually write.
		mustDDL(`CREATE FUNCTION qfv_ret_where(lo int) RETURNS TABLE (a int) LANGUAGE plpgsql AS $$
begin
  return query select a from qfv_t where a >= lo order by a;
end;
$$`)
		rows := runQuery(t, ctx, `SELECT * FROM qfv_ret_where(2)`)
		if len(rows) != 2 {
			t.Fatalf("qfv_ret_where(2) returned %d rows, want 2", len(rows))
		}
		if got := rows[0][0].Format(); got != "2" {
			t.Errorf("row 0 = %q, want %q", got, "2")
		}
		if got := rows[1][0].Format(); got != "3" {
			t.Errorf("row 1 = %q, want %q", got, "3")
		}
	})

	t.Run("return_query_array_subscript", func(t *testing.T) {
		// (c) The stats_ext.sql shape verbatim: a text[] local subscripted
		// inside RETURN QUERY. Exercises pass 1 of
		// substitutePlpgsqlFrameVarsInSQL through the RETURN QUERY path.
		mustDDL(`CREATE FUNCTION qfv_ret_arr() RETURNS TABLE (a int, b int) LANGUAGE plpgsql AS $$
declare tmp text[] := array['400','150'];
begin
  return query select tmp[1]::int, tmp[2]::int;
end;
$$`)
		rows := runQuery(t, ctx, `SELECT * FROM qfv_ret_arr()`)
		if len(rows) != 1 {
			t.Fatalf("qfv_ret_arr() returned %d rows, want 1", len(rows))
		}
		if got, want := rows[0][0].Format(), "400"; got != want {
			t.Errorf("estimated = %q, want %q", got, want)
		}
		if got, want := rows[0][1].Format(), "150"; got != want {
			t.Errorf("actual = %q, want %q", got, want)
		}
	})

	t.Run("null_array_subscript_is_null", func(t *testing.T) {
		// (d) Subscripting a NULL array yields NULL in PostgreSQL
		// (ExecEvalSubscriptingRef, execExprInterp.c). Pre-fix, pass 1 bailed
		// out on a null value and emitted the bare text `tmp[1]`, which pass 2
		// then skipped (it suppresses any identifier followed by '['), so the
		// planner saw a column reference and raised 42703. This is the shape
		// stats_ext.sql actually hits whenever the regexp does not match.
		mustDDL(`CREATE FUNCTION qfv_null_arr() RETURNS TABLE (a int) LANGUAGE plpgsql AS $$
declare tmp text[];
begin
  return query select tmp[1]::int;
end;
$$`)
		rows := runQuery(t, ctx, `SELECT * FROM qfv_null_arr()`)
		if len(rows) != 1 {
			t.Fatalf("qfv_null_arr() returned %d rows, want 1", len(rows))
		}
		if !rows[0][0].IsNull() {
			t.Errorf("qfv_null_arr() = %q, want NULL", rows[0][0].Format())
		}
	})

	t.Run("out_of_range_subscript_is_null", func(t *testing.T) {
		// (e) An in-bounds-array/out-of-bounds-index subscript is also NULL in
		// PostgreSQL, not an error and not a bare identifier.
		mustDDL(`CREATE FUNCTION qfv_oob_arr() RETURNS TABLE (a text) LANGUAGE plpgsql AS $$
declare tmp text[] := array['x','y'];
begin
  return query select tmp[9];
end;
$$`)
		rows := runQuery(t, ctx, `SELECT * FROM qfv_oob_arr()`)
		if len(rows) != 1 {
			t.Fatalf("qfv_oob_arr() returned %d rows, want 1", len(rows))
		}
		if !rows[0][0].IsNull() {
			t.Errorf("qfv_oob_arr() = %q, want NULL", rows[0][0].Format())
		}
	})

	t.Run("for_in_query_var_in_where", func(t *testing.T) {
		// (f) The FOR-IN-<static query> sibling. Same missing call, same 42703;
		// verified independently because a green RETURN QUERY test proves
		// nothing about this path (Hard-won Rule #2).
		mustDDL(`CREATE FUNCTION qfv_forloop() RETURNS int LANGUAGE plpgsql AS $$
declare v int := 2; r record; s int := 0;
begin
  for r in select a from qfv_t where a >= v loop
    s := s + r.a;
  end loop;
  return s;
end;
$$`)
		if got := runQuery(t, ctx, `SELECT qfv_forloop()`)[0][0].Format(); got != "5" {
			t.Errorf("qfv_forloop() = %q, want %q", got, "5")
		}
	})

	t.Run("for_in_execute_still_uses_using", func(t *testing.T) {
		// (g) Guard against over-reach: the FOR rec IN EXECUTE <expr> form must
		// NOT get frame substitution — its parameters travel through USING and
		// the string is opaque to PL/pgSQL, matching PG. Here the local `a`
		// deliberately shares a name with qfv_t's column: if the EXECUTE branch
		// were substituted, `select a from qfv_t` would become `select 99 ...`
		// and every row would read 99.
		mustDDL(`CREATE FUNCTION qfv_for_execute() RETURNS int LANGUAGE plpgsql AS $$
declare a int := 99; r record; s int := 0;
begin
  for r in execute 'select a from qfv_t order by a' loop
    s := s + r.a;
  end loop;
  return s;
end;
$$`)
		if got := runQuery(t, ctx, `SELECT qfv_for_execute()`)[0][0].Format(); got != "6" {
			t.Errorf("qfv_for_execute() = %q, want %q (1+2+3); a substituted EXECUTE string would give 297", got, "6")
		}
	})
}
