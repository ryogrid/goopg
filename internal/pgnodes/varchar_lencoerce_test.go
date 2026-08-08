package pgnodes

import "testing"

// TestVarcharLenCoerceMatchesGolden verifies that ResolveForColumnTypmod emits
// byte-for-byte the same pg_node_tree that PG18.3 stores in pg_attrdef.adbin for
// a varchar(N) / bpchar(N) column DEFAULT.
func TestVarcharLenCoerceMatchesGolden(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		colType    uint32
		colTypmod  int32
		golden     string
	}{
		{
			name:      "varchar10_hello",
			sql:       "'hello'",
			colType:   OidVarchar,
			colTypmod: 14, // 10 + 4
			golden:    `{FUNCEXPR :funcid 669 :funcresulttype 1043 :funcretset false :funcvariadic false :funcformat 2 :funccollid 100 :inputcollid 100 :args ({CONST :consttype 1043 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 9 [ 36 0 0 0 104 101 108 108 111 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 14 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "varchar20_world",
			sql:       "'world'",
			colType:   OidVarchar,
			colTypmod: 24, // 20 + 4
			golden:    `{FUNCEXPR :funcid 669 :funcresulttype 1043 :funcretset false :funcvariadic false :funcformat 2 :funccollid 100 :inputcollid 100 :args ({CONST :consttype 1043 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 9 [ 36 0 0 0 119 111 114 108 100 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 24 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "varchar5_empty",
			sql:       "''",
			colType:   OidVarchar,
			colTypmod: 9, // 5 + 4
			golden:    `{FUNCEXPR :funcid 669 :funcresulttype 1043 :funcretset false :funcvariadic false :funcformat 2 :funccollid 100 :inputcollid 100 :args ({CONST :consttype 1043 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 4 [ 16 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 9 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "bpchar5_abc",
			sql:       "'abc'",
			colType:   OidBpchar,
			colTypmod: 9, // 5 + 4
			golden:    `{FUNCEXPR :funcid 668 :funcresulttype 1042 :funcretset false :funcvariadic false :funcformat 2 :funccollid 100 :inputcollid 100 :args ({CONST :consttype 1042 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 7 [ 28 0 0 0 97 98 99 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 9 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "bpchar10_xyz",
			sql:       "'xyz'",
			colType:   OidBpchar,
			colTypmod: 14, // 10 + 4
			golden:    `{FUNCEXPR :funcid 668 :funcresulttype 1042 :funcretset false :funcvariadic false :funcformat 2 :funccollid 100 :inputcollid 100 :args ({CONST :consttype 1042 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 7 [ 28 0 0 0 120 121 122 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 14 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "bpchar3_empty",
			sql:       "''",
			colType:   OidBpchar,
			colTypmod: 7, // 3 + 4
			golden:    `{FUNCEXPR :funcid 668 :funcresulttype 1042 :funcretset false :funcvariadic false :funcformat 2 :funccollid 100 :inputcollid 100 :args ({CONST :consttype 1042 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 4 [ 16 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 7 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), tc.colType, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod(%q, %d, %d) rejected a canonical default",
					tc.sql, tc.colType, tc.colTypmod)
			}
			got := Out(n)
			if got != tc.golden {
				t.Errorf("Out mismatch:\n  got  %s\n  want %s", got, tc.golden)
			}
		})
	}
}

// TestVarcharLenCoerceRebuildRoundTrip verifies that extracting the inner varchar/bpchar
// Const from the length-coercion FuncExpr, rebuilding it back to SQL, and re-resolving
// through ResolveForColumnTypmod produces a byte-identical Out (fixed point).
func TestVarcharLenCoerceRebuildRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		colType   uint32
		colTypmod int32
	}{
		{"varchar10_hello", "'hello'", OidVarchar, 14},
		{"varchar20_world", "'world'", OidVarchar, 24},
		{"varchar5_empty", "''", OidVarchar, 9},
		{"bpchar5_abc", "'abc'", OidBpchar, 9},
		{"bpchar10_xyz", "'xyz'", OidBpchar, 14},
		{"bpchar3_empty", "''", OidBpchar, 7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Forward resolve.
			n1, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), tc.colType, tc.colTypmod)
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
				t.Fatalf("RebuildExpr: %v", err)
			}

			// Re-resolve through the same column context.
			n2, ok := ResolveForColumnTypmod(ast, tc.colType, tc.colTypmod)
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

// TestVarcharLenCoerceNoWrapBare verifies that a bare varchar (typmod -1) column DEFAULT
// emits a plain varchar Const with NO length-coercion FuncExpr.
func TestVarcharLenCoerceNoWrapBare(t *testing.T) {
	// Bare varchar — no length coercion.
	n, ok := ResolveForColumnTypmod(mustParse(t, "'hello'"), OidVarchar, -1)
	if !ok {
		t.Fatal("bare varchar rejected")
	}
	f, isFunc := n.(*FuncExpr)
	if isFunc {
		t.Fatalf("bare varchar got unexpected FuncExpr: funcid %d, format %d", f.Funcid, f.Funcformat)
	}
	c, isConst := n.(*Const)
	if !isConst || c.ConstType != OidVarchar || c.ConstTypmod != -1 {
		t.Fatalf("bare varchar expected plain varchar Const, got %T", n)
	}

	// Bare bpchar — no length coercion.
	n2, ok := ResolveForColumnTypmod(mustParse(t, "'abc'"), OidBpchar, -1)
	if !ok {
		t.Fatal("bare bpchar rejected")
	}
	if _, isFunc := n2.(*FuncExpr); isFunc {
		t.Fatal("bare bpchar got unexpected FuncExpr")
	}
	c2, isConst := n2.(*Const)
	if !isConst || c2.ConstType != OidBpchar || c2.ConstTypmod != -1 {
		t.Fatalf("bare bpchar expected plain bpchar Const, got %T", n2)
	}
}

// TestColumnTypmodVarcharBpchar verifies that ColumnTypmod computes the correct
// packed atttypmod for varchar/bpchar (maxlen + VARHDRSZ).
func TestColumnTypmodVarcharBpchar(t *testing.T) {
	cases := []struct {
		typeName string
		args     []int64
		want     int32
	}{
		{"varchar", []int64{10}, 14},
		{"character varying", []int64{20}, 24},
		{"char", []int64{5}, 9},
		{"character", []int64{10}, 14},
		{"bpchar", []int64{3}, 7},
		{"varchar", nil, -1},
		{"bpchar", []int64{}, -1},
		{"int4", []int64{}, -1},
		{"numeric", []int64{10, 2}, 655366}, // (10<<16)|2 + 4; backward compat
		{"decimal", []int64{5}, 327684},      // (5<<16)|0 + 4; backward compat
	}
	for _, tc := range cases {
		got := ColumnTypmod(tc.typeName, tc.args)
		if got != tc.want {
			t.Errorf("ColumnTypmod(%q, %v) = %d, want %d", tc.typeName, tc.args, got, tc.want)
		}
	}
}
