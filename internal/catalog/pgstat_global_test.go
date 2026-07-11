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

// TestPGStatProgressViewsRegistered confirms every pg_stat_progress_* view
// resolves as a virtual pg_catalog relation with the upstream PG 18.3 tupledesc
// (column names transcribed from system_views.sql) and returns ZERO rows: goopg
// does not instrument command progress, so — exactly like an idle real PG
// cluster with no VACUUM/ANALYZE/etc. in flight — these views are empty.
// M0122-0003.
func TestPGStatProgressViewsRegistered(t *testing.T) {
	c := NewInMemory()
	want := map[string][]string{
		"pg_stat_progress_vacuum": {
			"pid", "datid", "datname", "relid", "phase", "heap_blks_total",
			"heap_blks_scanned", "heap_blks_vacuumed", "index_vacuum_count",
			"max_dead_tuple_bytes", "dead_tuple_bytes", "num_dead_item_ids",
			"indexes_total", "indexes_processed", "delay_time",
		},
		"pg_stat_progress_analyze": {
			"pid", "datid", "datname", "relid", "phase", "sample_blks_total",
			"sample_blks_scanned", "ext_stats_total", "ext_stats_computed",
			"child_tables_total", "child_tables_done", "current_child_table_relid",
			"delay_time",
		},
		"pg_stat_progress_cluster": {
			"pid", "datid", "datname", "relid", "command", "phase",
			"cluster_index_relid", "heap_tuples_scanned", "heap_tuples_written",
			"heap_blks_total", "heap_blks_scanned", "index_rebuild_count",
		},
		"pg_stat_progress_create_index": {
			"pid", "datid", "datname", "relid", "index_relid", "command", "phase",
			"lockers_total", "lockers_done", "current_locker_pid", "blocks_total",
			"blocks_done", "tuples_total", "tuples_done", "partitions_total",
			"partitions_done",
		},
		"pg_stat_progress_basebackup": {
			"pid", "phase", "backup_total", "backup_streamed", "tablespaces_total",
			"tablespaces_streamed",
		},
		"pg_stat_progress_copy": {
			"pid", "datid", "datname", "relid", "command", "type", "bytes_processed",
			"bytes_total", "tuples_processed", "tuples_excluded", "tuples_skipped",
		},
	}
	for name, wantCols := range want {
		tbl := c.ns(DefaultDBOid).tables["pg_catalog."+name]
		if tbl == nil {
			t.Errorf("%s not registered as a virtual pg_catalog table", name)
			continue
		}
		if !tbl.Virtual {
			t.Errorf("%s: Virtual = false, want true", name)
		}
		if len(tbl.Columns) != len(wantCols) {
			t.Errorf("%s: %d columns, want %d", name, len(tbl.Columns), len(wantCols))
			continue
		}
		for i, wc := range wantCols {
			if tbl.Columns[i].Name != wc {
				t.Errorf("%s: column %d = %q, want %q", name, i, tbl.Columns[i].Name, wc)
			}
		}
		if tbl.VirtualRows == nil {
			t.Errorf("%s: VirtualRows is nil", name)
			continue
		}
		if rows := tbl.VirtualRows(); len(rows) != 0 {
			t.Errorf("%s: VirtualRows() = %d rows, want 0 (no progress instrumentation)", name, len(rows))
		}
	}
}
