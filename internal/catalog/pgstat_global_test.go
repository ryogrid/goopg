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

// TestPGStatDatabaseViewRegistered confirms pg_stat_database resolves as a
// virtual pg_catalog relation with the upstream PG 18.3 30-column tupledesc and
// returns a leading shared-objects row (datid=0, datname=NULL) followed by one
// honest-0 row per database in the live registry. M0122-0003.
func TestPGStatDatabaseViewRegistered(t *testing.T) {
	c := NewInMemory()
	wantCols := []string{
		"datid", "datname", "numbackends", "xact_commit", "xact_rollback",
		"blks_read", "blks_hit", "tup_returned", "tup_fetched", "tup_inserted",
		"tup_updated", "tup_deleted", "conflicts", "temp_files", "temp_bytes",
		"deadlocks", "checksum_failures", "checksum_last_failure", "blk_read_time",
		"blk_write_time", "session_time", "active_time", "idle_in_transaction_time",
		"sessions", "sessions_abandoned", "sessions_fatal", "sessions_killed",
		"parallel_workers_to_launch", "parallel_workers_launched", "stats_reset",
	}
	tbl := c.ns(DefaultDBOid).tables["pg_catalog.pg_stat_database"]
	if tbl == nil {
		t.Fatal("pg_stat_database not registered as a virtual pg_catalog table")
	}
	if !tbl.Virtual {
		t.Error("pg_stat_database: Virtual = false, want true")
	}
	if len(tbl.Columns) != len(wantCols) {
		t.Fatalf("pg_stat_database: %d columns, want %d", len(tbl.Columns), len(wantCols))
	}
	for i, want := range wantCols {
		if tbl.Columns[i].Name != want {
			t.Errorf("pg_stat_database: column %d = %q, want %q", i, tbl.Columns[i].Name, want)
		}
	}
	if tbl.VirtualRows == nil {
		t.Fatal("pg_stat_database: VirtualRows is nil")
	}
	rows := tbl.VirtualRows()
	// At least the shared row + the 3 bootstrap databases (template0/template1/
	// postgres) that ListDatabases seeds.
	if len(rows) < 4 {
		t.Fatalf("pg_stat_database: VirtualRows() = %d rows, want >= 4", len(rows))
	}
	// First row is the shared-objects row: datid 0, datname NULL.
	if rows[0][0] != "0" {
		t.Errorf("pg_stat_database: shared row datid = %q, want 0", rows[0][0])
	}
	if rows[0][1] != VirtualNull {
		t.Errorf("pg_stat_database: shared row datname = %q, want VirtualNull", rows[0][1])
	}
	// Every row: all counters 0, stats_reset (29) and checksum_last_failure (17)
	// NULL. Columns 2..16 and 18..28 are honest 0.
	for r, row := range rows {
		if len(row) != len(wantCols) {
			t.Fatalf("pg_stat_database: row %d has %d cols, want %d", r, len(row), len(wantCols))
		}
		for i := 2; i < len(row); i++ {
			if i == 17 || i == 29 {
				if row[i] != VirtualNull {
					t.Errorf("pg_stat_database: row %d col %d (%s) = %q, want VirtualNull", r, i, wantCols[i], row[i])
				}
				continue
			}
			if row[i] != "0" {
				t.Errorf("pg_stat_database: row %d col %d (%s) = %q, want 0", r, i, wantCols[i], row[i])
			}
		}
	}
	// The three bootstrap databases must appear with their canonical display
	// oids (template1→1, template0→4, postgres→16384 placeholder).
	byName := map[string]string{}
	for _, row := range rows[1:] {
		byName[row[1]] = row[0]
	}
	for name, wantOid := range map[string]string{"template1": "1", "template0": "4", "postgres": "16384"} {
		if got := byName[name]; got != wantOid {
			t.Errorf("pg_stat_database: datid for %q = %q, want %q", name, got, wantOid)
		}
	}
	// CREATE DATABASE must be reflected immediately (VirtualRows enumerates the
	// live registry) with the new database's real distinct display oid.
	if _, err := c.CreateDatabase("statdb", BootstrapSuperuserOID); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	rows2 := tbl.VirtualRows()
	if len(rows2) != len(rows)+1 {
		t.Fatalf("pg_stat_database: after CREATE DATABASE %d rows, want %d", len(rows2), len(rows)+1)
	}
	var found bool
	for _, row := range rows2 {
		if row[1] == "statdb" {
			found = true
			if row[0] != c.databaseDisplayOID("statdb") {
				t.Errorf("pg_stat_database: statdb datid = %q, want %q", row[0], c.databaseDisplayOID("statdb"))
			}
		}
	}
	if !found {
		t.Error("pg_stat_database: new database statdb not listed after CREATE DATABASE")
	}
}

// TestPGStatDatabaseConflictsViewRegistered confirms pg_stat_database_conflicts
// resolves as a virtual pg_catalog relation with the upstream PG 18.3 8-column
// tupledesc and returns one honest-0 row per database — with NO leading
// shared-objects row (upstream's view is a bare "FROM pg_database D"). The
// confl_* counters only ever bump on a standby recovery conflict, so a primary
// (goopg) reports 0 for every database. M0122-0003.
func TestPGStatDatabaseConflictsViewRegistered(t *testing.T) {
	c := NewInMemory()
	wantCols := []string{
		"datid", "datname", "confl_tablespace", "confl_lock", "confl_snapshot",
		"confl_bufferpin", "confl_deadlock", "confl_active_logicalslot",
	}
	tbl := c.ns(DefaultDBOid).tables["pg_catalog.pg_stat_database_conflicts"]
	if tbl == nil {
		t.Fatal("pg_stat_database_conflicts not registered as a virtual pg_catalog table")
	}
	if !tbl.Virtual {
		t.Error("pg_stat_database_conflicts: Virtual = false, want true")
	}
	if len(tbl.Columns) != len(wantCols) {
		t.Fatalf("pg_stat_database_conflicts: %d columns, want %d", len(tbl.Columns), len(wantCols))
	}
	for i, want := range wantCols {
		if tbl.Columns[i].Name != want {
			t.Errorf("pg_stat_database_conflicts: column %d = %q, want %q", i, tbl.Columns[i].Name, want)
		}
	}
	if tbl.VirtualRows == nil {
		t.Fatal("pg_stat_database_conflicts: VirtualRows is nil")
	}
	rows := tbl.VirtualRows()
	// One row per bootstrap database (template0/template1/postgres) — NO leading
	// shared row, so the datid=0 sentinel must NOT appear.
	if len(rows) < 3 {
		t.Fatalf("pg_stat_database_conflicts: VirtualRows() = %d rows, want >= 3", len(rows))
	}
	for r, row := range rows {
		if len(row) != len(wantCols) {
			t.Fatalf("pg_stat_database_conflicts: row %d has %d cols, want %d", r, len(row), len(wantCols))
		}
		if row[0] == "0" || row[1] == VirtualNull {
			t.Errorf("pg_stat_database_conflicts: row %d is a shared-objects row (datid=%q datname=%q); upstream has none", r, row[0], row[1])
		}
		// confl_* counters (cols 2..7) are honest 0 on a primary.
		for i := 2; i < len(row); i++ {
			if row[i] != "0" {
				t.Errorf("pg_stat_database_conflicts: row %d col %d (%s) = %q, want 0", r, i, wantCols[i], row[i])
			}
		}
	}
	// datid must join to pg_database.oid exactly as pg_stat_database does (shared
	// databaseDisplayOID helper). Bootstrap databases carry canonical display oids.
	byName := map[string]string{}
	for _, row := range rows {
		byName[row[1]] = row[0]
	}
	for name, wantOid := range map[string]string{"template1": "1", "template0": "4", "postgres": "16384"} {
		if got := byName[name]; got != wantOid {
			t.Errorf("pg_stat_database_conflicts: datid for %q = %q, want %q", name, got, wantOid)
		}
	}
	// CREATE DATABASE reflected immediately.
	if _, err := c.CreateDatabase("confldb", BootstrapSuperuserOID); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	rows2 := tbl.VirtualRows()
	if len(rows2) != len(rows)+1 {
		t.Fatalf("pg_stat_database_conflicts: after CREATE DATABASE %d rows, want %d", len(rows2), len(rows)+1)
	}
	var found bool
	for _, row := range rows2 {
		if row[1] == "confldb" {
			found = true
			if row[0] != c.databaseDisplayOID("confldb") {
				t.Errorf("pg_stat_database_conflicts: confldb datid = %q, want %q", row[0], c.databaseDisplayOID("confldb"))
			}
		}
	}
	if !found {
		t.Error("pg_stat_database_conflicts: new database confldb not listed after CREATE DATABASE")
	}
}
