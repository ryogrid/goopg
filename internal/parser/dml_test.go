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
	if !ok || bo.Op != "+" {
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
