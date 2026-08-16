package nodes

import (
	"reflect"
	"testing"
)

// M0123-S4 sub-slice 5: BOOLEANTEST (`x IS [NOT] TRUE/FALSE/UNKNOWN`).
//
// Every `want` below was captured VERBATIM from a live PostgreSQL 18.3 server
// (pg_attrdef.adbin is exactly nodeToString of the default expression):
//
//	CREATE TABLE bt(
//	  a bool DEFAULT (true IS TRUE),
//	  b bool DEFAULT (true IS NOT TRUE),
//	  c bool DEFAULT (false IS FALSE),
//	  d bool DEFAULT (false IS NOT FALSE),
//	  e bool DEFAULT ((1=1) IS UNKNOWN),
//	  f bool DEFAULT ((1=1) IS NOT UNKNOWN));
//	SELECT a.attname, ad.adbin::text FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='bt'::regclass ORDER BY a.attnum;
//
// so byte-equality is the adversarial oracle the milestone gate requires. The
// six booltesttype ordinals 0..5 (IS_TRUE..IS_NOT_UNKNOWN) each appear exactly
// once; cases e/f prove the argument may be a non-trivial sub-expression (an
// OPEXPR `1 = 1`, opno 96), not just a bare Const.

// booleanTestGolden pairs a SQL default expression with the real-PG adbin
// string, so the same case exercises the codec (Out/Read) AND the resolver
// (parse → ResolveExpr → Out) with a single source of truth.
var booleanTestGolden = []struct {
	name string
	sql  string
	want string
}{
	{
		name: "is_true",
		sql:  "true IS TRUE",
		want: `{BOOLEANTEST :arg {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :booltesttype 0 :location -1}`,
	},
	{
		name: "is_not_true",
		sql:  "true IS NOT TRUE",
		want: `{BOOLEANTEST :arg {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :booltesttype 1 :location -1}`,
	},
	{
		name: "is_false",
		sql:  "false IS FALSE",
		want: `{BOOLEANTEST :arg {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} :booltesttype 2 :location -1}`,
	},
	{
		name: "is_not_false",
		sql:  "false IS NOT FALSE",
		want: `{BOOLEANTEST :arg {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} :booltesttype 3 :location -1}`,
	},
	{
		name: "is_unknown",
		sql:  "(1=1) IS UNKNOWN",
		want: `{BOOLEANTEST :arg {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :booltesttype 4 :location -1}`,
	},
	{
		name: "is_not_unknown",
		sql:  "(1=1) IS NOT UNKNOWN",
		want: `{BOOLEANTEST :arg {OPEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} :booltesttype 5 :location -1}`,
	},
}

// TestBooleanTestResolveMatchesGolden parses each SQL default and asserts
// ResolveExpr → Out is byte-identical to the real-PG18 adbin. The result is
// always bool, so this also exercises ResolveForColumn's exact-type acceptance.
func TestBooleanTestResolveMatchesGolden(t *testing.T) {
	for _, tc := range booleanTestGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidBool)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidBool); !ok {
				t.Fatalf("ResolveForColumn(%q, bool) rejected a bool-typed default", tc.sql)
			}
		})
	}
}

// TestBooleanTestCodecRoundTrip closes the text → IR → text loop: Read must
// reconstruct an IR whose re-Out reproduces the exact bytes.
func TestBooleanTestCodecRoundTrip(t *testing.T) {
	for _, tc := range booleanTestGolden {
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

// TestBooleanTestResolveRebuildRoundTrip proves resolve → Rebuild → re-resolve
// is a fixed point: the rebuilt goopg AST re-resolves to a byte-identical tree,
// so the view/DEFAULT reload path is loss-free for every booltesttype ordinal.
func TestBooleanTestResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range booleanTestGolden {
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

// TestReadRejectsBadBooltesttype ensures a corrupt :booltesttype ordinal is a
// clean Rebuild error so the reload falls back to SQL text rather than
// producing a garbage node (Read accepts any int; Rebuild is the guard).
func TestReadRejectsBadBooltesttype(t *testing.T) {
	bad := `{BOOLEANTEST :arg {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]} :booltesttype 9 :location -1}`
	n, err := Read(bad)
	if err != nil {
		t.Fatalf("Read of structurally-valid BOOLEANTEST should succeed: %v", err)
	}
	if _, err := Rebuild(n); err == nil {
		t.Fatal("expected Rebuild error for out-of-range booltesttype 9, got nil")
	}
}
