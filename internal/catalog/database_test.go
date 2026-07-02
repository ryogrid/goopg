package catalog

import (
	"errors"
	"testing"
)

// TestNewInMemorySeedsPostgresDatabase pins the M0054-0001 invariant
// that a fresh catalog reports the three conventional bootstrap
// databases (`postgres`, `template1`, `template0`), mirroring initdb's
// pg_database seed. Connection-startup probes
// (`SELECT 1 FROM pg_database WHERE datname='postgres'`) and pg_amcheck's
// database-list resolution (M0110-0003 AC-002) depend on them.
func TestNewInMemorySeedsPostgresDatabase(t *testing.T) {
	c := NewInMemory()
	for _, want := range []string{"postgres", "template1", "template0"} {
		if !c.HasDatabase(want) {
			t.Errorf("fresh catalog should have %q in the database registry", want)
		}
	}
	got := c.ListDatabases()
	if len(got) != 3 {
		t.Errorf("ListDatabases = %v, want the 3 bootstrap databases", got)
	}
}

// TestCreateDropDatabaseRoundTrip confirms the basic create-then-drop
// flow used by HammerDB's setup path.
func TestCreateDropDatabaseRoundTrip(t *testing.T) {
	c := NewInMemory()
	if err := c.CreateDatabase("tpch"); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if !c.HasDatabase("tpch") {
		t.Error("HasDatabase(tpch) = false after create")
	}
	if err := c.DropDatabase("tpch"); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}
	if c.HasDatabase("tpch") {
		t.Error("HasDatabase(tpch) = true after drop")
	}
}

// TestCreateDatabaseDuplicateReturnsErrDatabaseExists matches upstream
// PostgreSQL's behaviour (SQLSTATE 42P04).
func TestCreateDatabaseDuplicateReturnsErrDatabaseExists(t *testing.T) {
	c := NewInMemory()
	if err := c.CreateDatabase("tpch"); err != nil {
		t.Fatalf("first CreateDatabase: %v", err)
	}
	err := c.CreateDatabase("tpch")
	if !errors.Is(err, ErrDatabaseExists) {
		t.Errorf("second CreateDatabase err = %v, want ErrDatabaseExists", err)
	}
}

// TestDropDatabaseUnknownReturnsErrDatabaseNotFound — same upstream
// alignment for DROP without IF EXISTS.
func TestDropDatabaseUnknownReturnsErrDatabaseNotFound(t *testing.T) {
	c := NewInMemory()
	err := c.DropDatabase("nope")
	if !errors.Is(err, ErrDatabaseNotFound) {
		t.Errorf("DropDatabase err = %v, want ErrDatabaseNotFound", err)
	}
}

// TestRegisterDatabaseDuringRecoveryIsIdempotent — the recovery
// driver may replay a CREATE DATABASE record whose effect is already
// captured in the in-memory state (e.g. because a SaveCatalog
// snapshot persisted it). The recovery hook must NOT surface
// "database already exists" in that case.
func TestRegisterDatabaseDuringRecoveryIsIdempotent(t *testing.T) {
	c := NewInMemory()
	c.RegisterDatabaseDuringRecovery("tpch")
	c.RegisterDatabaseDuringRecovery("tpch") // must not panic / error
	if !c.HasDatabase("tpch") {
		t.Error("after recovery register, tpch should be present")
	}
}

// TestPgDatabaseVirtualRowsEnumeratesRegistry confirms the
// pg_database virtual table backs onto the live database registry —
// not the pre-M0054-0001 hard-coded `postgres` row.
func TestPgDatabaseVirtualRowsEnumeratesRegistry(t *testing.T) {
	c := NewInMemory()
	if err := c.CreateDatabase("tpch"); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	// Reach directly into the tables map; LookupTable takes a
	// parser.ObjectName which would pull the parser package into this
	// test for no real benefit.
	tbl, ok := c.tables["pg_catalog.pg_database"]
	if !ok || tbl.VirtualRows == nil {
		t.Fatalf("pg_database virtual table not registered")
	}
	rows := tbl.VirtualRows()
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r[1]] = true // r[1] is datname; r[0] is oid
	}
	if !seen["postgres"] || !seen["tpch"] {
		t.Errorf("pg_database rows = %v, want both postgres and tpch", rows)
	}
}

// TestPgDatabaseExposesFrozenXidColumns pins the M0117-0008 catalog-parity
// addition: pg_database now projects datfrozenxid and datminmxid so monitoring
// queries (`age(datfrozenxid)`) and the intra-grant-inplace-db isolation spec
// resolve the columns. With no user heap frozen yet datfrozenxid reports the
// bootstrap FrozenTransactionID(2); datminmxid is the FirstMultiXactId(1) floor.
func TestPgDatabaseExposesFrozenXidColumns(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.tables["pg_catalog.pg_database"]
	if !ok {
		t.Fatalf("pg_database virtual table not registered")
	}
	colIdx := map[string]int{}
	for i, col := range tbl.Columns {
		colIdx[col.Name] = i
	}
	fi, ok := colIdx["datfrozenxid"]
	if !ok {
		t.Fatalf("pg_database missing datfrozenxid column; columns=%v", tbl.Columns)
	}
	mi, ok := colIdx["datminmxid"]
	if !ok {
		t.Fatalf("pg_database missing datminmxid column; columns=%v", tbl.Columns)
	}
	rows := tbl.VirtualRows()
	if len(rows) == 0 {
		t.Fatalf("pg_database returned no rows")
	}
	for _, r := range rows {
		if r[fi] != "2" {
			t.Errorf("datfrozenxid = %q for db %q, want bootstrap floor 2", r[fi], r[1])
		}
		if r[mi] != "1" {
			t.Errorf("datminmxid = %q for db %q, want FirstMultiXactId 1", r[mi], r[1])
		}
	}
}

// TestSetDatabaseConfigUpsertsInPlace pins SetDatabaseConfig's PG-mirroring
// GUC_array_change behaviour: re-SET of an already-overridden name replaces
// the existing entry in place (same slice position) rather than appending a
// duplicate. M0119-0004-ACLHEAP (ALTER DATABASE ... SET follow-up).
func TestSetDatabaseConfigUpsertsInPlace(t *testing.T) {
	c := NewInMemory()
	c.SetDatabaseConfig(FirstUserOID, "work_mem", "64MB")
	c.SetDatabaseConfig(FirstUserOID, "search_path", "public")
	c.SetDatabaseConfig(FirstUserOID, "work_mem", "128MB")

	got := c.DatabaseConfigEntries(FirstUserOID)
	want := []string{"work_mem=128MB", "search_path=public"}
	if len(got) != len(want) {
		t.Fatalf("DatabaseConfigEntries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestSetDatabaseConfigNameIsCaseInsensitive mirrors PG's GUC name matching:
// re-SET under a differently-cased spelling of an already-set name still
// replaces in place.
func TestSetDatabaseConfigNameIsCaseInsensitive(t *testing.T) {
	c := NewInMemory()
	c.SetDatabaseConfig(FirstUserOID, "Work_Mem", "64MB")
	c.SetDatabaseConfig(FirstUserOID, "work_mem", "128MB")
	got := c.DatabaseConfigEntries(FirstUserOID)
	if len(got) != 1 || got[0] != "work_mem=128MB" {
		t.Errorf("DatabaseConfigEntries = %v, want [\"work_mem=128MB\"]", got)
	}
}

// TestResetDatabaseConfigRemovesOnlyNamedEntry confirms RESET removes just
// the matching entry, leaving sibling overrides untouched.
func TestResetDatabaseConfigRemovesOnlyNamedEntry(t *testing.T) {
	c := NewInMemory()
	c.SetDatabaseConfig(FirstUserOID, "work_mem", "64MB")
	c.SetDatabaseConfig(FirstUserOID, "search_path", "public")
	c.ResetDatabaseConfig(FirstUserOID, "work_mem")
	got := c.DatabaseConfigEntries(FirstUserOID)
	if len(got) != 1 || got[0] != "search_path=public" {
		t.Errorf("DatabaseConfigEntries after reset = %v, want [\"search_path=public\"]", got)
	}
	// Resetting an unrecorded name is a no-op, not an error/panic.
	c.ResetDatabaseConfig(FirstUserOID, "no_such_guc")
	if len(c.DatabaseConfigEntries(FirstUserOID)) != 1 {
		t.Errorf("ResetDatabaseConfig on unknown name should be a no-op")
	}
}

// TestResetAllDatabaseConfigClearsEverything pins RESET ALL's full-clear
// semantics.
func TestResetAllDatabaseConfigClearsEverything(t *testing.T) {
	c := NewInMemory()
	c.SetDatabaseConfig(FirstUserOID, "work_mem", "64MB")
	c.SetDatabaseConfig(FirstUserOID, "search_path", "public")
	c.ResetAllDatabaseConfig(FirstUserOID)
	if got := c.DatabaseConfigEntries(FirstUserOID); len(got) != 0 {
		t.Errorf("DatabaseConfigEntries after RESET ALL = %v, want empty", got)
	}
}

// TestPgDbRoleSettingVirtualRowsProjectsOverrides confirms the previously
// permanently-empty pg_db_role_setting virtual table now projects real
// SetDatabaseConfig state as one row keyed by FirstUserOID/setrole=0, with
// setconfig rendered as a PG-native text[] literal.
func TestPgDbRoleSettingVirtualRowsProjectsOverrides(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.tables["pg_catalog.pg_db_role_setting"]
	if !ok || tbl.VirtualRows == nil {
		t.Fatalf("pg_db_role_setting virtual table not registered")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("fresh catalog: pg_db_role_setting rows = %v, want none", rows)
	}

	c.SetDatabaseConfig(FirstUserOID, "work_mem", "64MB")
	c.SetDatabaseConfig(FirstUserOID, "search_path", "public,pg_catalog")
	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_db_role_setting rows = %v, want exactly 1 row", rows)
	}
	row := rows[0]
	if row[0] != "16384" {
		t.Errorf("setdatabase = %q, want FirstUserOID (16384)", row[0])
	}
	if row[1] != "0" {
		t.Errorf("setrole = %q, want \"0\"", row[1])
	}
	want := `{work_mem=64MB,"search_path=public,pg_catalog"}`
	if row[2] != want {
		t.Errorf("setconfig = %q, want %q", row[2], want)
	}
}
