package executor

import "testing"

// TestPgStatUserTablesEndToEnd drives a real SELECT through the planner and
// executor to confirm pg_stat_user_tables resolves as a virtual view, the
// pg_stat_*_tables valuesOp branch fires, and the connecting database's own user
// table (items, created by newStorageFixture) is projected with real
// relname/schemaname. M0122-0003.
func TestPgStatUserTablesEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT schemaname, relname, n_live_tup FROM pg_stat_user_tables WHERE relname = 'items'")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1 (items)", len(rows))
	}
	if got := rows[0][0].Format(); got != "public" {
		t.Errorf("schemaname = %q, want public", got)
	}
	if got := rows[0][1].Format(); got != "items" {
		t.Errorf("relname = %q, want items", got)
	}
	// n_live_tup is non-NULL (an honest integer, 0 until ANALYZE runs).
	if rows[0][2].IsNull() {
		t.Errorf("n_live_tup is NULL, want an integer")
	}
}

// TestPgStatSysTablesExcludesUserTable confirms the schemaname split: a
// public-schema user table appears in pg_stat_user_tables but not
// pg_stat_sys_tables. M0122-0003.
func TestPgStatSysTablesExcludesUserTable(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT relname FROM pg_stat_sys_tables WHERE relname = 'items'")
	if len(rows) != 0 {
		t.Fatalf("pg_stat_sys_tables returned %d rows for user table items, want 0", len(rows))
	}
}
