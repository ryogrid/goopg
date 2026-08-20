package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPowEvalBinary pins evalBinary's `^` arm against PostgreSQL's dpow
// semantics (postgres/src/backend/utils/adt/float.c:dpow), ported verbatim
// at internal/executor/expr.go's dpow helper. Result is always float8
// (KindNumeric here — goopg has no KindFloat; float values round-trip
// through PGFloatOut text into KindNumeric, per floatTextDatum/codec.go).
// M0134-0019b.
func TestPowEvalBinary(t *testing.T) {
	cases := []struct {
		name string
		a, b Datum
		want string
	}{
		{"2^16", NewIntDatum(2), NewIntDatum(16), "65536"},
		{"2^3^2-left-assoc-inner", NewIntDatum(2), NewIntDatum(3), "8"}, // sanity: 2^3 alone
		{"nan-pow-zero", NewStringDatum("NaN"), NewIntDatum(0), "1"},
		{"one-pow-nan", NewIntDatum(1), NewStringDatum("NaN"), "1"},
		{"nan-pow-nonzero", NewStringDatum("NaN"), NewIntDatum(2), "NaN"},
	}
	for _, c := range cases {
		got, err := evalBinary(parser.OpPow, c.a, c.b, 0, nil)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if got.Format() != c.want {
			t.Errorf("%s: got=%s want=%s", c.name, got.Format(), c.want)
		}
	}
}

// TestPowNullPropagation: NULL on either side yields NULL, the existing
// binary-op convention (evalBinary's early NULL check, expr.go:1481).
func TestPowNullPropagation(t *testing.T) {
	got, err := evalBinary(parser.OpPow, NullDatum, NewIntDatum(2), 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsNull() {
		t.Errorf("NULL^2 = %+v, want NULL", got)
	}
	got, err = evalBinary(parser.OpPow, NewIntDatum(2), NullDatum, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsNull() {
		t.Errorf("2^NULL = %+v, want NULL", got)
	}
}

// TestPowErrorGuards pins the two dpow domain errors: 0^negative and
// negative^non-integer both raise SQLSTATE 22023 with PG's exact message
// text (not the divide-by-zero code, per dpow's own comment).
func TestPowErrorGuards(t *testing.T) {
	cases := []struct {
		name    string
		a, b    Datum
		wantMsg string
	}{
		{"zero-neg-power", NewIntDatum(0), NewIntDatum(-1), "zero raised to a negative power is undefined"},
		{"neg-noninteger-power", NewIntDatum(-2), NewStringDatum("0.5"), "a negative number raised to a non-integer power yields a complex result"},
	}
	for _, c := range cases {
		_, err := evalBinary(parser.OpPow, c.a, c.b, 0, nil)
		if err == nil {
			t.Errorf("%s: expected error, got none", c.name)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("%s: err type=%T, want *ExecError", c.name, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("%s: SQLSTATE=%s want 22023", c.name, ee.Code)
		}
		if ee.Message != c.wantMsg {
			t.Errorf("%s: message=%q want %q", c.name, ee.Message, c.wantMsg)
		}
	}
}
