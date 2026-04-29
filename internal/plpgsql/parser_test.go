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
	if !strings.Contains(err.Error(), "END") {
		t.Errorf("err = %v, want an 'END' diagnostic", err)
	}
}

// TestParseRejectsUnsupportedStatement guards Stage A 4a's scope:
// statements other than RETURN surface a specific
// "Stage A 4a accepts RETURN only" diagnostic — clearer than a
// generic syntax error so callers know the feature is recognised
// but not yet implemented.
func TestParseRejectsUnsupportedStatement(t *testing.T) {
	_, err := Parse("BEGIN PERFORM foo(); END")
	if err == nil {
		t.Fatal("expected SyntaxError for PERFORM")
	}
	if !strings.Contains(err.Error(), "Stage A 4a") {
		t.Errorf("err = %v, want a Stage-A-4a diagnostic", err)
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
