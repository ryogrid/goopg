package executor

import "testing"

// TestPlpgSQLSelectInto pins the M0118-0008 PL/pgSQL `SELECT … INTO` statement
// form (plpgsql-toast enabler): a top-level INTO clause binds the first result
// row to the named variable(s) instead of being mis-parsed as SQL's
// CREATE-TABLE-AS spelling. Covers scalar, multi-target, and no-row (NULL)
// binding.
func TestPlpgSQLSelectInto(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE t (a int, b text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO t VALUES (1, 'hello')`)

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("DDL %q: %v", sql, err)
		}
	}

	// Scalar SELECT INTO: single column → scalar variable.
	mustDDL(`CREATE FUNCTION sel_scalar() RETURNS text LANGUAGE plpgsql AS $$
declare x text;
begin
  select b into x from t where a = 1;
  return x;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT sel_scalar()`)[0][0].StringValue(); got != "hello" {
		t.Errorf("sel_scalar() = %q, want %q", got, "hello")
	}

	// Multiple targets: columns bind positionally to scalar variables.
	mustDDL(`CREATE FUNCTION sel_multi() RETURNS text LANGUAGE plpgsql AS $$
declare p int; q text;
begin
  select a, b into p, q from t where a = 1;
  return p::text || ':' || q;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT sel_multi()`)[0][0].StringValue(); got != "1:hello" {
		t.Errorf("sel_multi() = %q, want %q", got, "1:hello")
	}

	// No matching row (non-strict): target becomes NULL.
	mustDDL(`CREATE FUNCTION sel_norow() RETURNS text LANGUAGE plpgsql AS $$
declare x text;
begin
  x := 'sentinel';
  select b into x from t where a = 999;
  return x;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT sel_norow()`)[0][0]; !got.IsNull() {
		t.Errorf("sel_norow() = %v, want NULL", got.Format())
	}

	// Single-column SELECT INTO a `record` target: the result must stay a
	// first-class composite so the column remains addressable as a field
	// (r.table_name), instead of being scalar-bound to r and losing the field
	// name. This mirrors reindex-concurrently-toast's setup DO-block,
	// `SELECT INTO r reltoastrelid::regclass::text AS table_name ...` then
	// `EXECUTE 'ALTER TABLE ' || r.table_name || ...`. M0118-0008.
	mustDDL(`CREATE FUNCTION sel_rec_field() RETURNS text LANGUAGE plpgsql AS $$
declare r record;
begin
  select b as fname into r from t where a = 1;
  return 'ALTER ' || r.fname || ' X';
end;
$$`)
	if got := runQuery(t, ctx, `SELECT sel_rec_field()`)[0][0].StringValue(); got != "ALTER hello X" {
		t.Errorf("sel_rec_field() = %q, want %q", got, "ALTER hello X")
	}
}
