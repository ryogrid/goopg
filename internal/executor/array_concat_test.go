package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestEvalConcatArrays pins the executor's OpConcat array merge (already
// implemented at M0097-0065): `SELECT ARRAY['a','b'] || ARRAY['c','d']`
// evaluates to {a,b,c,d}. The analyzer used to reject array || array with
// 42883 "operator does not exist: text[] || text[]" before the executor ever
// ran; this guards the analyzer change (M0134-0002 C1) by pinning the runtime
// shape it now lets through. The text-form {…} datums below are exactly what
// the ARRAY[…] constructor / reloptions column produce at runtime.
func TestEvalConcatArrays(t *testing.T) {
	cases := []struct {
		name  string
		left  Datum
		right Datum
		want  string
	}{
		{"array || array (array_cat)", NewStringDatum("{a,b}"), NewStringDatum("{c,d}"), "{a,b,c,d}"},
		{"empty left", NewStringDatum("{}"), NewStringDatum("{c,d}"), "{c,d}"},
		{"empty right", NewStringDatum("{a,b}"), NewStringDatum("{}"), "{a,b}"},
		{"array || element (array_append)", NewStringDatum("{a,b}"), NewStringDatum("c"), "{a,b,c}"},
		{"element || array (array_prepend)", NewStringDatum("a"), NewStringDatum("{b,c}"), "{a,b,c}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := evalBinary(parser.OpConcat, c.left, c.right, 0, nil)
			if err != nil {
				t.Fatalf("evalBinary(||): %v", err)
			}
			if got.Kind != KindString || got.StringValue() != c.want {
				t.Errorf("|| = %+v (%q), want %q", got, got.StringValue(), c.want)
			}
		})
	}
}
