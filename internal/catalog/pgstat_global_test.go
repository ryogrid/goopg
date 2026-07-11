package catalog

import "testing"

// TestPGStatBgwriterViewRegistered confirms pg_stat_bgwriter resolves as a
// virtual pg_catalog relation with the upstream PG 17+ 4-column tupledesc
// (the checkpointer columns split out into pg_stat_checkpointer) and returns
// exactly one all-zero summary row instead of an unknown-relation error.
// M0122-0003.
func TestPGStatBgwriterViewRegistered(t *testing.T) {
	c := NewInMemory()
	wantCols := []string{"buffers_clean", "maxwritten_clean", "buffers_alloc", "stats_reset"}
	tbl := c.ns(DefaultDBOid).tables["pg_catalog.pg_stat_bgwriter"]
	if tbl == nil {
		t.Fatal("pg_stat_bgwriter not registered as a virtual pg_catalog table")
	}
	if !tbl.Virtual {
		t.Error("pg_stat_bgwriter: Virtual = false, want true")
	}
	if len(tbl.Columns) != len(wantCols) {
		t.Fatalf("pg_stat_bgwriter: %d columns, want %d", len(tbl.Columns), len(wantCols))
	}
	for i, want := range wantCols {
		if tbl.Columns[i].Name != want {
			t.Errorf("pg_stat_bgwriter: column %d = %q, want %q", i, tbl.Columns[i].Name, want)
		}
	}
	if tbl.VirtualRows == nil {
		t.Fatal("pg_stat_bgwriter: VirtualRows is nil")
	}
	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_stat_bgwriter: VirtualRows() = %d rows, want 1", len(rows))
	}
	// buffers_clean / maxwritten_clean / buffers_alloc are honest 0 (no live
	// bgwriter counter accumulator wired to these columns).
	for i := range 3 {
		if rows[0][i] != "0" {
			t.Errorf("pg_stat_bgwriter: col %d = %q, want 0", i, rows[0][i])
		}
	}
}

// TestPGStatArchiverViewRegistered confirms pg_stat_archiver resolves as a
// virtual pg_catalog relation with the upstream 7-column tupledesc and returns
// exactly one row whose two counts are 0 and whose last_* WAL-name / timestamp
// cells are NULL — matching a real PG 18.3 cluster with archive_mode=off (goopg
// has no WAL archiver). M0122-0003.
func TestPGStatArchiverViewRegistered(t *testing.T) {
	c := NewInMemory()
	wantCols := []string{
		"archived_count", "last_archived_wal", "last_archived_time",
		"failed_count", "last_failed_wal", "last_failed_time", "stats_reset",
	}
	tbl := c.ns(DefaultDBOid).tables["pg_catalog.pg_stat_archiver"]
	if tbl == nil {
		t.Fatal("pg_stat_archiver not registered as a virtual pg_catalog table")
	}
	if !tbl.Virtual {
		t.Error("pg_stat_archiver: Virtual = false, want true")
	}
	if len(tbl.Columns) != len(wantCols) {
		t.Fatalf("pg_stat_archiver: %d columns, want %d", len(tbl.Columns), len(wantCols))
	}
	for i, want := range wantCols {
		if tbl.Columns[i].Name != want {
			t.Errorf("pg_stat_archiver: column %d = %q, want %q", i, tbl.Columns[i].Name, want)
		}
	}
	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_stat_archiver: VirtualRows() = %d rows, want 1", len(rows))
	}
	if rows[0][0] != "0" || rows[0][3] != "0" {
		t.Errorf("pg_stat_archiver: archived_count=%q failed_count=%q, want 0/0", rows[0][0], rows[0][3])
	}
	// last_archived_wal/time and last_failed_wal/time are NULL (never archived,
	// never failed).
	for _, i := range []int{1, 2, 4, 5} {
		if rows[0][i] != VirtualNull {
			t.Errorf("pg_stat_archiver: col %d = %q, want VirtualNull", i, rows[0][i])
		}
	}
}
