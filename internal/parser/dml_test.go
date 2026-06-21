package parser

import "testing"

// TestParseInsertPgbench: the canonical INSERT pgbench's default
// script issues against pgbench_history. Pins column-list parsing,
// multiple bind parameters, a function call (CURRENT_TIMESTAMP) inside
// VALUES, and the choice of FuncCall vs ColumnRef for `f` followed by
// non-`(`.
func TestParseInsertPgbench(t *testing.T) {
	stmts, err := Parse("INSERT INTO pgbench_history (tid, bid, aid, delta, mtime) VALUES ($1, $2, $3, $4, current_timestamp())")
	if err != nil {
		t.Fatal(err)
	}
	ins, ok := stmts[0].(*InsertStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if ins.Target.Name != "pgbench_history" {
		t.Errorf("target=%+v", ins.Target)
	}
	if len(ins.Columns) != 5 || ins.Columns[0] != "tid" || ins.Columns[4] != "mtime" {
		t.Errorf("columns=%+v", ins.Columns)
	}
	if len(ins.Rows) != 1 || len(ins.Rows[0]) != 5 {
		t.Fatalf("rows shape=%v", ins.Rows)
	}
	if pr, ok := ins.Rows[0][0].(*ParamRef); !ok || pr.Number != 1 {
		t.Errorf("row[0][0]=%+v", ins.Rows[0][0])
	}
	if fc, ok := ins.Rows[0][4].(*FuncCall); !ok || fc.Name.Name != "current_timestamp" {
		t.Errorf("row[0][4]=%+v", ins.Rows[0][4])
	}
}

// TestParseInsertNoColumnsMultipleRows: column list omitted, multiple
// VALUES rows.
func TestParseInsertNoColumnsMultipleRows(t *testing.T) {
	stmts, err := Parse("INSERT INTO t VALUES (1, 'a'), (2, 'b'), (3, 'c')")
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if ins.Columns != nil {
		t.Errorf("Columns=%+v want nil", ins.Columns)
	}
	if len(ins.Rows) != 3 {
		t.Fatalf("rows=%d want 3", len(ins.Rows))
	}
}

// TestParseInsertParenthesisedSelectSource: `INSERT INTO t (SELECT …)` — a
// fully parenthesised query source. The leading '(' is NOT a column list
// (PostgreSQL's insert_rest allows a parenthesised SelectStmt as the source).
// Pins the disambiguation added for the partial-index isolation spec, whose
// setup uses `insert into test_t (select generate_series(0, 10000), 'a', 2)`.
func TestParseInsertParenthesisedSelectSource(t *testing.T) {
	stmts, err := Parse("INSERT INTO test_t (select generate_series(0, 10000), 'a', 2)")
	if err != nil {
		t.Fatal(err)
	}
	ins, ok := stmts[0].(*InsertStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if ins.Columns != nil {
		t.Errorf("Columns=%+v want nil (the '(' opens a query, not a column list)", ins.Columns)
	}
	if ins.Rows != nil {
		t.Errorf("Rows=%+v want nil", ins.Rows)
	}
	if ins.Select == nil {
		t.Fatalf("Select=nil want a SelectStmt source")
	}
	if !ins.Select.Parenthesized {
		t.Errorf("Select.Parenthesized=false want true")
	}
	if len(ins.Select.Targets) != 3 {
		t.Errorf("Select.Targets=%d want 3", len(ins.Select.Targets))
	}
}

// TestParseInsertColumnListThenParenthesisedSelect: a column list followed by
// a parenthesised query source, e.g. `INSERT INTO t (a, b) (SELECT …)`. Both
// the explicit column list and the parenthesised SELECT must be recognised.
func TestParseInsertColumnListThenParenthesisedSelect(t *testing.T) {
	stmts, err := Parse("INSERT INTO t (a, b) (SELECT x, y FROM s)")
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if len(ins.Columns) != 2 || ins.Columns[0] != "a" || ins.Columns[1] != "b" {
		t.Errorf("Columns=%+v want [a b]", ins.Columns)
	}
	if ins.Select == nil || !ins.Select.Parenthesized {
		t.Fatalf("Select=%+v want a parenthesised SelectStmt", ins.Select)
	}
}

// TestParseInsertPlainSelectSourceUnchanged: the non-parenthesised
// `INSERT INTO t SELECT …` form must keep working after the disambiguation.
func TestParseInsertPlainSelectSourceUnchanged(t *testing.T) {
	stmts, err := Parse("INSERT INTO t SELECT a, b FROM s")
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if ins.Columns != nil {
		t.Errorf("Columns=%+v want nil", ins.Columns)
	}
	if ins.Select == nil {
		t.Fatalf("Select=nil want a SelectStmt source")
	}
	if ins.Select.Parenthesized {
		t.Errorf("Select.Parenthesized=true want false (no enclosing parens)")
	}
}

// TestParseInsertReturning verifies RETURNING reuses the SELECT target
// list shape (so RETURNING * works too).
func TestParseInsertReturning(t *testing.T) {
	stmts, err := Parse("INSERT INTO t (a) VALUES (1) RETURNING *")
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if len(ins.Returning) != 1 {
		t.Fatalf("Returning=%+v", ins.Returning)
	}
	if _, ok := ins.Returning[0].Expr.(*StarExpr); !ok {
		t.Errorf("Returning[0]=%+v", ins.Returning[0].Expr)
	}
}

// TestParseUpdatePgbench: pgbench's UPDATE with a self-reference on
// the right-hand side and a parameterised WHERE.
func TestParseUpdatePgbench(t *testing.T) {
	stmts, err := Parse("UPDATE pgbench_accounts SET abalance = abalance + $1 WHERE aid = $2")
	if err != nil {
		t.Fatal(err)
	}
	upd, ok := stmts[0].(*UpdateStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if upd.Target.Name != "pgbench_accounts" {
		t.Errorf("target=%+v", upd.Target)
	}
	if len(upd.Set) != 1 || upd.Set[0].Column != "abalance" {
		t.Fatalf("set=%+v", upd.Set)
	}
	bo, ok := upd.Set[0].Expr.(*BinaryOp)
	if !ok || bo.Op != OpAdd {
		t.Fatalf("set[0].Expr=%+v", upd.Set[0].Expr)
	}
	if upd.Where == nil {
		t.Fatal("missing WHERE")
	}
}

// TestParseUpdateMultiAssign: comma-separated SET pairs.
func TestParseUpdateMultiAssign(t *testing.T) {
	stmts, err := Parse("UPDATE t SET a = 1, b = 2, c = a + b WHERE id = 7")
	if err != nil {
		t.Fatal(err)
	}
	upd := stmts[0].(*UpdateStmt)
	if len(upd.Set) != 3 {
		t.Fatalf("set=%+v", upd.Set)
	}
	if upd.Set[2].Column != "c" {
		t.Errorf("set[2]=%+v", upd.Set[2])
	}
}

// TestParseDelete: simple DELETE FROM with WHERE and RETURNING.
func TestParseDelete(t *testing.T) {
	stmts, err := Parse("DELETE FROM t WHERE id = $1 RETURNING id")
	if err != nil {
		t.Fatal(err)
	}
	del, ok := stmts[0].(*DeleteStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if del.Target.Name != "t" {
		t.Errorf("target=%+v", del.Target)
	}
	if del.Where == nil {
		t.Fatal("missing WHERE")
	}
	if len(del.Returning) != 1 {
		t.Fatalf("Returning=%+v", del.Returning)
	}
}

// TestParseDeleteUsing: DELETE FROM … USING …, … WHERE … RETURNING (M0097-0076).
func TestParseDeleteUsing(t *testing.T) {
	stmts, err := Parse("DELETE FROM t USING u, v w WHERE t.a = u.a AND t.b = w.b RETURNING t.a, u.x")
	if err != nil {
		t.Fatal(err)
	}
	del, ok := stmts[0].(*DeleteStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if del.Target.Name != "t" {
		t.Errorf("target=%+v", del.Target)
	}
	if len(del.Using) != 2 {
		t.Fatalf("Using=%+v", del.Using)
	}
	if del.Using[0].Name != "u" || del.Using[0].Alias != "" {
		t.Errorf("Using[0]=%+v", del.Using[0])
	}
	if del.Using[1].Name != "v" || del.Using[1].Alias != "w" {
		t.Errorf("Using[1]=%+v", del.Using[1])
	}
	if del.Where == nil {
		t.Fatal("missing WHERE")
	}
	if len(del.Returning) != 2 {
		t.Fatalf("Returning=%+v", del.Returning)
	}
}

// TestParseDMLSyntaxErrors pins SyntaxError type for the canonical
// missing-piece cases.
func TestParseDMLSyntaxErrors(t *testing.T) {
	cases := []string{
		"INSERT t VALUES (1)",    // missing INTO
		"INSERT INTO t",          // missing VALUES
		"INSERT INTO t VALUES",   // missing tuple
		"INSERT INTO t VALUES 1", // tuple not parenthesised
		"UPDATE",                 // missing target
		"UPDATE t",               // missing SET
		"UPDATE t SET",           // missing assignment
		"UPDATE t SET a",         // missing '='
		"DELETE t",               // missing FROM
		"DELETE FROM",            // missing target
	}
	for _, in := range cases {
		_, err := Parse(in)
		if err == nil {
			t.Errorf("Parse(%q) expected error", in)
			continue
		}
		if _, ok := err.(*SyntaxError); !ok {
			t.Errorf("Parse(%q) err type=%T", in, err)
		}
	}
}

// TestParseInsertValuesAcceptsDefaultKeyword: rung 15 — VALUES rows
// accept the bare DEFAULT keyword. The cell becomes a *DefaultMarker
// AST node; planInsert substitutes the column's catalog DefaultExpr
// (or NULL) before resolveExpr runs.
func TestParseInsertValuesAcceptsDefaultKeyword(t *testing.T) {
	stmts, err := Parse("INSERT INTO t (a, b) VALUES (1, DEFAULT)")
	if err != nil {
		t.Fatal(err)
	}
	ins, ok := stmts[0].(*InsertStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(ins.Rows) != 1 || len(ins.Rows[0]) != 2 {
		t.Fatalf("rows shape=%v", ins.Rows)
	}
	if _, ok := ins.Rows[0][0].(*IntegerConst); !ok {
		t.Errorf("row[0][0]=%T want *IntegerConst", ins.Rows[0][0])
	}
	if _, ok := ins.Rows[0][1].(*DefaultMarker); !ok {
		t.Errorf("row[0][1]=%T want *DefaultMarker", ins.Rows[0][1])
	}
}

// TestParseInsertValuesDefaultInMultipleRows: rung 15 — DEFAULT works
// across multiple rows and at any cell position.
func TestParseInsertValuesDefaultInMultipleRows(t *testing.T) {
	stmts, err := Parse("INSERT INTO t (a, b, c) VALUES (DEFAULT, 2, 'x'), (1, DEFAULT, DEFAULT)")
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if len(ins.Rows) != 2 {
		t.Fatalf("rows=%d want 2", len(ins.Rows))
	}
	if _, ok := ins.Rows[0][0].(*DefaultMarker); !ok {
		t.Errorf("row[0][0]=%T", ins.Rows[0][0])
	}
	if _, ok := ins.Rows[1][1].(*DefaultMarker); !ok {
		t.Errorf("row[1][1]=%T", ins.Rows[1][1])
	}
	if _, ok := ins.Rows[1][2].(*DefaultMarker); !ok {
		t.Errorf("row[1][2]=%T", ins.Rows[1][2])
	}
}

// TestParseInsertValuesRejectsBareDefaultInExpression: rung 15 — DEFAULT
// is only accepted as a complete cell, not as a sub-expression. Matches
// upstream PG behaviour.
func TestParseInsertValuesRejectsBareDefaultInExpression(t *testing.T) {
	_, err := Parse("INSERT INTO t (a) VALUES (DEFAULT + 1)")
	if err == nil {
		t.Fatal("expected syntax error, got nil")
	}
}

// TestParseUpdateSetDefaultKeyword: rung 16 — the bare DEFAULT keyword
// is accepted on the RHS of an UPDATE SET assignment and parsed as a
// *DefaultMarker sentinel. Plan() substitutes the marker with the
// column's catalog DefaultExpr (or NULL) before the analyzer runs.
func TestParseUpdateSetDefaultKeyword(t *testing.T) {
	stmts, err := Parse("UPDATE t SET v = DEFAULT WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	upd, ok := stmts[0].(*UpdateStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(upd.Set) != 1 {
		t.Fatalf("set len=%d want 1", len(upd.Set))
	}
	if upd.Set[0].Column != "v" {
		t.Errorf("set[0].Column=%q want v", upd.Set[0].Column)
	}
	if _, ok := upd.Set[0].Expr.(*DefaultMarker); !ok {
		t.Errorf("set[0].Expr=%T want *DefaultMarker", upd.Set[0].Expr)
	}
}

// TestParseUpdateSetDefaultMultiAssign: rung 16 — DEFAULT on a subset
// of comma-separated SET pairs, with plain expressions in the others.
func TestParseUpdateSetDefaultMultiAssign(t *testing.T) {
	stmts, err := Parse("UPDATE t SET v = DEFAULT, n = 42, w = DEFAULT WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	upd := stmts[0].(*UpdateStmt)
	if len(upd.Set) != 3 {
		t.Fatalf("set len=%d want 3", len(upd.Set))
	}
	if _, ok := upd.Set[0].Expr.(*DefaultMarker); !ok {
		t.Errorf("set[0].Expr=%T want *DefaultMarker", upd.Set[0].Expr)
	}
	if _, ok := upd.Set[1].Expr.(*IntegerConst); !ok {
		t.Errorf("set[1].Expr=%T want *IntegerConst", upd.Set[1].Expr)
	}
	if _, ok := upd.Set[2].Expr.(*DefaultMarker); !ok {
		t.Errorf("set[2].Expr=%T want *DefaultMarker", upd.Set[2].Expr)
	}
}

// TestParseUpdateSetRejectsBareDefaultInExpression: rung 16 — DEFAULT
// is accepted only as a complete RHS, not as a sub-expression. Matches
// upstream PG behaviour and rung 15's INSERT VALUES symmetry.
func TestParseUpdateSetRejectsBareDefaultInExpression(t *testing.T) {
	_, err := Parse("UPDATE t SET v = DEFAULT + 1 WHERE id = 1")
	if err == nil {
		t.Fatal("expected syntax error, got nil")
	}
}

// TestParseInsertDefaultValues: rung 17 — `INSERT INTO t DEFAULT VALUES`
// parses as an InsertStmt with DefaultValues=true, empty Rows, and no
// SELECT. The planner expands it into a row of DefaultMarkers sized to
// the target's insertable columns.
func TestParseInsertDefaultValues(t *testing.T) {
	stmts, err := Parse("INSERT INTO t DEFAULT VALUES")
	if err != nil {
		t.Fatal(err)
	}
	ins, ok := stmts[0].(*InsertStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if !ins.DefaultValues {
		t.Errorf("DefaultValues=false want true")
	}
	if len(ins.Rows) != 0 {
		t.Errorf("Rows len=%d want 0", len(ins.Rows))
	}
	if ins.Select != nil {
		t.Errorf("Select set unexpectedly")
	}
	if len(ins.Columns) != 0 {
		t.Errorf("Columns set unexpectedly: %v", ins.Columns)
	}
}

// TestParseInsertDefaultValuesWithReturning: rung 17 — RETURNING after
// DEFAULT VALUES is parsed normally.
func TestParseInsertDefaultValuesWithReturning(t *testing.T) {
	stmts, err := Parse("INSERT INTO t DEFAULT VALUES RETURNING id")
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if !ins.DefaultValues {
		t.Errorf("DefaultValues=false want true")
	}
	if len(ins.Returning) != 1 {
		t.Errorf("Returning len=%d want 1", len(ins.Returning))
	}
}

// TestParseInsertDefaultValuesRejectsExtraValues: rung 17 — DEFAULT
// must be followed by VALUES; any other token raises a syntax error.
func TestParseInsertDefaultValuesRejectsExtraValues(t *testing.T) {
	_, err := Parse("INSERT INTO t DEFAULT (1)")
	if err == nil {
		t.Fatal("expected syntax error, got nil")
	}
}
