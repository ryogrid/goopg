package pgnodes

import (
	"testing"
)

// M0123-S4 sub-slice 9: canonical DISTINCTEXPR (`a IS [NOT] DISTINCT FROM b`).
//
// Every `want` below was captured VERBATIM from a live PostgreSQL 18.3 server
// (pg_attrdef.adbin is exactly nodeToString of the default expression):
//
//	CREATE TABLE d1 (
//	  a bool DEFAULT (1 IS DISTINCT FROM 2),
//	  b bool DEFAULT (1 IS NOT DISTINCT FROM 2),
//	  c bool DEFAULT ('x' IS DISTINCT FROM 'y'),
//	  d bool DEFAULT (1.5 IS DISTINCT FROM 2.5),
//	  e bool DEFAULT (true IS DISTINCT FROM false));
//	SELECT a.attname, ad.adbin::text FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='d1'::regclass ORDER BY a.attnum;
//
// so byte-equality is the adversarial oracle the milestone gate requires. The
// cases cover: the plain DISTINCTEXPR (a), the NOT-wrapped IS NOT DISTINCT FROM
// (b — a NOT BOOLEXPR over a DISTINCTEXPR), text operands (c — inputcollid 100,
// opno 98/opfuncid 67), numeric operands (d — opno 1752/opfuncid 1718), and
// bool operands (e — opno 91/opfuncid 60). Each reuses the `=` operator's
// opno/opfuncid/collation exactly as a plain `a = b` (make_distinct_op just
// re-tags a make_op OpExpr).

var distinctGolden = []struct {
	name string
	sql  string
	want string
}{
	{
		name: "int",
		sql:  "1 IS DISTINCT FROM 2",
		want: `{DISTINCTEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name: "int_not_distinct",
		sql:  "1 IS NOT DISTINCT FROM 2",
		want: `{BOOLEXPR :boolop not :args ({DISTINCTEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1}) :location -1}`,
	},
	{
		name: "text",
		sql:  "'x' IS DISTINCT FROM 'y'",
		want: `{DISTINCTEXPR :opno 98 :opfuncid 67 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 100 :args ({CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 5 [ 20 0 0 0 120 ]} {CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 5 [ 20 0 0 0 121 ]}) :location -1}`,
	},
	{
		name: "numeric",
		sql:  "1.5 IS DISTINCT FROM 2.5",
		want: `{DISTINCTEXPR :opno 1752 :opfuncid 1718 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 1 0 -120 19 ]} {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]}) :location -1}`,
	},
	{
		name: "bool",
		sql:  "true IS DISTINCT FROM false",
		want: `{DISTINCTEXPR :opno 91 :opfuncid 60 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
}

// TestDistinctExprForward parses each bool default, resolves it against a bool
// column, and asserts the emitted node string is byte-for-byte identical to
// PG18.3's adbin; ResolveForColumn must also accept it as canonical.
func TestDistinctExprForward(t *testing.T) {
	for _, tc := range distinctGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidBool)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("adbin mismatch for %q:\n got:  %s\n want: %s", tc.sql, got, tc.want)
			}
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidBool); !ok {
				t.Fatalf("ResolveForColumn(%q, bool) rejected a matching-type default", tc.sql)
			}
		})
	}
}

// TestDistinctExprReadRoundTrip parses the PG18.3 adbin back into IR and
// re-emits it, asserting the round trip is byte-stable (exercises
// readDistinctExpr / outDistinctExpr independently of the resolver).
func TestDistinctExprReadRoundTrip(t *testing.T) {
	for _, tc := range distinctGolden {
		t.Run(tc.name, func(t *testing.T) {
			node, err := Read(tc.want)
			if err != nil {
				t.Fatalf("Read(%q): %v", tc.want, err)
			}
			if got := Out(node); got != tc.want {
				t.Fatalf("Read→Out not byte-stable:\n got:  %s\n want: %s", got, tc.want)
			}
		})
	}
}

// TestDistinctExprRebuildFixedPoint asserts resolve→Rebuild→re-resolve is a
// fixed point (the rebuilt AST re-resolves to the identical node string). The
// NOT form rebuilds to `NOT (a IS DISTINCT FROM b)`, a distinct spelling that
// must still re-resolve to the same NOT-BOOLEXPR/DISTINCTEXPR IR.
func TestDistinctExprRebuildFixedPoint(t *testing.T) {
	for _, tc := range distinctGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), OidBool)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			n2, err := ResolveExpr(ast, OidBool)
			if err != nil {
				t.Fatalf("re-ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n2); got != tc.want {
				t.Fatalf("resolve→Rebuild→re-resolve not a fixed point for %q:\n got:  %s\n want: %s", tc.sql, got, tc.want)
			}
		})
	}
}
