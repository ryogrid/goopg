package nodes

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// string_numeric_typmod_cast_test.go — M0123-S4 sub-slice 30 gate: an unknown-type
// string literal cast to numeric(p,s) — e.g. '5.5'::numeric(10,2) — folds at parse
// time in PG: coerce_type first converts the unknown literal to a bare numeric Const
// via stringTypeToConst → numeric_in, then coerce_type_typmod applies the length
// coercion numeric(numeric,int4) (funcid 1703, funcformat 1 = COERCE_EXPLICIT_CAST).
// The stored adbin is a FuncExpr wrapping the numeric Const + int4 packed-typmod
// Const. The column DEFAULT path uses funcformat 2 (COERCE_IMPLICIT_CAST), so the
// two paths are NOT byte-identical — they differ in funcformat.
//
// This extends sub-slices 29/29b/29c (bare-numeric/NaN/float/oid string folds from
// sub-slice 28) and mirrors the existing resolveNumericTypmodCast path for int4/int8
// operands.

// TestStringNumericTypmodCastResolve proves the explicit typmod'd string cast resolves
// to a FuncExpr(funcid=1703, funcformat=1) whose first arg is a bare numeric Const and
// whose second arg is the packed int4 typmod Const. Also checks codec round-trip.
func TestStringNumericTypmodCastResolve(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantPackedLSB byte // least-significant byte of the packed typmod
	}{
		// packed typmod = ((prec << 16) | (scale & 0x7ff)) + 4
		// numeric(10,2): ((10<<16)|(2&0x7ff))+4 = 655366; LE bytes [6, 0, 10, 0]
		// numeric(8,1):  ((8<<16)|(1&0x7ff))+4  = 524293; LE bytes [5, 0, 8, 0]
		// numeric(5):    ((5<<16)|(0&0x7ff))+4  = 327684; LE bytes [4, 0, 5, 0]
		{"numeric_10_2", "'5.5'::numeric(10,2)", 6},
		{"numeric_8_1", "'123.45'::numeric(8,1)", 5},
		{"numeric_5", "'42'::numeric(5)", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), 0)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			f, ok := n.(*FuncExpr)
			if !ok {
				t.Fatalf("%q resolved to %T, want *FuncExpr", tc.sql, n)
			}
			if f.Funcid != 1703 {
				t.Errorf("Funcid = %d, want 1703", f.Funcid)
			}
			if f.Funcformat != 1 {
				t.Errorf("Funcformat = %d, want 1 (COERCE_EXPLICIT_CAST)", f.Funcformat)
			}
			if f.Funcresulttype != OidNumeric {
				t.Errorf("Funcresulttype = %d, want %d (OidNumeric)", f.Funcresulttype, OidNumeric)
			}
			if len(f.Args) != 2 {
				t.Fatalf("len(Args) = %d, want 2", len(f.Args))
			}
			// Args[0] must be a bare numeric Const (not another FuncExpr / RelabelType)
			c, ok := f.Args[0].(*Const)
			if !ok {
				t.Fatalf("Args[0] is %T, want *Const", f.Args[0])
			}
			if c.ConstType != OidNumeric {
				t.Errorf("Args[0].ConstType = %d, want %d (OidNumeric)", c.ConstType, OidNumeric)
			}
			if c.ConstTypmod != -1 {
				t.Errorf("Args[0].ConstTypmod = %d, want -1 (bare numeric, no typmod)", c.ConstTypmod)
			}
			// Args[1] must be an int4 Const with the packed typmod
			tc2, ok := f.Args[1].(*Const)
			if !ok {
				t.Fatalf("Args[1] is %T, want *Const", f.Args[1])
			}
			if tc2.ConstType != OidInt4 {
				t.Errorf("Args[1].ConstType = %d, want %d (OidInt4)", tc2.ConstType, OidInt4)
			}
			if tc2.ConstLen != 4 {
				t.Errorf("Args[1].ConstLen = %d, want 4", tc2.ConstLen)
			}
			if len(tc2.Datum) < 4 {
				t.Errorf("len(Args[1].Datum) = %d, want >= 4", len(tc2.Datum))
			}
			if len(tc2.Datum) >= 1 && tc2.Datum[0] != tc.wantPackedLSB {
				t.Errorf("Args[1].Datum[0] = %d, want %d (packed typmod LSB)", tc2.Datum[0], tc.wantPackedLSB)
			}
			// Codec round-trip: Out → Read → Out must be stable
			got := Out(n)
			back, err := Read(got)
			if err != nil {
				t.Fatalf("Read(Out(%q)): %v", tc.sql, err)
			}
			if Out(back) != got {
				t.Fatalf("codec round-trip mismatch for %q:\n got: %s\nwant: %s", tc.name, Out(back), got)
			}
		})
	}
}

// TestStringNumericTypmodCastRebuildRoundTrip proves resolve → Rebuild → re-resolve
// is a fixed point: the stored FuncExpr(funcformat=1) rebuilds to a CastExpr with
// Typmods, and re-resolving produces the identical FuncExpr.
func TestStringNumericTypmodCastRebuildRoundTrip(t *testing.T) {
	cases := []string{
		"'5.5'::numeric(10,2)",
		"'123.45'::numeric(8,1)",
		"'42'::numeric(5)",
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, sql), 0)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", sql, err)
			}
			// Rebuild must produce a CastExpr, not a bare literal
			if _, ok := ast.(*parser.CastExpr); !ok {
				t.Errorf("Rebuild(%q) returned %T, want *parser.CastExpr", sql, ast)
			}
			// Re-resolve the rebuilt AST as an explicit cast (expected=0)
			n2, err := ResolveExpr(ast, 0)
			if err != nil {
				t.Fatalf("re-resolve(%q): %v", sql, err)
			}
			if Out(n1) != Out(n2) {
				t.Fatalf("rebuild round-trip mismatch for %q:\n res1: %s\n res2: %s",
					sql, Out(n1), Out(n2))
			}
		})
	}
}

// TestStringNumericTypmodCastDegrades verifies graceful degradation: a non-numeric
// string cast to numeric(p,s), out-of-range typmod, and typmod'd non-numeric targets
// all degrade to SQL text (ErrUnsupported).
func TestStringNumericTypmodCastDegrades(t *testing.T) {
	shouldDegrade := []string{
		"'hello'::numeric(10,2)", // non-numeric string
		"'5.5'::numeric(1001,2)", // precision out of range (>1000)
		"'5.5'::numeric(10,11)",  // scale > precision
		"'hello'::varchar(10)",   // typmod'd non-numeric target
	}
	for _, sql := range shouldDegrade {
		t.Run(sql, func(t *testing.T) {
			_, err := ResolveExpr(mustParse(t, sql), 0)
			if err == nil {
				t.Fatalf("ResolveExpr(%q) succeeded, want ErrUnsupported", sql)
			}
		})
	}
}
