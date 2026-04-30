package plpgsql

import (
	"errors"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestParseReturnConstant pins the smallest acceptable PL/pgSQL
// body: `BEGIN RETURN <int>; END`. Verifies the Block + ReturnStmt
// AST shape and that the inner expression parsed via
// parser.ParseExpr produces an IntLiteral the future interpreter
// can fold trivially.
func TestParseReturnConstant(t *testing.T) {
	blk, err := Parse("BEGIN RETURN 42; END")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Statements) != 1 {
		t.Fatalf("Statements len = %d, want 1", len(blk.Statements))
	}
	ret, ok := blk.Statements[0].(*ReturnStmt)
	if !ok {
		t.Fatalf("Statements[0] = %T, want *ReturnStmt", blk.Statements[0])
	}
	lit, ok := ret.Expr.(*parser.IntegerConst)
	if !ok {
		t.Fatalf("Expr = %T, want *parser.IntegerConst", ret.Expr)
	}
	if lit.Value != 42 {
		t.Errorf("Value = %d, want 42", lit.Value)
	}
}

// TestParseReturnExpression pins that the RETURN expression is
// parsed by the SQL expression parser, so a non-trivial expression
// like `x + 1` produces the expected BinaryExpr / ColumnRef shape.
func TestParseReturnExpression(t *testing.T) {
	blk, err := Parse("BEGIN RETURN x + 1; END")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ret := blk.Statements[0].(*ReturnStmt)
	if _, ok := ret.Expr.(*parser.BinaryOp); !ok {
		t.Fatalf("Expr = %T, want *parser.BinaryOp", ret.Expr)
	}
}

// TestParseEmptyBlock pins that `BEGIN END` is accepted — an
// empty body is unusual but not malformed and matches upstream's
// surface.
func TestParseEmptyBlock(t *testing.T) {
	blk, err := Parse("BEGIN END")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Statements) != 0 {
		t.Errorf("Statements len = %d, want 0", len(blk.Statements))
	}
}

// TestParseBlockTrailingSemicolon pins that `BEGIN ... END;` is
// equivalent to `BEGIN ... END`. PL/pgSQL function bodies almost
// always include the trailing semicolon.
func TestParseBlockTrailingSemicolon(t *testing.T) {
	blk, err := Parse("BEGIN RETURN 1; END;")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Statements) != 1 {
		t.Errorf("Statements len = %d, want 1", len(blk.Statements))
	}
}

// TestParseMultipleReturns pins that multiple statements can sit
// inside a block. Stage A 4a only knows RETURN, but the framework
// must support the shape so future slices can drop in DECLARE /
// IF / etc. without reworking parseTopBlock.
func TestParseMultipleStatements(t *testing.T) {
	blk, err := Parse("BEGIN RETURN 1; RETURN 2; END")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Statements) != 2 {
		t.Errorf("Statements len = %d, want 2", len(blk.Statements))
	}
}

// TestParseRequiresBegin: missing leading BEGIN surfaces a
// specific diagnostic.
func TestParseRequiresBegin(t *testing.T) {
	_, err := Parse("RETURN 1; END")
	if err == nil {
		t.Fatal("expected SyntaxError for missing BEGIN")
	}
	se, ok := err.(*SyntaxError)
	if !ok {
		t.Fatalf("err = %T, want *SyntaxError", err)
	}
	if !strings.Contains(se.Message, "BEGIN") {
		t.Errorf("err = %v, want a 'BEGIN' diagnostic", err)
	}
}

// TestParseRequiresEnd: missing END before EOF is a specific
// diagnostic.
func TestParseRequiresEnd(t *testing.T) {
	_, err := Parse("BEGIN RETURN 1;")
	if err == nil {
		t.Fatal("expected SyntaxError for missing END")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "END") {
		t.Errorf("err = %v, want an 'END' diagnostic", err)
	}
}

// TestParseRejectsUnsupportedStatement guards Stage A 4b's scope:
// PERFORM is an identifier-led statement that doesn't have `:=`
// after the leading identifier, so the assignment parser surfaces
// a Stage-A-4b diagnostic naming RETURN and assignment as the
// only supported shapes.
func TestParseRejectsUnsupportedStatement(t *testing.T) {
	_, err := Parse("BEGIN PERFORM foo(); END")
	if err == nil {
		t.Fatal("expected SyntaxError for PERFORM")
	}
	if !strings.Contains(err.Error(), "Stage A 4b") {
		t.Errorf("err = %v, want a Stage-A-4b diagnostic", err)
	}
}

// TestParseRejectsBareReturn: Stage A requires a return value;
// `RETURN;` (which is upstream-legal for void / OUT-only routines)
// surfaces a specific diagnostic.
func TestParseRejectsBareReturn(t *testing.T) {
	_, err := Parse("BEGIN RETURN; END")
	if err == nil {
		t.Fatal("expected SyntaxError for RETURN without value")
	}
	if !strings.Contains(err.Error(), "RETURN") {
		t.Errorf("err = %v", err)
	}
}

// TestParseReturnExpressionError: a malformed expression inside
// RETURN surfaces a SyntaxError pinned at the expression's start
// position so the diagnostic points at the offending source.
func TestParseReturnExpressionError(t *testing.T) {
	_, err := Parse("BEGIN RETURN +; END")
	if err == nil {
		t.Fatal("expected SyntaxError for bad expression")
	}
	se, ok := err.(*SyntaxError)
	if !ok {
		t.Fatalf("err = %T, want *SyntaxError", err)
	}
	if se.Pos == 0 {
		t.Errorf("Pos = 0, want offset of expression start")
	}
}

// TestParseDeclareSection: Stage A 4b accepts a DECLARE prefix
// with one-or-more typed variable declarations.
func TestParseDeclareSection(t *testing.T) {
	src := `DECLARE
		x int;
		y text;
	BEGIN
		RETURN 1;
	END`
	blk, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Declarations) != 2 {
		t.Fatalf("Declarations len = %d, want 2", len(blk.Declarations))
	}
	if blk.Declarations[0].Name != "x" || blk.Declarations[0].Type.Name != "int" {
		t.Errorf("decl[0] = %+v, want x int", blk.Declarations[0])
	}
	if blk.Declarations[1].Name != "y" || blk.Declarations[1].Type.Name != "text" {
		t.Errorf("decl[1] = %+v, want y text", blk.Declarations[1])
	}
}

// TestParseDeclareWithDefaults pins both initializer forms —
// `DEFAULT expr` and `:= expr` — landing on the same Default
// field.
func TestParseDeclareWithDefaults(t *testing.T) {
	src := `DECLARE
		a int := 5;
		b int DEFAULT 10;
	BEGIN
	END`
	blk, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if blk.Declarations[0].Default == nil {
		t.Errorf("decl[0].Default = nil, want non-nil for `:= 5`")
	}
	if blk.Declarations[1].Default == nil {
		t.Errorf("decl[1].Default = nil, want non-nil for `DEFAULT 10`")
	}
}

// TestParseDeclareTypeWithArgs pins that `numeric(10, 2)`-style
// type args round-trip into the catalog ColumnType — drives the
// type-arg list path through the SQL parser.
func TestParseDeclareTypeWithArgs(t *testing.T) {
	src := `DECLARE
		amt numeric(10, 2);
	BEGIN
	END`
	blk, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	d := blk.Declarations[0]
	if d.Type.Name != "numeric" {
		t.Errorf("Type.Name = %q, want numeric", d.Type.Name)
	}
	if len(d.Type.Args) != 2 || d.Type.Args[0] != 10 || d.Type.Args[1] != 2 {
		t.Errorf("Type.Args = %v, want [10 2]", d.Type.Args)
	}
}

// TestParseDeclareRejectsConstant pins the Stage A 4b deferral —
// CONSTANT declarations surface a specific diagnostic instead of
// a generic syntax error.
func TestParseDeclareRejectsConstant(t *testing.T) {
	_, err := Parse("DECLARE x CONSTANT int := 1; BEGIN END")
	if err == nil {
		t.Fatal("expected SyntaxError for CONSTANT")
	}
	if !strings.Contains(err.Error(), "Stage A 4b") {
		t.Errorf("err = %v, want Stage-A-4b diagnostic", err)
	}
}

// TestParseDeclareRejectsNotNull: NOT NULL deferred to a later
// slice; surface a specific diagnostic.
func TestParseDeclareRejectsNotNull(t *testing.T) {
	_, err := Parse("DECLARE x int NOT NULL; BEGIN END")
	if err == nil {
		t.Fatal("expected SyntaxError for NOT NULL")
	}
	if !strings.Contains(err.Error(), "Stage A 4b") {
		t.Errorf("err = %v, want Stage-A-4b diagnostic", err)
	}
}

// TestParseAssignment pins the assignment shape in a body
// statement: the target name lands on AssignStmt.Target and the
// expression hits the SQL expression AST.
func TestParseAssignment(t *testing.T) {
	src := `BEGIN
		x := 1 + 2;
		RETURN x;
	END`
	blk, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Statements) != 2 {
		t.Fatalf("Statements len = %d, want 2", len(blk.Statements))
	}
	a, ok := blk.Statements[0].(*AssignStmt)
	if !ok {
		t.Fatalf("Statements[0] = %T, want *AssignStmt", blk.Statements[0])
	}
	if a.Target != "x" {
		t.Errorf("Target = %q, want x", a.Target)
	}
	if _, ok := a.Value.(*parser.BinaryOp); !ok {
		t.Errorf("Value = %T, want *parser.BinaryOp", a.Value)
	}
}

// TestParseDeclareRequiresBegin: DECLARE section followed by EOF
// rather than BEGIN surfaces a specific diagnostic.
func TestParseDeclareRequiresBegin(t *testing.T) {
	_, err := Parse("DECLARE x int;")
	if err == nil {
		t.Fatal("expected SyntaxError")
	}
	if !strings.Contains(err.Error(), "BEGIN") {
		t.Errorf("err = %v, want a BEGIN diagnostic", err)
	}
}

// TestParseDeclareEmpty: bare `DECLARE BEGIN END` is upstream-
// legal; we accept it so future label-prefix parsing doesn't have
// to undo a special-case lookahead.
func TestParseDeclareEmpty(t *testing.T) {
	blk, err := Parse("DECLARE BEGIN END")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Declarations) != 0 {
		t.Errorf("Declarations len = %d, want 0", len(blk.Declarations))
	}
}

// TestParseAssignWithoutColonEqError: bare-identifier statement
// without `:=` surfaces the Stage-A-4b diagnostic naming the two
// supported shapes.
func TestParseAssignWithoutColonEqError(t *testing.T) {
	_, err := Parse("BEGIN foo bar; END")
	if err == nil {
		t.Fatal("expected SyntaxError")
	}
	if !strings.Contains(err.Error(), ":=") {
		t.Errorf("err = %v, want a `:=` diagnostic", err)
	}
}

// TestParseIf pins the basic IF statement shape.
func TestParseIf(t *testing.T) {
	src := `BEGIN
		IF x > 0 THEN
			RETURN 1;
		END IF;
	END`
	blk, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Statements) != 1 {
		t.Fatalf("Statements len = %d, want 1", len(blk.Statements))
	}
	iff, ok := blk.Statements[0].(*IfStmt)
	if !ok {
		t.Fatalf("Statements[0] = %T, want *IfStmt", blk.Statements[0])
	}
	if _, ok := iff.Cond.(*parser.BinaryOp); !ok {
		t.Errorf("Cond = %T, want *parser.BinaryOp", iff.Cond)
	}
	if len(iff.Then) != 1 {
		t.Errorf("Then len = %d, want 1", len(iff.Then))
	}
}

// TestParseIfElsifElse pins the full IF surface including multiple
// ELSIF/ELSEIF variants and the ELSE branch.
func TestParseIfElsifElse(t *testing.T) {
	src := `BEGIN
		IF x = 1 THEN
			RETURN 1;
		ELSIF x = 2 THEN
			RETURN 2;
		ELSEIF x = 3 THEN
			RETURN 3;
		ELSE
			RETURN 0;
		END IF;
	END`
	blk, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	iff := blk.Statements[0].(*IfStmt)
	if len(iff.Elsifs) != 2 {
		t.Fatalf("Elsifs len = %d, want 2", len(iff.Elsifs))
	}
	if len(iff.Else) != 1 {
		t.Errorf("Else len = %d, want 1", len(iff.Else))
	}
}

// TestParseIfMissingEndIf pins the diagnostic for an unterminated
// IF block.
func TestParseIfMissingEndIf(t *testing.T) {
	_, err := Parse("BEGIN IF x THEN RETURN 1; END;")
	if err == nil {
		t.Fatal("expected SyntaxError")
	}
	if !strings.Contains(err.Error(), "END IF") {
		t.Errorf("err = %v, want an 'END IF' diagnostic", err)
	}
}

// TestParseLexErrorWrapped ensures upstream lexer errors surface
// as SyntaxError so callers don't need to type-switch on
// `*parser.LexError` separately.
func TestParseLexErrorWrapped(t *testing.T) {
	// An unterminated string literal triggers parser.LexError.
	_, err := Parse("BEGIN RETURN 'unterminated; END")
	if err == nil {
		t.Fatal("expected SyntaxError")
	}
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Errorf("err = %T, want errors.As *SyntaxError", err)
	}
}
