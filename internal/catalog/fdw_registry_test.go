package catalog

import (
	"strconv"
	"testing"
)

// TestForeignDataWrapperRegistry pins the FDW registry that lets a CREATE
// FOREIGN DATA WRAPPER round-trip through pg_dump (DU-002 slice 375):
// registration mints a stable OID, re-registration is idempotent (no OID
// churn), Drop removes it, and pg_foreign_data_wrapper surfaces every
// registered FDW with the columns getForeignDataWrappers reads.
func TestForeignDataWrapperRegistry(t *testing.T) {
	c := NewInMemory()

	// Empty registry → view is empty (mirrors a fresh server).
	if rows := c.tables["pg_catalog.pg_foreign_data_wrapper"].VirtualRows(); len(rows) != 0 {
		t.Fatalf("empty FDW registry: pg_foreign_data_wrapper has %d rows, want 0", len(rows))
	}

	f := c.RegisterForeignDataWrapper("myfdw")
	if f.OID < FirstUserOID {
		t.Fatalf("RegisterForeignDataWrapper minted OID %d below FirstUserOID %d", f.OID, FirstUserOID)
	}
	// Idempotent: same name returns the same OID, no churn.
	if f2 := c.RegisterForeignDataWrapper("myfdw"); f2.OID != f.OID {
		t.Fatalf("re-register myfdw changed OID: %d → %d", f.OID, f2.OID)
	}

	// A second FDW gets a distinct OID; ListForeignDataWrappers is name-sorted.
	c.RegisterForeignDataWrapper("alpha_fdw")
	list := c.ListForeignDataWrappers()
	if len(list) != 2 || list[0].Name != "alpha_fdw" || list[1].Name != "myfdw" {
		t.Fatalf("ListForeignDataWrappers = %+v; want [alpha_fdw, myfdw]", list)
	}
	if list[0].OID == list[1].OID {
		t.Fatalf("distinct FDWs share OID %d", list[0].OID)
	}

	// The virtual view exposes the getForeignDataWrappers columns: handler and
	// validator are 0 (regproc 0 → '-'), acl/options are NULL (empty), owner is
	// the bootstrap superuser (10).
	rows := c.tables["pg_catalog.pg_foreign_data_wrapper"].VirtualRows()
	if len(rows) != 2 {
		t.Fatalf("pg_foreign_data_wrapper has %d rows, want 2", len(rows))
	}
	myrow := rows[1] // sorted: alpha_fdw, myfdw
	want := []string{strconv.FormatUint(uint64(f.OID), 10), "myfdw", "10", "0", "0", "", ""}
	if len(myrow) != len(want) {
		t.Fatalf("FDW row width %d, want %d", len(myrow), len(want))
	}
	for i := range want {
		if myrow[i] != want[i] {
			t.Fatalf("FDW row col %d = %q, want %q (full row %+v)", i, myrow[i], want[i], myrow)
		}
	}

	// Drop removes it from the view; dropping a missing name is a no-op.
	if !c.DropForeignDataWrapper("myfdw") {
		t.Fatalf("DropForeignDataWrapper(myfdw) returned false")
	}
	if c.DropForeignDataWrapper("myfdw") {
		t.Fatalf("DropForeignDataWrapper(myfdw) second call returned true")
	}
	if rows := c.tables["pg_catalog.pg_foreign_data_wrapper"].VirtualRows(); len(rows) != 1 {
		t.Fatalf("after drop: pg_foreign_data_wrapper has %d rows, want 1", len(rows))
	}
}
