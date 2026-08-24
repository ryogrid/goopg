package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestEvalStartsWith pins starts_with(text, text) (pg_proc oid 3696,
// postgres/src/backend/utils/adt/varlena.c text_starts_with). starts_with
// was fully registered in the catalog but had zero evalFuncCall dispatch
// arm, so every call raised "function starts_with does not exist" —
// surfaced live by create_index_spgist.sql. M0134-0111.
func TestEvalStartsWith(t *testing.T) {
	cases := []struct {
		name       string
		s, prefix  optimizer.Expr
		wantNull   bool
		wantResult bool
	}{
		{name: "matching prefix", s: &optimizer.StringConst{Value: "Worthington St"}, prefix: &optimizer.StringConst{Value: "Worth"}, wantResult: true},
		{name: "non-matching prefix", s: &optimizer.StringConst{Value: "Aztec Ct"}, prefix: &optimizer.StringConst{Value: "Worth"}, wantResult: false},
		{name: "empty prefix always matches", s: &optimizer.StringConst{Value: "anything"}, prefix: &optimizer.StringConst{Value: ""}, wantResult: true},
		{name: "prefix longer than string", s: &optimizer.StringConst{Value: "Wo"}, prefix: &optimizer.StringConst{Value: "Worth"}, wantResult: false},
		{name: "NULL string propagates NULL", s: &optimizer.NullConst{}, prefix: &optimizer.StringConst{Value: "Worth"}, wantNull: true},
		{name: "NULL prefix propagates NULL", s: &optimizer.StringConst{Value: "Worth St"}, prefix: &optimizer.NullConst{}, wantNull: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &optimizer.FuncCall{Name: "starts_with", Args: []optimizer.Expr{c.s, c.prefix}}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if got.IsNull() != c.wantNull {
				t.Fatalf("got null=%v, want null=%v", got.IsNull(), c.wantNull)
			}
			if !c.wantNull && got.BoolValue() != c.wantResult {
				t.Fatalf("got %+v, want %v", got, c.wantResult)
			}
		})
	}
}
