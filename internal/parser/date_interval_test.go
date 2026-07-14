package parser

import (
	"testing"
)

// TestParseDateLiteral pins the TPC-H typed-string-literal
// shape: `date 'YYYY-MM-DD'` parses to a TypedStringLit with
// Type=date.
func TestParseDateLiteral(t *testing.T) {
	stmts, err := Parse("SELECT date '1998-12-01' FROM t")
	if err != nil {
		t.Fatal(err)
	}
	tgt := stmts[0].(*SelectStmt).Targets[0].Expr.(*TypedStringLit)
	if tgt.Type != "date" || tgt.Value != "1998-12-01" {
		t.Errorf("typed=%+v", tgt)
	}
}

// TestParseIntervalLiteral pins the trailing typmod-field form: only the
// SINGULAR field keywords are grammar keywords, so `interval '90' day`
// carries a Qualified typmod (Unit=day). A plural (`days`) is NOT a keyword —
// PostgreSQL parses it as a column alias on the bare interval literal
// (`interval '90' days` = 00:00:05 AS days), so the parsed Expr is a bare
// (PreComputed, seconds-default) IntervalLit and the ResTarget carries the
// alias. Matches live PG 18.3 (unimplemented_feat #5(d-iv)).
func TestParseIntervalLiteral(t *testing.T) {
	singular := []struct {
		sql, wantUnit string
	}{
		{"SELECT interval '90' day FROM t", "day"},
		{"SELECT interval '3' month FROM t", "month"},
		{"SELECT interval '1' year FROM t", "year"},
		{"SELECT interval '5' hour FROM t", "hour"},
		{"SELECT interval '5' minute FROM t", "minute"},
		{"SELECT interval '5' second FROM t", "second"},
	}
	for _, tc := range singular {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		iv := stmts[0].(*SelectStmt).Targets[0].Expr.(*IntervalLit)
		if !iv.Qualified || iv.Unit != tc.wantUnit {
			t.Errorf("%q: qualified=%v unit=%q want qualified unit %q", tc.sql, iv.Qualified, iv.Unit, tc.wantUnit)
		}
	}
	// Plurals / abbreviations are column aliases, not typmod fields.
	plural := []struct{ sql, wantAlias string }{
		{"SELECT interval '90' days FROM t", "days"},
		{"SELECT interval '3' months FROM t", "months"},
		{"SELECT interval '1' years FROM t", "years"},
		{"SELECT interval '5' min FROM t", "min"},
		{"SELECT interval '5' millisecond FROM t", "millisecond"},
	}
	for _, tc := range plural {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		rt := stmts[0].(*SelectStmt).Targets[0]
		if rt.Alias != tc.wantAlias {
			t.Errorf("%q: alias=%q want %q", tc.sql, rt.Alias, tc.wantAlias)
		}
		iv, ok := rt.Expr.(*IntervalLit)
		if !ok || iv.Qualified {
			t.Errorf("%q: expr=%T qualified=%v — want a bare (unqualified) IntervalLit", tc.sql, rt.Expr, ok && iv.Qualified)
		}
	}
}

// TestDateMinusIntervalParses pins the TPC-H Q1 shape parses
// without errors: the `date '...' - interval '...' day` chain
// becomes a BinaryOp over two typed leaves.
func TestDateMinusIntervalParses(t *testing.T) {
	_, err := Parse("SELECT * FROM lineitem WHERE l_shipdate <= date '1998-12-01' - interval '90' day")
	if err != nil {
		t.Fatal(err)
	}
}

// TestParseIntervalEmbeddedUnit pins the SQL-standard form
// `interval '<N> <unit>'` where the magnitude and unit share a
// single string literal. HammerDB's TPC-H templates emit this
// shape: Q1's `interval ':1 day'` becomes `interval '90 day'`
// after parameter substitution; Q5/Q6 use `interval '1 year'`;
// Q4 uses `interval '3 month'`. The IntervalLit produced is
// indistinguishable from the trailing-unit form so downstream
// passes don't need to learn about the embedded variant.
func TestParseIntervalEmbeddedUnit(t *testing.T) {
	cases := []struct {
		sql      string
		wantVal  string
		wantUnit string
	}{
		{"SELECT interval '90 day' FROM t", "90", "day"},
		{"SELECT interval '90 days' FROM t", "90", "day"},
		{"SELECT interval '3 month' FROM t", "3", "month"},
		{"SELECT interval '3 months' FROM t", "3", "month"},
		{"SELECT interval '1 year' FROM t", "1", "year"},
		{"SELECT interval '1 years' FROM t", "1", "year"},
		{"SELECT interval '-1 day' FROM t", "-1", "day"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		iv := stmts[0].(*SelectStmt).Targets[0].Expr.(*IntervalLit)
		if iv.Value != tc.wantVal || iv.Unit != tc.wantUnit {
			t.Errorf("%q: got (val=%q, unit=%q), want (val=%q, unit=%q)", tc.sql, iv.Value, iv.Unit, tc.wantVal, tc.wantUnit)
		}
	}
}

// TestParseExtractExpr pins the TPC-H Q7/Q8/Q9 shape:
// `EXTRACT(year FROM o_orderdate)` parses to ExtractExpr
// with the lower-cased field name.
func TestParseExtractExpr(t *testing.T) {
	stmts, err := Parse("SELECT EXTRACT(YEAR FROM o_orderdate) FROM orders")
	if err != nil {
		t.Fatal(err)
	}
	tgt := stmts[0].(*SelectStmt).Targets[0].Expr.(*ExtractExpr)
	if tgt.Field != "year" {
		t.Errorf("Field=%q want year", tgt.Field)
	}
	if _, ok := tgt.Source.(*ColumnRef); !ok {
		t.Errorf("Source=%T want *ColumnRef", tgt.Source)
	}
}
