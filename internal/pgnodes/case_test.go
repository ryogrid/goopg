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

// TestCaseDegradesGracefully covers the bounded-subset boundaries: a
// mixed-result-type CASE (select_common_type coercion — deferred), the simple
// form (`CASE operand WHEN …`, a CaseTestExpr placeholder subset — deferred),
// and a collatable result type (text — would need a non-zero casecollid). Each
// must NOT resolve to a canonical node, so the writer keeps SQL text.
func TestCaseDegradesGracefully(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		colType uint32
	}{
		{"mixed_int_numeric", "CASE WHEN true THEN 1 ELSE 2.5 END", OidNumeric},
		{"simple_form", "CASE 1 WHEN 1 THEN 10 ELSE 20 END", OidInt4},
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
