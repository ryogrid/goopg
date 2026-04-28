package parser

import (
	"testing"
)

// TestParseFetchFirstAsLimit pins the TPC-H Q2/Q3/Q10/Q18/Q21
// shape: `FETCH FIRST n ROWS ONLY` parses as a synonym for
// LIMIT n. Both `FIRST`/`NEXT`, `ROW`/`ROWS`, and the
// count-omitted form are accepted.
func TestParseFetchFirstAsLimit(t *testing.T) {
	cases := []struct{ sql string }{
		{"SELECT id FROM t FETCH FIRST 3 ROWS ONLY"},
		{"SELECT id FROM t FETCH FIRST 1 ROW ONLY"},
		{"SELECT id FROM t FETCH FIRST ROW ONLY"},
		{"SELECT id FROM t FETCH NEXT 2 ROWS ONLY"},
		{"SELECT id FROM t OFFSET 2 ROWS FETCH NEXT 2 ROWS ONLY"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		sel := stmts[0].(*SelectStmt)
		if sel.Limit == nil {
			t.Errorf("%q: Limit is nil", tc.sql)
		}
	}
}

// TestParseFetchFirstRejectsCombinedLimit pins that LIMIT and
// FETCH FIRST in the same SELECT is a syntax error.
func TestParseFetchFirstRejectsCombinedLimit(t *testing.T) {
	_, err := Parse("SELECT id FROM t LIMIT 5 FETCH FIRST 3 ROWS ONLY")
	if err == nil {
		t.Errorf("expected error for combined LIMIT + FETCH FIRST")
	}
}
