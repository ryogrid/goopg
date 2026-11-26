package executor

import "testing"

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
