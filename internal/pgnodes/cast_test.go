package pgnodes

// M0123-S4 sub-slice 19+20 — explicit numeric-family `::type` casts.
//
// PostgreSQL stores an explicit `expr::int2/int4/int8/numeric` cast in
// pg_attrdef.adbin as a COERCE_EXPLICIT_CAST (funcformat 1) FuncExpr naming the
// pg_cast conversion function — UNLESS the cast is to the operand's own type,
// which coerce_type treats as a no-op and stores as the bare inner node. This is
// the funcformat-1 sibling of the implicit-cast helpers (wrapInt4ToInt8Cast /
// wrapIntToNumericCast, funcformat 2): the same conversion-function OIDs, but an
// explicit `::type` keeps its cast node in adbin while an implicit coercion PG
// re-derives from context and unwraps.
//
// Every `want` below is captured LIVE from a real PostgreSQL 18.3 server (the
// oracle_pgnodes_adbin_test.go gate re-derives and diffs these byte-for-byte).
// Cast function OIDs (pg_proc.dat / pg_cast.dat), all castmethod 'f':
// int2(int4)=314, int8(int4)=481, int4(int8)=480, int2(int8)=714 (int→int);
// int4_numeric=1740, int8_numeric=1781 (int→numeric); numeric_int2=1783,
// numeric_int4=1744, numeric_int8=1779 (numeric→int).

import "testing"

var castGolden = []struct {
	name      string
	sql       string
	colType   uint32
	colTypmod int32 // packed atttypmod for a typmod-qualified column (0 = none)
	want      string
}{
	// int4 source (a literal that fits int4) → wider/narrower integer target.
	{
		name:    "explicit_int4_to_int2",
		sql:     "5::int2",
		colType: OidInt2,
		want:    `{FUNCEXPR :funcid 314 :funcresulttype 21 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name:    "explicit_int4_to_int8",
		sql:     "5::int8",
		colType: OidInt8,
		want:    `{FUNCEXPR :funcid 481 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	// int8 source (a literal too large for int4 → make_const types it int8) →
	// narrower integer target. Exercises the int8-source cast arms (480/714).
	{
		name:    "explicit_int8_to_int4",
		sql:     "9999999999::int4",
		colType: OidInt4,
		want:    `{FUNCEXPR :funcid 480 :funcresulttype 23 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -1 -29 11 84 2 0 0 0 ]}) :location -1}`,
	},
	{
		name:    "explicit_int8_to_int2",
		sql:     "9999999999::int2",
		colType: OidInt2,
		want:    `{FUNCEXPR :funcid 714 :funcresulttype 21 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -1 -29 11 84 2 0 0 0 ]}) :location -1}`,
	},
	// A cast to the operand's own type is a no-op: PG stores just the inner Const,
	// no RelabelType or FuncExpr. `5::int4` (int4 literal) and `9999999999::int8`
	// (already-int8 literal) both fall here.
	{
		name:    "explicit_noop_int4",
		sql:     "5::int4",
		colType: OidInt4,
		want:    `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}`,
	},
	{
		name:    "explicit_noop_int8",
		sql:     "9999999999::int8",
		colType: OidInt8,
		want:    `{CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -1 -29 11 84 2 0 0 0 ]}`,
	},
	// Simple-form CASE with an EXPLICIT-cast operand (closes the "explicit-cast
	// operand simple CASE" item): the operand `5::int8` resolves to the int8(int4)
	// cast FuncExpr, so the CaseTestExpr placeholder is typed int8 (typeMod -1,
	// collation 0 via operandTypmodCollid's FuncExpr arm). Each result is itself an
	// explicit `::int8` cast, so casetype == int8 == column and no outer column
	// coercion wraps the CASE. The WHEN comparison uses the native int8=int4
	// operator (opno 416) with the value un-coerced (sub-slice 18).
	{
		name:    "simple_case_explicit_cast_operand",
		sql:     "CASE 5::int8 WHEN 1 THEN 10::int8 ELSE 20::int8 END",
		colType: OidInt8,
		want:    `{CASEEXPR :casetype 20 :casecollid 0 :arg {FUNCEXPR :funcid 481 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1} :args ({CASEWHEN :expr {OPEXPR :opno 416 :opfuncid 474 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 20 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 481 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 10 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 481 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 20 0 0 0 0 0 0 0 ]}) :location -1} :location -1}`,
	},

	// M0123-S4 sub-slice 20 — explicit numeric↔integer `::type` casts. PG stores
	// each as a COERCE_EXPLICIT_CAST (funcformat 1) FuncExpr naming the pg_cast
	// conversion function; the operand is resolved at its NATURAL type first (a
	// decimal literal → numeric Const, an integer literal → int4/int8 Const). The
	// funcformat-1 numeric arms (1740/1781) are siblings of the implicit-cast
	// helpers (funcformat 2, same OIDs) which PG re-derives from context; the
	// explicit `::type` keeps its node in adbin. numeric_int4=1744, numeric_int8=
	// 1779, numeric_int2=1783 (numeric→int); int4_numeric=1740, int8_numeric=1781
	// (int→numeric). Every `want` captured LIVE from PostgreSQL 18.3.

	// numeric source (a decimal literal → numeric Const 5.5) → integer target.
	{
		name:    "explicit_numeric_to_int4",
		sql:     "5.5::int4",
		colType: OidInt4,
		want:    `{FUNCEXPR :funcid 1744 :funcresulttype 23 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]}) :location -1}`,
	},
	{
		name:    "explicit_numeric_to_int8",
		sql:     "5.5::int8",
		colType: OidInt8,
		want:    `{FUNCEXPR :funcid 1779 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]}) :location -1}`,
	},
	{
		name:    "explicit_numeric_to_int2",
		sql:     "5.5::int2",
		colType: OidInt2,
		want:    `{FUNCEXPR :funcid 1783 :funcresulttype 21 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]}) :location -1}`,
	},
	// A folded negative numeric operand `(-2.5)` (parens force `::` to bind after
	// the unary minus) → int4. Exercises decodeNumericVar's negative-sign rebuild.
	{
		name:    "explicit_neg_numeric_to_int4",
		sql:     "(-2.5)::int4",
		colType: OidInt4,
		want:    `{FUNCEXPR :funcid 1744 :funcresulttype 23 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -96 2 0 -120 19 ]}) :location -1}`,
	},
	// integer source (a literal → int4/int8 Const) → numeric target. int4_numeric
	// (1740) here is the funcformat-1 EXPLICIT sibling of the funcformat-2 implicit
	// int→numeric coercion (sub-slice 4a) — same OID, kept in adbin because `::` is
	// explicit.
	{
		name:    "explicit_int4_to_numeric",
		sql:     "5::numeric",
		colType: OidNumeric,
		want:    `{FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name:    "explicit_int8_to_numeric",
		sql:     "9999999999::numeric",
		colType: OidNumeric,
		want:    `{FUNCEXPR :funcid 1781 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -1 -29 11 84 2 0 0 0 ]}) :location -1}`,
	},

	// M0123-S4 sub-slice 21 — explicit float-family `::type` casts (float4/float8),
	// extending the funcformat-1 machinery across the binary-float boundary. A
	// literal source is int4/int8 (integer) or numeric (decimal), so the reachable
	// arms are int→float and numeric→float; PG stores each as a COERCE_EXPLICIT_CAST
	// FuncExpr naming the pg_cast conversion function. int→float4: float4(int4)=318,
	// float4(int8)=652; int→float8: float8(int4)=316, float8(int8)=482; numeric→
	// float: numeric_float8=1746, numeric_float4=1745. A nested `(x::float8)::int4`
	// reaches the float→int arm (int4(float8)=317). Every `want` captured LIVE from
	// PostgreSQL 18.3. NOTE 316/482/1746 are the funcformat-1 EXPLICIT siblings of
	// the funcformat-2 implicit CASE →float8 coercion (wrapToFloat8Cast); the
	// funcformat keeps them apart on the rebuild path.

	// integer source → float4 / float8 (i4tof=318, i4tod=316).
	{
		name:    "explicit_int4_to_float4",
		sql:     "5::float4",
		colType: OidFloat4,
		want:    `{FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name:    "explicit_int4_to_float8",
		sql:     "5::float8",
		colType: OidFloat8,
		want:    `{FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	// int8 source (a literal too large for int4) → float4 / float8 (i8tof=652,
	// i8tod=482).
	{
		name:    "explicit_int8_to_float4",
		sql:     "9999999999::float4",
		colType: OidFloat4,
		want:    `{FUNCEXPR :funcid 652 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -1 -29 11 84 2 0 0 0 ]}) :location -1}`,
	},
	{
		name:    "explicit_int8_to_float8",
		sql:     "9999999999::float8",
		colType: OidFloat8,
		want:    `{FUNCEXPR :funcid 482 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -1 -29 11 84 2 0 0 0 ]}) :location -1}`,
	},
	// numeric source (a decimal literal → numeric Const 5.5) → float4 / float8
	// (numeric_float4=1745, numeric_float8=1746).
	{
		name:    "explicit_numeric_to_float4",
		sql:     "5.5::float4",
		colType: OidFloat4,
		want:    `{FUNCEXPR :funcid 1745 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]}) :location -1}`,
	},
	{
		name:    "explicit_numeric_to_float8",
		sql:     "5.5::float8",
		colType: OidFloat8,
		want:    `{FUNCEXPR :funcid 1746 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]}) :location -1}`,
	},
	// A NESTED cast reaches a float SOURCE arm: `(5.5::float8)::int4` resolves the
	// operand to numeric_float8 (1746), then float8→int4 via int4(float8)=317. This
	// is the only way a float-typed operand appears (there is no float literal leaf).
	{
		name:    "explicit_nested_float8_to_int4",
		sql:     "(5.5::float8)::int4",
		colType: OidInt4,
		want:    `{FUNCEXPR :funcid 317 :funcresulttype 23 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1746 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]}) :location -1}) :location -1}`,
	},

	// M0123-S4 sub-slice 22 — explicit typmod-qualified numeric cast `::numeric(p,s)`.
	// PG's coerce_to_target_type follows the base int→numeric cast with a length
	// coercion numeric(numeric, int4) = funcid 1703 (funcformat 1, EXPLICIT) whose
	// second arg is an int4 Const holding numerictypmodin(p,s) = ((p<<16)|s)+VARHDRSZ(4)
	// — numeric(10,2)=655366 [6 0 10 0], numeric(10,0)=655364 [4 0 10 0]. arg0 is the
	// operand coerced to numeric: an integer operand wraps in int4_numeric (1740,
	// funcformat 2, IMPLICIT); a decimal operand is already a numeric Const. This is
	// the byte shape ONLY when the column typmod matches the cast (colTypmod set); a
	// bare-numeric column wraps the coercion in a RelabelType not modeled here.
	{
		name:      "explicit_int4_to_numeric_10_2",
		sql:       "5::numeric(10,2)",
		colType:   OidNumeric,
		colTypmod: 655366,
		want:      `{FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 10 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name:      "explicit_numeric_to_numeric_10_2",
		sql:       "5.5::numeric(10,2)",
		colType:   OidNumeric,
		colTypmod: 655366,
		want:      `{FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 6 0 10 0 0 0 0 0 ]}) :location -1}`,
	},
	// scale defaults to 0 for numeric(p): numeric(10,0)=655364 [4 0 10 0].
	{
		name:      "explicit_int4_to_numeric_10_0",
		sql:       "5::numeric(10,0)",
		colType:   OidNumeric,
		colTypmod: 655364,
		want:      `{FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 4 0 10 0 0 0 0 0 ]}) :location -1}`,
	},
}

// TestCastResolveMatchesGolden parses each SQL default and asserts ResolveExpr →
// Out is byte-identical to the real-PG18 adbin, and that ResolveForColumn accepts
// it as canonical for the column's type.
func TestCastResolveMatchesGolden(t *testing.T) {
	for _, tc := range castGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), tc.colType)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			if _, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), tc.colType, tc.colTypmod); !ok {
				t.Fatalf("ResolveForColumnTypmod(%q, %d, %d) rejected a matching-type default", tc.sql, tc.colType, tc.colTypmod)
			}
		})
	}
}

// TestCastCodecRoundTrip closes the text → IR → text loop: Read must reconstruct
// an IR whose re-Out reproduces the exact bytes.
func TestCastCodecRoundTrip(t *testing.T) {
	for _, tc := range castGolden {
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

// TestCastResolveRebuildRoundTrip proves resolve → Rebuild → re-resolve is a
// fixed point: an explicit cast rebuilds to a `::type` CastExpr (not the bare
// argument, unlike an implicit cast) and re-resolves to a byte-identical tree, so
// the DEFAULT reload path is loss-free.
func TestCastResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range castGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), tc.colType)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			n2, err := ResolveExpr(ast, tc.colType)
			if err != nil {
				t.Fatalf("re-ResolveExpr(%q): %v", tc.sql, err)
			}
			if Out(n1) != Out(n2) {
				t.Fatalf("resolve→Rebuild→re-resolve not a fixed point for %q:\n first: %s\nsecond: %s",
					tc.sql, Out(n1), Out(n2))
			}
		})
	}
}

// TestCastDegradesGracefully documents the bounded-subset boundary: only casts
// within PG's numeric type category (int2/int4/int8/numeric/float4/float8)² are
// modeled. A cast target outside that category (text) and a source type this slice
// does not carry a cast arm for (text→int4, text→float8) must degrade to SQL text
// rather than emit a divergent tree. A `::numeric(p,s)` cast targeting a BARE
// numeric column (ResolveForColumn = typmod -1) also degrades: PG wraps the length
// coercion in a RelabelType to the column's typmod (a node this slice does not
// model), so only a matching numeric(p,s) column (ResolveForColumnTypmod, tested in
// the golden set) emits it canonically.
func TestCastDegradesGracefully(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		colType uint32
	}{
		{"int_to_text_target", "5::text", OidText},           // non-numeric-category target
		{"text_literal_to_float8", "'5'::float8", OidFloat8}, // text source arm not modeled
		{"text_literal_to_int4", "'5'::int4", OidInt4},       // text source arm not modeled
		// (sub-slice 24 now models the bare-numeric-col typmod'd cast via an implicit
		// RelabelType — covered in numeric_relabel_test.go — so it is no longer a degrade.)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.colType); ok {
				t.Fatalf("ResolveForColumn(%q, %d) should degrade to SQL text, but accepted it", tc.sql, tc.colType)
			}
		})
	}
}
