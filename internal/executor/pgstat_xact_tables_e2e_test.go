package executor

import "testing"

// TestPgStatXactUserTablesEndToEnd drives a real SELECT through the planner and
// executor to confirm pg_stat_xact_user_tables resolves as a virtual view, the
// pg_stat_xact_*_tables valuesOp branch fires, and the connecting database's own
// user table (items, created by newStorageFixture) is projected with real
// relname/schemaname and 0 per-transaction delta counters. M0122-0003.
func TestPgStatXactUserTablesEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT schemaname, relname, seq_scan, n_tup_ins FROM pg_stat_xact_user_tables WHERE relname = 'items'")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1 (items)", len(rows))
	}
	if got := rows[0][0].Format(); got != "public" {
		t.Errorf("schemaname = %q, want public", got)
	}
	if got := rows[0][1].Format(); got != "items" {
		t.Errorf("relname = %q, want items", got)
	}
	// Per-transaction delta counters are honest 0 (no per-xact tracking), not NULL.
	if rows[0][2].IsNull() || rows[0][2].Format() != "0" {
		t.Errorf("seq_scan = %v, want 0", rows[0][2].Format())
	}
	if rows[0][3].IsNull() || rows[0][3].Format() != "0" {
		t.Errorf("n_tup_ins = %v, want 0", rows[0][3].Format())
	}
}

// TestPgStatXactSysTablesExcludesUserTable confirms the schemaname split: a
// public-schema user table appears in pg_stat_xact_user_tables but not
// pg_stat_xact_sys_tables. M0122-0003.
func TestPgStatXactSysTablesExcludesUserTable(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT relname FROM pg_stat_xact_sys_tables WHERE relname = 'items'")
	if len(rows) != 0 {
		t.Fatalf("pg_stat_xact_sys_tables returned %d rows for user table items, want 0", len(rows))
	}
}
