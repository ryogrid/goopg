package parser

import "testing"

// TestParseSetTransactionCommaSeparated covers the comma-separated transaction
// mode list pg_dump's setup_connection() emits:
//
//	SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY
//
// The mode loop previously stopped at the comma (default → goto setTxDone),
// leaving `, READ ONLY` as trailing tokens → parse error. The loop now consumes
// an optional comma between modes. (M0110-0001, pg_dump connection setup.)
func TestParseSetTransactionCommaSeparated(t *testing.T) {
	cases := []struct {
		sql       string
		wantLevel string
	}{
		{"SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY", "repeatable read"},
		{"SET TRANSACTION READ ONLY, ISOLATION LEVEL SERIALIZABLE", "serializable"},
		{"SET TRANSACTION ISOLATION LEVEL READ COMMITTED", "read committed"},
		{"SET TRANSACTION READ ONLY", ""},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", tc.sql, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("Parse(%q): got %d stmts, want 1", tc.sql, len(stmts))
		}
		st, ok := stmts[0].(*SetTransactionStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T, want *SetTransactionStmt", tc.sql, stmts[0])
		}
		if st.IsolationLevel != tc.wantLevel {
			t.Errorf("Parse(%q): IsolationLevel=%q want %q", tc.sql, st.IsolationLevel, tc.wantLevel)
		}
	}
}
