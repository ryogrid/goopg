package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestNumericScale pins numeric_scale: the display scale of the argument.
func TestNumericScale(t *testing.T) {
	for in, want := range map[string]int64{"1.50": 2, "12345.000001": 6, "0": 0} {
		fc := &optimizer.FuncCall{Name: "scale", Args: []optimizer.Expr{&optimizer.NumericConst{Value: in}}}
		d, err := evalFuncCall(fc, nil, &Context{})
		if err != nil || d.Kind != KindInt || d.Int != want {
			t.Errorf("scale(%s) = %v, %v; want %d", in, d, err, want)
		}
	}
	fc := &optimizer.FuncCall{Name: "scale", Args: []optimizer.Expr{&optimizer.NullConst{}}}
	if d, err := evalFuncCall(fc, nil, &Context{}); err != nil || !d.IsNull() {
		t.Errorf("scale(NULL) = %v, %v", d, err)
	}
}
