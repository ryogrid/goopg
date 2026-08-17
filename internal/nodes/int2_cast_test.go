package nodes

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// int2_cast_test.go — M0123-S4 sub-slice 31 gate: the implicit int→int2 cast.
// A bare INTEGER literal assigned to an int2 (smallint) column is NOT an int2
// Const — PG's scanner types it int4 (or int8 if it exceeds int4 range) and
// coerce_to_target_type wraps it in an implicit-cast FuncExpr (int2(int4) funcid
// 314 / int2(int8) funcid 714, funcformat 2 = COERCE_IMPLICIT_CAST).
//
// Every `want` was captured VERBATIM from PostgreSQL 18.3:
//
//	CREATE TABLE t_int2_def(a int2 DEFAULT 5, b int2 DEFAULT 0,
//	                        c int2 DEFAULT (-3), d int2 DEFAULT 32767);
//	SELECT a.attname, ad.adbin::text FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='t_int2_def'::regclass ORDER BY a.attnum;
var int2CastGolden = []struct {
	name string
	sql  string
	want string
}{
	{
		name: "int4_positive",
		sql:  "5",
		want: `{FUNCEXPR :funcid 314 :funcresulttype 21 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name: "int4_zero",
		sql:  "0",
		want: `{FUNCEXPR :funcid 314 :funcresulttype 21 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name: "int4_negative_fold",
		sql:  "-3",
		want: `{FUNCEXPR :funcid 314 :funcresulttype 21 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -3 -1 -1 -1 -1 -1 -1 -1 ]}) :location -1}`,
	},
	{
		name: "int4_int2max",
		sql:  "32767",
		want: `{FUNCEXPR :funcid 314 :funcresulttype 21 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -1 127 0 0 0 0 0 0 ]}) :location -1}`,
	},
}

// TestInt2CastResolveMatchesGolden is the forward oracle: an integer literal
// in an int2 column context must resolve byte-identical to PG18.3's adbin —
// an implicit-cast FuncExpr wrapping the int4 Const, not a bare Const.
func TestInt2CastResolveMatchesGolden(t *testing.T) {
	for _, tc := range int2CastGolden {
		t.Run(tc.name, func(t *testing.T) {
			e := mustParse(t, tc.sql)
			n, ok := ResolveForColumn(e, OidInt2)
			if !ok {
				t.Fatalf("ResolveForColumn(%q, int2) returned ok=false", tc.sql)
			}
			got := Out(n)
			if got != tc.want {
				t.Errorf("ResolveForColumn(%q, int2) =\n  %s\nwant:\n  %s", tc.sql, got, tc.want)
			}
		})
	}
}

// TestInt2CastResolveForColumnAccepts guards: ResolveForColumn must ACCEPT a
// bare integer literal in an int2 context (the int4→int2 implicit cast FuncExpr
// now covers it). Before sub-slice 31 it fell back to SQL text.
func TestInt2CastResolveForColumnAccepts(t *testing.T) {
	for _, tc := range int2CastGolden {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidInt2); !ok {
				t.Fatalf("ResolveForColumn(%q, int2) rejected; want accepted", tc.sql)
			}
		})
	}
}

// TestInt2CastNoWrapInIntContext guards the scope: the same integer literal in
// an int4 context must stay a bare Const — the cast is only synthesised when
// the column type is exactly int2.
func TestInt2CastNoWrapInIntContext(t *testing.T) {
	n, err := ResolveExpr(mustParse(t, "5"), OidInt4)
	if err != nil {
		t.Fatalf("ResolveExpr: %v", err)
	}
	if _, ok := n.(*Const); !ok {
		t.Fatalf("int4 context: got %T, want a bare *Const (no int2 cast)", n)
	}
}

// TestInt2CastCodecRoundTrip closes text -> IR -> text (Read then re-Out).
func TestInt2CastCodecRoundTrip(t *testing.T) {
	for _, tc := range int2CastGolden {
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

// TestInt2CastResolveRebuildRoundTrip proves resolve -> Rebuild -> re-resolve
// is a fixed point: the cast FuncExpr must rebuild to the inner integer literal
// (not an int2(<int>) call) so the re-resolve re-wraps identical bytes.
func TestInt2CastResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range int2CastGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), OidInt2)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			// The rebuilt AST is a plain integer literal (or its negation), never
			// a function call — the implicit cast has no SQL surface syntax.
			switch a := ast.(type) {
			case *parser.IntegerConst:
			case *parser.UnaryOp:
				if a.Op != parser.OpUnaryNeg {
					t.Fatalf("rebuilt %q = UnaryOp{%v}, want OpUnaryNeg", tc.sql, a.Op)
				}
			default:
				t.Fatalf("rebuilt %q = %T, want IntegerConst or UnaryOp{Neg, IntegerConst}", tc.sql, ast)
			}
			n2, err := ResolveExpr(ast, OidInt2)
			if err != nil {
				t.Fatalf("re-ResolveExpr(%q): %v", tc.sql, err)
			}
			if !reflect.DeepEqual(n1, n2) {
				t.Fatalf("resolve->Rebuild->re-resolve not a fixed point for %q:\n first: %s\nsecond: %s",
					tc.sql, Out(n1), Out(n2))
			}
		})
	}
}

// TestInt2CastDeepEqual verifies the resolved node is DeepEqual to its
// Read(Out(node)) round-trip, confirming encode/decode agree.
func TestInt2CastDeepEqual(t *testing.T) {
	for _, tc := range int2CastGolden {
		t.Run(tc.name, func(t *testing.T) {
			e := mustParse(t, tc.sql)
			n1, ok := ResolveForColumn(e, OidInt2)
			if !ok {
				t.Fatalf("ResolveForColumn(%q, int2) returned ok=false", tc.sql)
			}
			n2, err := Read(Out(n1))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if !reflect.DeepEqual(n1, n2) {
				t.Errorf("ResolveForColumn(%q, int2) not DeepEqual to Read(Out(node))", tc.sql)
			}
		})
	}
}

// TestInt2CastShape asserts the exact structure: FuncExpr wrapping an int4 Const.
func TestInt2CastShape(t *testing.T) {
	e := mustParse(t, "5")
	n, ok := ResolveForColumn(e, OidInt2)
	if !ok {
		t.Fatal("ResolveForColumn(5, int2) returned ok=false")
	}
	fe, ok := n.(*FuncExpr)
	if !ok {
		t.Fatalf("ResolveForColumn(5, int2) = %T, want *FuncExpr (int4→int2 implicit cast)", n)
	}
	if fe.Funcid != 314 {
		t.Errorf("funcid = %d, want 314 (int2(int4))", fe.Funcid)
	}
	if fe.Funcformat != 2 {
		t.Errorf("funcformat = %d, want 2 (COERCE_IMPLICIT_CAST)", fe.Funcformat)
	}
	if fe.Funcresulttype != OidInt2 {
		t.Errorf("funcresulttype = %d, want %d (int2)", fe.Funcresulttype, OidInt2)
	}
	if len(fe.Args) != 1 {
		t.Fatalf("len(args) = %d, want 1", len(fe.Args))
	}
	arg, ok := fe.Args[0].(*Const)
	if !ok {
		t.Fatalf("arg = %T, want *Const", fe.Args[0])
	}
	if arg.ConstType != OidInt4 {
		t.Errorf("inner Const type = %d, want %d (int4)", arg.ConstType, OidInt4)
	}
}
