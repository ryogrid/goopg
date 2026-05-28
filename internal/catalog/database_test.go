package catalog

import (
	"errors"
	"testing"
)

// TestNewInMemorySeedsPostgresDatabase pins the M0054-0001 invariant
// that a fresh catalog reports the conventional `postgres` bootstrap
// database. Connection-startup probes
// (`SELECT 1 FROM pg_database WHERE datname='postgres'`) depend on it.
func TestNewInMemorySeedsPostgresDatabase(t *testing.T) {
	c := NewInMemory()
	if !c.HasDatabase("postgres") {
		t.Error("fresh catalog should have postgres in the database registry")
	}
	if got := c.ListDatabases(); len(got) != 1 || got[0] != "postgres" {
		t.Errorf("ListDatabases = %v, want [postgres]", got)
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
