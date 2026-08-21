package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestSimilarToEndToEnd exercises the M0134-0070 SIMILAR TO / NOT SIMILAR TO
// constant-fold path all the way through planning and execution — the parser
// converts the pattern to a POSIX ERE at parse time (see
// internal/parser/similar_to_test.go for the AST-shape pins), so this test
// only needs to confirm the resulting BinaryOp{Op: OpRegexMatch/OpRegexNoMatch}
// evaluates the same booleans upstream does (PG oracle:
// postgres/src/test/regress/expected/strings.out:568-614).
func TestSimilarToEndToEnd(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want bool
	}{
		{`SELECT 'abcdefg' SIMILAR TO '_bcd%'`, true},
		{`SELECT 'abcdefg' SIMILAR TO 'bcd%'`, false},
		{`SELECT 'abcdefg' SIMILAR TO '_bcd#%' ESCAPE '#'`, false},
		{`SELECT 'abcd%' SIMILAR TO '_bcd#%' ESCAPE '#'`, true},
		{`SELECT 'abcdefg' SIMILAR TO '_bcd\%'`, false},
		{`SELECT 'abcd\efg' SIMILAR TO '_bcd\%' ESCAPE ''`, true},
		{`SELECT 'abcdefg' NOT SIMILAR TO 'bcd%'`, true},
		{`SELECT 'abcdefg' NOT SIMILAR TO '_bcd%'`, false},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			got := simpleBoolExprResult(t, ctx, c.sql)
			if got.Kind != KindBool {
				t.Fatalf("Kind=%v, want KindBool (datum=%#v)", got.Kind, got)
			}
			if got.BoolValue() != c.want {
				t.Errorf("%s = %v, want %v", c.sql, got.BoolValue(), c.want)
			}
		})
	}
}

// TestSimilarToEscapeNullEndToEnd pins ESCAPE NULL propagating to a NULL
// result (STRICT-function semantics, no PG_ARGISNULL special case). PG
// oracle: strings.out:611-613.
func TestSimilarToEscapeNullEndToEnd(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	got := simpleBoolExprResult(t, ctx, `SELECT 'abcdefg' SIMILAR TO '_bcd%' ESCAPE NULL`)
	if !got.IsNull() {
		t.Errorf("got=%#v, want NULL", got)
	}
}

// TestSimilarToEscapeTooLongErrorsAtParse pins ERROR 22025 for a >1-char
// ESCAPE string. M0134-0070's constant-fold raises this at Parse() time
// (see internal/parser/similar_to_test.go TestParseSimilarToEscapeTooLong for
// the SyntaxError shape); this test confirms the query never reaches
// planning/execution. PG oracle: strings.out:614-616.
func TestSimilarToEscapeTooLongErrorsAtParse(t *testing.T) {
	_, err := parser.Parse(`SELECT 'abcdefg' SIMILAR TO '_bcd#%' ESCAPE '##'`)
	if err == nil {
		t.Fatal("Parse: want error, got nil")
	}
	se, ok := err.(*parser.SyntaxError)
	if !ok {
		t.Fatalf("err=%T(%v), want *parser.SyntaxError", err, err)
	}
	if se.Code != "22025" {
		t.Errorf("Code=%q, want 22025", se.Code)
	}
}

// simpleBoolExprResult runs a single-row single-column bool-expression query
// (no FROM clause) and returns the datum. Mirrors byteaExprResult
// (bytea_value_test.go) minus the type-name return, since every case here is
// a bare boolean expression.
func simpleBoolExprResult(t *testing.T, ctx *Context, sql string) Datum {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open(%q): %v", sql, err)
	}
	rows, err := drainScan(op)
	_ = op.Close()
	if err != nil {
		t.Fatalf("exec(%q): %v", sql, err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("%q: want 1x1 result, got %d rows", sql, len(rows))
	}
	return rows[0][0]
}
