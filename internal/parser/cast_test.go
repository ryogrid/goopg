package parser

import "testing"

// TestParseCastSimpleSchemaQualified pins the pgbench-i probe shape
// `SELECT relkind FROM pg_catalog.pg_class WHERE oid =
// $1::pg_catalog.regclass`. The cast is the only piece this loop
// adds; the rest of the SELECT is already supported.
func TestParseCastSimpleSchemaQualified(t *testing.T) {
	stmts, err := Parse(`SELECT 1 WHERE oid = $1::pg_catalog.regclass`)
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if s.Where == nil {
		t.Fatal("missing WHERE")
	}
	bin, ok := s.Where.(*BinaryOp)
	if !ok {
		t.Fatalf("WHERE=%T want *BinaryOp", s.Where)
	}
	cast, ok := bin.Right.(*CastExpr)
	if !ok {
		t.Fatalf("right=%T want *CastExpr", bin.Right)
	}
	if _, ok := cast.Operand.(*ParamRef); !ok {
		t.Errorf("cast.Operand=%T want *ParamRef", cast.Operand)
	}
	if cast.Type.Schema != "pg_catalog" || cast.Type.Name != "regclass" {
		t.Errorf("cast.Type=%+v want pg_catalog.regclass", cast.Type)
	}
}

// TestParseCastTypmod covers the `(N)` and `(N,M)` typmod tail.
func TestParseCastTypmod(t *testing.T) {
	stmts, err := Parse(`SELECT 'x'::varchar(10)`)
	if err != nil {
		t.Fatal(err)
	}
	cast, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*CastExpr)
	if !ok {
		t.Fatalf("target=%T", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if cast.Type.Name != "varchar" {
		t.Errorf("type=%q", cast.Type.Name)
	}
	if len(cast.Typmods) != 1 || cast.Typmods[0] != 10 {
		t.Errorf("typmods=%v", cast.Typmods)
	}
}

// TestParseCastChained: `expr::int8::text` should produce a
// CastExpr whose Operand is itself a CastExpr.
func TestParseCastChained(t *testing.T) {
	stmts, err := Parse(`SELECT 1::int8::text`)
	if err != nil {
		t.Fatal(err)
	}
	outer, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*CastExpr)
	if !ok {
		t.Fatalf("got %T", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if outer.Type.Name != "text" {
		t.Errorf("outer type=%q want text", outer.Type.Name)
	}
	inner, ok := outer.Operand.(*CastExpr)
	if !ok {
		t.Fatalf("inner=%T want *CastExpr", outer.Operand)
	}
	if inner.Type.Name != "int8" {
		t.Errorf("inner type=%q want int8", inner.Type.Name)
	}
}
