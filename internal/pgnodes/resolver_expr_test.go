package pgnodes

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// mustParse parses a scalar SQL expression or fails the test.
func mustParse(t *testing.T, sql string) parser.Expr {
	t.Helper()
	e, err := parser.ParseExpr(sql)
	if err != nil {
		t.Fatalf("ParseExpr(%q): %v", sql, err)
	}
	return e
}

// TestResolveExprCanonicalOut pins ResolveExpr's canonical byte output for the
// supported scalar subset (literal Const, folded unary minus, OpExpr). The
// int-literal and negative-int strings are byte-identical to the S1 goldens.
func TestResolveExprCanonicalOut(t *testing.T) {
	cases := []struct {
		sql        string
		targetType uint32
		want       string
	}{
		{
			sql:        "42",
			targetType: OidInt4,
			want:       "{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 42 0 0 0 0 0 0 0 ]}",
		},
		{
			// PG folds `- 1` into one negative int4 Const (doNegate); the datum
			// word sign-extends to 0xFF bytes → -1 in the signed decimal wire form.
			sql:        "-1",
			targetType: OidInt4,
			want:       "{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -1 -1 -1 -1 -1 -1 -1 -1 ]}",
		},
		{
			// Bigint literal (does not fit int4) → int8 Const.
			sql:        "5000000000",
			targetType: 0,
			want:       "{CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]}",
		},
		{
			// text literal in a text context.
			sql:        "'hi'",
			targetType: OidText,
			want:       "{CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 6 [ 24 0 0 0 104 105 ]}",
		},
		{
			// Plain built-in FuncExpr: upper('x'). funcid 871 (upper),
			// funcresulttype 25 (text), collations DEFAULT_COLLATION_OID (100)
			// on both the text arg and result. Byte-for-byte identical to a live
			// PG18.3 pg_attrdef.adbin captured from `b text DEFAULT upper('x')`.
			sql:        "upper('x')",
			targetType: OidText,
			want:       "{FUNCEXPR :funcid 871 :funcresulttype 25 :funcretset false :funcvariadic false :funcformat 0 :funccollid 100 :inputcollid 100 :args ({CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 5 [ 20 0 0 0 120 ]}) :location -1}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), tc.targetType)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
		})
	}
}

// TestResolveBinaryOpForwardResolves verifies `40 + 2` resolves to an OpExpr
// whose operator OID/funcid/result type come from the S0 pg_operator index and
// whose two args are the int4 operand Consts.
func TestResolveBinaryOpForwardResolves(t *testing.T) {
	n, err := ResolveExpr(mustParse(t, "40 + 2"), OidInt4)
	if err != nil {
		t.Fatalf("ResolveExpr(40 + 2): %v", err)
	}
	op, ok := n.(*OpExpr)
	if !ok {
		t.Fatalf("want *OpExpr, got %T", n)
	}
	if op.Opno == 0 || op.Opfuncid == 0 {
		t.Fatalf("operator not forward-resolved: opno=%d opfuncid=%d", op.Opno, op.Opfuncid)
	}
	if op.Opresulttype != OidInt4 {
		t.Fatalf("opresulttype = %d, want %d (int4)", op.Opresulttype, OidInt4)
	}
	if op.Opcollid != 0 || op.Inputcollid != 0 {
		t.Fatalf("int4 op collations must be 0, got opcollid=%d inputcollid=%d", op.Opcollid, op.Inputcollid)
	}
	if len(op.Args) != 2 {
		t.Fatalf("want 2 args, got %d", len(op.Args))
	}
	for i, a := range op.Args {
		c, ok := a.(*Const)
		if !ok || c.ConstType != OidInt4 {
			t.Fatalf("arg %d = %T (want int4 Const)", i, a)
		}
	}
}

// TestResolveRebuildRoundTrip runs the full reload path: resolve → Out → Read →
// Rebuild → re-resolve, and asserts the canonical bytes are stable across it.
// This proves Rebuild is a faithful inverse without depending on parser node
// positions (which differ between a fresh parse and a rebuilt AST).
func TestResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		sql        string
		targetType uint32
	}{
		{"42", OidInt4},
		{"-1", OidInt4},
		{"5000000000", 0},
		{"40 + 2", OidInt4},
		{"1 + 2 * 3", OidInt4},
		{"upper('x')", OidText}, // FuncExpr: funcid→proname→re-resolve fixed point
	} {
		t.Run(tc.sql, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), tc.targetType)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			s1 := Out(n1)

			// Read back the canonical text into a fresh IR and confirm it is
			// byte-stable (S1 guarantee, re-checked here for the resolved shape).
			n2, err := Read(s1)
			if err != nil {
				t.Fatalf("Read(%q): %v", s1, err)
			}
			if got := Out(n2); got != s1 {
				t.Fatalf("Read round-trip changed bytes:\n got: %s\nwant: %s", got, s1)
			}

			// Rebuild to a goopg AST and re-resolve; canonical bytes must match.
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild: %v", err)
			}
			n3, err := ResolveExpr(ast, tc.targetType)
			if err != nil {
				t.Fatalf("re-ResolveExpr rebuilt AST: %v", err)
			}
			if got := Out(n3); got != s1 {
				t.Fatalf("Rebuild not faithful for %q:\n got: %s\nwant: %s", tc.sql, got, s1)
			}
		})
	}
}

// TestRebuildNegativeConstShape confirms a negative int Const rebuilds to the
// parser's canonical `-N` shape (UnaryOp over a positive IntegerConst).
func TestRebuildNegativeConstShape(t *testing.T) {
	n, err := ResolveExpr(mustParse(t, "-7"), OidInt4)
	if err != nil {
		t.Fatalf("ResolveExpr(-7): %v", err)
	}
	ast, err := Rebuild(n)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	u, ok := ast.(*parser.UnaryOp)
	if !ok || u.Op != parser.OpUnaryNeg {
		t.Fatalf("want UnaryOp{-}, got %T", ast)
	}
	lit, ok := u.Operand.(*parser.IntegerConst)
	if !ok || lit.Value != 7 {
		t.Fatalf("want IntegerConst{7}, got %+v", u.Operand)
	}
}

// TestSupportsExprRejectsOutOfSubset verifies the all-or-nothing shape check
// declines expressions with any unsupported node (a non-built-in cast, a
// column reference, or a string literal in a non-text context) so the writer
// keeps SQL text — while accepting the supported subset (literals including
// numeric, folded unary minus, OpExpr, and plain built-in FuncExpr).
func TestSupportsExprRejectsOutOfSubset(t *testing.T) {
	supported := []struct {
		sql        string
		targetType uint32
	}{
		{"1", OidInt4},
		{"-2147483648", OidInt4},
		{"'ok'", OidText},
		{"10 - 3", OidInt4},
		{"upper('x')", OidText}, // plain built-in FuncExpr forward-resolves
		{"1.5", OidNumeric},     // numeric datum (M0123-S4 sub-slice 3)
		{"-2.5", OidNumeric},    // folded negative numeric Const
	}
	for _, tc := range supported {
		if !SupportsExpr(mustParse(t, tc.sql), tc.targetType) {
			t.Errorf("SupportsExpr(%q, %d) = false, want true", tc.sql, tc.targetType)
		}
	}

	unsupported := []struct {
		sql        string
		targetType uint32
	}{
		{"'5'", OidInt4},   // string literal in a non-text context
		{"a + 1", OidInt4}, // column reference
	}
	for _, tc := range unsupported {
		if SupportsExpr(mustParse(t, tc.sql), tc.targetType) {
			t.Errorf("SupportsExpr(%q, %d) = true, want false", tc.sql, tc.targetType)
		}
	}
}

// TestResolveExprTypeStability guards the resolve() result-type propagation:
// an int4 op over int4 operands stays int4 (so an enclosing operator resolves).
func TestResolveExprTypeStability(t *testing.T) {
	n, ty, err := resolve(mustParse(t, "1 + 1"), OidInt4)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ty != OidInt4 {
		t.Fatalf("result type = %d, want int4 (%d)", ty, OidInt4)
	}
	if _, ok := n.(*OpExpr); !ok {
		t.Fatalf("want *OpExpr, got %T", n)
	}
	_ = reflect.TypeOf(n) // keep reflect import if shape checks are trimmed later
}

// TestResolveForColumn covers the writer-facing, exact-type-match gate used to
// decide whether a column DEFAULT is stored as a canonical pg_node_tree
// (pgnodes.ResolveForColumn true) or degrades to SQL text (false). The guard is
// a fidelity requirement: PG's build_column_default returns the default already
// coerced to the attribute type, so a canonical tree whose top node's type does
// not match the column type would misinsert on a standby.
func TestResolveForColumn(t *testing.T) {
	const oidNumeric, oidInt2 = 1700, 21
	cases := []struct {
		sql        string
		targetType uint32
		wantOK     bool
	}{
		// Exact-match: accepted.
		{"40 + 2", OidInt4, true},   // OpExpr int4 == int4 column
		{"-1", OidInt4, true},       // negative int4 Const
		{"upper('x')", OidText, true}, // FuncExpr text == text column
		{"5000000000", OidInt8, true}, // int8 literal == int8 column
		{"'hi'", OidText, true},     // text Const == text column
		// Integer literal in a numeric column: canonical via the implicit
		// int4_numeric/int8_numeric cast FuncExpr (M0123-S4 sub-slice 4a).
		{"0", oidNumeric, true},       // int4 -> numeric implicit cast
		{"5000000000", oidNumeric, true}, // int8 -> numeric implicit cast
		// Type mismatch: must fall back to SQL text (wantOK false) so a standby
		// never inserts a mistyped Datum.
		{"5", oidInt2, false}, // int4 Const for a smallint column (int2 cast unsupported)
		{"'x'", OidInt4, false},  // string literal on a non-text column
		// Outside the canonical subset at all: SQL text.
		{"now()", OidText, false}, // now() has no seeded overload here
	}
	for _, tc := range cases {
		node, ok := ResolveForColumn(mustParse(t, tc.sql), tc.targetType)
		if ok != tc.wantOK {
			t.Errorf("ResolveForColumn(%q, %d) ok = %v, want %v", tc.sql, tc.targetType, ok, tc.wantOK)
			continue
		}
		if ok && node == nil {
			t.Errorf("ResolveForColumn(%q, %d) returned nil node with ok=true", tc.sql, tc.targetType)
		}
		if !ok && node != nil {
			t.Errorf("ResolveForColumn(%q, %d) returned non-nil node with ok=false", tc.sql, tc.targetType)
		}
	}
}

// TestResolveForColumnRoundTripDiscriminator asserts the canonical output the
// writer stores always opens with '{' (the reload discriminator) while the SQL
// text fallback never does — the property rebuildAttrdefExpr keys on.
func TestResolveForColumnRoundTripDiscriminator(t *testing.T) {
	node, ok := ResolveForColumn(mustParse(t, "40 + 2"), OidInt4)
	if !ok {
		t.Fatal("expected canonical support for 40 + 2 on int4")
	}
	adbin := Out(node)
	if len(adbin) == 0 || adbin[0] != '{' {
		t.Fatalf("canonical adbin %q must open with '{'", adbin)
	}
	// Reload inverse: Read -> Rebuild reproduces an evaluable AST.
	back, err := Read(adbin)
	if err != nil {
		t.Fatalf("Read(%q): %v", adbin, err)
	}
	if _, err := Rebuild(back); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
}
