package catalog

import (
	"errors"
	"strconv"
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
	if _, err := c.CreateDatabase("tpch", BootstrapSuperuserOID); err != nil {
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
	if _, err := c.CreateDatabase("tpch", BootstrapSuperuserOID); err != nil {
		t.Fatalf("first CreateDatabase: %v", err)
	}
	_, err := c.CreateDatabase("tpch", BootstrapSuperuserOID)
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
	c.RegisterDatabaseDuringRecovery("tpch", BootstrapSuperuserOID, 16385)
	c.RegisterDatabaseDuringRecovery("tpch", BootstrapSuperuserOID, 16385) // must not panic / error
	if !c.HasDatabase("tpch") {
		t.Error("after recovery register, tpch should be present")
	}
}

// TestPgDatabaseVirtualRowsEnumeratesRegistry confirms the
// pg_database virtual table backs onto the live database registry —
// not the pre-M0054-0001 hard-coded `postgres` row.
func TestPgDatabaseVirtualRowsEnumeratesRegistry(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateDatabase("tpch", BootstrapSuperuserOID); err != nil {
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

// TestCreateDatabaseAllocatesDistinctDisplayedOid pins the M0122-0007
// physical-storage-isolation slice-1 fix: before this change every
// non-template database rendered the SAME hardcoded "16384" placeholder in
// pg_database.oid, so two CREATE DATABASE calls were indistinguishable by
// oid — a real bug (pg_database.oid is a primary key in upstream PG) and the
// blocking prerequisite for ever allocating a real base/<dbOid> directory.
// The live "postgres" row must keep its pre-existing placeholder unchanged
// (CREATE SUBSCRIPTION's subdbid / datacl heap resync depend on it — see the
// VirtualRows doc comment), so only newly created, non-bootstrap databases
// are expected to diverge.
func TestCreateDatabaseAllocatesDistinctDisplayedOid(t *testing.T) {
	c := NewInMemory()
	oidA, err := c.CreateDatabase("dba", BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase(dba): %v", err)
	}
	oidB, err := c.CreateDatabase("dbb", BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase(dbb): %v", err)
	}
	if oidA == 0 || oidB == 0 || oidA == oidB {
		t.Fatalf("CreateDatabase oids = (%d, %d), want distinct non-zero values", oidA, oidB)
	}
	if got := c.DatabaseOid("dba"); got != oidA {
		t.Errorf("DatabaseOid(dba) = %d, want %d", got, oidA)
	}
	if got := c.DatabaseOid("dbb"); got != oidB {
		t.Errorf("DatabaseOid(dbb) = %d, want %d", got, oidB)
	}
	// The bootstrap rows never went through CreateDatabase — DatabaseOid
	// reports 0 ("no override") for them, same as before this change.
	if got := c.DatabaseOid("postgres"); got != 0 {
		t.Errorf("DatabaseOid(postgres) = %d, want 0 (no override)", got)
	}

	tbl, ok := c.tables["pg_catalog.pg_database"]
	if !ok || tbl.VirtualRows == nil {
		t.Fatalf("pg_database virtual table not registered")
	}
	byName := map[string]string{}
	for _, r := range tbl.VirtualRows() {
		byName[r[1]] = r[0] // r[1] datname, r[0] oid
	}
	wantOidA := strconv.FormatUint(uint64(oidA), 10)
	wantOidB := strconv.FormatUint(uint64(oidB), 10)
	if byName["dba"] != wantOidA {
		t.Errorf("pg_database.oid for dba = %q, want %q", byName["dba"], wantOidA)
	}
	if byName["dbb"] != wantOidB {
		t.Errorf("pg_database.oid for dbb = %q, want %q", byName["dbb"], wantOidB)
	}
	if byName["dba"] == byName["dbb"] {
		t.Errorf("dba and dbb rendered the same pg_database.oid %q — must be distinct", byName["dba"])
	}
	// The live "postgres" row's displayed oid is unchanged by this slice
	// (still the legacy 16384 placeholder — see the VirtualRows doc comment
	// on why it must not diverge).
	if byName["postgres"] != "16384" {
		t.Errorf("pg_database.oid for postgres = %q, want unchanged placeholder 16384", byName["postgres"])
	}
}

// TestResolveDatabaseOid confirms ResolveDatabaseOid reports the REAL
// physical oid used to key on-disk storage/ACLs for each database kind, NOT
// pg_database's displayed-oid placeholder (which TestCreateDatabaseAllocates
// DistinctDisplayedOid above pins as unchanged for "postgres"). M0122-0007
// physical-storage-isolation slice 4a.
func TestResolveDatabaseOid(t *testing.T) {
	c := NewInMemory()
	c.SetDBOID(5) // mirrors detectCatalogDBOID's real-world PG18 "postgres" oid

	if oid, ok := c.ResolveDatabaseOid("postgres"); !ok || oid != 5 {
		t.Errorf("ResolveDatabaseOid(postgres) = (%d, %v), want (5, true)", oid, ok)
	}
	if oid, ok := c.ResolveDatabaseOid("template1"); !ok || oid != 1 {
		t.Errorf("ResolveDatabaseOid(template1) = (%d, %v), want (1, true)", oid, ok)
	}
	if oid, ok := c.ResolveDatabaseOid("template0"); !ok || oid != 4 {
		t.Errorf("ResolveDatabaseOid(template0) = (%d, %v), want (4, true)", oid, ok)
	}

	created, err := c.CreateDatabase("dba", BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase(dba): %v", err)
	}
	if oid, ok := c.ResolveDatabaseOid("dba"); !ok || oid != created {
		t.Errorf("ResolveDatabaseOid(dba) = (%d, %v), want (%d, true)", oid, ok, created)
	}

	if oid, ok := c.ResolveDatabaseOid("no-such-database"); ok || oid != 0 {
		t.Errorf("ResolveDatabaseOid(no-such-database) = (%d, %v), want (0, false)", oid, ok)
	}
}

// TestRegisterDatabaseDuringRecoveryAdvancesNextOID confirms replaying a
// CREATE DATABASE WAL record (which carries the original real oid, M0122-0007
// physical-storage-isolation slice 1) advances the catalog's nextOID counter
// past it — otherwise a later CREATE TABLE/DATABASE in the same process could
// reallocate an oid a crash-recovered database already owns.
func TestRegisterDatabaseDuringRecoveryAdvancesNextOID(t *testing.T) {
	c := NewInMemory()
	const recoveredOid = 50000
	c.RegisterDatabaseDuringRecovery("recovered", BootstrapSuperuserOID, recoveredOid)
	if got := c.DatabaseOid("recovered"); got != recoveredOid {
		t.Errorf("DatabaseOid(recovered) = %d, want %d", got, recoveredOid)
	}
	if c.NextOID() <= recoveredOid {
		t.Errorf("NextOID() = %d after registering oid %d during recovery, want > %d", c.NextOID(), recoveredOid, recoveredOid)
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

// TestResetDatabaseConfigLastEntryDeletesMapKey guards against a phantom
// pg_db_role_setting row: RESETting the last remaining override for a dbOid
// must delete the dbRoleSettings map entry entirely, not leave behind a
// present-but-empty slice — real PG has zero pg_db_role_setting rows for
// this dbOid once its last override is reset (deferral ledger 2026-07-03,
// loop #82 row).
func TestResetDatabaseConfigLastEntryDeletesMapKey(t *testing.T) {
	c := NewInMemory()
	c.SetDatabaseConfig(FirstUserOID, "work_mem", "64MB")
	c.ResetDatabaseConfig(FirstUserOID, "work_mem")

	if _, ok := c.dbRoleSettings[FirstUserOID]; ok {
		t.Errorf("dbRoleSettings still has a key for %d after its last entry was reset", FirstUserOID)
	}
	tbl, ok := c.tables["pg_catalog.pg_db_role_setting"]
	if !ok || tbl.VirtualRows == nil {
		t.Fatalf("pg_db_role_setting virtual table not registered")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("pg_db_role_setting rows = %v, want none after last override reset", rows)
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

// TestSetRoleConfigUpsertsInPlace mirrors TestSetDatabaseConfigUpsertsInPlace
// for the setrole != 0 half (M0119-0004-ACLHEAP, ALTER ROLE ... SET
// follow-up): SetRoleConfig replaces a same-name entry in place and appends
// otherwise, independent of the dbOid scope.
func TestSetRoleConfigUpsertsInPlace(t *testing.T) {
	c := NewInMemory()
	c.RegisterRole("alice")
	roleOid, _ := c.RoleOID("alice")

	c.SetRoleConfig(roleOid, 0, "work_mem", "64MB")
	c.SetRoleConfig(roleOid, 0, "search_path", "public")
	c.SetRoleConfig(roleOid, 0, "work_mem", "128MB")

	got := c.RoleConfigEntries(roleOid, 0)
	want := []string{"work_mem=128MB", "search_path=public"}
	if len(got) != len(want) {
		t.Fatalf("RoleConfigEntries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestRoleConfigScopedByDatabase confirms the same role's cluster-wide
// (dbOid=0) and IN-DATABASE (dbOid=FirstUserOID) overrides are independent
// pg_db_role_setting rows.
func TestRoleConfigScopedByDatabase(t *testing.T) {
	c := NewInMemory()
	c.RegisterRole("alice")
	roleOid, _ := c.RoleOID("alice")

	c.SetRoleConfig(roleOid, 0, "work_mem", "64MB")
	c.SetRoleConfig(roleOid, FirstUserOID, "work_mem", "1MB")

	if got := c.RoleConfigEntries(roleOid, 0); len(got) != 1 || got[0] != "work_mem=64MB" {
		t.Errorf("RoleConfigEntries(role, 0) = %v, want [work_mem=64MB]", got)
	}
	if got := c.RoleConfigEntries(roleOid, FirstUserOID); len(got) != 1 || got[0] != "work_mem=1MB" {
		t.Errorf("RoleConfigEntries(role, FirstUserOID) = %v, want [work_mem=1MB]", got)
	}

	c.ResetAllRoleConfig(roleOid, 0)
	if got := c.RoleConfigEntries(roleOid, 0); len(got) != 0 {
		t.Errorf("RoleConfigEntries(role, 0) after RESET ALL = %v, want empty", got)
	}
	if got := c.RoleConfigEntries(roleOid, FirstUserOID); len(got) != 1 {
		t.Errorf("RESET ALL on dbOid=0 must not touch the IN-DATABASE scope: %v", got)
	}
}

// TestResetRoleConfigRemovesOnlyNamedEntry mirrors
// TestResetDatabaseConfigRemovesOnlyNamedEntry.
func TestResetRoleConfigRemovesOnlyNamedEntry(t *testing.T) {
	c := NewInMemory()
	c.RegisterRole("alice")
	roleOid, _ := c.RoleOID("alice")
	c.SetRoleConfig(roleOid, 0, "work_mem", "64MB")
	c.SetRoleConfig(roleOid, 0, "search_path", "public")
	c.ResetRoleConfig(roleOid, 0, "work_mem")
	got := c.RoleConfigEntries(roleOid, 0)
	if len(got) != 1 || got[0] != "search_path=public" {
		t.Errorf("RoleConfigEntries after reset = %v, want [\"search_path=public\"]", got)
	}
	// Resetting an unrecorded name is a no-op, not an error/panic.
	c.ResetRoleConfig(roleOid, 0, "no_such_guc")
	if len(c.RoleConfigEntries(roleOid, 0)) != 1 {
		t.Errorf("ResetRoleConfig on unknown name should be a no-op")
	}
}

// TestResetRoleConfigLastEntryDeletesMapKey guards against a phantom
// pg_db_role_setting row: RESETting the last remaining override for a
// (roleOid, dbOid) pair must delete the roleSettings map entry entirely, not
// leave behind a present-but-empty slice — AllRoleConfigRows/pg_db_role_setting
// must show zero rows for this role, matching real PG (deferral ledger
// 2026-07-03, loop #82 row).
func TestResetRoleConfigLastEntryDeletesMapKey(t *testing.T) {
	c := NewInMemory()
	c.RegisterRole("alice")
	roleOid, _ := c.RoleOID("alice")
	c.SetRoleConfig(roleOid, 0, "work_mem", "64MB")
	c.ResetRoleConfig(roleOid, 0, "work_mem")

	key := roleSettingKey{RoleOID: roleOid, DBOid: 0}
	if _, ok := c.roleSettings[key]; ok {
		t.Errorf("roleSettings still has a key for %+v after its last entry was reset", key)
	}
	for _, row := range c.AllRoleConfigRows() {
		if row.RoleOID == roleOid {
			t.Errorf("AllRoleConfigRows still contains a row for %d after its last override was reset: %+v", roleOid, row)
		}
	}
}

// TestUnregisterRoleDropsRoleConfigRows verifies DROP ROLE also cascades
// removal of any pg_db_role_setting (setrole != 0) rows keyed by the
// dropped role's OID, in both the cluster-wide (dbOid=0) and IN-DATABASE
// scopes — a pre-existing gap alongside the pg_auth_members sweep in
// TestUnregisterRoleDropsMembershipRows (M0119-0004-ACLHEAP, GRANT/REVOKE
// role membership follow-up, deferral ledger 2026-07-03 row (d)). A leaked
// entry would keep haunting pg_dumpall --globals-only output for a role
// name PG would consider fully gone.
func TestUnregisterRoleDropsRoleConfigRows(t *testing.T) {
	c := NewInMemory()
	c.RegisterRole("alice")
	roleOid, _ := c.RoleOID("alice")
	c.SetRoleConfig(roleOid, 0, "work_mem", "64MB")
	c.SetRoleConfig(roleOid, FirstUserOID, "search_path", "public")

	c.UnregisterRole("alice")

	if got := c.RoleConfigEntries(roleOid, 0); len(got) != 0 {
		t.Errorf("cluster-wide RoleConfigEntries after UnregisterRole = %v, want empty", got)
	}
	if got := c.RoleConfigEntries(roleOid, FirstUserOID); len(got) != 0 {
		t.Errorf("IN-DATABASE RoleConfigEntries after UnregisterRole = %v, want empty", got)
	}
	for _, row := range c.AllRoleConfigRows() {
		if row.RoleOID == roleOid {
			t.Errorf("AllRoleConfigRows still contains a row for the dropped role: %+v", row)
		}
	}
}

// TestPgDbRoleSettingVirtualRowsProjectsRoleOverrides confirms
// pg_db_role_setting also projects setrole != 0 rows (both the cluster-wide
// dbOid=0 and IN-DATABASE FirstUserOID forms) alongside the pre-existing
// setrole=0 database row, sorted deterministically by (RoleOID, DBOid).
func TestPgDbRoleSettingVirtualRowsProjectsRoleOverrides(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.tables["pg_catalog.pg_db_role_setting"]
	if !ok || tbl.VirtualRows == nil {
		t.Fatalf("pg_db_role_setting virtual table not registered")
	}

	c.RegisterRole("alice")
	roleOid, _ := c.RoleOID("alice")
	c.SetDatabaseConfig(FirstUserOID, "log_statement", "all")
	c.SetRoleConfig(roleOid, 0, "work_mem", "64MB")
	c.SetRoleConfig(roleOid, FirstUserOID, "search_path", "public")

	rows := tbl.VirtualRows()
	if len(rows) != 3 {
		t.Fatalf("pg_db_role_setting rows = %v, want exactly 3 rows", rows)
	}
	// row 0: the pre-existing setrole=0 database row.
	if rows[0][0] != "16384" || rows[0][1] != "0" {
		t.Errorf("row 0 (setdatabase, setrole) = (%q, %q), want (16384, 0)", rows[0][0], rows[0][1])
	}
	roleOidStr := strconv.FormatUint(uint64(roleOid), 10)
	// row 1: the role's cluster-wide (dbOid=0) override.
	if rows[1][0] != "0" || rows[1][1] != roleOidStr || rows[1][2] != "{work_mem=64MB}" {
		t.Errorf("row 1 = %v, want (0, %s, {work_mem=64MB})", rows[1], roleOidStr)
	}
	// row 2: the role's IN-DATABASE override.
	if rows[2][0] != "16384" || rows[2][1] != roleOidStr || rows[2][2] != "{search_path=public}" {
		t.Errorf("row 2 = %v, want (16384, %s, {search_path=public})", rows[2], roleOidStr)
	}
}
