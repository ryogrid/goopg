package pgnodes

import (
	"testing"
)

// TestTimestampLenCoerceStructure verifies that ResolveForColumnTypmod emits the
// correct FuncExpr shape for timestamp(N) column DEFAULTs: funcid 1961,
// funcformat 2 (IMPLICIT), two args (timestamp Const + int4 typmod Const).
func TestTimestampLenCoerceStructure(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"ts0_epoch", "'epoch'", 0},
		{"ts3_frac", "'2024-01-15 10:30:00.123456'", 3},
		{"ts6_full", "'2024-01-15 10:30:00.123456'", 6},
		{"ts0_trunc", "'2024-01-15 10:30:00.123456'", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast := mustParse(t, tt.sql)
			n, ok := ResolveForColumnTypmod(ast, OidTimestamp, tt.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod returned false")
			}

			f, ok := n.(*FuncExpr)
			if !ok {
				t.Fatalf("expected *FuncExpr, got %T", n)
			}

			// Verify the FuncExpr metadata.
			if f.Funcid != 1961 {
				t.Errorf("Funcid = %d, want 1961", f.Funcid)
			}
			if f.Funcresulttype != OidTimestamp {
				t.Errorf("Funcresulttype = %d, want %d", f.Funcresulttype, OidTimestamp)
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

			// Arg[0] must be a timestamp Const.
			inner, ok := f.Args[0].(*Const)
			if !ok {
				t.Fatalf("Args[0] = %T, want *Const", f.Args[0])
			}
			if inner.ConstType != OidTimestamp {
				t.Errorf("inner ConstType = %d, want %d", inner.ConstType, OidTimestamp)
			}
			if inner.ConstTypmod != -1 {
				t.Errorf("inner ConstTypmod = %d, want -1", inner.ConstTypmod)
			}
			if inner.ConstIsNull {
				t.Error("inner Const should not be null")
			}
			if inner.ConstLen != 8 {
				t.Errorf("inner ConstLen = %d, want 8", inner.ConstLen)
			}
			if !inner.ConstByval {
				t.Error("inner Const should be byval")
			}

			// Arg[1] must be a non-null int4 Const holding the typmod.
			tm, ok := f.Args[1].(*Const)
			if !ok {
				t.Fatalf("Args[1] = %T, want *Const", f.Args[1])
			}
			if tm.ConstType != OidInt4 {
				t.Errorf("typmod ConstType = %d, want %d", tm.ConstType, OidInt4)
			}
			if tm.ConstIsNull {
				t.Error("typmod Const should not be null")
			}
			gotTypmod := int32(int64FromByvalWord(tm.Datum))
			if gotTypmod != tt.colTypmod {
				t.Errorf("typmod = %d, want %d", gotTypmod, tt.colTypmod)
			}
		})
	}
}

// TestTimestampLenCoerceNoWrap verifies that a timestamp column without a
// precision qualifier (typmod < 0) does NOT wrap the result.
func TestTimestampLenCoerceNoWrap(t *testing.T) {
	ast := mustParse(t, "'2024-01-15 10:30:00'")
	n, ok := ResolveForColumnTypmod(ast, OidTimestamp, -1)
	if !ok {
		t.Fatalf("ResolveForColumnTypmod returned false")
	}
	// With no typmod, the result should be a bare Const, not a FuncExpr.
	f, isFunc := n.(*FuncExpr)
	if isFunc {
		t.Fatalf("bare timestamp got unexpected FuncExpr: funcid %d, format %d", f.Funcid, f.Funcformat)
	}
	c, isConst := n.(*Const)
	if !isConst || c.ConstType != OidTimestamp || c.ConstTypmod != -1 {
		t.Fatalf("bare timestamp expected plain timestamp Const, got %T", n)
	}
}

// TestTimestampLenCoerceRebuildRoundTrip verifies that Resolve → Out → Read →
// Rebuild → Resolve → Out is stable (the rebuild fixed point). The implicit
// length coercion unwraps transparently when rebuilt.
func TestTimestampLenCoerceRebuildRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"ts0_epoch", "'epoch'", 0},
		{"ts3_frac", "'2024-01-15 10:30:00.123456'", 3},
		{"ts6_full", "'2024-01-15 10:30:00.123456'", 6},
		{"ts0_trunc", "'2024-01-15 10:30:00.123456'", 0},
		{"ts0_no_frac", "'2024-01-15 10:30:00'", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Forward resolve.
			n1, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidTimestamp, tc.colTypmod)
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
			n2, ok := ResolveForColumnTypmod(ast, OidTimestamp, tc.colTypmod)
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

// TestColumnTypmodTimestamp verifies the ColumnTypmod helper for timestamp.
func TestColumnTypmodTimestamp(t *testing.T) {
	tests := []struct {
		args []int64
		want int32
	}{
		{[]int64{0}, 0},
		{[]int64{3}, 3},
		{[]int64{6}, 6},
		{[]int64{}, -1},      // no args → bare timestamp
		{[]int64{7}, -1},     // exceeds MAX_TIMESTAMP_PRECISION
		{[]int64{-1}, -1},    // negative precision rejected
		{[]int64{0, 0}, -1},  // too many args
	}
	for _, tt := range tests {
		got := ColumnTypmod("timestamp", tt.args)
		if got != tt.want {
			t.Errorf("ColumnTypmod(\"timestamp\", %v) = %d, want %d", tt.args, got, tt.want)
		}
	}
}

// TestTimestampLenCoerceExplicitCast verifies that an explicit ::timestamp
// cast on a string literal folds to a bare timestamp Const (no cast node),
// byte-identical to the same literal in a timestamp column context.
func TestTimestampLenCoerceExplicitCast(t *testing.T) {
	// Resolve as explicit cast.
	ast1 := mustParse(t, "'2024-01-15 10:30:00'::timestamp")
	n1, err := ResolveExpr(ast1, OidTimestamp)
	if err != nil {
		t.Fatalf("ResolveExpr via cast: %v", err)
	}

	// Resolve as column context (string literal in timestamp context).
	ast2 := mustParse(t, "'2024-01-15 10:30:00'")
	n2, ok := ResolveForColumn(ast2, OidTimestamp)
	if !ok {
		t.Fatalf("ResolveForColumn returned false")
	}

	out1 := Out(n1)
	out2 := Out(n2)
	if out1 != out2 {
		t.Errorf("explicit ::timestamp cast and column-context fold differ:\n  cast:  %s\n  col:   %s", out1, out2)
	}

	// Both must be bare Consts, not FuncExprs.
	if _, isFunc := n1.(*FuncExpr); isFunc {
		t.Error("explicit ::timestamp cast should fold to bare Const")
	}
	if _, isFunc := n2.(*FuncExpr); isFunc {
		t.Error("column-context timestamp literal should fold to bare Const")
	}
}

// TestTimestampConstRebuild verifies that a bare timestamp Const survives
// a Out→Read→Rebuild→Resolve→Out round-trip.
func TestTimestampConstRebuild(t *testing.T) {
	tests := []string{
		"'2024-01-15 10:30:00'::timestamp",
		"'2024-01-15 10:30:00.123456'::timestamp",
		"'epoch'::timestamp",
		"'2024-03-15 00:00:00'::timestamp",
	}

	for _, sql := range tests {
		t.Run(sql, func(t *testing.T) {
			ast := mustParse(t, sql)
			n1, err := ResolveExpr(ast, OidTimestamp)
			if err != nil {
				t.Fatalf("ResolveExpr: %v", err)
			}
			out1 := Out(n1)

			gotNode, err := Read(out1)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}

			rebuilt, err := Rebuild(gotNode)
			if err != nil {
				t.Fatalf("Rebuild: %v", err)
			}

			n2, ok := ResolveForColumn(rebuilt, OidTimestamp)
			if !ok {
				t.Fatalf("re-resolve returned false on rebuilt")
			}
			out2 := Out(n2)

			if out1 != out2 {
				t.Errorf("round-trip mismatch:\n  resolve1: %s\n  resolve2: %s", out1, out2)
			}
		})
	}
}

// TestTimestampLenCoerceMatchesGolden verifies that ResolveForColumnTypmod emits
// byte-for-byte the same pg_node_tree that PG18.3 stores in pg_attrdef.adbin for
// a timestamp(N) column DEFAULT.
func TestTimestampLenCoerceMatchesGolden(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		colTypmod int32
		golden    string
	}{
		{
			name:      "ts0_2024_01_15",
			sql:       "'2024-01-15 10:30:00'",
			colTypmod: 0,
			golden:    `{FUNCEXPR :funcid 1961 :funcresulttype 1114 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1114 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -70 -66 67 -8 -79 2 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "ts3_2024_01_15_123456",
			sql:       "'2024-01-15 10:30:00.123456'",
			colTypmod: 3,
			golden:    `{FUNCEXPR :funcid 1961 :funcresulttype 1114 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1114 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 64 -100 -64 67 -8 -79 2 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 3 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "ts6_2024_01_15_123456",
			sql:       "'2024-01-15 10:30:00.123456'",
			colTypmod: 6,
			golden:    `{FUNCEXPR :funcid 1961 :funcresulttype 1114 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1114 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 64 -100 -64 67 -8 -79 2 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "ts0_truncate",
			sql:       "'2024-01-15 10:30:00.123456'",
			colTypmod: 0,
			golden:    `{FUNCEXPR :funcid 1961 :funcresulttype 1114 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1114 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 64 -100 -64 67 -8 -79 2 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			name:      "ts0_epoch",
			sql:       "'epoch'",
			colTypmod: 0,
			golden:    `{FUNCEXPR :funcid 1961 :funcresulttype 1114 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1114 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 32 -56 -60 -2 -94 -4 -1 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidTimestamp, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod(%q, timestamp, %d) rejected a canonical default",
					tc.sql, tc.colTypmod)
			}
			got := Out(n)
			if got != tc.golden {
				t.Errorf("Out mismatch:\n  got  %s\n  want %s", got, tc.golden)
			}
		})
	}
}
