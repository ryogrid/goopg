package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// TestRecordInNormalizesFields pins record_in for composite casts and
// assignment (M0149-0046c): each field goes through its type's input and
// the record is re-rendered by record_out — a float8 field 1.0 prints 1, an
// int field 02 prints 2, an array field and an array of composites are
// re-spelled element by element, NULL fields stay empty, and a record with
// the wrong field count is 22P02 "malformed record literal". goopg kept the
// input text, so subscription/t/002_types.pl's composite rows read
// (1.0,a,1) where PG reads (1,a,1). An enum field also keys through the
// enum's sort order (encodeBTreeKeyForColumn), so a composite with an enum
// field can be a btree key.
func TestRecordInNormalizesFields(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TYPE e46 AS ENUM ('a','b','c','d','e')",
		"CREATE TYPE cb46 AS (a FLOAT, b TEXT, c INTEGER)",
		"CREATE TYPE ca46 AS (a FLOAT, b e46[], c INTEGER)",
		"CREATE TYPE cm46 AS (a cb46, b cb46[], c e46)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	for _, tc := range []struct{ typ, in, want string }{
		{"cb46", "(1.0,a,1)", "(1,a,1)"},
		{"cb46", "(2.50,b,02)", "(2.5,b,2)"},
		{"cb46", "(,,5)", "(,,5)"},
		{"cb46", `(1,"x y",1)`, `(1,"x y",1)`},
		{"ca46", `(5.0,"{NULL, e, NULL}",5)`, `(5,"{NULL,e,NULL}",5)`},
		{"cb46[]", `{"(1.0,a,1)",NULL}`, `{"(1,a,1)",NULL}`},
		{"cm46", `("(1.0,a,1)","{""(2.0,b,2)""}",c)`, `("(1,a,1)","{""(2,b,2)""}",c)`},
	} {
		d, err := evalCast(NewStringDatum(tc.in), tc.typ, 0, ctx)
		if err != nil {
			t.Fatalf("%s::%s: %v", tc.in, tc.typ, err)
		}
		if d.StringValue() != tc.want {
			t.Errorf("%s::%s = %s, want %s", tc.in, tc.typ, d.StringValue(), tc.want)
		}
	}
	for _, bad := range []string{"(1,a)", "(1,a,1,2)", "1,a,1"} {
		if _, err := evalCast(NewStringDatum(bad), "cb46", 0, ctx); err == nil {
			t.Errorf("%s::cb46: want 22P02, got nil", bad)
		} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22P02" {
			t.Errorf("%s::cb46: err %v, want 22P02", bad, err)
		}
	}
}

// TestRangeInAtAssignment pins range_in for range columns and range arrays
// (M0149-0046d): INSERT / UPDATE assignment and array casts canonicalize a
// discrete range — '[1, 10]' into int4range is [1,11), '(1, 90)' is
// [2,90), an int8range[] element likewise, NULL elements kept — while a
// continuous range (numrange) keeps its bounds' inclusivity. goopg stored
// the literal as typed, so subscription/t/002_types.pl's range tables read
// [1, 10] where PG reads [1,11).
func TestRangeInAtAssignment(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	for _, tc := range []struct{ typ, in, want string }{
		{"int8range[]", `{"[1,2]", "[10,20]"}`, `{"[1,3)","[10,21)"}`},
		{"int8range[]", `{NULL, "[11,9999999]"}`, `{NULL,"[11,10000000)"}`},
		{"numrange[]", `{"[1.5,2.5]"}`, `{"[1.5,2.5]"}`},
	} {
		d, err := evalCast(NewStringDatum(tc.in), tc.typ, 0, ctx)
		if err != nil {
			t.Fatalf("%s::%s: %v", tc.in, tc.typ, err)
		}
		if d.StringValue() != tc.want {
			t.Errorf("%s::%s = %s, want %s", tc.in, tc.typ, d.StringValue(), tc.want)
		}
	}
	cols := []catalog.Column{
		{Name: "b", Type: catalog.Type{Name: "int4range"}},
		{Name: "d", Type: catalog.Type{Name: "int8range", IsArray: true}},
		{Name: "t", Type: catalog.Type{Name: "text"}},
	}
	row := Row{NewStringDatum("[1, 10]"), NewStringDatum(`{"[4,5]", NULL}`), NewStringDatum("[1, 10]")}
	if err := coerceRowForConstraintChecks(cols, row, func(int) bool { return true }, ctx, 0); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"[1,11)", `{"[4,6)",NULL}`, "[1, 10]"} {
		if row[i].StringValue() != want {
			t.Errorf("column %s = %s, want %s", cols[i].Name, row[i].StringValue(), want)
		}
	}
	if !rangeColumnType(ctx, "int4range") || rangeColumnType(ctx, "text") || rangeColumnType(ctx, "int4") {
		t.Error("rangeColumnType misclassifies int4range / text / int4")
	}
}

// TestRangeOutputSessionStyle pins range_out for the date-time range types
// (M0149-0046e): a stored tstzrange's bounds render in the session
// TimeZone and DateStyle (timestamptz_out), tsrange / daterange bounds in
// the DateStyle, infinite and absent bounds and `empty` are kept. goopg
// returned the stored text, so after `SET timezone = '+2'`
// subscription/t/002_types.pl read "2014-08-03 22:00:00+00" where PG reads
// "2014-08-04 00:00:00+02". The decode-time style is live for a scan of
// these columns (colsHaveArray).
func TestRangeOutputSessionStyle(t *testing.T) {
	st := array.DefaultOutputStyle()
	st.Zone = "<+02>-02" // SET timezone = '+2' as the session resolves it
	for _, tc := range []struct{ typ, in, want string }{
		{"tstzrange", `["2014-08-03 22:00:00+00",infinity)`, `["2014-08-04 00:00:00+02",infinity)`},
		{"tstzrange", `(,"2014-08-04 00:00:00+00"]`, `(,"2014-08-04 02:00:00+02"]`},
		{"tstzrange", "empty", "empty"},
		{"tsrange", `["2014-08-01 10:00:00","2014-08-02 00:00:00")`, `["2014-08-01 10:00:00","2014-08-02 00:00:00")`},
	} {
		if got := restyleRangeText(tc.typ, tc.in, st); got != tc.want {
			t.Errorf("restyleRangeText(%s, %s) = %s, want %s", tc.typ, tc.in, got, tc.want)
		}
	}
	german := array.DefaultOutputStyle()
	german.Style, german.Order = "German", "DMY"
	if got := restyleRangeText("daterange", "[2014-08-01,2014-08-06)", german); got != "[01.08.2014,06.08.2014)" {
		t.Errorf("German daterange = %s", got)
	}
	if !colsHaveArray([]catalog.Column{{Name: "b", Type: catalog.Type{Name: "tstzrange"}}}) {
		t.Error("colsHaveArray(tstzrange column) = false: the scan would not resolve the session style")
	}
}

// TestApplyWorkerTypedTextAndCatalogUnwrap pins two apply-side fixes of
// subscription/t/002_types.pl's PG-publisher pairing (M0149-0046g): an
// array column's pgoutput text is kept as array text (the column's Type
// names the ELEMENT type; parsing it as int4 stopped the apply worker),
// and a composite / enum key encodes through the search-path catalog the
// apply worker runs with (catalog.WithSearchPath) — a bare *InMemory
// assertion failed there and the replicated row was silently left out of
// its primary-key index.
func TestApplyWorkerTypedTextAndCatalogUnwrap(t *testing.T) {
	d, err := parsePgoutputText(catalog.Type{Name: "int4", IsArray: true}, []byte("{1,2,3}"))
	if err != nil || d.StringValue() != "{1,2,3}" {
		t.Fatalf("parsePgoutputText(int4[]) = %v, %v; want the array text", d, err)
	}
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TYPE e46g AS ENUM ('a','b')",
		"CREATE TYPE c46g AS (a FLOAT, b e46g)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	wrapped := *ctx
	wrapped.Catalog = catalog.WithSearchPath(ctx.Catalog, nil)
	for _, tc := range []struct{ typ, val string }{{"e46g", "b"}, {"c46g", "(1,a)"}} {
		col := &catalog.Column{Name: "a", Type: catalog.Type{Name: tc.typ}}
		k1, err1 := encodeBTreeKeyForColumn(ctx, NewStringDatum(tc.val), col, 0)
		k2, err2 := encodeBTreeKeyForColumn(&wrapped, NewStringDatum(tc.val), col, 0)
		if err1 != nil || err2 != nil {
			t.Fatalf("%s key: bare %v, wrapped %v", tc.typ, err1, err2)
		}
		if string(k1) != string(k2) {
			t.Errorf("%s key differs through the search-path wrapper", tc.typ)
		}
	}
}

// TestArrayCastNormalisesScalarElements pins array_in for casts to an
// array of a scalar type (M0149-0046k): '{5, NULL, 3}'::int[] is {5,NULL,3},
// '{2.50,1.0}'::float8[] is {2.5,1}, '{"a", b}'::text[] is {a,b}. The planner
// casts an unknown literal compared with an array column to the column's
// array type, so `a = '{1, 2, 3}'` compares normalised arrays; goopg kept
// the literal's spelling and subscription/t/002_types.pl's UPDATE / DELETE
// by an int[] key matched no row.
func TestArrayCastNormalisesScalarElements(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	for _, tc := range []struct{ typ, in, want string }{
		{"int4[]", "{5, NULL, 3}", "{5,NULL,3}"},
		{"int4[]", "{1, 2, 3}", "{1,2,3}"},
		{"float8[]", "{2.50,1.0}", "{2.5,1}"},
		{"text[]", `{"a", b}`, "{a,b}"},
		{"text[]", `{"x y",NULL,"NULL"}`, `{"x y",NULL,"NULL"}`},
	} {
		d, err := evalCast(NewStringDatum(tc.in), tc.typ, 0, ctx)
		if err != nil {
			t.Fatalf("%s::%s: %v", tc.in, tc.typ, err)
		}
		if d.StringValue() != tc.want {
			t.Errorf("%s::%s = %s, want %s", tc.in, tc.typ, d.StringValue(), tc.want)
		}
	}
}

// TestRestartTruncatedSequencesOwnedBy pins TRUNCATE … RESTART IDENTITY's
// sequence rule (M0149-0047a): a sequence OWNED BY a column of the table is
// reset to its START, as PG's getOwnedSequences does — the naming-convention
// and nextval-default lookups missed `CREATE SEQUENCE s OWNED BY t.a` with
// no default. The logical-replication apply of a Truncate message with
// RESTART_SEQS uses the same helper (subscription/t/010_truncate.pl).
func TestRestartTruncatedSequencesOwnedBy(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE tr47 (a int PRIMARY KEY)",
		"CREATE SEQUENCE seq47 OWNED BY tr47.a",
		"ALTER SEQUENCE seq47 START 101",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	next := func() string {
		t.Helper()
		rows := runSQL(t, ctx, "SELECT nextval('seq47')")
		return rows[0][0].Format()
	}
	if got := next(); got != "1" {
		t.Fatalf("first nextval = %s, want 1", got)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "tr47"})
	if !ok {
		t.Fatal("tr47 not found")
	}
	restartTruncatedSequences(ctx, tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid), nil)
	if got := next(); got != "101" {
		t.Errorf("nextval after restart = %s, want 101 (the sequence's START)", got)
	}
}
