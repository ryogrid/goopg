package catalog

import "testing"

// TestCreateTablespaceRegistry covers the runtime in-place tablespace registry:
// create allocates a fresh OID, a duplicate name errors, and drop returns the
// OID and removes the entry so the name is reusable. M0095-0003.
func TestCreateTablespaceRegistry(t *testing.T) {
	c := NewInMemory()

	oid1, err := c.CreateTablespace("ts1", "", "")
	if err != nil {
		t.Fatalf("CreateTablespace ts1: %v", err)
	}
	if oid1 == 0 {
		t.Fatal("CreateTablespace returned OID 0")
	}

	oid2, err := c.CreateTablespace("ts2", "alice", "")
	if err != nil {
		t.Fatalf("CreateTablespace ts2: %v", err)
	}
	if oid2 == oid1 {
		t.Fatalf("ts1 and ts2 share OID %d", oid1)
	}

	// Duplicate (case-insensitive) name is rejected.
	if _, err := c.CreateTablespace("TS1", "", ""); err == nil {
		t.Fatal("duplicate CreateTablespace TS1: want error, got nil")
	}

	// Drop returns the OID and clears the entry.
	gotOID, found := c.DropTablespace("ts1")
	if !found {
		t.Fatal("DropTablespace ts1: not found")
	}
	if gotOID != oid1 {
		t.Fatalf("DropTablespace ts1: OID=%d want %d", gotOID, oid1)
	}

	// Dropping again reports not-found.
	if _, found := c.DropTablespace("ts1"); found {
		t.Fatal("second DropTablespace ts1: want not-found")
	}

	// The name is reusable after drop (gets a new OID).
	oid3, err := c.CreateTablespace("ts1", "", "")
	if err != nil {
		t.Fatalf("re-create ts1: %v", err)
	}
	if oid3 == oid1 {
		t.Fatalf("re-created ts1 reused OID %d", oid1)
	}
}
