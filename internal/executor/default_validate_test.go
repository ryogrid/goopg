package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestDefaultExprRejectsNestedColumnRefs is a regression test for the
// validateDefaultExpr recursion gap. Before this slice the validator only
// recursed into FuncCall args, BinaryOp, UnaryOp, and CastExpr; a column
// reference, aggregate, or subquery hidden inside a compound expression
// (ARRAY[...], CASE, a row constructor, IN, IS [NOT] NULL/DISTINCT, COLLATE,
// EXISTS, ARRAY(subquery), a subscript, or EXTRACT) slipped through and the
// CREATE TABLE was wrongly accepted — diverging from PostgreSQL, which rejects
// all of these with 42P17 / 42803 / 0A000.
//
// Each case nests an offending leaf inside a different compound node so the new
// recursion arms are exercised individually.
func TestDefaultExprRejectsNestedColumnRefs(t *testing.T) {
	cases := []struct {
		name string
		ddl  string
		want string // substring the error must contain
	}{
		{
			name: "column ref in ARRAY constructor",
			ddl:  "CREATE TABLE bad1 (a integer, b integer[] DEFAULT ARRAY[a])",
			want: "column reference",
		},
		{
			name: "column ref in CASE result",
			ddl:  "CREATE TABLE bad2 (a integer, b integer DEFAULT CASE WHEN true THEN a ELSE 0 END)",
			want: "column reference",
		},
		{
			name: "column ref in CASE condition",
			ddl:  "CREATE TABLE bad3 (a integer, b integer DEFAULT CASE WHEN a > 0 THEN 1 ELSE 0 END)",
			want: "column reference",
		},
		{
			name: "column ref in row constructor",
			ddl:  "CREATE TABLE bad4 (a integer, b integer DEFAULT (a, 1))",
			want: "column reference",
		},
		{
			name: "column ref in IN list",
			ddl:  "CREATE TABLE bad5 (a integer, b boolean DEFAULT (1 IN (a, 2)))",
			want: "column reference",
		},
		{
			name: "column ref in IS NULL",
			ddl:  "CREATE TABLE bad6 (a integer, b boolean DEFAULT (a IS NULL))",
			want: "column reference",
		},
		{
			name: "column ref in IS DISTINCT FROM",
			ddl:  "CREATE TABLE bad7 (a integer, b boolean DEFAULT (a IS DISTINCT FROM 1))",
			want: "column reference",
		},
		{
			name: "column ref in COLLATE",
			ddl:  `CREATE TABLE bad8 (a text, b text DEFAULT (a COLLATE "C"))`,
			want: "column reference",
		},
		{
			name: "aggregate in ARRAY constructor",
			ddl:  "CREATE TABLE bad9 (a integer, b integer[] DEFAULT ARRAY[sum(1)])",
			want: "aggregate",
		},
		{
			name: "subquery in IN",
			ddl:  "CREATE TABLE bad10 (a integer, b boolean DEFAULT (1 IN (SELECT 1)))",
			want: "subquery",
		},
		{
			name: "EXISTS subquery",
			ddl:  "CREATE TABLE bad11 (a integer, b boolean DEFAULT EXISTS(SELECT 1))",
			want: "subquery",
		},
		{
			name: "subquery nested in CASE",
			ddl:  "CREATE TABLE bad12 (a integer, b integer DEFAULT CASE WHEN true THEN (SELECT 1) ELSE 0 END)",
			want: "subquery",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()

			err := runDDL(t, ctx, tc.ddl)
			if err == nil {
				t.Fatalf("expected DEFAULT validation error, got nil for %q", tc.ddl)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q for %q", err.Error(), tc.want, tc.ddl)
			}
		})
	}
}

// TestDefaultExprAcceptsConstantCompounds guards against over-rejection: the
// same compound expression shapes are valid DEFAULTs when every leaf is a
// constant (no column ref / aggregate / subquery). These are exactly the forms
// pg_dump's defcol fixture round-trips (slice 181), so the validator must let
// them through.
func TestDefaultExprAcceptsConstantCompounds(t *testing.T) {
	good := []string{
		"CREATE TABLE ok1 (a integer[] DEFAULT ARRAY[1, 2, 3])",
		"CREATE TABLE ok2 (a integer DEFAULT CASE WHEN true THEN 1 ELSE 0 END)",
		"CREATE TABLE ok3 (a boolean DEFAULT (1 IS NOT NULL))",
		"CREATE TABLE ok4 (a boolean DEFAULT (1 IS DISTINCT FROM 2))",
		"CREATE TABLE ok5 (a boolean DEFAULT (1 IN (1, 2, 3)))",
	}
	for _, ddl := range good {
		t.Run(ddl, func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()

			if err := runDDL(t, ctx, ddl); err != nil {
				t.Fatalf("valid compound DEFAULT rejected: %v (%q)", err, ddl)
			}
		})
	}
}

// TestDefaultExprToSQLBinaryParen guards the executor-side deparse renderer's
// BinaryOp full parenthesization (DU-002 slice 298). defaultExprToSQL feeds the
// non-pretty pg_get_expr contexts — index predicates (CREATE INDEX ... WHERE),
// expression index columns, partition-key expressions, and function-argument
// defaults. Real pg_dump 18.3 fully parenthesizes every binary OpExpr in those
// contexts, so `qty > 0` dumps as `(qty > 0)` and `(qty + id) * mgr_id` dumps as
// `((qty + id) * mgr_id)`. Without the parens, nested arithmetic round-trips with
// corrupted precedence on restore. This is the executor twin of the catalog
// renderer covered by TestFormatExprForAttrdefExpr; the two MUST stay in sync.
func TestDefaultExprToSQLBinaryParen(t *testing.T) {
	cases := []struct {
		name string
		expr parser.Expr
		want string
	}{
		{
			"simple comparison",
			&parser.BinaryOp{Op: parser.OpGt, Left: &parser.ColumnRef{Column: "qty"}, Right: &parser.IntegerConst{Value: 0}},
			"(qty > 0)",
		},
		{
			"binary add",
			&parser.BinaryOp{Op: parser.OpAdd, Left: &parser.IntegerConst{Value: 1}, Right: &parser.IntegerConst{Value: 1}},
			"(1 + 1)",
		},
		{
			// `(qty + id) * mgr_id > 0` parses to Gt(Mul(Add(qty,id),mgr_id),0); the
			// recursion parenthesizes each operand → byte-identical to real pg_dump 18.3.
			"nested arithmetic predicate",
			&parser.BinaryOp{Op: parser.OpGt,
				Left: &parser.BinaryOp{Op: parser.OpMul,
					Left:  &parser.BinaryOp{Op: parser.OpAdd, Left: &parser.ColumnRef{Column: "qty"}, Right: &parser.ColumnRef{Column: "id"}},
					Right: &parser.ColumnRef{Column: "mgr_id"}},
				Right: &parser.IntegerConst{Value: 0}},
			"(((qty + id) * mgr_id) > 0)",
		},
		{
			// Unary minus is tagged OpUnaryNeg (NOT OpSub); a bare numeric operand
			// renders `-N` (PG folds it to `'-N'::type`, deferred). DU-002 slice 302.
			"unary minus literal",
			&parser.UnaryOp{Op: parser.OpUnaryNeg, Operand: &parser.IntegerConst{Value: 1}},
			"-1",
		},
		{
			// Unary minus on a COMPOUND operand deparses `(- (operand))`,
			// byte-identical to real pg_dump 18.3. Twin of the catalog renderer's
			// "unary minus compound" case; the two MUST stay in sync. DU-002 slice 302.
			"unary minus compound",
			&parser.UnaryOp{Op: parser.OpUnaryNeg, Operand: &parser.BinaryOp{Op: parser.OpAdd, Left: &parser.IntegerConst{Value: 1}, Right: &parser.IntegerConst{Value: 2}}},
			"(- (1 + 2))",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultExprToSQL(tc.expr); got != tc.want {
				t.Errorf("defaultExprToSQL = %q, want %q", got, tc.want)
			}
		})
	}
}
