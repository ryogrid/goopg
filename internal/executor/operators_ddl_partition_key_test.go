package executor

// operators_ddl_partition_key_test.go — M0134-0002 partition-key DROP COLUMN
// guard: unit test for partitionKeyExprUsesColumn, the structural expression
// walker that replaced the nondeterministic
// `strings.Contains(strings.ToLower(fmt.Sprintf("%v", expr)), colLower)`
// heuristic in execAlterDropColumn (the old test false-positived when the Go
// `%v` rendering of a parser node embedded a pointer hex digit matching the
// target column name). Mirrors funcExprContainsName's recursion shape, extended
// to the CaseExpr/ExtractExpr/IsNullExpr arms. PG oracle: has_partition_attrs
// (postgres/src/backend/catalog/partition.c:255), call site tablecmds.c:9358.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestPartitionKeyExprUsesColumn(t *testing.T) {
	cases := []struct {
		name string
		expr parser.Expr
		col  string
		want bool
	}{
		{name: "bare column match", expr: &parser.ColumnRef{Column: "b"}, col: "b", want: true},
		{name: "bare column no-match", expr: &parser.ColumnRef{Column: "b"}, col: "x", want: false},
		// Qualification is ignored — the plain-key loop and evalPartitionKeyExpr
		// both match by column name only.
		{name: "table-qualified column", expr: &parser.ColumnRef{Table: "t", Column: "b"}, col: "b", want: true},
		// The exact nondeterministic false-positive the old heuristic hit: a
		// func arg "a" must not match a drop of column "b" just because "b"
		// appears somewhere in the `%v` rendering.
		{name: "func expr no false positive", expr: &parser.FuncCall{Name: parser.ObjectName{Name: "plusone"}, Args: []parser.Expr{&parser.ColumnRef{Column: "a"}}}, col: "b", want: false},
		{name: "func expr match arg", expr: &parser.FuncCall{Name: parser.ObjectName{Name: "plusone"}, Args: []parser.Expr{&parser.ColumnRef{Column: "a"}}}, col: "a", want: true},
		{name: "func expr match filter", expr: &parser.FuncCall{Name: parser.ObjectName{Name: "count"}, Args: []parser.Expr{&parser.ColumnRef{Column: "x"}}, Filter: &parser.ColumnRef{Column: "b"}}, col: "b", want: true},
		{name: "nested binary op", expr: &parser.BinaryOp{Left: &parser.ColumnRef{Column: "x"}, Right: &parser.ColumnRef{Column: "b"}}, col: "b", want: true},
		{name: "unary op", expr: &parser.UnaryOp{Operand: &parser.ColumnRef{Column: "b"}}, col: "b", want: true},
		{name: "case when", expr: &parser.CaseExpr{Whens: []parser.CaseWhen{{When: &parser.ColumnRef{Column: "b"}}}}, col: "b", want: true},
		{name: "case then", expr: &parser.CaseExpr{Whens: []parser.CaseWhen{{Then: &parser.ColumnRef{Column: "b"}}}}, col: "b", want: true},
		{name: "case else", expr: &parser.CaseExpr{Else: &parser.ColumnRef{Column: "b"}}, col: "b", want: true},
		{name: "cast", expr: &parser.CastExpr{Operand: &parser.ColumnRef{Column: "b"}}, col: "b", want: true},
		{name: "collate", expr: &parser.CollateExpr{Operand: &parser.ColumnRef{Column: "b"}}, col: "b", want: true},
		{name: "extract", expr: &parser.ExtractExpr{Source: &parser.ColumnRef{Column: "b"}}, col: "b", want: true},
		{name: "is null", expr: &parser.IsNullExpr{Operand: &parser.ColumnRef{Column: "b"}}, col: "b", want: true},
		{name: "nil expr", expr: nil, col: "b", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := partitionKeyExprUsesColumn(tc.expr, tc.col); got != tc.want {
				t.Errorf("partitionKeyExprUsesColumn(%v, %q) = %v, want %v", tc.expr, tc.col, got, tc.want)
			}
		})
	}
}
