package pgnodes

// M0123-S4 sub-slice 23 — IMPLICIT numeric column length coercion.
//
// PostgreSQL's build_column_default coerces a stored DEFAULT to the attribute's
// (type, typmod). For a NUMERIC column carrying a length qualifier, coerce_type_typmod
// (parse_coerce.c) adds an IMPLICIT length coercion numeric(numeric, int4) = funcid
// 1703, funcformat 2 (COERCE_IMPLICIT_CAST) whenever the stored default's own typmod
// differs from the column's — so `col numeric(10,2) DEFAULT 5.5` stores a funcformat-2
// 1703 wrapping the bare 5.5 Const, DEFAULT 0 wraps the int4_numeric implicit cast,
// and DEFAULT 5.5::numeric(8,1) wraps sub-slice 22's funcformat-1 explicit cast. The
// wrap is invisible to pg_get_expr (an implicit cast has no `::type` syntax), so the
// rebuild path unwraps it (isImplicitNumericLengthCoercion) for a re-resolve fixed
// point. This is the funcformat-2 sibling of the funcformat-1 explicit `::numeric(p,s)`
// cast (sub-slice 22, same funcid 1703): the funcformat distinguishes them.
//
// Every `want` below is captured LIVE from a real PostgreSQL 18.3 server; the
// oracle_pgnodes_adbin_test.go gate re-derives and diffs these byte-for-byte. The
// packed typmod for numeric(10,2) is 655366 ([6 0 10 0]) and for numeric(10,0)/
// numeric(10) is 655364 ([4 0 10 0]) — numerictypmodin = ((p<<16)|s)+VARHDRSZ(4).

import "testing"

var numericLenCoerceGolden = []struct {
	name      string
	sql       string
	colTypmod int32 // packed atttypmod of the numeric(p,s) column
	want      string
}{
	// A bare decimal literal: the wrap holds the numeric Const directly.
	{
		name:      "numeric_10_2_default_decimal",
		sql:       "5.5",
		colTypmod: 655366,
		want:      `{FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 10 0 0 0 0 0 ]}) :location -1}`,
	},
	// An int4 literal in a numeric column: int4_numeric (1740) implicit cast, then the
	// length coercion wraps it (a double funcformat-2 nest).
	{
		name:      "numeric_10_2_default_int4",
		sql:       "0",
		colTypmod: 655366,
		want:      `{FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 10 0 0 0 0 0 ]}) :location -1}`,
	},
	// An int8-magnitude literal: int8_numeric (1781) implicit cast, then the wrap.
	{
		name:      "numeric_10_2_default_int8",
		sql:       "5000000000",
		colTypmod: 655366,
		want:      `{FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1781 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 10 0 0 0 0 0 ]}) :location -1}`,
	},
	// An explicit `::numeric(8,1)` cast whose typmod differs from the column's (10,2):
	// the funcformat-1 explicit cast (sub-slice 22) nests inside the funcformat-2 wrap.
	{
		name:      "numeric_10_2_default_explicit_8_1",
		sql:       "5.5::numeric(8,1)",
		colTypmod: 655366,
		want:      `{FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 8 0 0 0 0 0 ]}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 10 0 0 0 0 0 ]}) :location -1}`,
	},
	// A scale-0 column numeric(10,0): the wrap's typmod Const is 655364 ([4 0 10 0]).
	{
		name:      "numeric_10_0_default_decimal",
		sql:       "5.5",
		colTypmod: 655364,
		want:      `{FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 4 0 10 0 0 0 0 0 ]}) :location -1}`,
	},
	// A negative decimal (doNegate fold) still resolves to a numeric Const, wrapped.
	{
		name:      "numeric_10_2_default_negative",
		sql:       "-2.5",
		colTypmod: 655366,
		want:      `{FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -96 2 0 -120 19 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 10 0 0 0 0 0 ]}) :location -1}`,
	},
}

// TestNumericLenCoerceMatchesGolden asserts ResolveForColumnTypmod applies the
// implicit numeric length coercion so the emitted adbin is byte-identical to real
// PG18's for a numeric(p,s) column DEFAULT.
func TestNumericLenCoerceMatchesGolden(t *testing.T) {
	for _, tc := range numericLenCoerceGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidNumeric, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod(%q, numeric, %d) degraded, want canonical", tc.sql, tc.colTypmod)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
		})
	}
}

// TestNumericLenCoerceCodecRoundTrip closes the text → IR → text loop for the wrap.
func TestNumericLenCoerceCodecRoundTrip(t *testing.T) {
	for _, tc := range numericLenCoerceGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := Read(tc.want)
			if err != nil {
				t.Fatalf("Read(%q): %v", tc.name, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("re-Out mismatch:\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestNumericLenCoerceRebuildRoundTrip proves resolve → Rebuild → re-resolve is a
// fixed point: the implicit length coercion unwraps in Rebuild (pg_get_expr renders
// it invisibly), and re-resolving the same SQL for the same column typmod re-wraps a
// byte-identical tree, so the DEFAULT reload path is loss-free.
func TestNumericLenCoerceRebuildRoundTrip(t *testing.T) {
	for _, tc := range numericLenCoerceGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidNumeric, tc.colTypmod)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod(%q): degraded", tc.sql)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			n2, ok := ResolveForColumnTypmod(ast, OidNumeric, tc.colTypmod)
			if !ok {
				t.Fatalf("re-ResolveForColumnTypmod(%q): degraded", tc.sql)
			}
			if Out(n1) != Out(n2) {
				t.Fatalf("resolve→Rebuild→re-resolve not a fixed point for %q:\n first: %s\nsecond: %s",
					tc.sql, Out(n1), Out(n2))
			}
		})
	}
}

// TestNumericLenCoerceNoWrapWhenTypmodMatches guards the coerce_type_typmod no-op
// branch: an explicit `::numeric(10,2)` cast into a numeric(10,2) column already
// carries the column typmod, so NO implicit length coercion is added (the stored
// node stays the funcformat-1 cast). A bare numeric column (typmod -1) with the same
// cast still degrades (RelabelType, not modeled).
func TestNumericLenCoerceNoWrapWhenTypmodMatches(t *testing.T) {
	// Matching typmod: the resolved node is the funcformat-1 explicit cast, unwrapped.
	n, ok := ResolveForColumnTypmod(mustParse(t, "5.5::numeric(10,2)"), OidNumeric, 655366)
	if !ok {
		t.Fatalf("ResolveForColumnTypmod matching typmod: degraded")
	}
	f, isFunc := n.(*FuncExpr)
	if !isFunc || f.Funcformat != 1 {
		t.Fatalf("matching typmod: got %T funcformat, want funcformat-1 explicit cast (no implicit wrap)", n)
	}

	// Bare numeric column: the explicit typmod'd cast still degrades (RelabelType).
	if _, ok := ResolveForColumnTypmod(mustParse(t, "5.5::numeric(10,2)"), OidNumeric, -1); ok {
		t.Fatalf("bare numeric column with a typmod'd cast should degrade (RelabelType), got canonical")
	}
}
