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

// TestPgStatDatabaseEndToEnd drives a real SELECT through the planner and
// executor to confirm pg_stat_database resolves as a virtual view, returns the
// shared-objects row (datid=0, datname NULL) plus one honest-0 row per
// database, and renders NULL for stats_reset. M0122-0003.
func TestPgStatDatabaseEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT datid, datname, numbackends, xact_commit, blks_read, session_time, stats_reset FROM pg_stat_database ORDER BY datid")
	if len(rows) < 1 {
		t.Fatalf("row count = %d, want >= 1", len(rows))
	}
	// datid ordered ascending -> the shared row (datid 0, datname NULL) is first.
	shared := rows[0]
	if shared[0].IsNull() || shared[0].Format() != "0" {
		t.Errorf("shared row datid = %v, want 0", shared[0].Format())
	}
	if !shared[1].IsNull() {
		t.Errorf("shared row datname = %v, want NULL", shared[1].Format())
	}
	// Every row: numbackends/xact_commit/blks_read honest 0, session_time 0,
	// stats_reset NULL.
	for i, row := range rows {
		for c, name := range map[int]string{2: "numbackends", 3: "xact_commit", 4: "blks_read", 5: "session_time"} {
			if row[c].IsNull() || row[c].Format() != "0" {
				t.Errorf("row %d %s = %v, want 0", i, name, row[c].Format())
			}
		}
		if !row[6].IsNull() {
			t.Errorf("row %d stats_reset = %v, want NULL", i, row[6].Format())
		}
	}
}

// TestPgStatDatabaseConflictsEndToEnd drives pg_stat_database_conflicts through
// the full SQL executor: one row per database, NO shared (datid=0) row, and
// every confl_* counter an honest 0 (goopg is a primary with no recovery-
// conflict accumulator). M0122-0003.
func TestPgStatDatabaseConflictsEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT datid, datname, confl_tablespace, confl_lock, confl_snapshot, confl_bufferpin, confl_deadlock, confl_active_logicalslot FROM pg_stat_database_conflicts ORDER BY datid")
	if len(rows) < 1 {
		t.Fatalf("row count = %d, want >= 1", len(rows))
	}
	for i, row := range rows {
		// No shared-objects row: datid is never 0 and datname is never NULL.
		if row[0].IsNull() || row[0].Format() == "0" {
			t.Errorf("row %d datid = %v, want a real database oid (no shared row)", i, row[0].Format())
		}
		if row[1].IsNull() {
			t.Errorf("row %d datname = NULL, want a real database name (no shared row)", i)
		}
		// confl_* counters (cols 2..7) are honest 0 on a primary.
		for c := 2; c <= 7; c++ {
			if row[c].IsNull() || row[c].Format() != "0" {
				t.Errorf("row %d confl col %d = %v, want 0", i, c, row[c].Format())
			}
		}
	}
}
