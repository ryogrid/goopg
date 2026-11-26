package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
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
