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
