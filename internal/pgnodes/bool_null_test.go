package pgnodes

import (
	"reflect"
	"testing"
)

// M0123-S4 sub-slice 1: BOOLEXPR (AND/OR/NOT) and NULLTEST (IS [NOT] NULL).
//
// Every `want` below was captured VERBATIM from a live PostgreSQL 18.3 server
// (pg_attrdef.adbin is exactly nodeToString of the default expression):
//
//	CREATE TABLE t(
//	  a bool DEFAULT (1 IS NULL),
//	  b bool DEFAULT (1 IS NOT NULL),
//	  c bool DEFAULT (true AND false),
//	  d bool DEFAULT (true OR false),
//	  e bool DEFAULT (NOT true),
//	  f bool DEFAULT ((1 < 2) AND (3 > 2) AND (5 = 5)));
//	SELECT a.attname, ad.adbin::text FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='t'::regclass ORDER BY a.attnum;
//
// so byte-equality is the adversarial oracle the milestone gate requires. Case
// `f` proves the makeAndExpr flattening: `(1<2) AND (3>2) AND (5=5)` is ONE
// BoolExpr with three OpExpr args, not a nested pair.

// boolNullGolden pairs a SQL default expression with the real-PG adbin string,
// so the same case exercises the codec (Out/Read) AND the resolver (parse →
// ResolveExpr → Out) with a single source of truth.
var boolNullGolden = []struct {
	name string
	sql  string
	want string
}{
	{
		name: "isnull",
		sql:  "1 IS NULL",
		want: `{NULLTEST :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} :nulltesttype 0 :argisrow false :location -1}`,
	},
	{
		name: "isnotnull",
		sql:  "1 IS NOT NULL",
		want: `{NULLTEST :arg {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} :nulltesttype 1 :argisrow false :location -1}`,
	},
	{
		name: "and",
		sql:  "true AND false",
		want: `{BOOLEXPR :boolop and :args ({CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name: "or",
		sql:  "true OR false",
		want: `{BOOLEXPR :boolop or :args ({CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name: "not",
		sql:  "NOT true",
		want: `{BOOLEXPR :boolop not :args ({CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name: "and_flatten_three",
		sql:  "(1 < 2) AND (3 > 2) AND (5 = 5)",
		want: `{BOOLEXPR :boolop and :args ({OPEXPR :opno 97 :opfuncid 66 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} {OPEXPR :opno 521 :opfuncid 147 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 3 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}) :location -1}`,
	},
}

// TestBoolNullResolveMatchesGolden parses each SQL default and asserts
// ResolveExpr → Out is byte-identical to the real-PG18 adbin. The target type is
// bool (these are boolean-valued defaults), so this also exercises
// ResolveForColumn's exact-type-match acceptance path.
func TestBoolNullResolveMatchesGolden(t *testing.T) {
	for _, tc := range boolNullGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidBool)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			// The whole expression is bool-typed, so the writer's exact-match
			// gate must accept it as canonical (not fall back to SQL text).
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidBool); !ok {
				t.Fatalf("ResolveForColumn(%q, bool) rejected a bool-typed default", tc.sql)
			}
		})
	}
}

// TestBoolNullCodecRoundTrip closes the text → IR → text loop against each
// golden: Read must reconstruct an IR whose re-Out reproduces the exact bytes.
func TestBoolNullCodecRoundTrip(t *testing.T) {
	for _, tc := range boolNullGolden {
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

// TestBoolNullResolveRebuildRoundTrip proves resolve → Rebuild → re-resolve is a
// fixed point: the rebuilt goopg AST re-resolves to a byte-identical tree. This
// is the load-bearing check for the AND-flattening inverse (an n-ary BoolExpr
// rebuilds to a left-nested BinaryOp chain that PG's flattening collapses back
// to the same n args).
func TestBoolNullResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range boolNullGolden {
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
			if !reflect.DeepEqual(n1, n2) {
				t.Fatalf("resolve→Rebuild→re-resolve not a fixed point for %q:\n first: %s\nsecond: %s",
					tc.sql, Out(n1), Out(n2))
			}
		})
	}
}

// TestBoolExprNestedRightNotFlattened verifies the parenthesized right side
// `a AND (b AND c)` stays NESTED (a two-arg outer BoolExpr whose second arg is
// itself a BoolExpr), mirroring makeAndExpr, which flattens only the left spine.
func TestBoolExprNestedRightNotFlattened(t *testing.T) {
	n, err := ResolveExpr(mustParse(t, "true AND (false AND true)"), OidBool)
	if err != nil {
		t.Fatalf("ResolveExpr: %v", err)
	}
	be, ok := n.(*BoolExpr)
	if !ok || be.Boolop != BoolExprAnd {
		t.Fatalf("want top AND BoolExpr, got %T", n)
	}
	if len(be.Args) != 2 {
		t.Fatalf("want 2 args (nested right), got %d: %s", len(be.Args), Out(n))
	}
	if _, ok := be.Args[1].(*BoolExpr); !ok {
		t.Fatalf("want args[1] to stay a nested BoolExpr, got %T", be.Args[1])
	}
}

// TestReadRejectsBadBoolop ensures a corrupt :boolop token is a clean error so
// the reload falls back to SQL text rather than mis-parsing.
func TestReadRejectsBadBoolop(t *testing.T) {
	if _, err := Read(`{BOOLEXPR :boolop xor :args (<>) :location -1}`); err == nil {
		t.Fatal("expected error for unrecognized boolop, got nil")
	}
}
