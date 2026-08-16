package nodes

import (
	"testing"
)

// TestTimeTZLenCoerceStructure verifies that ResolveForColumnTypmod emits the
// correct FuncExpr shape for timetz(N) column DEFAULTs: funcid 1969,
// funcformat 2 (IMPLICIT), two args (timetz Const + int4 typmod Const).
func TestTimeTZLenCoerceStructure(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"tz0_midnight_utc", "'00:00:00+00:00'", 0},
		{"tz0_pos_offset", "'10:30:00+05:30'", 0},
		{"tz3_frac", "'10:30:00.123+05:30'", 3},
		{"tz6_full", "'10:30:00.123456+05:30'", 6},
		{"tz0_trunc", "'10:30:00.123456+05:30'", 0},
		{"tz0_zulu", "'10:30:00Z'", 0},
		{"tz3_neg_offset", "'10:30:00.100-03:00'", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast := mustParse(t, tt.sql)
			n, ok := ResolveForColumnTypmod(ast, OidTimeTZ, tt.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod returned false")
			}

			f, ok := n.(*FuncExpr)
			if !ok {
				t.Fatalf("expected *FuncExpr, got %T", n)
			}

			// Verify the FuncExpr metadata.
			if f.Funcid != 1969 {
				t.Errorf("Funcid = %d, want 1969", f.Funcid)
			}
			if f.Funcresulttype != OidTimeTZ {
				t.Errorf("Funcresulttype = %d, want %d", f.Funcresulttype, OidTimeTZ)
			}
			if f.Funcformat != 2 {
				t.Errorf("Funcformat = %d, want 2 (COERCE_IMPLICIT_CAST)", f.Funcformat)
			}
			if f.Funcretset {
				t.Error("Funcretset should be false")
			}
			if f.Funcvariadic {
				t.Error("Funcvariadic should be false")
			}
			if len(f.Args) != 2 {
				t.Fatalf("len(Args) = %d, want 2", len(f.Args))
			}

			// Arg[0] must be a timetz Const.
			inner, ok := f.Args[0].(*Const)
			if !ok {
				t.Fatalf("Args[0] = %T, want *Const", f.Args[0])
			}
			if inner.ConstType != OidTimeTZ {
				t.Errorf("inner ConstType = %d, want %d", inner.ConstType, OidTimeTZ)
			}
			if inner.ConstTypmod != -1 {
				t.Errorf("inner ConstTypmod = %d, want -1", inner.ConstTypmod)
			}
			if inner.ConstLen != 12 {
				t.Errorf("inner ConstLen = %d, want 12", inner.ConstLen)
			}
			if inner.ConstByval {
				t.Error("inner ConstByval should be false")
			}

			// Arg[1] must be an int4 Const with the typmod value.
			tm, ok := f.Args[1].(*Const)
			if !ok {
				t.Fatalf("Args[1] = %T, want *Const", f.Args[1])
			}
			if tm.ConstType != OidInt4 {
				t.Errorf("typmod ConstType = %d, want %d", tm.ConstType, OidInt4)
			}
			gotTypmod := int32(int64FromByvalWord(tm.Datum))
			if gotTypmod != tt.colTypmod {
				t.Errorf("typmod = %d, want %d", gotTypmod, tt.colTypmod)
			}
		})
	}
}

// TestTimeTZLenCoerceRebuildRoundTrip verifies that Resolve → Out → Read →
// Rebuild → Resolve → Out is stable (the rebuild fixed point). The implicit
// length coercion unwraps transparently when rebuilt.
func TestTimeTZLenCoerceRebuildRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"tz0_midnight_utc", "'00:00:00+00:00'", 0},
		{"tz0_pos_offset", "'10:30:00+05:30'", 0},
		{"tz3_frac", "'10:30:00.123+05:30'", 3},
		{"tz6_full", "'10:30:00.123456+05:30'", 6},
		{"tz0_trunc", "'10:30:00.123456+05:30'", 0},
		{"tz0_zulu", "'10:30:00Z'", 0},
		{"tz3_neg_offset", "'10:30:00.100-03:00'", 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Forward resolve.
			n1, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidTimeTZ, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod rejected %q", tc.sql)
			}
			out1 := Out(n1)

			// Codec round-trip through PG's text form.
			gotNode, err := Read(out1)
			if err != nil {
				t.Fatalf("Read(Out(n)): %v", err)
			}

			// Rebuild to parser AST.
			ast, err := Rebuild(gotNode)
			if err != nil {
				t.Fatalf("Rebuild: %v", err)
			}

			// Re-resolve through the same column context.
			n2, ok := ResolveForColumnTypmod(ast, OidTimeTZ, tc.colTypmod)
			if !ok {
				t.Fatalf("re-resolve rejected rebuilt AST")
			}
			out2 := Out(n2)

			if out2 != out1 {
				t.Errorf("rebuild round-trip not fixed point:\n  first  %s\n  second %s", out1, out2)
			}
		})
	}
}

// TestTimeTZLenCoerceNoWrap verifies that a timetz column without a precision
// qualifier (typmod < 0) does NOT wrap the result.
func TestTimeTZLenCoerceNoWrap(t *testing.T) {
	ast := mustParse(t, "'10:30:00+05:30'")
	n, ok := ResolveForColumnTypmod(ast, OidTimeTZ, -1)
	if !ok {
		t.Fatal("ResolveForColumnTypmod returned false")
	}
	// With no typmod, the result should be a bare Const, not a FuncExpr.
	f, isFunc := n.(*FuncExpr)
	if isFunc {
		t.Fatalf("bare timetz got unexpected FuncExpr: funcid %d, format %d", f.Funcid, f.Funcformat)
	}
	c, isConst := n.(*Const)
	if !isConst || c.ConstType != OidTimeTZ || c.ConstTypmod != -1 {
		t.Fatalf("bare timetz expected plain timetz Const, got %T", n)
	}
}

// TestTimeTZLenCoerceColumnTypmod verifies that ColumnTypmod returns the precision
// for the "timetz" and "time with time zone" type names.
func TestTimeTZLenCoerceColumnTypmod(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		args     []int64
		want     int32
	}{
		{"tz3", "timetz", []int64{3}, 3},
		{"tz0", "timetz", []int64{0}, 0},
		{"tz6", "timetz", []int64{6}, 6},
		{"twtz3", "time with time zone", []int64{3}, 3},
		{"twtz6", "time with time zone", []int64{6}, 6},
		{"noargs", "timetz", nil, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ColumnTypmod(tt.typeName, tt.args)
			if got != tt.want {
				t.Errorf("ColumnTypmod(%q, %v) = %d, want %d",
					tt.typeName, tt.args, got, tt.want)
			}
		})
	}
}
