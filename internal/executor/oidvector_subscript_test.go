package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestOidvectorSubscriptIsZeroBasedOid pins oidvector / int2vector
// subscripting (M0149-0001h1a22): pg_proc.proargtypes[0] is the first
// argument type OID, as in PG. goopg indexed the text form's characters, so
// proargtypes[0] of "23 23 2275" was "2". The planner stamps the element
// type (oid / int2) on the array_subscript call.
func TestOidvectorSubscriptIsZeroBasedOid(t *testing.T) {
	sub := func(elem string, idx int64) Datum {
		t.Helper()
		e := &optimizer.FuncCall{Name: "array_subscript", ReturnType: elem,
			Args: []optimizer.Expr{&optimizer.StringConst{Value: "23 23 2275"}, &optimizer.IntegerConst{Value: idx}}}
		d, err := evalExpr(e, nil, &Context{})
		if err != nil {
			t.Fatalf("%s[%d]: %v", elem, idx, err)
		}
		return d
	}
	if d := sub("oid", 0); d.Kind != KindInt || d.Int != 23 {
		t.Errorf("oidvector[0] = %+v, want 23", d)
	}
	if d := sub("oid", 2); d.Kind != KindInt || d.Int != 2275 {
		t.Errorf("oidvector[2] = %+v, want 2275", d)
	}
	if d := sub("int2", 3); !d.IsNull() {
		t.Errorf("int2vector[3] = %+v, want NULL", d)
	}
	// A name subscript keeps its 0-based character semantics.
	if d := sub("char", 0); d.StringValue() != "2" {
		t.Errorf("name[0] = %+v, want \"2\"", d)
	}
}
