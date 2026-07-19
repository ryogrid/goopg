package pgnodes

import (
	"reflect"
	"testing"
)

// M0123-S4 sub-slice 7: canonical CASEEXPR / CASEWHEN (searched form).
//
// Every `want` below was captured VERBATIM from a live PostgreSQL 18.3 server
// (pg_attrdef.adbin is exactly nodeToString of the default expression):
//
//	CREATE TABLE c1 (
//	  d1 int     DEFAULT (CASE WHEN true THEN 1 ELSE 2 END),
//	  d2 int     DEFAULT (CASE WHEN true THEN 1 END),
//	  d3 int     DEFAULT (CASE WHEN (1=1) THEN 10 WHEN (2=2) THEN 20 ELSE 30 END),
//	  d4 numeric DEFAULT (CASE WHEN false THEN 1.5 ELSE 2.5 END),
//	  d5 bool    DEFAULT (CASE WHEN (1<2) THEN true ELSE false END));
//	SELECT a.attname, ad.adbin::text FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='c1'::regclass ORDER BY a.attnum;
//
// so byte-equality is the adversarial oracle the milestone gate requires. The
// cases cover: an explicit ELSE (d1/d3/d4/d5) and an omitted one (d2 — the
// synthesized typed NULL defresult), a single and a multi-WHEN body (d3), Const
// vs OPEXPR WHEN conditions, and int/numeric/bool casetype (each casecollid 0).

// caseGolden pairs a SQL default expression with the real-PG adbin string and
// the resolving column type, so the same case exercises the codec (Out/Read)
// AND the resolver (parse → ResolveExpr → Out) with a single source of truth.
var caseGolden = []struct {
	name    string
	sql     string
	colType uint32
	want    string
}{
	{
		name:    "int_else",
		sql:     "CASE WHEN true THEN 1 ELSE 2 END",
		colType: OidInt4,
		want:    `{CASEEXPR :casetype 23 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]} :location -1}`,
	},
	{
		name:    "int_no_else_null_defresult",
		sql:     "CASE WHEN true THEN 1 END",
		colType: OidInt4,
		want:    `{CASEEXPR :casetype 23 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull true :location -1 :constvalue <>} :location -1}`,
	},
	{
		name:    "int_two_when_opexpr",
		sql:     "CASE WHEN (1=1) THEN 10 WHEN (2=2) THEN 20 ELSE 30 END",
		colType: OidInt4,
		want:    `{CASEEXPR :casetype 23 :casecollid 0 :arg <> :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 10 0 0 0 0 0 0 0 ]} :location -1} {CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 20 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 30 0 0 0 0 0 0 0 ]} :location -1}`,
	},
	{
		name:    "numeric_else",
		sql:     "CASE WHEN false THEN 1.5 ELSE 2.5 END",
		colType: OidNumeric,
		want:    `{CASEEXPR :casetype 1700 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} :result {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 1 0 -120 19 ]} :location -1}) :defresult {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]} :location -1}`,
	},
	{
		name:    "bool_opexpr_lt",
		sql:     "CASE WHEN (1<2) THEN true ELSE false END",
		colType: OidBool,
		want:    `{CASEEXPR :casetype 16 :casecollid 0 :arg <> :args ({CASEWHEN :expr {OPEXPR :opno 97 :opfuncid 66 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} :location -1}`,
	},
	// Simple form (sub-slice 12): the operand becomes CaseExpr.arg (a Const here)
	// and each `WHEN val` becomes an OpExpr `CaseTestExpr = val`. Captured live
	// from PG18.3 (table cs):
	//   s1 int     DEFAULT (CASE 1 WHEN 1 THEN 10 ELSE 20 END)
	//   s2 int     DEFAULT (CASE 2 WHEN 1 THEN 10 WHEN 2 THEN 20 END)  -- no ELSE
	//   s3 numeric DEFAULT (CASE 3 WHEN 3 THEN 1.5 ELSE 2.5 END)
	//   s4 int     DEFAULT (CASE 5 WHEN 4 THEN 40 WHEN 5 THEN 50 ELSE 60 END)
	{
		name:    "simple_int_else",
		sql:     "CASE 1 WHEN 1 THEN 10 ELSE 20 END",
		colType: OidInt4,
		want:    `{CASEEXPR :casetype 23 :casecollid 0 :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 10 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 20 0 0 0 0 0 0 0 ]} :location -1}`,
	},
	{
		name:    "simple_int_two_when_no_else",
		sql:     "CASE 2 WHEN 1 THEN 10 WHEN 2 THEN 20 END",
		colType: OidInt4,
		want:    `{CASEEXPR :casetype 23 :casecollid 0 :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]} :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 10 0 0 0 0 0 0 0 ]} :location -1} {CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 20 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull true :location -1 :constvalue <>} :location -1}`,
	},
	{
		name:    "simple_numeric_else",
		sql:     "CASE 3 WHEN 3 THEN 1.5 ELSE 2.5 END",
		colType: OidNumeric,
		want:    `{CASEEXPR :casetype 1700 :casecollid 0 :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 3 0 0 0 0 0 0 0 ]} :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 3 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 1 0 -120 19 ]} :location -1}) :defresult {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]} :location -1}`,
	},
	{
		name:    "simple_int_two_when_else",
		sql:     "CASE 5 WHEN 4 THEN 40 WHEN 5 THEN 50 ELSE 60 END",
		colType: OidInt4,
		want:    `{CASEEXPR :casetype 23 :casecollid 0 :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]} :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 4 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 40 0 0 0 0 0 0 0 ]} :location -1} {CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 50 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 60 0 0 0 0 0 0 0 ]} :location -1}`,
	},
	// Cross-type result coercion (sub-slice 13): a CASE whose WHEN/ELSE results
	// differ in type. PG's transformCaseExpr runs select_common_type over the
	// results, then coerce_to_common_type wraps each mismatched result in a cast.
	// For the numeric family an int result coerces to numeric via the implicit
	// int4_numeric (1740) / int8_numeric (1781) FuncExpr (un-const-folded in the
	// stored tree). Captured live from PG18.3 (tables cx/cy):
	//   m1 numeric DEFAULT (CASE WHEN true THEN 1 ELSE 2.5 END)        -- cast on WHEN
	//   m2 numeric DEFAULT (CASE 1 WHEN 1 THEN 1 ELSE 2.5 END)         -- simple form
	//   m3 numeric DEFAULT (CASE WHEN true THEN 2.5 ELSE 1 END)        -- cast on ELSE
	//   m4 numeric DEFAULT (CASE WHEN false THEN 5000000000 WHEN true THEN 3 ELSE 2.5 END) -- int8+int4 casts
	{
		name:    "crosstype_int_then_numeric_else",
		sql:     "CASE WHEN true THEN 1 ELSE 2.5 END",
		colType: OidNumeric,
		want:    `{CASEEXPR :casetype 1700 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]} :location -1}`,
	},
	{
		name:    "crosstype_simple_int_numeric",
		sql:     "CASE 1 WHEN 1 THEN 1 ELSE 2.5 END",
		colType: OidNumeric,
		want:    `{CASEEXPR :casetype 1700 :casecollid 0 :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]} :location -1}`,
	},
	{
		name:    "crosstype_numeric_then_int_else",
		sql:     "CASE WHEN true THEN 2.5 ELSE 1 END",
		colType: OidNumeric,
		want:    `{CASEEXPR :casetype 1700 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]} :location -1}) :defresult {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}`,
	},
	{
		name:    "crosstype_int8_int4_numeric",
		sql:     "CASE WHEN false THEN 5000000000 WHEN true THEN 3 ELSE 2.5 END",
		colType: OidNumeric,
		want:    `{CASEEXPR :casetype 1700 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 1781 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]}) :location -1} :location -1} {CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 3 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]} :location -1}`,
	},
}

// TestCaseResolveMatchesGolden parses each SQL default and asserts
// ResolveExpr → Out is byte-identical to the real-PG18 adbin, and that
// ResolveForColumn accepts it as canonical for the column's type.
func TestCaseResolveMatchesGolden(t *testing.T) {
	for _, tc := range caseGolden {
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

// TestCaseCodecRoundTrip closes the text → IR → text loop: Read must
// reconstruct an IR whose re-Out reproduces the exact bytes (including the typed
// NULL defresult of the no-ELSE case).
func TestCaseCodecRoundTrip(t *testing.T) {
	for _, tc := range caseGolden {
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

// TestCaseResolveRebuildRoundTrip proves resolve → Rebuild → re-resolve is a
// fixed point: the rebuilt goopg AST re-resolves to a byte-identical tree, so
// the DEFAULT/view reload path is loss-free — including that a synthesized NULL
// defresult rebuilds to an omitted ELSE and re-resolves to the same bytes.
func TestCaseResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range caseGolden {
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
			if !reflect.DeepEqual(n1, n2) {
				t.Fatalf("resolve→Rebuild→re-resolve not a fixed point for %q:\n first: %s\nsecond: %s",
					tc.sql, Out(n1), Out(n2))
			}
		})
	}
}

// TestCaseDegradesGracefully covers the bounded-subset boundaries that remain
// SQL text after sub-slice 13's cross-type coercion landed: a collatable result
// type (text — would need a non-zero casecollid), and an integer-width-only mix
// (int4+int8 with no numeric, whose PG common type int8 needs an int→int width
// cast this subset does not model). Each must NOT resolve to a canonical node, so
// the writer keeps SQL text.
func TestCaseDegradesGracefully(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		colType uint32
	}{
		{"text_result", "CASE WHEN true THEN 'a' ELSE 'b' END", OidText},
		{"int4_int8_no_numeric", "CASE WHEN true THEN 1 ELSE 5000000000 END", OidInt8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.colType); ok {
				t.Fatalf("ResolveForColumn(%q, %d) should degrade to SQL text, but accepted it", tc.sql, tc.colType)
			}
		})
	}
}
