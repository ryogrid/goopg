package executor

import "testing"

// TestPlpgSQLRecordForLoopAndText pins the M0118-0008 plpgsql-toast assign4/5/6
// enabler: a record (or named-composite-type) variable bound by a query — either
// `SELECT * INTO r` into a named composite type (assign4) or a `FOR r IN SELECT …`
// cursor loop (assign5/6) — is a first-class composite. `r::text` reassembles the
// `(c0,c1,…)` framing even for a single-column query. Before this, a single-column
// record FOR-loop variable held the raw column value, so `length(r::text)` was the
// value length, not the composite length (6000 vs PG's 6002).
func TestPlpgSQLRecordForLoopAndText(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE test1 (a int, b text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TYPE test2 AS (a bigint, b text)`); err != nil {
		t.Fatalf("create type: %v", err)
	}

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("DDL %q: %v", sql, err)
		}
	}

	// assign4 (expanded_record_set_fields): SELECT * INTO a NAMED composite type
	// variable. test1 row (1, repeat('foo',2000)) → r::text = "(1," + 6000 + ")"
	// = 6004.
	runSQL(t, ctx, `INSERT INTO test1 VALUES (1, repeat('foo', 2000))`)
	mustDDL(`CREATE FUNCTION a4() RETURNS int LANGUAGE plpgsql AS $$
declare r test2;
begin
  select * into r from test1;
  return length(r::text);
end;
$$`)
	if got := runQuery(t, ctx, `SELECT a4()`)[0][0].Int; got != 6004 {
		t.Errorf("a4() = %d, want 6004 (named composite r::text)", got)
	}

	// assign5 (expanded_record_set_tuple): FOR r IN SELECT test1.b (single column)
	// then r::text = "(" + 6000 + ")" = 6002.
	mustDDL(`CREATE FUNCTION a5() RETURNS int LANGUAGE plpgsql AS $$
declare r record;
begin
  for r in select test1.b from test1 loop
    null;
  end loop;
  return length(r::text);
end;
$$`)
	if got := runQuery(t, ctx, `SELECT a5()`)[0][0].Int; got != 6002 {
		t.Errorf("a5() = %d, want 6002 (single-column record FOR-loop r::text)", got)
	}

	// assign6 (FOR loop per-iteration framing): three rows of length 6000/9000/12000
	// each render as composite (+2 framing) → 6002/9002/12002.
	runSQL(t, ctx, `INSERT INTO test1 VALUES (2, repeat('bar', 3000))`)
	runSQL(t, ctx, `INSERT INTO test1 VALUES (3, repeat('baz', 4000))`)
	mustDDL(`CREATE FUNCTION a6() RETURNS text LANGUAGE plpgsql AS $$
declare
  r record;
  acc text := '';
begin
  for r in select test1.b from test1 loop
    acc := acc || length(r::text)::text || ' ';
  end loop;
  return acc;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT a6()`)[0][0].StringValue(); got != "6002 9002 12002 " {
		t.Errorf("a6() = %q, want %q", got, "6002 9002 12002 ")
	}
}

// TestPlpgSQLForLoopMaterializeAndRecordFieldSubst pins the two final
// plpgsql-toast pieces (design 0118-0054):
//
//  1. A FOR-query loop materializes its rows up front, so the loop iterates over
//     the rows fetched at loop start even when the body DELETEs from the scanned
//     table — mirroring PG, which holds the implicit cursor's snapshot across the
//     iteration (and across a COMMIT, in a procedure). A streaming operator would
//     instead see the deletes and stop after one iteration.
//  2. SELECT … INTO inside a body substitutes PL/pgSQL record-field references
//     (`r.a`) before planning; without it the query plans `r` as a missing table.
func TestPlpgSQLForLoopMaterializeAndRecordFieldSubst(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE t1 (a int, b text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO t1 VALUES (1, repeat('foo', 2000))`)
	runSQL(t, ctx, `INSERT INTO t1 VALUES (2, repeat('bar', 3000))`)
	runSQL(t, ctx, `INSERT INTO t1 VALUES (3, repeat('baz', 4000))`)

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("DDL %q: %v", sql, err)
		}
	}

	// (1) The body DELETEs every row each iteration; the loop must still run once
	// per originally-fetched row (3), proving the rows were materialized up front.
	mustDDL(`CREATE FUNCTION loopcount() RETURNS int LANGUAGE plpgsql AS $$
declare r record; n int := 0;
begin
  for r in select t1.a from t1 loop
    delete from t1;
    n := n + 1;
  end loop;
  return n;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT loopcount()`)[0][0].Int; got != 3 {
		t.Errorf("loopcount() = %d, want 3 (FOR loop must materialize rows before body DELETE)", got)
	}

	// Restore the rows for test (2).
	runSQL(t, ctx, `INSERT INTO t1 VALUES (1, repeat('foo', 2000))`)
	runSQL(t, ctx, `INSERT INTO t1 VALUES (2, repeat('bar', 3000))`)
	runSQL(t, ctx, `INSERT INTO t1 VALUES (3, repeat('baz', 4000))`)

	// (2) The inner SELECT … INTO references the loop record field r.a; it must be
	// substituted with the per-iteration value so the lengths (6000/9000/12000)
	// come back in row order.
	mustDDL(`CREATE FUNCTION fieldref() RETURNS text LANGUAGE plpgsql AS $$
declare r record; v text; acc text := '';
begin
  for r in select t1.a from t1 loop
    select b into v from t1 where a = r.a;
    acc := acc || length(v)::text || ' ';
  end loop;
  return acc;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT fieldref()`)[0][0].StringValue(); got != "6000 9000 12000 " {
		t.Errorf("fieldref() = %q, want %q (record-field r.a substitution in SELECT INTO)", got, "6000 9000 12000 ")
	}
}
