package executor

import "testing"

// TestPlpgSQLRecordFieldAndText pins the M0118-0008 plpgsql-toast assign3
// enabler (expanded_record_set_field): a `record` variable bound via
// `SELECT * INTO r` is now a first-class composite — its fields can be read
// (`r.a`), a field can be re-assigned from a scalar subquery (`r.b := (SELECT …)`),
// and the whole record can be cast to text (`r::text`) which reassembles the
// `(c0,c1,…)` framing. Before this, `SELECT * INTO r` only populated `_r_<col>`
// sub-fields and left the record variable NULL, so `r::text` rendered NULL.
func TestPlpgSQLRecordFieldAndText(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE test1 (a int, b text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO test1 VALUES (1, 'hello')`)

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("DDL %q: %v", sql, err)
		}
	}

	// record-to-text: SELECT * INTO r then r::text reassembles "(1,hello)".
	// "(" + "1" + "," + "hello" + ")" = 1+1+1+5+1 = 9.
	mustDDL(`CREATE FUNCTION rec_text() RETURNS int LANGUAGE plpgsql AS $$
declare r record;
begin
  select * into r from test1;
  return length(r::text);
end;
$$`)
	if got := runQuery(t, ctx, `SELECT rec_text()`)[0][0].Int; got != 9 {
		t.Errorf("rec_text() = %d, want 9 (len of \"(1,hello)\")", got)
	}

	// field read: r.a and r.b resolve against the bound record.
	mustDDL(`CREATE FUNCTION rec_fields() RETURNS text LANGUAGE plpgsql AS $$
declare r record;
begin
  select * into r from test1;
  return r.a::text || ':' || r.b;
end;
$$`)
	if got := runQuery(t, ctx, `SELECT rec_fields()`)[0][0].StringValue(); got != "1:hello" {
		t.Errorf("rec_fields() = %q, want %q", got, "1:hello")
	}

	// field assignment from a scalar subquery (expanded_record_set_field): the
	// re-assigned field flows into r::text framing.
	mustDDL(`CREATE FUNCTION rec_set_field() RETURNS int LANGUAGE plpgsql AS $$
declare r record;
begin
  select * into r from test1;
  r.b := (select test1.b from test1);
  return length(r::text);
end;
$$`)
	if got := runQuery(t, ctx, `SELECT rec_set_field()`)[0][0].Int; got != 9 {
		t.Errorf("rec_set_field() = %d, want 9", got)
	}

	// Exact assign3 shape with a large TOASTable value: repeat('foo',2000) is
	// 6000 chars, so r::text = "(1," + 6000 + ")" = 6004.
	runSQL(t, ctx, `DELETE FROM test1`)
	runSQL(t, ctx, `INSERT INTO test1 VALUES (1, repeat('foo', 2000))`)
	mustDDL(`CREATE FUNCTION rec_set_field_big() RETURNS int LANGUAGE plpgsql AS $$
declare r record;
begin
  select * into r from test1;
  r.b := (select test1.b from test1);
  return length(r::text);
end;
$$`)
	if got := runQuery(t, ctx, `SELECT rec_set_field_big()`)[0][0].Int; got != 6004 {
		t.Errorf("rec_set_field_big() = %d, want 6004", got)
	}
}
