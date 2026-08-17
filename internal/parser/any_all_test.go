package parser

import (
	"testing"
)

// TestParseAnySomeAllOrderingOperators pins the M0122-0004 extension: the
// ordering comparisons (< > <= >=) now desugar to InExpr{AnyOp, AllOp} the
// same way `=`/`!=`/regex operators already did, and SOME is accepted as a
// synonym for ANY everywhere ANY is.
func TestParseAnySomeAllOrderingOperators(t *testing.T) {
	cases := []struct {
		sql     string
		wantOp  OpCode
		wantAll bool
	}{
		{"SELECT * FROM t WHERE x < ANY (ARRAY[1,2,3])", OpLt, false},
		{"SELECT * FROM t WHERE x < SOME (ARRAY[1,2,3])", OpLt, false},
		{"SELECT * FROM t WHERE x < ALL (ARRAY[1,2,3])", OpLt, true},
		{"SELECT * FROM t WHERE x > ANY (ARRAY[1,2,3])", OpGt, false},
		{"SELECT * FROM t WHERE x > ALL (ARRAY[1,2,3])", OpGt, true},
		{"SELECT * FROM t WHERE x <= ANY (ARRAY[1,2,3])", OpLe, false},
		{"SELECT * FROM t WHERE x <= ALL (ARRAY[1,2,3])", OpLe, true},
		{"SELECT * FROM t WHERE x >= ANY (ARRAY[1,2,3])", OpGe, false},
		{"SELECT * FROM t WHERE x >= ALL (ARRAY[1,2,3])", OpGe, true},
		{"SELECT * FROM t WHERE x != ALL (ARRAY[1,2,3])", OpNe, true},
		{"SELECT * FROM t WHERE x <> ALL (ARRAY[1,2,3])", OpNe, true},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.sql, err)
			}
			sel := stmts[0].(*SelectStmt)
			ie, ok := sel.Where.(*InExpr)
			if !ok {
				t.Fatalf("WHERE root=%T(%v), want *InExpr", sel.Where, sel.Where)
			}
			if ie.AnyOp != c.wantOp {
				t.Errorf("AnyOp=%v, want %v", ie.AnyOp, c.wantOp)
			}
			if ie.AllOp != c.wantAll {
				t.Errorf("AllOp=%v, want %v", ie.AllOp, c.wantAll)
			}
			if len(ie.List) != 3 {
				t.Errorf("List=%v, want 3 elements", ie.List)
			}
		})
	}
}

// TestParseEqualitySome pins SOME as a synonym for ANY in the pre-existing
// `=`/`!=`/`<>` ANY block, which does not set AnyOp (equality uses the
// default InExpr comparison path, not the generic AnyOp/evalBinary path).
func TestParseEqualitySome(t *testing.T) {
	stmts, err := Parse("SELECT * FROM t WHERE x = SOME (ARRAY[1,2,3])")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	ie, ok := sel.Where.(*InExpr)
	if !ok {
		t.Fatalf("WHERE root=%T(%v), want *InExpr", sel.Where, sel.Where)
	}
	if ie.AnyOp != OpUnknown {
		t.Errorf("AnyOp=%v, want OpUnknown (plain equality path)", ie.AnyOp)
	}
	if ie.NotEqualAny {
		t.Errorf("NotEqualAny=true, want false for `= SOME`")
	}
	if len(ie.List) != 3 {
		t.Errorf("List=%v, want 3 elements", ie.List)
	}
}

// TestParseAnyAllSubquery pins the new `expr op ANY|ALL (SELECT ...)` form
// — previously only array-literal/scalar operands were accepted; a SELECT
// subquery now populates InExpr.Subquery exactly like `IN (subquery)` does.
func TestParseAnyAllSubquery(t *testing.T) {
	cases := []struct {
		sql     string
		wantOp  OpCode
		wantAll bool
	}{
		{"SELECT * FROM t WHERE x > ANY (SELECT y FROM u)", OpGt, false},
		{"SELECT * FROM t WHERE x > SOME (SELECT y FROM u)", OpGt, false},
		{"SELECT * FROM t WHERE x > ALL (SELECT y FROM u)", OpGt, true},
		{"SELECT * FROM t WHERE x = ANY (SELECT y FROM u)", OpUnknown, false},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.sql, err)
			}
			sel := stmts[0].(*SelectStmt)
			ie, ok := sel.Where.(*InExpr)
			if !ok {
				t.Fatalf("WHERE root=%T(%v), want *InExpr", sel.Where, sel.Where)
			}
			if ie.Subquery == nil {
				t.Fatalf("Subquery is nil, want a *SelectStmt")
			}
			if ie.AnyOp != c.wantOp {
				t.Errorf("AnyOp=%v, want %v", ie.AnyOp, c.wantOp)
			}
			if ie.AllOp != c.wantAll {
				t.Errorf("AllOp=%v, want %v", ie.AllOp, c.wantAll)
			}
		})
	}
}

// TestParseRegexAnyAll pins ALL support (previously ANY-only) and the SOME
// synonym for the POSIX regex operators.
func TestParseRegexAnyAll(t *testing.T) {
	cases := []struct {
		sql     string
		wantOp  OpCode
		wantAll bool
	}{
		{"SELECT * FROM t WHERE x ~ ANY (ARRAY['a','b'])", OpRegexMatch, false},
		{"SELECT * FROM t WHERE x ~ SOME (ARRAY['a','b'])", OpRegexMatch, false},
		{"SELECT * FROM t WHERE x ~ ALL (ARRAY['a','b'])", OpRegexMatch, true},
		{"SELECT * FROM t WHERE x !~* ALL (ARRAY['a','b'])", OpRegexINoMatch, true},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.sql, err)
			}
			sel := stmts[0].(*SelectStmt)
			ie, ok := sel.Where.(*InExpr)
			if !ok {
				t.Fatalf("WHERE root=%T(%v), want *InExpr", sel.Where, sel.Where)
			}
			if ie.AnyOp != c.wantOp {
				t.Errorf("AnyOp=%v, want %v", ie.AnyOp, c.wantOp)
			}
			if ie.AllOp != c.wantAll {
				t.Errorf("AllOp=%v, want %v", ie.AllOp, c.wantAll)
			}
		})
	}
}

// TestParseLikeFamilyAnyAll pins ANY/SOME/ALL support for the LIKE-family
// operators (LIKE, NOT LIKE, ILIKE, NOT ILIKE), mirroring the regex-operator
// desugaring above. M0134-0003.
func TestParseLikeFamilyAnyAll(t *testing.T) {
	cases := []struct {
		sql     string
		wantOp  OpCode
		wantAll bool
	}{
		{"SELECT * FROM t WHERE x LIKE ANY (ARRAY['%a', '%b'])", OpLike, false},
		{"SELECT * FROM t WHERE x LIKE SOME (ARRAY['%a', '%b'])", OpLike, false},
		{"SELECT * FROM t WHERE x LIKE ALL (ARRAY['%a', '%b'])", OpLike, true},
		{"SELECT * FROM t WHERE x NOT LIKE ANY (ARRAY['%a', '%b'])", OpNotLike, false},
		{"SELECT * FROM t WHERE x NOT LIKE SOME (ARRAY['%a', '%b'])", OpNotLike, false},
		{"SELECT * FROM t WHERE x NOT LIKE ALL (ARRAY['%a', '%b'])", OpNotLike, true},
		{"SELECT * FROM t WHERE x ILIKE ANY (ARRAY['%a', '%b'])", OpILike, false},
		{"SELECT * FROM t WHERE x ILIKE SOME (ARRAY['%a', '%b'])", OpILike, false},
		{"SELECT * FROM t WHERE x ILIKE ALL (ARRAY['%a', '%b'])", OpILike, true},
		{"SELECT * FROM t WHERE x NOT ILIKE ANY (ARRAY['%a', '%b'])", OpNotILike, false},
		{"SELECT * FROM t WHERE x NOT ILIKE SOME (ARRAY['%a', '%b'])", OpNotILike, false},
		{"SELECT * FROM t WHERE x NOT ILIKE ALL (ARRAY['%a', '%b'])", OpNotILike, true},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.sql, err)
			}
			sel := stmts[0].(*SelectStmt)
			ie, ok := sel.Where.(*InExpr)
			if !ok {
				t.Fatalf("WHERE root=%T(%v), want *InExpr", sel.Where, sel.Where)
			}
			if ie.AnyOp != c.wantOp {
				t.Errorf("AnyOp=%v, want %v", ie.AnyOp, c.wantOp)
			}
			if ie.AllOp != c.wantAll {
				t.Errorf("AllOp=%v, want %v", ie.AllOp, c.wantAll)
			}
		})
	}
}
