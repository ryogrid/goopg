package pgnodes

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// numeric_test.go — M0123-S4 sub-slice 3 gate: canonical numeric (OID 1700)
// Const datums. A decimal/scientific literal packs into the on-disk NumericData
// varlena (a 4-byte varlena header + short/long NumericData header + int16
// base-10000 digits, all little-endian as an x86-64 PG holds it in memory), and
// outfuncs.c dumps that byte-for-byte — so byte-equality with a live PG18.3
// adbin/ev_action is the adversarial oracle.
//
// Every scalar `want` below was captured VERBATIM from PostgreSQL 18.3:
//
//	CREATE TABLE tn(a numeric DEFAULT 100.50, d numeric DEFAULT 0.001,
//	                e numeric DEFAULT 9999.9999, g numeric DEFAULT 3.14159265358979,
//	                h numeric DEFAULT (- 2.5), i numeric DEFAULT 1E-10);
//	SELECT a.attname, ad.adbin::text FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='tn'::regclass ORDER BY a.attnum;
//
// Note: an INTEGER-valued numeric default (DEFAULT 0, DEFAULT 12345) is NOT a
// numeric Const — PG types the bare integer int4 and wraps it in an int4_numeric
// cast FuncExpr (funcid 1740). Only a literal with a decimal point / exponent is
// typed numeric by the scanner, i.e. exactly the parser.NumericConst cases here.
var numericScalarGolden = []struct {
	name string
	sql  string
	want string
}{
	{
		name: "frac_trailing_zero",
		sql:  "100.50",
		want: `{CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 0 -127 100 0 -120 19 ]}`,
	},
	{
		name: "small_frac_neg_weight",
		sql:  "0.001",
		want: `{CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 8 [ 32 0 0 0 -1 -127 10 0 ]}`,
	},
	{
		name: "four_frac_digits",
		sql:  "9999.9999",
		want: `{CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 0 -126 15 39 15 39 ]}`,
	},
	{
		name: "many_digits",
		sql:  "3.14159265358979",
		want: `{CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 16 [ 64 0 0 0 0 -121 3 0 -121 5 49 36 5 14 -36 30 ]}`,
	},
	{
		name: "negative",
		sql:  "-2.5",
		want: `{CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -96 2 0 -120 19 ]}`,
	},
	{
		name: "scientific_neg_exp",
		sql:  "1E-10",
		want: `{CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 8 [ 32 0 0 0 125 -123 100 0 ]}`,
	},
}

// TestNumericResolveMatchesGolden parses each numeric literal and asserts
// ResolveExpr → Out is byte-identical to the real-PG18 adbin. Target type is
// numeric, so the negative case also exercises the doNegate folding (a single
// negative Const, not UnaryOp over a positive one).
func TestNumericResolveMatchesGolden(t *testing.T) {
	for _, tc := range numericScalarGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidNumeric)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			// A numeric literal on a numeric column must be accepted as canonical
			// (top result type == numeric), not fall back to SQL text.
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidNumeric); !ok {
				t.Fatalf("ResolveForColumn(%q, numeric) rejected a numeric-typed default", tc.sql)
			}
		})
	}
}

// TestNumericCodecRoundTrip closes the text → IR → text loop: Read must
// reconstruct an IR whose re-Out reproduces the exact bytes (the by-reference
// Datum path in readfuncs/outfuncs, unchanged, must handle numeric varlenas).
func TestNumericCodecRoundTrip(t *testing.T) {
	for _, tc := range numericScalarGolden {
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

// TestNumericResolveRebuildRoundTrip proves resolve → Rebuild → re-resolve is a
// fixed point. This is the load-bearing check for the decode side: RebuildConst
// must reconstruct a decimal literal whose dscale (trailing zeros) and sign
// re-encode to a byte-identical NumericData. "100.50" must NOT rebuild to "100.5".
func TestNumericResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range numericScalarGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), OidNumeric)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			n2, err := ResolveExpr(ast, OidNumeric)
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

// TestNumericRebuildPreservesScale checks the exact rebuilt literal text so a
// value that happens to re-encode identically can't mask a wrong decimal
// rendering. "100.50" keeps its trailing zero; "1E-10" renders as 0.0000000001.
func TestNumericRebuildPreservesScale(t *testing.T) {
	cases := []struct{ sql, wantLit string }{
		{"100.50", "100.50"},
		{"0.001", "0.001"},
		{"9999.9999", "9999.9999"},
		{"1E-10", "0.0000000001"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, c.sql), OidNumeric)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", c.sql, err)
			}
			ast, err := Rebuild(n)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", c.sql, err)
			}
			lit, ok := ast.(*parser.NumericConst)
			if !ok {
				t.Fatalf("rebuilt %q = %T, want *parser.NumericConst", c.sql, ast)
			}
			if lit.Value != c.wantLit {
				t.Fatalf("rebuilt literal for %q = %q, want %q", c.sql, lit.Value, c.wantLit)
			}
		})
	}
}

// TestNumericNegativeRebuildShape verifies a negative numeric Const rebuilds to
// UnaryOp{-, NumericConst}, matching how the parser tags `-2.5` (doNegate's
// inverse), not a NumericConst carrying a leading minus.
func TestNumericNegativeRebuildShape(t *testing.T) {
	n, err := ResolveExpr(mustParse(t, "-2.5"), OidNumeric)
	if err != nil {
		t.Fatalf("ResolveExpr: %v", err)
	}
	ast, err := Rebuild(n)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	u, ok := ast.(*parser.UnaryOp)
	if !ok || u.Op != parser.OpUnaryNeg {
		t.Fatalf("rebuilt = %#v, want UnaryOp{OpUnaryNeg}", ast)
	}
	lit, ok := u.Operand.(*parser.NumericConst)
	if !ok || lit.Value != "2.5" {
		t.Fatalf("rebuilt operand = %#v, want NumericConst{2.5}", u.Operand)
	}
}

// --- View-query numeric gate ------------------------------------------------

// srcnResolver stubs the catalog for the numeric-column view goldens:
//
//	CREATE TABLE srcn(id int, amount numeric, rate numeric);   -- relid 16403
//
// id is int4 (attno 1), amount/rate are numeric (attno 2/3, non-collatable),
// exactly as a live PG18.3 catalog reports them.
type srcnResolver struct{}

func (srcnResolver) LookupRelation(schema, name string) (*RelationInfo, bool) {
	if (schema != "" && schema != "public") || name != "srcn" {
		return nil, false
	}
	return &RelationInfo{
		Relid:   16403,
		Relname: "srcn",
		Relkind: 'r',
		Columns: []ColumnInfo{
			{Name: "id", Attno: 1, TypeOID: OidInt4, Typmod: -1, Collation: 0},
			{Name: "amount", Attno: 2, TypeOID: OidNumeric, Typmod: -1, Collation: 0},
			{Name: "rate", Attno: 3, TypeOID: OidNumeric, Typmod: -1, Collation: 0},
		},
	}, true
}

// goldenViewVN is the ev_action PostgreSQL 18.3 stored for
//
//	CREATE VIEW vn AS SELECT id, amount FROM srcn
//	  WHERE amount > 100.50 AND rate < 0.001;
//
// The qual is a two-arg AND BOOLEXPR of `amount > 100.50` (OPEXPR opno 1756,
// numeric >) and `rate < 0.001` (OPEXPR opno 1754, numeric <), each with a
// numeric Var and a numeric Const — the same packed bytes as the scalar goldens.
const goldenViewVN = `({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> :resultRelation 0 :hasAggs false :hasWindowFuncs false :hasTargetSRFs false :hasSubLinks false :hasDistinctOn false :hasRecursive false :hasModifyingCTE false :hasForUpdate false :hasRowSecurity false :hasGroupRTE false :isReturn false :cteList <> :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname srcn :colnames ("id" "amount" "rate")} :rtekind 0 :relid 16403 :inh true :relkind r :rellockmode 1 :perminfoindex 1 :tablesample <> :lateral false :inFromCl true :securityQuals <>}) :rteperminfos ({RTEPERMISSIONINFO :relid 16403 :inh true :requiredPerms 2 :checkAsUser 0 :selectedCols (b 8 9 10) :insertedCols (b) :updatedCols (b)}) :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1}) :quals {BOOLEXPR :boolop and :args ({OPEXPR :opno 1756 :opfuncid 1720 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 2 :vartype 1700 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 0 -127 100 0 -120 19 ]}) :location -1} {OPEXPR :opno 1754 :opfuncid 1722 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 3 :vartype 1700 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 3 :location -1} {CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 8 [ 32 0 0 0 -1 -127 10 0 ]}) :location -1}) :location -1}} :mergeActionList <> :mergeTargetRelation 0 :mergeJoinCondition <> :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 1 :location -1} :resno 1 :resname id :ressortgroupref 0 :resorigtbl 16403 :resorigcol 1 :resjunk false} {TARGETENTRY :expr {VAR :varno 1 :varattno 2 :vartype 1700 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varreturningtype 0 :varnosyn 1 :varattnosyn 2 :location -1} :resno 2 :resname amount :ressortgroupref 0 :resorigtbl 16403 :resorigcol 2 :resjunk false}) :override 0 :onConflict <> :returningOldAlias <> :returningNewAlias <> :returningList <> :groupClause <> :groupDistinct false :groupingSets <> :havingQual <> :windowClause <> :distinctClause <> :sortClause <> :limitOffset <> :limitCount <> :limitOption 0 :rowMarks <> :setOperations <> :constraintDeps <> :withCheckOptions <> :stmt_location -1 :stmt_len -1})`

const viewNumericSQL = "SELECT id, amount FROM srcn WHERE amount > 100.50 AND rate < 0.001"

// TestResolveViewQueryNumeric is the forward gate: a view qual comparing numeric
// columns to numeric literals must resolve byte-identical to PG18.3's ev_action.
func TestResolveViewQueryNumeric(t *testing.T) {
	q, err := ResolveViewQuery(parseSelect(t, viewNumericSQL), srcnResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery: %v", err)
	}
	if got := OutRuleAction([]Node{q}); got != goldenViewVN {
		t.Fatalf("ev_action mismatch:\n got=%s\nwant=%s", got, goldenViewVN)
	}
}

// TestResolveViewQueryNumericRoundTrip closes the codec loop (Out → Read → Out).
func TestResolveViewQueryNumericRoundTrip(t *testing.T) {
	q, err := ResolveViewQuery(parseSelect(t, viewNumericSQL), srcnResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery: %v", err)
	}
	first := OutRuleAction([]Node{q})
	nodes, err := ReadRuleAction(first)
	if err != nil {
		t.Fatalf("ReadRuleAction: %v", err)
	}
	if got := OutRuleAction(nodes); got != first {
		t.Fatalf("re-Out mismatch:\n first=%s\n reOut=%s", first, got)
	}
}

// TestRebuildViewQueryNumeric is the reload-inverse fixed point: resolve →
// RebuildViewQuery → re-resolve reproduces the exact golden, proving the
// query-scoped rebuild turns a numeric Const back into a literal that
// re-resolves to identical canonical bytes.
func TestRebuildViewQueryNumeric(t *testing.T) {
	q, err := ResolveViewQuery(parseSelect(t, viewNumericSQL), srcnResolver{})
	if err != nil {
		t.Fatalf("ResolveViewQuery: %v", err)
	}
	sel, err := RebuildViewQuery(q)
	if err != nil {
		t.Fatalf("RebuildViewQuery: %v", err)
	}
	q2, err := ResolveViewQuery(sel, srcnResolver{})
	if err != nil {
		t.Fatalf("re-ResolveViewQuery: %v", err)
	}
	if got := OutRuleAction([]Node{q2}); got != goldenViewVN {
		t.Fatalf("rebuild round-trip ev_action mismatch:\n got=%s\nwant=%s", got, goldenViewVN)
	}
}
