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
// In Stage A 4b, PERFORM is not supported and should surface a
// Stage-A-4b diagnostic. However, the current parser implementation
// already supports PERFORM, so this test needs revision.
func TestParseRejectsUnsupportedStatement(t *testing.T) {
	// The parser now supports PERFORM statements
	blk, err := Parse("BEGIN PERFORM foo(); END")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Statements) != 1 {
		t.Fatalf("Statements len = %d, want 1", len(blk.Statements))
	}
	if _, ok := blk.Statements[0].(*PerformStmt); !ok {
		t.Fatalf("Statements[0] = %T, want *PerformStmt", blk.Statements[0])
	}
}

// TestParseAcceptsBareReturn: `RETURN;` (no expression) is upstream-legal for
// void-returning functions and procedures, and is the canonical early-exit
// form. It must parse to a ReturnStmt with a nil expression; the void-vs-value
// distinction is enforced at runtime (it needs the function's return type,
// which the parser does not know). M0118-0009 (subxid-overflow gen_subxids).
func TestParseAcceptsBareReturn(t *testing.T) {
	blk, err := Parse("BEGIN RETURN; END")
	if err != nil {
		t.Fatalf("expected bare RETURN to parse, got %v", err)
	}
	if len(blk.Statements) != 1 {
		t.Fatalf("got %d stmts, want 1", len(blk.Statements))
	}
	rs, ok := blk.Statements[0].(*ReturnStmt)
	if !ok {
		t.Fatalf("stmt = %T, want *ReturnStmt", blk.Statements[0])
	}
	if rs.Expr != nil {
		t.Errorf("Expr = %v, want nil for bare RETURN", rs.Expr)
	}
}

// TestParseNullStatement: the PL/pgSQL `NULL;` no-op statement (used as an
// empty EXCEPTION handler body) parses to a NullStmt. M0118-0009.
func TestParseNullStatement(t *testing.T) {
	blk, err := Parse("BEGIN NULL; END")
	if err != nil {
		t.Fatalf("expected NULL; to parse, got %v", err)
	}
	if len(blk.Statements) != 1 {
		t.Fatalf("got %d stmts, want 1", len(blk.Statements))
	}
	if _, ok := blk.Statements[0].(*NullStmt); !ok {
		t.Fatalf("stmt = %T, want *NullStmt", blk.Statements[0])
	}
}

// TestParseExceptionHandlerNullBody: a full function-style body with an empty
// EXCEPTION handler (`WHEN ... THEN NULL;`) parses — the exact shape used by
// the subxid-overflow gen_subxids function. M0118-0009.
func TestParseExceptionHandlerNullBody(t *testing.T) {
	src := "BEGIN\n  PERFORM 1;\n  RETURN;\nEXCEPTION\n  WHEN raise_exception THEN NULL;\nEND"
	if _, err := Parse(src); err != nil {
		t.Fatalf("expected EXCEPTION/NULL body to parse, got %v", err)
	}
}

// TestParseTransactionControl: `COMMIT;` and `ROLLBACK;` parse into
// TxControlStmt nodes (PL/pgSQL transaction control). M0118-0008 (plpgsql-toast).
func TestParseTransactionControl(t *testing.T) {
	for _, tc := range []struct {
		src      string
		rollback bool
	}{
		{"BEGIN COMMIT; END", false},
		{"BEGIN ROLLBACK; END", true},
	} {
		blk, err := Parse(tc.src)
		if err != nil {
			t.Fatalf("%q: expected parse, got %v", tc.src, err)
		}
		if len(blk.Statements) != 1 {
			t.Fatalf("%q: got %d stmts, want 1", tc.src, len(blk.Statements))
		}
		st, ok := blk.Statements[0].(*TxControlStmt)
		if !ok {
			t.Fatalf("%q: stmt = %T, want *TxControlStmt", tc.src, blk.Statements[0])
		}
		if st.Rollback != tc.rollback {
			t.Errorf("%q: Rollback = %v, want %v", tc.src, st.Rollback, tc.rollback)
		}
	}
}

// TestParseSelectInto pins the M0118-0008 PL/pgSQL SELECT … INTO statement
// form: a top-level INTO clause is reinterpreted as variable assignment (not
// SQL's CREATE-TABLE-AS), the INTO clause is stripped from the captured query,
// and the target variable name(s) are recorded.
func TestParseSelectInto(t *testing.T) {
	for _, tc := range []struct {
		src         string
		wantSQL     string
		wantTargets []string
		wantStrict  bool
	}{
		{
			src:         "BEGIN select test1.b into x from test1; END",
			wantSQL:     "select test1.b  from test1",
			wantTargets: []string{"x"},
		},
		{
			src:         "BEGIN select * into r from test1; END",
			wantSQL:     "select *  from test1",
			wantTargets: []string{"r"},
		},
		{
			src:         "BEGIN select a, b into strict x, y from t where a = 1; END",
			wantSQL:     "select a, b  from t where a = 1",
			wantTargets: []string{"x", "y"},
			wantStrict:  true,
		},
	} {
		blk, err := Parse(tc.src)
		if err != nil {
			t.Fatalf("%q: expected parse, got %v", tc.src, err)
		}
		if len(blk.Statements) != 1 {
			t.Fatalf("%q: got %d stmts, want 1", tc.src, len(blk.Statements))
		}
		st, ok := blk.Statements[0].(*SelectIntoStmt)
		if !ok {
			t.Fatalf("%q: stmt = %T, want *SelectIntoStmt", tc.src, blk.Statements[0])
		}
		if st.SQL != tc.wantSQL {
			t.Errorf("%q: SQL = %q, want %q", tc.src, st.SQL, tc.wantSQL)
		}
		if len(st.Targets) != len(tc.wantTargets) {
			t.Fatalf("%q: Targets = %v, want %v", tc.src, st.Targets, tc.wantTargets)
		}
		for i := range tc.wantTargets {
			if st.Targets[i] != tc.wantTargets[i] {
				t.Errorf("%q: Targets[%d] = %q, want %q", tc.src, i, st.Targets[i], tc.wantTargets[i])
			}
		}
		if st.Strict != tc.wantStrict {
			t.Errorf("%q: Strict = %v, want %v", tc.src, st.Strict, tc.wantStrict)
		}
	}
}

// TestParseSelectNoIntoIsEmbeddedSQL confirms a plain SELECT (no INTO) is still
// captured verbatim as a *SQLStmt, not a SelectIntoStmt.
func TestParseSelectNoIntoIsEmbeddedSQL(t *testing.T) {
	blk, err := Parse("BEGIN select count(*) from t; END")
	if err != nil {
		t.Fatalf("expected parse, got %v", err)
	}
	if _, ok := blk.Statements[0].(*SQLStmt); !ok {
		t.Fatalf("stmt = %T, want *SQLStmt", blk.Statements[0])
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

// TestParseTriggerNewFieldAssignColonEquals and the `=` variant verify
// that `NEW.<col> := <expr>;` / `NEW.<col> = <expr>;` produce a real
// AssignStmt whose Target is the injected `_new_<col>` frame slot —
// the path partition-key-update-1.spec's
// `func_footrg_mod_a` ("NEW.a = 2;") depends on for cross-partition
// re-routing. Pre-M0100-0005p these were silently swallowed as
// `_plpgsql_noop`, so the trigger ran but never modified the new row.
func TestParseTriggerNewFieldAssignColonEquals(t *testing.T) {
	blk, err := Parse("BEGIN NEW.a := 2; RETURN NEW; END")
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
	if a.Target != "_new_a" {
		t.Errorf("Target = %q, want %q (NEW.a must lower to the _new_a frame slot)", a.Target, "_new_a")
	}
	if _, ok := a.Value.(*parser.IntegerConst); !ok {
		t.Errorf("Value = %T, want *parser.IntegerConst", a.Value)
	}
}

func TestParseTriggerNewFieldAssignBareEquals(t *testing.T) {
	// upstream PG accepts both `:=` and `=` for PL/pgSQL field
	// assignment; the spec script uses `NEW.a = 2;` (bare `=`).
	blk, err := Parse("BEGIN NEW.a = 2; RETURN NEW; END")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a, ok := blk.Statements[0].(*AssignStmt)
	if !ok {
		t.Fatalf("Statements[0] = %T, want *AssignStmt", blk.Statements[0])
	}
	if a.Target != "_new_a" {
		t.Errorf("Target = %q, want %q", a.Target, "_new_a")
	}
}

// TestParseTriggerOldFieldAssignStaysNoop pins that OLD.* writes are
// still discarded (OLD is immutable in upstream PG; in v0 we drop the
// statement rather than raise an error so existing trigger bodies that
// touch OLD.* compile cleanly).
// TestParseTriggerOldFieldAssign verifies OLD.<col> := expr produces a real
// AssignStmt targeting the `_old_<col>` frame slot.  Cross-partition UPDATEs
// fire BEFORE DELETE triggers (M0100-0005aa) whose bodies legitimately mutate
// OLD before referencing it in embedded SQL — partition-key-update-4.spec's
// `OLD.b = OLD.b || ' trigger'; INSERT INTO triglog select OLD.*` shape.
func TestParseTriggerOldFieldAssign(t *testing.T) {
	blk, err := Parse("BEGIN OLD.a = 99; RETURN OLD; END")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a, ok := blk.Statements[0].(*AssignStmt)
	if !ok {
		t.Fatalf("Statements[0] = %T, want *AssignStmt", blk.Statements[0])
	}
	if a.Target != "_old_a" {
		t.Errorf("Target = %q, want %q", a.Target, "_old_a")
	}
}

// TestParseExecuteIntoStrict pins the EXECUTE … INTO STRICT var form
// (horizons.spec enabler, M0118-0009 design 0118-0101): the optional STRICT
// modifier between INTO and the target variable is recognised and flagged on
// the ExecuteStmt, while a plain INTO stays non-strict.
func TestParseExecuteIntoStrict(t *testing.T) {
	blk, err := Parse("BEGIN EXECUTE p_query INTO STRICT v_ret; END")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blk.Statements) != 1 {
		t.Fatalf("Statements len = %d, want 1", len(blk.Statements))
	}
	ex, ok := blk.Statements[0].(*ExecuteStmt)
	if !ok {
		t.Fatalf("Statements[0] = %T, want *ExecuteStmt", blk.Statements[0])
	}
	if !ex.Strict {
		t.Errorf("Strict = false, want true")
	}
	if ex.IntoVar != "v_ret" {
		t.Errorf("IntoVar = %q, want %q", ex.IntoVar, "v_ret")
	}

	// Plain INTO (no STRICT) must stay non-strict.
	blk2, err := Parse("BEGIN EXECUTE p_query INTO v_ret; END")
	if err != nil {
		t.Fatalf("Parse non-strict: %v", err)
	}
	ex2 := blk2.Statements[0].(*ExecuteStmt)
	if ex2.Strict {
		t.Errorf("non-strict Strict = true, want false")
	}
	if ex2.IntoVar != "v_ret" {
		t.Errorf("non-strict IntoVar = %q, want %q", ex2.IntoVar, "v_ret")
	}
}

// TestParseGrantRevokeEmbeddedSQL: GRANT/REVOKE in a PL/pgSQL body parse as
// embedded SQL statements (not bare-identifier assignments). The intra-grant-
// inplace spec's revoke4 step wraps `REVOKE … FROM PUBLIC;` inside a DO block;
// before M0118-0009 this failed with "expected ':=' or '=' after revoke".
func TestParseGrantRevokeEmbeddedSQL(t *testing.T) {
	for _, sql := range []string{
		"BEGIN REVOKE SELECT ON intra_grant_inplace FROM PUBLIC; END",
		"BEGIN GRANT SELECT ON intra_grant_inplace TO PUBLIC; END",
	} {
		blk, err := Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		if len(blk.Statements) != 1 {
			t.Fatalf("Parse(%q): Statements len = %d, want 1", sql, len(blk.Statements))
		}
		stmt, ok := blk.Statements[0].(*SQLStmt)
		if !ok {
			t.Fatalf("Parse(%q): stmt = %T, want *SQLStmt", sql, blk.Statements[0])
		}
		if !strings.HasSuffix(stmt.SQL, "PUBLIC") {
			t.Errorf("Parse(%q): SQL = %q, want it to capture the full statement", sql, stmt.SQL)
		}
	}
}
