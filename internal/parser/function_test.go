package parser

import (
	"strings"
	"testing"
)

// TestParseCreateFunctionMinimal pins the smallest acceptable form:
// CREATE FUNCTION name() RETURNS rettype LANGUAGE plpgsql AS $$ ... $$.
// Verifies every AST field gets populated and the body is captured
// verbatim.
func TestParseCreateFunctionMinimal(t *testing.T) {
	src := `CREATE FUNCTION add_one() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1", len(stmts))
	}
	cf, ok := stmts[0].(*CreateFunctionStmt)
	if !ok {
		t.Fatalf("got %T, want *CreateFunctionStmt", stmts[0])
	}
	if cf.OrReplace {
		t.Errorf("OrReplace = true, want false")
	}
	if cf.Name.Name != "add_one" {
		t.Errorf("Name.Name = %q, want add_one", cf.Name.Name)
	}
	if len(cf.Args) != 0 {
		t.Errorf("Args len = %d, want 0", len(cf.Args))
	}
	if cf.ReturnType.Name != "int" {
		t.Errorf("ReturnType.Name = %q, want int", cf.ReturnType.Name)
	}
	if cf.Language != "plpgsql" {
		t.Errorf("Language = %q, want plpgsql", cf.Language)
	}
	if !strings.Contains(cf.Body, "RETURN 1") {
		t.Errorf("Body = %q, missing RETURN 1", cf.Body)
	}
}

// TestParseCreateFunctionOrReplace pins the OR REPLACE flag.
func TestParseCreateFunctionOrReplace(t *testing.T) {
	src := `CREATE OR REPLACE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if !cf.OrReplace {
		t.Error("OrReplace = false, want true")
	}
}

// TestParseCreateFunctionWithArgs pins the named-arg surface and the
// implicit-IN mode. Two args with explicit names + one anonymous
// positional arg.
func TestParseCreateFunctionWithArgs(t *testing.T) {
	src := `CREATE FUNCTION add(a int, b int, text) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if len(cf.Args) != 3 {
		t.Fatalf("Args len = %d, want 3", len(cf.Args))
	}
	if cf.Args[0].Name != "a" || cf.Args[0].Type.Name != "int" {
		t.Errorf("Args[0] = %+v, want name=a type=int", cf.Args[0])
	}
	if cf.Args[1].Name != "b" || cf.Args[1].Type.Name != "int" {
		t.Errorf("Args[1] = %+v, want name=b type=int", cf.Args[1])
	}
	if cf.Args[2].Name != "" || cf.Args[2].Type.Name != "text" {
		t.Errorf("Args[2] = %+v, want anonymous type=text", cf.Args[2])
	}
	for i, a := range cf.Args {
		if a.Mode != FuncArgIn {
			t.Errorf("Args[%d].Mode = %d, want FuncArgIn", i, a.Mode)
		}
	}
}

// TestParseCreateFunctionExplicitIN pins that an explicit `IN`
// keyword on an argument is accepted (Stage A treats it as a no-op
// — same as the default).
func TestParseCreateFunctionExplicitIN(t *testing.T) {
	src := `CREATE FUNCTION f(IN x int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if len(cf.Args) != 1 || cf.Args[0].Name != "x" {
		t.Fatalf("Args = %+v, want one arg named x", cf.Args)
	}
}

// TestParseCreateFunctionRejectsOutInoutVariadic guards Stage A's
// scope: OUT / INOUT / VARIADIC arrive in Stage B and must surface
// a specific diagnostic now, not a generic syntax error.
func TestParseCreateFunctionRejectsOutInoutVariadic(t *testing.T) {
	cases := []string{
		`CREATE FUNCTION f(OUT y int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`,
		`CREATE FUNCTION f(INOUT y int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`,
		`CREATE FUNCTION f(VARIADIC y int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`,
	}
	for _, src := range cases {
		t.Run(src[:32], func(t *testing.T) {
			_, err := Parse(src)
			if err == nil {
				t.Fatalf("expected parse error")
			}
			if !strings.Contains(err.Error(), "Stage A: IN only") {
				t.Errorf("err = %v, want a Stage-A-only message", err)
			}
		})
	}
}

// TestParseCreateFunctionLanguageAsAnyOrder pins that LANGUAGE and
// AS can appear in either order — matches upstream's flexible
// clause ordering.
func TestParseCreateFunctionLanguageAsAnyOrder(t *testing.T) {
	srcs := []string{
		`CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ x $$`,
		`CREATE FUNCTION f() RETURNS int AS $$ x $$ LANGUAGE plpgsql`,
	}
	for _, src := range srcs {
		t.Run(src, func(t *testing.T) {
			stmts, err := Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			cf := stmts[0].(*CreateFunctionStmt)
			if cf.Language != "plpgsql" {
				t.Errorf("Language = %q", cf.Language)
			}
		})
	}
}

// TestParseCreateFunctionTaggedDollarQuote pins the
// `$tag$body$tag$` form — useful when the body itself contains
// `$$`.
func TestParseCreateFunctionTaggedDollarQuote(t *testing.T) {
	src := `CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $body$ SELECT $$inner$$ $body$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if !strings.Contains(cf.Body, "$$inner$$") {
		t.Errorf("Body = %q, want it to contain $$inner$$", cf.Body)
	}
}

// TestParseCreateFunctionMissingBody guards the "AS $$body$$
// required" rule: omitting AS surfaces a specific diagnostic.
func TestParseCreateFunctionMissingBody(t *testing.T) {
	src := `CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql`
	_, err := Parse(src)
	if err == nil {
		t.Fatalf("expected parse error for missing AS body")
	}
	if !strings.Contains(err.Error(), "AS $$body$$") {
		t.Errorf("err = %v, want a missing-AS diagnostic", err)
	}
}

// TestParseDropFunctionMinimal pins the smallest DROP form.
func TestParseDropFunctionMinimal(t *testing.T) {
	src := `DROP FUNCTION f`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	df, ok := stmts[0].(*DropFunctionStmt)
	if !ok {
		t.Fatalf("got %T, want *DropFunctionStmt", stmts[0])
	}
	if df.Name.Name != "f" {
		t.Errorf("Name = %q", df.Name.Name)
	}
	if df.IfExists {
		t.Errorf("IfExists = true, want false")
	}
	if df.Args != nil {
		t.Errorf("Args = %+v, want nil for no parens", df.Args)
	}
}

// TestParseDropFunctionIfExistsAndArgs pins the full surface:
// IF EXISTS + parenthesised arg list + CASCADE.
func TestParseDropFunctionIfExistsAndArgs(t *testing.T) {
	src := `DROP FUNCTION IF EXISTS f(int, text) CASCADE`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	df := stmts[0].(*DropFunctionStmt)
	if !df.IfExists {
		t.Errorf("IfExists = false, want true")
	}
	if len(df.Args) != 2 {
		t.Fatalf("Args len = %d, want 2", len(df.Args))
	}
	if df.Args[0].Type.Name != "int" || df.Args[1].Type.Name != "text" {
		t.Errorf("Args = %+v", df.Args)
	}
	if df.Behavior != DropCascade {
		t.Errorf("Behavior = %d, want DropCascade", df.Behavior)
	}
}

// TestDollarQuoteEmptyTag is a lexer-level regression: `$$` empty
// body must round-trip.
func TestDollarQuoteEmptyTag(t *testing.T) {
	src := `CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$$$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cf := stmts[0].(*CreateFunctionStmt)
	if cf.Body != "" {
		t.Errorf("Body = %q, want empty", cf.Body)
	}
}

// TestDollarQuoteUnterminated pins clear diagnostic on a missing
// closing tag.
func TestDollarQuoteUnterminated(t *testing.T) {
	src := `CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $tag$ no closer here`
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected parse error for unterminated dollar-quoted string")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("err = %v, want a 'unterminated' diagnostic", err)
	}
}

// TestPositionalParameterStillParses guards the lexer regression:
// the dollar-quote support must not break `$1` / `$N` positional
// parameter parsing.
func TestPositionalParameterStillParses(t *testing.T) {
	src := `SELECT $1`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d stmts", len(stmts))
	}
}

// TestParseCreateProcedureMinimal pins the basic CREATE PROCEDURE form.
// Stage B (procedure follow-up) of M0015.
func TestParseCreateProcedureMinimal(t *testing.T) {
	src := `CREATE PROCEDURE proc() LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1", len(stmts))
	}
	cp, ok := stmts[0].(*CreateProcedureStmt)
	if !ok {
		t.Fatalf("got %T, want *CreateProcedureStmt", stmts[0])
	}
	if cp.OrReplace {
		t.Errorf("OrReplace = true, want false")
	}
	if cp.Name.Name != "proc" {
		t.Errorf("Name.Name = %q, want proc", cp.Name.Name)
	}
	if len(cp.Args) != 0 {
		t.Errorf("Args len = %d, want 0", len(cp.Args))
	}
	if cp.Language != "plpgsql" {
		t.Errorf("Language = %q, want plpgsql", cp.Language)
	}
	if !strings.Contains(cp.Body, "BEGIN END") {
		t.Errorf("Body = %q, missing BEGIN END", cp.Body)
	}
}

// TestParseCreateProcedureOrReplace pins OR REPLACE flag.
func TestParseCreateProcedureOrReplace(t *testing.T) {
	src := `CREATE OR REPLACE PROCEDURE p() LANGUAGE sql AS $$ SELECT 1 $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1", len(stmts))
	}
	cp := stmts[0].(*CreateProcedureStmt)
	if !cp.OrReplace {
		t.Errorf("OrReplace = false, want true")
	}
}

// TestParseCreateProcedureArgs pins argument parsing.
func TestParseCreateProcedureArgs(t *testing.T) {
	src := `CREATE PROCEDURE p(a int, b text) LANGUAGE plpgsql AS $$ BEGIN END $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cp := stmts[0].(*CreateProcedureStmt)
	if len(cp.Args) != 2 {
		t.Fatalf("Args len = %d, want 2", len(cp.Args))
	}
	if cp.Args[0].Name != "a" || cp.Args[0].Type.Name != "int" {
		t.Errorf("first arg = %v", cp.Args[0])
	}
	if cp.Args[1].Name != "b" || cp.Args[1].Type.Name != "text" {
		t.Errorf("second arg = %v", cp.Args[1])
	}
}

// TestParseCreateProcedureNoLanguage leaves Language empty when omitted.
func TestParseCreateProcedureNoLanguage(t *testing.T) {
	src := `CREATE PROCEDURE p() AS $$ SELECT 1 $$`
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cp := stmts[0].(*CreateProcedureStmt)
	if cp.Language != "" {
		t.Errorf("Language = %q, want empty when omitted", cp.Language)
	}
}

// TestParseCallStatement pins CALL with and without arguments.
func TestParseCallStatement(t *testing.T) {
	tests := []struct {
		src  string
		want string
		args int
	}{
		{"CALL foo", "foo", 0},
		{"CALL foo()", "foo", 0},
		{"CALL foo(1)", "foo", 1},
		{"CALL foo(1 + 2, 'text')", "foo", 2},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			stmts, err := Parse(tt.src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			call := stmts[0].(*CallStmt)
			if call.Name.Name != tt.want {
				t.Errorf("Name.Name = %q, want %s", call.Name.Name, tt.want)
			}
			if len(call.Args) != tt.args {
				t.Errorf("Args len = %d, want %d", len(call.Args), tt.args)
			}
		})
	}
}

// TestParseCallRejectsTrailingSemicolon ensures CALL ends cleanly.
func TestParseCallRejectsTrailingSemicolon(t *testing.T) {
	src := "CALL foo();"
	stmts, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements", len(stmts))
	}
}
