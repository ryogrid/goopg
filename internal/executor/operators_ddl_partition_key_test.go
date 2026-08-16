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

// TestAlterTablePartitionKeyGuardAlterType pins the M0134-0002 ALTER-TYPE
// partition-key guard in execAlterColumnType — the sibling of the DROP COLUMN
// guard in execAlterDropColumn. PG refuses to alter the type of a column that
// is part of the partition key (a bare key column, or a column referenced
// inside an expression key) BEFORE any rewrite — even when the target type is
// unchanged — with byte-exact 42P16 "cannot alter column %q because it is part
// of the partition key of relation %q" and an errposition at the column name
// (ATExecAlterColumnType, parser_errposition(pstate, def->location);
// postgres/src/backend/commands/tablecmds.c:14443,14450). The errposition is
// covered by the alter_table regress run (expected alter_table.out:3977-3979);
// here we assert Code+Message only. Tables are built for real through the DDL
// executor so PartitionKey/PartitionKeyExprs come from CREATE TABLE, never
// faked in the fixture. Non-key columns still alter fine.
func TestAlterTablePartitionKeyGuardAlterType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE alt_pk (a int, b int) PARTITION BY RANGE (a)",
		"CREATE TABLE alt_pk_expr (a int, b int) PARTITION BY RANGE (plusone(a))",
		"CREATE TABLE alt_pk_plain (a int, b int) PARTITION BY RANGE (a)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	tests := []struct {
		name    string
		sql     string
		wantErr bool
		code    string
		message string
	}{
		{
			name:    "key column refused",
			sql:     "ALTER TABLE alt_pk ALTER COLUMN a TYPE bigint",
			wantErr: true,
			code:    "42P16",
			message: `cannot alter column "a" because it is part of the partition key of relation "alt_pk"`,
		},
		{
			// The guard sits before the no-op type check, so PG refuses even
			// when the type name is unchanged.
			name:    "key column refused even when type unchanged",
			sql:     "ALTER TABLE alt_pk ALTER COLUMN a TYPE int",
			wantErr: true,
			code:    "42P16",
			message: `cannot alter column "a" because it is part of the partition key of relation "alt_pk"`,
		},
		{
			// PARTITION BY RANGE (plusone(a)) stores a FuncCall in
			// PartitionKeyExprs; the structural walker must catch column a.
			name:    "expression key column refused",
			sql:     "ALTER TABLE alt_pk_expr ALTER COLUMN a TYPE bigint",
			wantErr: true,
			code:    "42P16",
			message: `cannot alter column "a" because it is part of the partition key of relation "alt_pk_expr"`,
		},
		{
			// Guard must not false-positive on a column outside the key.
			name: "non-key column alter ok",
			sql:  "ALTER TABLE alt_pk_plain ALTER COLUMN b TYPE bigint",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.sql)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("%s: %v", tc.sql, err)
				}
				return
			}
			wantExecError(t, err, tc.code, tc.message)
		})
	}
}
