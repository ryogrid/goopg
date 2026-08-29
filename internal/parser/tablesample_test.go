package parser

import "testing"

// TestTableSampleClause pins the TABLESAMPLE grammar (M0134-0175), including
// the two shapes the oracle distinguishes by ACCEPTING one and REJECTING the
// other: the clause attaches to a relation, never to a derived table.
func TestTableSampleClause(t *testing.T) {
	accept := []struct {
		sql        string
		method     string
		nargs      int
		repeatable bool
	}{
		{"SELECT t.id FROM ts AS t TABLESAMPLE SYSTEM (50) REPEATABLE (0)", "system", 1, true},
		{"SELECT id FROM ts TABLESAMPLE SYSTEM (100.0/11) REPEATABLE (0)", "system", 1, true},
		{"SELECT id FROM ts TABLESAMPLE BERNOULLI (5.5)", "bernoulli", 1, false},
		{"SELECT count(*) FROM ts TABLESAMPLE SYSTEM (100) REPEATABLE (1+2)", "system", 1, true},
		{"SELECT count(*) FROM ts TABLESAMPLE SYSTEM (100) REPEATABLE (0.4)", "system", 1, true},
		// The method name is NOT validated by the grammar: upstream defers it
		// so an unknown method is 42704 with a caret on the name, not a
		// syntax error (parse_clause.c:929).
		{"SELECT id FROM ts TABLESAMPLE FOOBAR (1)", "foobar", 1, false},
		// Downcased regardless of source spelling, as PG's scanner does.
		{"select * from ts tablesample bernoulli (100)", "bernoulli", 1, false},
		{"SELECT id FROM ts ts2 TABLESAMPLE SYSTEM (10)", "system", 1, false},
	}
	for _, c := range accept {
		stmts, err := Parse(c.sql)
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		ts := stmts[0].(*SelectStmt).From[0].TableSample
		if ts == nil {
			t.Errorf("%s: TableSample not attached", c.sql)
			continue
		}
		if ts.Method != c.method || len(ts.Args) != c.nargs || (ts.Repeatable != nil) != c.repeatable {
			t.Errorf("%s: method=%q nargs=%d repeatable=%v, want %q/%d/%v",
				c.sql, ts.Method, len(ts.Args), ts.Repeatable != nil, c.method, c.nargs, c.repeatable)
		}
		// gram.y stamps @2 — the METHOD NAME, not the TABLESAMPLE keyword —
		// because that is where the oracle draws its caret.
		if got, want := ts.Pos(), indexOfMethod(c.sql, c.method); got != want {
			t.Errorf("%s: pos=%d, want %d (method-name offset)", c.sql, got, want)
		}
	}

	// A derived table must NOT accept TABLESAMPLE: upstream attaches the
	// clause to relation_expr only, so this is a syntax error in PG too
	// (expected/tablesample.out: `syntax error at or near "TABLESAMPLE"`).
	reject := []string{
		"SELECT q.* FROM (SELECT * FROM ts) as q TABLESAMPLE BERNOULLI (5)",
	}
	for _, sql := range reject {
		if _, err := Parse(sql); err == nil {
			t.Errorf("%s: accepted, want syntax error", sql)
		}
	}

	// A plain FROM item must still carry no descriptor.
	stmts, err := Parse("SELECT id FROM ts")
	if err != nil {
		t.Fatal(err)
	}
	if stmts[0].(*SelectStmt).From[0].TableSample != nil {
		t.Error("plain FROM item gained a TableSample")
	}
}

func indexOfMethod(sql, method string) int {
	for i := 0; i+len(method) <= len(sql); i++ {
		if lowerEq(sql[i:i+len(method)], method) {
			return i
		}
	}
	return -1
}

func lowerEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 32
		}
		if y >= 'A' && y <= 'Z' {
			y += 32
		}
		if x != y {
			return false
		}
	}
	return true
}
