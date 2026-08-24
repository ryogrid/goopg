package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestEvalCardinality pins PostgreSQL's cardinality(anyarray) semantics
// (postgres/src/backend/utils/adt/array_userfuncs.c array_cardinality):
// total element count, 0 (not NULL) for an empty array, NULL for a NULL
// array. Surfaced by brin.sql's brinopers_check CHECK constraint (M0134-0095
// sizing), which was raising "function cardinality does not exist" —
// cardinality had a pg_proc entry but no evalFuncCall dispatch arm.
func TestEvalCardinality(t *testing.T) {
	cases := []struct {
		name string
		arg  optimizer.Expr
		want Datum
	}{
		{
			name: "counts elements",
			arg:  &optimizer.StringConst{Value: "{1,2,3}"},
			want: NewIntDatum(3),
		},
		{
			name: "empty array is zero not NULL",
			arg:  &optimizer.StringConst{Value: "{}"},
			want: NewIntDatum(0),
		},
		{
			name: "single element",
			arg:  &optimizer.StringConst{Value: "{a}"},
			want: NewIntDatum(1),
		},
		{
			name: "NULL array propagates NULL",
			arg:  &optimizer.NullConst{},
			want: NullDatum,
		},
		{
			// parseTextArray previously failed to skip the space PG's own
			// array literal syntax allows after a comma (`", "`), so the
			// unquoted-element branch swallowed the space AND the next
			// element's opening quote as one bogus element — e.g. this
			// 5-element tid array mis-counted as 9. Surfaced live by
			// brin.sql's brinopers_check CHECK constraint (M0134-0095
			// sizing): a multi-row INSERT's tidcol row raised a spurious
			// "violates check constraint" once cardinality() itself worked.
			name: "quoted elements separated by comma-space count correctly",
			arg:  &optimizer.StringConst{Value: `{"(0,0)", "(0,0)", "(8800,0)", "(9999,19)", "(9999,19)"}`},
			want: NewIntDatum(5),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &optimizer.FuncCall{Name: "cardinality", Args: []optimizer.Expr{c.arg}}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if got.IsNull() != c.want.IsNull() {
				t.Fatalf("got null=%v, want null=%v", got.IsNull(), c.want.IsNull())
			}
			if !got.IsNull() && got.Int != c.want.Int {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
