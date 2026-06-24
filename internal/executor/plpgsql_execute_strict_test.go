package executor

import (
	"strings"
	"testing"
)

// TestPlpgSQLExecuteIntoStrict pins the M0118-0009 PL/pgSQL `EXECUTE … INTO
// STRICT var` form (horizons.spec enabler, design 0118-0101): dynamic SQL whose
// INTO clause carries the STRICT modifier must bind the single result row and
// raise the PG-canonical errors (P0002 "query returned no rows" / P0003 "query
// returned more than one row") when the row count is not exactly one — mirroring
// the static `SELECT … INTO STRICT` path.
func TestPlpgSQLExecuteIntoStrict(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE t (a int, b text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO t VALUES (1, 'hello'), (2, 'world')`)

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("DDL %q: %v", sql, err)
		}
	}

	// Exactly one row: STRICT binds the value just like a plain INTO.
	mustDDL(`CREATE FUNCTION exec_strict_one() RETURNS text LANGUAGE plpgsql AS $$
declare x text;
begin
  execute 'select b from t where a = 1' into strict x;
  return x;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT exec_strict_one()`)[0][0].StringValue(); got != "hello" {
		t.Errorf("exec_strict_one() = %q, want %q", got, "hello")
	}

	// Zero rows → P0002 "query returned no rows".
	mustDDL(`CREATE FUNCTION exec_strict_none() RETURNS text LANGUAGE plpgsql AS $$
declare x text;
begin
  execute 'select b from t where a = 999' into strict x;
  return x;
end;
$$`)
	err := runQueryExpectErr(ctx, `SELECT exec_strict_none()`)
	if err == nil {
		t.Fatalf("exec_strict_none(): expected P0002 error, got nil")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "P0002" {
		t.Errorf("exec_strict_none() error = %v, want code P0002", err)
	} else if !strings.Contains(ee.Message, "no rows") {
		t.Errorf("exec_strict_none() message = %q, want 'no rows'", ee.Message)
	}

	// More than one row → P0003 "query returned more than one row".
	mustDDL(`CREATE FUNCTION exec_strict_many() RETURNS text LANGUAGE plpgsql AS $$
declare x text;
begin
  execute 'select b from t' into strict x;
  return x;
end;
$$`)
	err = runQueryExpectErr(ctx, `SELECT exec_strict_many()`)
	if err == nil {
		t.Fatalf("exec_strict_many(): expected P0003 error, got nil")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "P0003" {
		t.Errorf("exec_strict_many() error = %v, want code P0003", err)
	} else if !strings.Contains(ee.Message, "more than one row") {
		t.Errorf("exec_strict_many() message = %q, want 'more than one row'", ee.Message)
	}

	// Non-STRICT EXECUTE ... INTO with multiple rows must NOT error (binds the
	// first row only) — confirms STRICT is the gating modifier, not INTO itself.
	mustDDL(`CREATE FUNCTION exec_nonstrict_many() RETURNS text LANGUAGE plpgsql AS $$
declare x text;
begin
  execute 'select b from t order by a' into x;
  return x;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT exec_nonstrict_many()`)[0][0].StringValue(); got != "hello" {
		t.Errorf("exec_nonstrict_many() = %q, want %q (first row)", got, "hello")
	}
}
