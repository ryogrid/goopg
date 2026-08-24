package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestEvalPointBoxContainment pins point<@box / box@>point dispatch
// (postgres/src/backend/utils/adt/geo_ops.c point_box.c: box_contain_pt /
// on_pb). The OpContainedBy/OpContains/OpOverlap arm added by M0097-0023
// only ever attempted box-vs-box parsing (parseBoxText on both operands),
// so a bare point operand always hard-errored "invalid box value" even in
// create_index_spgist.sql's pure-seqscan section, which needs no index at
// all. M0134-0111.
func TestEvalPointBoxContainment(t *testing.T) {
	box := NewStringDatum("(1000,1000),(200,200)")
	inside := NewStringDatum("(333,400)")
	outside := NewStringDatum("(5000,4000)")

	got, err := evalBinary(parser.OpContainedBy, inside, box, 0, &Context{})
	if err != nil {
		t.Fatalf("point <@ box (inside): %v", err)
	}
	if got.IsNull() || !got.BoolValue() {
		t.Fatalf("point <@ box (inside) = %+v, want true", got)
	}

	got, err = evalBinary(parser.OpContainedBy, outside, box, 0, &Context{})
	if err != nil {
		t.Fatalf("point <@ box (outside): %v", err)
	}
	if got.IsNull() || got.BoolValue() {
		t.Fatalf("point <@ box (outside) = %+v, want false", got)
	}

	got, err = evalBinary(parser.OpContains, box, inside, 0, &Context{})
	if err != nil {
		t.Fatalf("box @> point (inside): %v", err)
	}
	if got.IsNull() || !got.BoolValue() {
		t.Fatalf("box @> point (inside) = %+v, want true", got)
	}

	got, err = evalBinary(parser.OpContains, box, outside, 0, &Context{})
	if err != nil {
		t.Fatalf("box @> point (outside): %v", err)
	}
	if got.IsNull() || got.BoolValue() {
		t.Fatalf("box @> point (outside) = %+v, want false", got)
	}

	// Box-vs-box (the pre-existing path) must still work unchanged.
	inner := NewStringDatum("(900,900),(300,300)")
	got, err = evalBinary(parser.OpContainedBy, inner, box, 0, &Context{})
	if err != nil {
		t.Fatalf("box <@ box: %v", err)
	}
	if got.IsNull() || !got.BoolValue() {
		t.Fatalf("box <@ box = %+v, want true", got)
	}
}
