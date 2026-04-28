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

// TestParseIntervalLiteral pins the unit normalisation
// (plurals collapse to singular) for the three units TPC-H uses.
func TestParseIntervalLiteral(t *testing.T) {
	cases := []struct {
		sql, wantUnit string
	}{
		{"SELECT interval '90' day FROM t", "day"},
		{"SELECT interval '90' days FROM t", "day"},
		{"SELECT interval '3' month FROM t", "month"},
		{"SELECT interval '3' months FROM t", "month"},
		{"SELECT interval '1' year FROM t", "year"},
		{"SELECT interval '1' years FROM t", "year"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		iv := stmts[0].(*SelectStmt).Targets[0].Expr.(*IntervalLit)
		if iv.Unit != tc.wantUnit {
			t.Errorf("%q: unit=%q want %q", tc.sql, iv.Unit, tc.wantUnit)
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
