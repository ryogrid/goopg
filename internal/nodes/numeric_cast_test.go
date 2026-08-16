package nodes

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// numeric_cast_test.go — M0123-S4 sub-slice 4(a) gate: the implicit int->numeric
// cast. A bare INTEGER literal assigned to a numeric column is NOT a numeric
// Const — PG's scanner types it int4/int8 and coerce_to_target_type wraps it in
// an implicit-cast FuncExpr (int4_numeric funcid 1740 / int8_numeric funcid
// 1781, funcformat 2 = COERCE_IMPLICIT_CAST). This closes the gap the sub-slice-3
// numeric work explicitly deferred.
//
// Every `want` was captured VERBATIM from PostgreSQL 18.3:
//
//	CREATE TABLE tni(a numeric DEFAULT 12345, b numeric DEFAULT 0,
//	                 c numeric DEFAULT (- 5), d numeric DEFAULT 5000000000,
//	                 e numeric DEFAULT 32767);
//	SELECT a.attname, ad.adbin::text FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='tni'::regclass ORDER BY a.attnum;
var numericCastGolden = []struct {
	name string
	sql  string
	want string
}{
	{
		name: "int4_positive",
		sql:  "12345",
		want: `{FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 57 48 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name: "int4_zero",
		sql:  "0",
		want: `{FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
	},
	{
		name: "int4_negative_fold",
		sql:  "-5",
		want: `{FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -5 -1 -1 -1 -1 -1 -1 -1 ]}) :location -1}`,
	},
	{
		name: "int8_over_int4max",
		sql:  "5000000000",
		want: `{FUNCEXPR :funcid 1781 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]}) :location -1}`,
	},
	{
		name: "int4_int2range_still_int4",
		sql:  "32767",
		want: `{FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -1 127 0 0 0 0 0 0 ]}) :location -1}`,
	},
}

// TestNumericCastResolveMatchesGolden is the forward oracle: an integer literal
// in a numeric column context must resolve byte-identical to PG18.3's adbin —
// an implicit-cast FuncExpr wrapping the int4/int8 Const, not a bare Const.
func TestNumericCastResolveMatchesGolden(t *testing.T) {
	for _, tc := range numericCastGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidNumeric)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			// The whole point of the cast: ResolveForColumn must now ACCEPT the
			// default as canonical (top type == numeric), not fall back to SQL text.
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidNumeric); !ok {
				t.Fatalf("ResolveForColumn(%q, numeric) rejected an integer numeric default", tc.sql)
			}
		})
	}
}

// TestNumericCastNoWrapInIntContext guards the scope: the same integer literal in
// an int4/int8 (or unknown) context must stay a bare Const — the cast is only
// synthesised when the column type is exactly numeric.
func TestNumericCastNoWrapInIntContext(t *testing.T) {
	n, err := ResolveExpr(mustParse(t, "12345"), OidInt4)
	if err != nil {
		t.Fatalf("ResolveExpr: %v", err)
	}
	if _, ok := n.(*Const); !ok {
		t.Fatalf("int4 context: got %T, want a bare *Const (no numeric cast)", n)
	}
}

// TestNumericCastCodecRoundTrip closes text -> IR -> text (Read then re-Out).
func TestNumericCastCodecRoundTrip(t *testing.T) {
	for _, tc := range numericCastGolden {
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

// TestNumericCastResolveRebuildRoundTrip proves resolve -> Rebuild -> re-resolve
// is a fixed point: the cast FuncExpr must rebuild to the inner integer literal
// (not a numeric(<int>) call) so the re-resolve re-wraps identical bytes.
func TestNumericCastResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range numericCastGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), OidNumeric)
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
				t.Fatalf("rebuilt %q = %T, want IntegerConst or UnaryOp", tc.sql, ast)
			}
			n2, err := ResolveExpr(ast, OidNumeric)
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
