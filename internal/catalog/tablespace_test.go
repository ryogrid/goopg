package catalog

import (
	"strconv"
	"testing"
)

// TestPgTablespaceVirtualView covers the pg_tablespace virtual view added for
// pg_dump's getTables (M0110-0001 / DU-002): the two bootstrap tablespaces are
// always present, and runtime in-place tablespaces appear ordered by OID.
func TestPgTablespaceVirtualView(t *testing.T) {
	c := NewInMemory()

	tbl, ok := c.tables["pg_catalog.pg_tablespace"]
	if !ok {
		t.Fatal("pg_catalog.pg_tablespace not registered")
	}
	if tbl.VirtualRows == nil {
		t.Fatal("pg_tablespace has no VirtualRows callback")
	}
	want := []string{"oid", "spcname", "spcowner", "spcacl", "spcoptions"}
	if len(tbl.Columns) != len(want) {
		t.Fatalf("pg_tablespace columns=%d want %d", len(tbl.Columns), len(want))
	}
	for i, n := range want {
		if tbl.Columns[i].Name != n {
			t.Fatalf("pg_tablespace column %d = %q want %q", i, tbl.Columns[i].Name, n)
		}
	}

	rows := tbl.VirtualRows()
	if len(rows) != 2 {
		t.Fatalf("bootstrap rows=%d want 2", len(rows))
	}
	if rows[0][0] != "1663" || rows[0][1] != "pg_default" {
		t.Fatalf("row0=%v want [1663 pg_default ...]", rows[0])
	}
	if rows[1][0] != "1664" || rows[1][1] != "pg_global" {
		t.Fatalf("row1=%v want [1664 pg_global ...]", rows[1])
	}

	// A runtime in-place tablespace appears as a third row, after the bootstrap
	// pair, carrying its allocated OID and name.
	oid, err := c.CreateTablespace("myspace", "alice", "")
	if err != nil {
		t.Fatalf("CreateTablespace: %v", err)
	}
	rows = tbl.VirtualRows()
	if len(rows) != 3 {
		t.Fatalf("rows after create=%d want 3", len(rows))
	}
	last := rows[2]
	if last[1] != "myspace" {
		t.Fatalf("runtime row spcname=%q want myspace", last[1])
	}
	if last[0] != strconv.FormatUint(uint64(oid), 10) {
		t.Fatalf("runtime row oid=%q want %d", last[0], oid)
	}
}

// TestPgDependAndForeignTableViews verifies the empty pg_depend and
// pg_foreign_table views are registered with the correct schema so pg_dump's
// catalog queries resolve their column references. M0110-0001 / DU-002.
func TestPgDependAndForeignTableViews(t *testing.T) {
	c := NewInMemory()

	cases := []struct {
		name string
		cols []string
	}{
		{"pg_catalog.pg_depend", []string{"classid", "objid", "objsubid", "refclassid", "refobjid", "refobjsubid", "deptype"}},
		{"pg_catalog.pg_foreign_table", []string{"ftrelid", "ftserver", "ftoptions"}},
	}
	for _, tc := range cases {
		tbl, ok := c.tables[tc.name]
		if !ok {
			t.Fatalf("%s not registered", tc.name)
		}
		if tbl.VirtualRows == nil {
			t.Fatalf("%s has no VirtualRows callback", tc.name)
		}
		if rows := tbl.VirtualRows(); len(rows) != 0 {
			t.Fatalf("%s rows=%d want 0 (empty view)", tc.name, len(rows))
		}
		if len(tbl.Columns) != len(tc.cols) {
			t.Fatalf("%s columns=%d want %d", tc.name, len(tbl.Columns), len(tc.cols))
		}
		for i, n := range tc.cols {
			if tbl.Columns[i].Name != n {
				t.Fatalf("%s column %d = %q want %q", tc.name, i, tbl.Columns[i].Name, n)
			}
		}
	}
}

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
