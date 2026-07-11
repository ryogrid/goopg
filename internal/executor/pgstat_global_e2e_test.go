package executor

import "testing"

// TestPgStatBgwriterEndToEnd drives a real SELECT through the planner and
// executor to confirm pg_stat_bgwriter resolves as a virtual view and returns
// exactly one all-zero global summary row. M0122-0003.
func TestPgStatBgwriterEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT buffers_clean, maxwritten_clean, buffers_alloc FROM pg_stat_bgwriter")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	for i, name := range []string{"buffers_clean", "maxwritten_clean", "buffers_alloc"} {
		if rows[0][i].IsNull() || rows[0][i].Format() != "0" {
			t.Errorf("%s = %v, want 0", name, rows[0][i].Format())
		}
	}
}

// TestPgStatArchiverEndToEnd confirms pg_stat_archiver resolves and reports 0
// counts with NULL last_* cells (goopg has no WAL archiver, archive_mode off).
// M0122-0003.
func TestPgStatArchiverEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT archived_count, last_archived_wal, failed_count, last_failed_time FROM pg_stat_archiver")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if rows[0][0].IsNull() || rows[0][0].Format() != "0" {
		t.Errorf("archived_count = %v, want 0", rows[0][0].Format())
	}
	if rows[0][2].IsNull() || rows[0][2].Format() != "0" {
		t.Errorf("failed_count = %v, want 0", rows[0][2].Format())
	}
	if !rows[0][1].IsNull() {
		t.Errorf("last_archived_wal = %v, want NULL", rows[0][1].Format())
	}
	if !rows[0][3].IsNull() {
		t.Errorf("last_failed_time = %v, want NULL", rows[0][3].Format())
	}
}
