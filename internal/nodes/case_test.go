package nodes

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
	// Cross-FAMILY (integer-width) result coercion (sub-slice 14): a CASE whose
	// results mix int4 and int8 with NO numeric. select_common_type walks the
	// exact-integer family — int4 implicitly coerces to int8 but not the reverse,
	// and neither is a preferred type — so the common type is int8 (casetype 20),
	// and each int4 result is wrapped in the implicit int8(int4) cast FuncExpr
	// (funcid 481, funcformat 2), un-const-folded in the stored tree. Captured
	// live from PG18.3 (table cw):
	//   w1 int8 DEFAULT (CASE WHEN true THEN 1 ELSE 5000000000 END)        -- cast on WHEN
	//   w2 int8 DEFAULT (CASE WHEN true THEN 5000000000 ELSE 1 END)        -- cast on ELSE
	//   w3 int8 DEFAULT (CASE 1 WHEN 1 THEN 1 ELSE 5000000000 END)         -- simple form
	//   w4 int8 DEFAULT (CASE WHEN false THEN 1 WHEN true THEN 2 ELSE 5000000000 END) -- two int4 casts
	{
		name:    "crossfam_int4_then_int8_else",
		sql:     "CASE WHEN true THEN 1 ELSE 5000000000 END",
		colType: OidInt8,
		want:    `{CASEEXPR :casetype 20 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 481 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]} :location -1}`,
	},
	{
		name:    "crossfam_int8_then_int4_else",
		sql:     "CASE WHEN true THEN 5000000000 ELSE 1 END",
		colType: OidInt8,
		want:    `{CASEEXPR :casetype 20 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]} :location -1}) :defresult {FUNCEXPR :funcid 481 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}`,
	},
	{
		name:    "crossfam_simple_int4_int8",
		sql:     "CASE 1 WHEN 1 THEN 1 ELSE 5000000000 END",
		colType: OidInt8,
		want:    `{CASEEXPR :casetype 20 :casecollid 0 :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 481 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]} :location -1}`,
	},
	{
		name:    "crossfam_two_int4_casts",
		sql:     "CASE WHEN false THEN 1 WHEN true THEN 2 ELSE 5000000000 END",
		colType: OidInt8,
		want:    `{CASEEXPR :casetype 20 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 481 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1} {CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 481 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]} :location -1}`,
	},
	// M0123-S4 sub-slice 15: CASE cross-FAMILY *float* coercion. The results
	// mix float4 and float8 (produced here by the float4()/float8() conversion
	// functions — funcid 318/316, funcformat 0 — since this canonicalizer has
	// no float *literal* leaf). select_common_type walks the binary-float
	// family: float4 implicitly coerces to float8 (a preferred type) but not the
	// reverse, so the common type is float8 (casetype 701), and each float4
	// result is wrapped in the implicit float8(float4) cast FuncExpr (funcid 311,
	// funcformat 2), un-const-folded in the stored tree. Captured live from
	// PG18.3 (table cf):
	//   f1 float8 DEFAULT (CASE WHEN true THEN float4(1) ELSE float8(2) END)  -- cast on WHEN
	//   f2 float8 DEFAULT (CASE WHEN true THEN float8(1) ELSE float4(2) END)  -- cast on ELSE
	//   f3 float8 DEFAULT (CASE WHEN (1=1) THEN float4(10) WHEN (2=2) THEN float8(20) ELSE float4(30) END)
	{
		name:    "crossfam_float4_then_float8_else",
		sql:     "CASE WHEN true THEN float4(1) ELSE float8(2) END",
		colType: OidFloat8,
		want:    `{CASEEXPR :casetype 701 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 311 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :location -1}`,
	},
	{
		name:    "crossfam_float8_then_float4_else",
		sql:     "CASE WHEN true THEN float8(1) ELSE float4(2) END",
		colType: OidFloat8,
		want:    `{CASEEXPR :casetype 701 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 311 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} :location -1}`,
	},
	{
		name:    "crossfam_two_float4_casts",
		sql:     "CASE WHEN (1=1) THEN float4(10) WHEN (2=2) THEN float8(20) ELSE float4(30) END",
		colType: OidFloat8,
		want:    `{CASEEXPR :casetype 701 :casecollid 0 :arg <> :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 311 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 10 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} :location -1} {CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 20 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 311 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 30 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} :location -1}`,
	},
	// M0123-S4 sub-slice 16: UNIFIED cross-FAMILY CASE coercion — a mix spanning
	// the exact-integer/numeric family {int4,int8,numeric} and the binary-float
	// family that CONTAINS float8. PG's select_common_type walks the whole numeric
	// type category (TYPCATEGORY_NUMERIC 'N'); float8 is that category's PREFERRED
	// type, so whenever any result is float8 the common type is float8 (casetype
	// 701) and every non-float8 result is wrapped in its implicit float8 cast,
	// un-const-folded: float8(int4) 316, float8(int8) 482, float8(numeric) 1746,
	// float8(float4) 311 (all funcformat 2). Captured live from PG18.3 (tables
	// ucf/ucf5). A float4-but-no-float8 mix instead resolves to float4 + an outer
	// column cast (unmodeled) — see the degrade test.
	//   uc1 float8 DEFAULT (CASE WHEN true THEN 1 ELSE float8(2) END)        -- int4->float8 on WHEN
	//   uc2 float8 DEFAULT (CASE WHEN true THEN float8(1) ELSE 5000000000 END) -- int8->float8 on ELSE
	//   uc3 float8 DEFAULT (CASE WHEN true THEN 1.5 ELSE float8(2) END)      -- numeric->float8 on WHEN
	//   uc5 float8 DEFAULT (CASE WHEN (1=1) THEN 1 WHEN (2=2) THEN float4(20) ELSE float8(30) END)
	{
		name:    "unified_int4_to_float8_when",
		sql:     "CASE WHEN true THEN 1 ELSE float8(2) END",
		colType: OidFloat8,
		want:    `{CASEEXPR :casetype 701 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :location -1}`,
	},
	{
		name:    "unified_int8_to_float8_else",
		sql:     "CASE WHEN true THEN float8(1) ELSE 5000000000 END",
		colType: OidFloat8,
		want:    `{CASEEXPR :casetype 701 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 482 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]}) :location -1} :location -1}`,
	},
	{
		name:    "unified_numeric_to_float8_when",
		sql:     "CASE WHEN true THEN 1.5 ELSE float8(2) END",
		colType: OidFloat8,
		want:    `{CASEEXPR :casetype 701 :casecollid 0 :arg <> :args ({CASEWHEN :expr {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :result {FUNCEXPR :funcid 1746 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 1 0 -120 19 ]}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :location -1}`,
	},
	{
		name:    "unified_int4_float4_float8_three_families",
		sql:     "CASE WHEN (1=1) THEN 1 WHEN (2=2) THEN float4(20) ELSE float8(30) END",
		colType: OidFloat8,
		want:    `{CASEEXPR :casetype 701 :casecollid 0 :arg <> :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1} {CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 311 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 20 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 316 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 30 0 0 0 0 0 0 0 ]}) :location -1} :location -1}`,
	},
	// Simple-form WHEN-value implicit coercion (sub-slice 17): the operand and the
	// WHEN value differ in type, so PG's make_op (transformCaseExpr → makeSimpleA_Expr
	// "=") coerces the value to the operand's type. For a numeric operand there is no
	// cross-type `numeric = int4` operator, so PG selects numeric_eq (opno 1752 /
	// opfuncid 1718) and wraps the int4 value in the implicit int4_numeric (funcid
	// 1740, funcformat 2) cast — the CaseTestExpr placeholder stays un-coerced. goopg
	// reaches the identical tree because resolveCaseWhenCond resolves the value with
	// the operand type as its expected type, so resolveIntLiteral applies the same
	// int4_numeric cast before buildOpExpr picks the exact numeric_eq operator.
	// Captured live from PG18.3 (table sd):
	//   n1 numeric DEFAULT (CASE 5.0 WHEN 1 THEN 100.0 ELSE 200.0 END)
	//   n2 numeric DEFAULT (CASE 3.5 WHEN 1 THEN 1.5 WHEN 2 THEN 2.5 ELSE 3.5 END)
	{
		name:    "simple_numeric_operand_int_when_coerce",
		sql:     "CASE 5.0 WHEN 1 THEN 100.0 ELSE 200.0 END",
		colType: OidNumeric,
		want:    `{CASEEXPR :casetype 1700 :casecollid 0 :arg {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 8 [ 32 0 0 0 -128 -128 5 0 ]} :args ({CASEWHEN :expr {OPEXPR :opno 1752 :opfuncid 1718 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 1700 :typeMod -1 :collation 0} {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} :result {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 8 [ 32 0 0 0 -128 -128 100 0 ]} :location -1}) :defresult {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 8 [ 32 0 0 0 -128 -128 -56 0 ]} :location -1}`,
	},
	{
		name:    "simple_numeric_operand_int_when_coerce_multi",
		sql:     "CASE 3.5 WHEN 1 THEN 1.5 WHEN 2 THEN 2.5 ELSE 3.5 END",
		colType: OidNumeric,
		want:    `{CASEEXPR :casetype 1700 :casecollid 0 :arg {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 3 0 -120 19 ]} :args ({CASEWHEN :expr {OPEXPR :opno 1752 :opfuncid 1718 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 1700 :typeMod -1 :collation 0} {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} :result {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 1 0 -120 19 ]} :location -1} {CASEWHEN :expr {OPEXPR :opno 1752 :opfuncid 1718 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 1700 :typeMod -1 :collation 0} {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} :result {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]} :location -1}) :defresult {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 3 0 -120 19 ]} :location -1}`,
	},
	// Simple-form WHEN-value NATIVE cross-type operator (sub-slice 18): unlike the
	// numeric operand above, an integer operand paired with a differently-sized
	// integer value has a native cross-type `=` operator, so PG's make_op picks it
	// with the value LEFT UN-coerced (no int4_numeric-style cast). An int8 operand
	// + int4 value resolves to int8=int4 (opno 416 / opfuncid 474 int84eq); the
	// commutated int4 operand + int8 value resolves to int4=int8 (opno 15 /
	// opfuncid 852 int48eq). The CaseTestExpr placeholder keeps the operand's exact
	// type and the value Const keeps its own type — this is the shape ruleutils
	// deparses. Captured live from PG18.3 (bytes verified byte-for-byte by the
	// oracle_pgnodes_adbin_test.go live gate):
	//   b1 int8 DEFAULT (CASE 5000000000 WHEN 1 THEN 10000000000 ELSE 20000000000 END)
	//   b2 int  DEFAULT (CASE 1 WHEN 5000000000 THEN 10 ELSE 20 END)
	{
		name:    "simple_int8_operand_int4_when_native",
		sql:     "CASE 5000000000 WHEN 1 THEN 10000000000 ELSE 20000000000 END",
		colType: OidInt8,
		want:    `{CASEEXPR :casetype 20 :casecollid 0 :arg {CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]} :args ({CASEWHEN :expr {OPEXPR :opno 416 :opfuncid 474 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 20 :typeMod -1 :collation 0} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -28 11 84 2 0 0 0 ]} :location -1}) :defresult {CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -56 23 -88 4 0 0 0 ]} :location -1}`,
	},
	{
		name:    "simple_int4_operand_int8_when_native",
		sql:     "CASE 1 WHEN 5000000000 THEN 10 ELSE 20 END",
		colType: OidInt4,
		want:    `{CASEEXPR :casetype 23 :casecollid 0 :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} :args ({CASEWHEN :expr {OPEXPR :opno 15 :opfuncid 852 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CASETESTEXPR :typeId 23 :typeMod -1 :collation 0} {CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]}) :location -1} :result {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 10 0 0 0 0 0 0 0 ]} :location -1}) :defresult {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 20 0 0 0 0 0 0 0 ]} :location -1}`,
	},
	// float4-common CASE (sub-slice 32): the common type is float4 when at least
	// one result is float4 and no float8 result is present. PG stores casetype 700
	// with each narrower result wrapped in an implicit float4(...) FuncExpr
	// (funcformat 2). Captured live from PG18.3 (table cf4):
	//   d4 real DEFAULT (CASE WHEN (1=1) THEN 1 ELSE float4(2) END)
	//   d5 real DEFAULT (CASE WHEN (1=1) THEN 1.5 ELSE float4(2.5) END)
	//   d1 real DEFAULT (CASE WHEN (1=1) THEN 1.5 WHEN (2=2) THEN float4(20) ELSE 30 END)
	{
		name:    "crossfam_int4_float4_common",
		sql:     "CASE WHEN (1=1) THEN 1 ELSE float4(2) END",
		colType: OidFloat4,
		want:    `{CASEEXPR :casetype 700 :casecollid 0 :arg <> :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :location -1}`,
	},
	{
		name:    "crossfam_numeric_float4_common",
		sql:     "CASE WHEN (1=1) THEN 1.5 ELSE float4(2.5) END",
		colType: OidFloat4,
		want:    `{CASEEXPR :casetype 700 :casecollid 0 :arg <> :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 1745 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 1 0 -120 19 ]}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 1745 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]}) :location -1} :location -1}`,
	},
	{
		name:    "crossfam_multi_float4_common",
		sql:     "CASE WHEN (1=1) THEN 1.5 WHEN (2=2) THEN float4(20) ELSE 30 END",
		colType: OidFloat4,
		want:    `{CASEEXPR :casetype 700 :casecollid 0 :arg <> :args ({CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 1745 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 1 0 -120 19 ]}) :location -1} :location -1} {CASEWHEN :expr {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} :result {FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 20 0 0 0 0 0 0 0 ]}) :location -1} :location -1}) :defresult {FUNCEXPR :funcid 318 :funcresulttype 700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 30 0 0 0 0 0 0 0 ]}) :location -1} :location -1}`,
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
// SQL text after sub-slice 32's float4-common coercion landed: a
// collatable result type (text — would need a non-zero casecollid).
// Each must NOT resolve to a canonical node, so the writer keeps SQL text.
func TestCaseDegradesGracefully(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		colType uint32
	}{
		{"text_result", "CASE WHEN true THEN 'a' ELSE 'b' END", OidText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.colType); ok {
				t.Fatalf("ResolveForColumn(%q, %d) should degrade to SQL text, but accepted it", tc.sql, tc.colType)
			}
		})
	}
}
