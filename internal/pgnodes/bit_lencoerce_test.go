package pgnodes

import (
	"testing"
)

// TestBitLenCoerceStructure verifies that ResolveForColumnTypmod emits the
// correct FuncExpr shape for bit(N) column DEFAULTs with bare literals (implicit
// fold): funcid 1685, funcformat 2 (COERCE_IMPLICIT_CAST), three args
// (bit Const + int4 typmod Const + bool isExplicit=false).
func TestBitLenCoerceStructure(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"bit4_1010", "'1010'", 4},
		{"bit8_11110000", "'11110000'", 8},
		{"bit1_single", "'1'", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast := mustParse(t, tt.sql)
			n, ok := ResolveForColumnTypmod(ast, OidBit, tt.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod returned false")
			}

			f, ok := n.(*FuncExpr)
			if !ok {
				t.Fatalf("expected *FuncExpr, got %T", n)
			}

			if f.Funcid != 1685 {
				t.Errorf("Funcid = %d, want 1685", f.Funcid)
			}
			if f.Funcresulttype != OidBit {
				t.Errorf("Funcresulttype = %d, want %d", f.Funcresulttype, OidBit)
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
			if len(f.Args) != 3 {
				t.Fatalf("len(Args) = %d, want 3", len(f.Args))
			}

			// Arg[0] must be a bit Const.
			inner, ok := f.Args[0].(*Const)
			if !ok {
				t.Fatalf("Args[0] = %T, want *Const", f.Args[0])
			}
			if inner.ConstType != OidBit {
				t.Errorf("Args[0].ConstType = %d, want %d", inner.ConstType, OidBit)
			}
			if inner.ConstTypmod != -1 {
				t.Errorf("Args[0].ConstTypmod = %d, want -1", inner.ConstTypmod)
			}

			// Arg[1] must be an int4 Const carrying the column typmod.
			tc, ok := f.Args[1].(*Const)
			if !ok {
				t.Fatalf("Args[1] = %T, want *Const", f.Args[1])
			}
			if tc.ConstType != OidInt4 {
				t.Errorf("Args[1].ConstType = %d, want %d", tc.ConstType, OidInt4)
			}
			tm := int32(int64FromByvalWord(tc.Datum))
			if tm != tt.colTypmod {
				t.Errorf("Args[1] typmod = %d, want %d", tm, tt.colTypmod)
			}

			// Arg[2] must be a bool Const = false (isExplicit=false for implicit).
			bc, ok := f.Args[2].(*Const)
			if !ok {
				t.Fatalf("Args[2] = %T, want *Const", f.Args[2])
			}
			if bc.ConstType != OidBool {
				t.Errorf("Args[2].ConstType = %d, want %d", bc.ConstType, OidBool)
			}
			if bc.Datum[0] != 0 {
				t.Error("Args[2] should be false")
			}
		})
	}
}

// TestVarBitLenCoerceStructure verifies the varbit length coercion shape for
// bare literals: funcid 1687, funcformat 2, three args.
func TestVarBitLenCoerceStructure(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"varbit6_111000", "'111000'", 6},
		{"varbit8_10101010", "'10101010'", 8},
		{"varbit3_101", "'101'", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast := mustParse(t, tt.sql)
			n, ok := ResolveForColumnTypmod(ast, OidVarBit, tt.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod returned false")
			}

			f, ok := n.(*FuncExpr)
			if !ok {
				t.Fatalf("expected *FuncExpr, got %T", n)
			}

			if f.Funcid != 1687 {
				t.Errorf("Funcid = %d, want 1687", f.Funcid)
			}
			if f.Funcresulttype != OidVarBit {
				t.Errorf("Funcresulttype = %d, want %d", f.Funcresulttype, OidVarBit)
			}
			if f.Funcformat != 2 {
				t.Errorf("Funcformat = %d, want 2 (COERCE_IMPLICIT_CAST)", f.Funcformat)
			}
			if len(f.Args) != 3 {
				t.Fatalf("len(Args) = %d, want 3", len(f.Args))
			}

			inner, ok := f.Args[0].(*Const)
			if !ok {
				t.Fatalf("Args[0] = %T, want *Const", f.Args[0])
			}
			if inner.ConstType != OidVarBit {
				t.Errorf("Args[0].ConstType = %d, want %d", inner.ConstType, OidVarBit)
			}

			bc, ok := f.Args[2].(*Const)
			if !ok {
				t.Fatalf("Args[2] = %T, want *Const", f.Args[2])
			}
			if bc.Datum[0] != 0 {
				t.Error("Args[2] should be false")
			}
		})
	}
}

// TestVarbitNoLengthQualifier verifies that a varbit column without a length
// qualifier (typmod -1) does NOT wrap in a length coercion — PG stores a bare
// Const. (bit without N defaults to bit(1), so that case always wraps.)
func TestVarbitNoLengthQualifier(t *testing.T) {
	ast := mustParse(t, "'10101'")
	n, ok := ResolveForColumnTypmod(ast, OidVarBit, -1)
	if !ok {
		t.Fatalf("ResolveForColumnTypmod returned false")
	}

	c, ok := n.(*Const)
	if !ok {
		t.Fatalf("expected *Const, got %T", n)
	}
	if c.ConstType != OidVarBit {
		t.Errorf("ConstType = %d, want %d", c.ConstType, OidVarBit)
	}
	if c.ConstTypmod != -1 {
		t.Errorf("ConstTypmod = %d, want -1", c.ConstTypmod)
	}
}

// TestBitParseFormatRoundTrip verifies parseBitFromString + formatBit are inverses.
func TestBitParseFormatRoundTrip(t *testing.T) {
	tests := []string{
		"",
		"0",
		"1",
		"1010",
		"11110000",
		"10101010",
		"1010101010101010",
	}

	for _, s := range tests {
		t.Run(s, func(t *testing.T) {
			bitLen, data, ok := parseBitFromString(s)
			if !ok {
				t.Fatalf("parseBitFromString(%q) returned false", s)
			}
			if bitLen != int32(len(s)) {
				t.Errorf("bitLen = %d, want %d", bitLen, len(s))
			}
			got := formatBit(bitLen, data)
			if got != s {
				t.Errorf("formatBit = %q, want %q", got, s)
			}
		})
	}
}

// TestBitConstRoundTrip verifies NewBitConst → varlena → formatBit reproduces
// the input string.
func TestBitConstRoundTrip(t *testing.T) {
	for _, s := range []string{"0", "1", "1010", "11110000", "10101010"} {
		c, err := NewBitConst(s)
		if err != nil {
			t.Fatalf("NewBitConst(%q): %v", s, err)
		}
		bitLen := bitLenFromVarlena(c.Datum)
		data := bitDataFromVarlena(c.Datum)
		got := formatBit(bitLen, data)
		if got != s {
			t.Errorf("round-trip = %q, want %q", got, s)
		}
	}
}

// TestNewBitConstRejectsNonBit verifies NewBitConst errors on invalid input.
func TestNewBitConstRejectsNonBit(t *testing.T) {
	for _, s := range []string{"2", "a", "01a", "10 10"} {
		_, err := NewBitConst(s)
		if err == nil {
			t.Errorf("NewBitConst(%q) should have returned an error", s)
		}
	}
}

// TestColumnTypmodBit verifies ColumnTypmod for bit/varbit type names.
func TestColumnTypmodBit(t *testing.T) {
	tests := []struct {
		typeName string
		args     []int64
		want     int32
	}{
		{"bit", []int64{4}, 4},
		{"bit", []int64{8}, 8},
		{"bit", []int64{}, -1},
		{"bit", []int64{0}, -1},
		{"bit varying", []int64{6}, 6},
		{"bit varying", []int64{}, -1},
		{"varbit", []int64{8}, 8},
		{"varbit", []int64{}, -1},
	}

	for _, tt := range tests {
		got := ColumnTypmod(tt.typeName, tt.args)
		if got != tt.want {
			t.Errorf("ColumnTypmod(%q, %v) = %d, want %d", tt.typeName, tt.args, got, tt.want)
		}
	}
}

// TestBitLenCoerceRebuildRoundTrip verifies the Resolve → Out → Read →
// Rebuild → Resolve → Out fixed point for bit(N) length coercion.
func TestBitLenCoerceRebuildRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"bit4_1010", "'1010'", 4},
		{"bit8_11110000", "'11110000'", 8},
		{"bit1_1", "'1'", 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n1, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidBit, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod rejected %q", tc.sql)
			}
			out1 := Out(n1)

			gotNode, err := Read(out1)
			if err != nil {
				t.Fatalf("Read(Out(n)): %v", err)
			}

			ast, err := Rebuild(gotNode)
			if err != nil {
				t.Fatalf("Rebuild: %v", err)
			}

			n2, ok := ResolveForColumnTypmod(ast, OidBit, tc.colTypmod)
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

// TestVarBitLenCoerceRebuildRoundTrip verifies the rebuild fixed point for
// varbit(N) length coercion.
func TestVarBitLenCoerceRebuildRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"varbit6_111000", "'111000'", 6},
		{"varbit8_10101010", "'10101010'", 8},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n1, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidVarBit, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod rejected %q", tc.sql)
			}
			out1 := Out(n1)

			gotNode, err := Read(out1)
			if err != nil {
				t.Fatalf("Read(Out(n)): %v", err)
			}

			ast, err := Rebuild(gotNode)
			if err != nil {
				t.Fatalf("Rebuild: %v", err)
			}

			n2, ok := ResolveForColumnTypmod(ast, OidVarBit, tc.colTypmod)
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

// TestBitLenCoerceMatchesGolden verifies that ResolveForColumnTypmod emits
// byte-for-byte the same pg_node_tree that PG18.3 stores in pg_attrdef.adbin for
// a bit(N) column DEFAULT with bare string literals.
func TestBitLenCoerceMatchesGolden(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		colTypmod int32
		golden    string
	}{
		{
			name:      "bit4_1010",
			sql:       "'1010'",
			colTypmod: 4,
			golden:    `{FUNCEXPR :funcid 1685 :funcresulttype 1560 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1560 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 9 [ 36 0 0 0 4 0 0 0 -96 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 4 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "bit8_11110000",
			sql:       "'11110000'",
			colTypmod: 8,
			golden:    `{FUNCEXPR :funcid 1685 :funcresulttype 1560 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1560 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 9 [ 36 0 0 0 8 0 0 0 -16 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 8 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "bit1_1",
			sql:       "'1'",
			colTypmod: 1,
			golden:    `{FUNCEXPR :funcid 1685 :funcresulttype 1560 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1560 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 9 [ 36 0 0 0 1 0 0 0 -128 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidBit, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod returned false")
			}
			got := Out(n)
			if got != tc.golden {
				t.Errorf("adbin mismatch:\n  got  %s\n  want %s", got, tc.golden)
			}
		})
	}
}

// TestVarBitLenCoerceMatchesGolden verifies byte-identical goldens for
// varbit(N) length coercion.
func TestVarBitLenCoerceMatchesGolden(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		colTypmod int32
		golden    string
	}{
		{
			name:      "varbit6_111000",
			sql:       "'111000'",
			colTypmod: 6,
			golden:    `{FUNCEXPR :funcid 1687 :funcresulttype 1562 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1562 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 9 [ 36 0 0 0 6 0 0 0 -32 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "varbit8_10101010",
			sql:       "'10101010'",
			colTypmod: 8,
			golden:    `{FUNCEXPR :funcid 1687 :funcresulttype 1562 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1562 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 9 [ 36 0 0 0 8 0 0 0 -86 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 8 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidVarBit, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod returned false")
			}
			got := Out(n)
			if got != tc.golden {
				t.Errorf("adbin mismatch:\n  got  %s\n  want %s", got, tc.golden)
			}
		})
	}
}
