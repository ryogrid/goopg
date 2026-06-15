package parser

import (
	"testing"
)

// TestParseNamedArgColonEqual covers the legacy `name := value` named-argument
// spelling that `pg_amcheck` emits for the amcheck SQL surface (M0110-0003 S5).
// goopg maps named arguments positionally (M0097-0003), so the assertion is that
// the names are stripped and the remaining positional values land in fc.Args in
// the order written — which matches the positional order the verify_heapam /
// bt_index_check executors expect.
func TestParseNamedArgColonEqual(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantArgs int
	}{
		{
			// pg_amcheck prepare_btree_command (non-parent form).
			name:     "bt_index_check",
			sql:      `SELECT bt_index_check(index := c.oid, heapallindexed := false)`,
			wantArgs: 2,
		},
		{
			// pg_amcheck prepare_btree_command (parent form, with checkunique).
			name:     "bt_index_parent_check",
			sql:      `SELECT bt_index_parent_check(index := c.oid, heapallindexed := false, rootdescend := false, checkunique := true)`,
			wantArgs: 4,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if len(stmts) != 1 {
				t.Fatalf("expected 1 stmt, got %d", len(stmts))
			}
			sel, ok := stmts[0].(*SelectStmt)
			if !ok {
				t.Fatalf("expected *SelectStmt, got %T", stmts[0])
			}
			args := targetFuncArgs(sel)
			if args == nil {
				t.Fatalf("no *FuncCall found in parsed statement")
			}
			if len(args) != tc.wantArgs {
				t.Fatalf("expected %d positional args after name-stripping, got %d", tc.wantArgs, len(args))
			}
		})
	}
}

// TestParseNamedArgColonEqualFromSRF covers the FROM-clause SRF sibling path:
// pg_amcheck's prepare_heap_command emits verify_heapam(...) with `:=` named
// arguments inside a comma-join with pg_class (an implicit-LATERAL correlation).
// Parsing must strip the names and keep the six positional args in order; the
// `c.oid` correlated reference parses as a ColumnRef (lateral *resolution* is a
// separate planner concern, intentionally not exercised here).
func TestParseNamedArgColonEqualFromSRF(t *testing.T) {
	sql := `SELECT v.blkno FROM pg_catalog.pg_class c, ` +
		`verify_heapam(relation := c.oid, on_error_stop := false, check_toast := false, ` +
		`skip := 'none', startblock := 0, endblock := 0) v`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	sel, ok := stmts[0].(*SelectStmt)
	if !ok {
		t.Fatalf("expected *SelectStmt, got %T", stmts[0])
	}
	var tf *TableFuncRef
	for i := range sel.From {
		if sel.From[i].TableFunc != nil {
			tf = sel.From[i].TableFunc
			break
		}
	}
	if tf == nil {
		t.Fatalf("no TableFuncRef found in FROM clause")
	}
	if tf.Name != "verify_heapam" {
		t.Fatalf("expected verify_heapam SRF, got %q", tf.Name)
	}
	if len(tf.Args) != 6 {
		t.Fatalf("expected 6 positional args after name-stripping, got %d", len(tf.Args))
	}
	// First positional arg is the correlated `c.oid` reference.
	if _, ok := tf.Args[0].(*ColumnRef); !ok {
		t.Fatalf("expected first arg to be *ColumnRef (c.oid), got %T", tf.Args[0])
	}
}

// TestParseNamedArgColonEqualEquivalentToFatArrow confirms `:=` and `=>` produce
// the same positional argument list, so the two spellings are interchangeable.
func TestParseNamedArgColonEqualEquivalentToFatArrow(t *testing.T) {
	colonEq, err := Parse(`SELECT bt_index_check(index := c.oid, heapallindexed := false)`)
	if err != nil {
		t.Fatalf("Parse(:=) error: %v", err)
	}
	fatArrow, err := Parse(`SELECT bt_index_check(index => c.oid, heapallindexed => false)`)
	if err != nil {
		t.Fatalf("Parse(=>) error: %v", err)
	}
	a := targetFuncArgs(colonEq[0].(*SelectStmt))
	b := targetFuncArgs(fatArrow[0].(*SelectStmt))
	if a == nil || b == nil {
		t.Fatalf("missing FuncCall: := %v, => %v", a, b)
	}
	if len(a) != len(b) {
		t.Fatalf(":= produced %d args, => produced %d", len(a), len(b))
	}
}

// targetFuncArgs returns the Args of the first *FuncCall in the select's target
// list, or nil if there is none.
func targetFuncArgs(sel *SelectStmt) []Expr {
	for _, tgt := range sel.Targets {
		if fc, ok := tgt.Expr.(*FuncCall); ok {
			return fc.Args
		}
	}
	return nil
}
