package optimizer

import "testing"

// TestFoldPgTypeofKeepsSetReturningArgument: pg_typeof(generate_series(1,2))
// returns two rows, so folding must keep the set-returning call where
// buildSelectSrfProjectSet can find it, and drop any other argument.
func TestFoldPgTypeofKeepsSetReturningArgument(t *testing.T) {
	srf := &FuncCall{Name: "generate_series", Args: []Expr{&IntegerConst{Value: 1}, &IntegerConst{Value: 2}}, ReturnType: "int4"}
	fc := foldPgTypeof(1, srf)
	if len(fc.Args) != 2 || fc.Args[1] != Expr(srf) {
		t.Fatalf("args = %#v", fc.Args)
	}
	if findFirstNestedSRF(fc) != srf {
		t.Error("the set-returning call is not found inside the folded call")
	}
	if _, ok := fc.Args[0].(*StringConst); !ok {
		t.Errorf("first argument = %#v", fc.Args[0])
	}
	plain := foldPgTypeof(1, &IntegerConst{Value: 1})
	if len(plain.Args) != 1 {
		t.Errorf("plain argument kept: %#v", plain.Args)
	}
}
