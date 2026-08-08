package pgnodes

import (
	"testing"
)

// TestTimestamptzLenCoerceStructure verifies that ResolveForColumnTypmod emits the
// correct FuncExpr shape for timestamptz(N) column DEFAULTs: funcid 1967,
// funcformat 2 (IMPLICIT), two args (timestamptz Const + int4 typmod Const).
func TestTimestamptzLenCoerceStructure(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"tstz0_epoch", "'epoch'", 0},
		{"tstz3_frac", "'2024-01-15 10:30:00.123456+00'", 3},
		{"tstz6_full", "'2024-01-15 10:30:00.123456+00'", 6},
		{"tstz0_trunc", "'2024-01-15 10:30:00.123456+00'", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast := mustParse(t, tt.sql)
			n, ok := ResolveForColumnTypmod(ast, OidTimestamptz, tt.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod returned false")
			}

			f, ok := n.(*FuncExpr)
			if !ok {
				t.Fatalf("expected *FuncExpr, got %T", n)
			}

			// Verify the FuncExpr metadata.
			if f.Funcid != 1967 {
				t.Errorf("Funcid = %d, want 1967", f.Funcid)
			}
			if f.Funcresulttype != OidTimestamptz {
				t.Errorf("Funcresulttype = %d, want %d", f.Funcresulttype, OidTimestamptz)
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

			// Arg[0] must be a timestamptz Const.
			inner, ok := f.Args[0].(*Const)
			if !ok {
				t.Fatalf("Args[0] = %T, want *Const", f.Args[0])
			}
			if inner.ConstType != OidTimestamptz {
				t.Errorf("inner ConstType = %d, want %d", inner.ConstType, OidTimestamptz)
			}
			if inner.ConstTypmod != -1 {
				t.Errorf("inner ConstTypmod = %d, want -1", inner.ConstTypmod)
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

// TestTimestamptzLenCoerceRebuildRoundTrip verifies that Resolve → Out → Read →
// Rebuild → Resolve → Out is stable (the rebuild fixed point). The implicit
// length coercion unwraps transparently when rebuilt.
func TestTimestamptzLenCoerceRebuildRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		colTypmod int32
	}{
		{"tstz0_epoch", "'epoch'", 0},
		{"tstz3_frac", "'2024-01-15 10:30:00.123456+00'", 3},
		{"tstz6_full", "'2024-01-15 10:30:00.123456+00'", 6},
		{"tstz0_trunc", "'2024-01-15 10:30:00.123456+00'", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Forward resolve.
			n1, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidTimestamptz, tc.colTypmod)
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
			n2, ok := ResolveForColumnTypmod(ast, OidTimestamptz, tc.colTypmod)
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

// TestTimestamptzLenCoerceNoWrap verifies that a timestamptz column without a
// precision qualifier (typmod < 0) does NOT wrap the result.
func TestTimestamptzLenCoerceNoWrap(t *testing.T) {
	ast := mustParse(t, "'2024-01-15 10:30:00+00'")
	n, ok := ResolveForColumnTypmod(ast, OidTimestamptz, -1)
	if !ok {
		t.Fatal("ResolveForColumnTypmod returned false")
	}
	// With no typmod, the result should be a bare Const, not a FuncExpr.
	f, isFunc := n.(*FuncExpr)
	if isFunc {
		t.Fatalf("bare timestamptz got unexpected FuncExpr: funcid %d, format %d", f.Funcid, f.Funcformat)
	}
	c, isConst := n.(*Const)
	if !isConst || c.ConstType != OidTimestamptz || c.ConstTypmod != -1 {
		t.Fatalf("bare timestamptz expected plain timestamptz Const, got %T", n)
	}
}

// TestTimestamptzLenCoerceGoldenForward verifies forward resolution against
// PG18.3 captured goldens byte-for-byte.
func TestTimestamptzLenCoerceGoldenForward(t *testing.T) {
	for _, tc := range timestamptzLenGolden {
		t.Run(tc.name, func(t *testing.T) {
			ast := mustParse(t, tc.sql)
			n, ok := ResolveForColumnTypmod(ast, OidTimestamptz, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod returned false")
			}
			got := Out(n)
			if got != tc.want {
				t.Errorf("forward mismatch:\n  got:  %s\n  want: %s", got, tc.want)
			}
		})
	}
}

// TestTimestamptzLenCoerceGoldenRoundTrip verifies codec round-trip and rebuild
// fixed point for every golden.
func TestTimestamptzLenCoerceGoldenRoundTrip(t *testing.T) {
	for _, tc := range timestamptzLenGolden {
		t.Run(tc.name, func(t *testing.T) {
			ast := mustParse(t, tc.sql)
			n1, ok := ResolveForColumnTypmod(ast, OidTimestamptz, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod returned false")
			}
			out1 := Out(n1)

			// Codec round-trip.
			gotNode, err := Read(out1)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}

			// Codec → Out must be byte-identical.
			outDecoded := Out(gotNode)
			if outDecoded != out1 {
				t.Errorf("codec round-trip mismatch:\n  orig:    %s\n  decoded: %s", out1, outDecoded)
			}

			// Rebuild → re-resolve → fixed point.
			rebAst, err := Rebuild(gotNode)
			if err != nil {
				t.Fatalf("Rebuild: %v", err)
			}
			n2, ok := ResolveForColumnTypmod(rebAst, OidTimestamptz, tc.colTypmod)
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

// timestamptzLenGolden holds PG18.3-captured byte-identical goldens for
// timestamptz(N) length coercion (funcid 1967, funcformat 2). Captured from
// PG 18.3 against a table with DEFAULT clauses and SELECT adbin::text FROM
// pg_attrdef.
var timestamptzLenGolden = []struct {
	name      string
	sql       string
	colTypmod int32
	want      string
}{
	{
		name:      "tstz0_epoch",
		sql:       "'epoch'",
		colTypmod: 0,
		want: `{FUNCEXPR :funcid 1967 :funcresulttype 1184 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 32 -56 -60 -2 -94 -4 -1 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name:      "tstz3_frac",
		sql:       "'2024-01-15 10:30:00.123456+00'",
		colTypmod: 3,
		want: `{FUNCEXPR :funcid 1967 :funcresulttype 1184 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 64 -100 -64 67 -8 -79 2 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 3 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name:      "tstz6_full",
		sql:       "'2024-01-15 10:30:00.123456+00'",
		colTypmod: 6,
		want: `{FUNCEXPR :funcid 1967 :funcresulttype 1184 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 64 -100 -64 67 -8 -79 2 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name:      "tstz0_trunc",
		sql:       "'2024-01-15 10:30:00.123456+00'",
		colTypmod: 0,
		want: `{FUNCEXPR :funcid 1967 :funcresulttype 1184 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 64 -100 -64 67 -8 -79 2 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name:      "tstz_bare",
		sql:       "'2024-01-15 10:30:00+00'",
		colTypmod: -1,
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -70 -66 67 -8 -79 2 0 ]}`,
	},
}

// TestTimestamptzColumnTypmod verifies that ColumnTypmod packs timestamptz
// precision correctly (N → N, same as timestamp).
func TestTimestamptzColumnTypmod(t *testing.T) {
	tests := []struct {
		args []int64
		want int32
	}{
		{[]int64{0}, 0},
		{[]int64{3}, 3},
		{[]int64{6}, 6},
		{[]int64{}, -1},
		{[]int64{7}, -1},
		{[]int64{-1}, -1},
		{[]int64{0, 0}, -1},
	}
	for _, tt := range tests {
		got := ColumnTypmod("timestamptz", tt.args)
		if got != tt.want {
			t.Errorf("ColumnTypmod(\"timestamptz\", %v) = %d, want %d", tt.args, got, tt.want)
		}
	}
}
