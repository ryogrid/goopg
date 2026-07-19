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
	name    string
	sql     string
	colType uint32
	want    string
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
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.colType); !ok {
				t.Fatalf("ResolveForColumn(%q, %d) rejected a matching-type default", tc.sql, tc.colType)
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

// TestCastDegradesGracefully documents the bounded-subset boundary: only
// numeric-family casts (int2/int4/int8/numeric)² are modeled. A cast target
// outside that family, a source type this slice does not carry a cast arm for
// (text→int4), and a typmod-qualified numeric target (a distinct length-coercion
// call) must degrade to SQL text rather than emit a divergent tree.
func TestCastDegradesGracefully(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		colType uint32
	}{
		{"int_to_text_target", "5::text", OidText},             // non-numeric-family target
		{"numeric_to_float8_target", "5.5::float8", OidFloat8}, // float family not modeled
		{"text_literal_to_int4", "'5'::int4", OidInt4},         // text source arm not modeled
		{"typmod_qualified", "5::numeric(10,2)", OidNumeric},   // typmod-qualified target
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.colType); ok {
				t.Fatalf("ResolveForColumn(%q, %d) should degrade to SQL text, but accepted it", tc.sql, tc.colType)
			}
		})
	}
}
