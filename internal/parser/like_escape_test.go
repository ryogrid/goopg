package parser

import "testing"

// TestParseLikeEscapeClause pins the M0134-0070 grammar extension: `[NOT]
// LIKE/ILIKE pattern ESCAPE escape_expr`. The ESCAPE clause is wrapped into
// a LikeEscapePattern that becomes the BinaryOp's Right operand — the
// BinaryOp struct itself is untouched (M0134-0070 design constraint).
func TestParseLikeEscapeClause(t *testing.T) {
	cases := []struct {
		sql     string
		wantOp  OpCode
		pattern string
		escape  string
	}{
		{"SELECT * FROM t WHERE x LIKE 'h%' ESCAPE '#'", OpLike, "h%", "#"},
		{"SELECT * FROM t WHERE x NOT LIKE 'h%' ESCAPE '#'", OpNotLike, "h%", "#"},
		{"SELECT * FROM t WHERE x ILIKE 'h%' ESCAPE '#'", OpILike, "h%", "#"},
		{"SELECT * FROM t WHERE x NOT ILIKE 'h%' ESCAPE '#'", OpNotILike, "h%", "#"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.sql, err)
			}
			sel := stmts[0].(*SelectStmt)
			bo, ok := sel.Where.(*BinaryOp)
			if !ok {
				t.Fatalf("WHERE root=%T(%v), want *BinaryOp", sel.Where, sel.Where)
			}
			if bo.Op != c.wantOp {
				t.Errorf("Op=%v, want %v", bo.Op, c.wantOp)
			}
			lep, ok := bo.Right.(*LikeEscapePattern)
			if !ok {
				t.Fatalf("Right=%T(%v), want *LikeEscapePattern", bo.Right, bo.Right)
			}
			pat, ok := lep.Pattern.(*StringConst)
			if !ok || pat.Value != c.pattern {
				t.Errorf("Pattern=%#v, want StringConst(%q)", lep.Pattern, c.pattern)
			}
			esc, ok := lep.Escape.(*StringConst)
			if !ok || esc.Value != c.escape {
				t.Errorf("Escape=%#v, want StringConst(%q)", lep.Escape, c.escape)
			}
		})
	}
}

// TestParseLikeNoEscapeRegression is a regression guard: bare LIKE/ILIKE
// (no ESCAPE clause) must keep producing the old plain-Right AST shape —
// Right is the bare pattern expr, never wrapped in LikeEscapePattern.
func TestParseLikeNoEscapeRegression(t *testing.T) {
	cases := []string{
		"SELECT * FROM t WHERE x LIKE 'h%'",
		"SELECT * FROM t WHERE x NOT LIKE 'h%'",
		"SELECT * FROM t WHERE x ILIKE 'h%'",
		"SELECT * FROM t WHERE x NOT ILIKE 'h%'",
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			stmts, err := Parse(sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", sql, err)
			}
			sel := stmts[0].(*SelectStmt)
			bo, ok := sel.Where.(*BinaryOp)
			if !ok {
				t.Fatalf("WHERE root=%T(%v), want *BinaryOp", sel.Where, sel.Where)
			}
			pat, ok := bo.Right.(*StringConst)
			if !ok || pat.Value != "h%" {
				t.Errorf("Right=%#v, want bare StringConst(h%%)", bo.Right)
			}
		})
	}
}

// TestParseEscapeAsIdentifier regression-guards ESCAPE's unreserved-keyword
// status (PG kwlist.h:159, UNRESERVED_KEYWORD): it must still parse as an
// ordinary identifier/column alias outside a LIKE...ESCAPE tail.
func TestParseEscapeAsIdentifier(t *testing.T) {
	stmts, err := Parse(`SELECT 1 AS escape`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	if len(sel.Targets) != 1 || sel.Targets[0].Alias != "escape" {
		t.Fatalf("targets=%#v, want alias \"escape\"", sel.Targets)
	}
}
