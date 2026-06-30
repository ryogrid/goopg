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

// TestForeignServerRegistry pins the foreign-server registry that lets a CREATE
// SERVER round-trip through pg_dump (DU-002 slice 376): registration mints a
// stable OID, re-registration is idempotent (no OID churn) but refreshes the FDW
// association, Drop removes it, and pg_foreign_server surfaces every registered
// server with the columns getForeignServers reads — crucially resolving srvfdw
// to the referenced FDW's stable OID.
func TestForeignServerRegistry(t *testing.T) {
	c := NewInMemory()

	// Empty registry → view is empty (mirrors a fresh server).
	if rows := c.tables["pg_catalog.pg_foreign_server"].VirtualRows(); len(rows) != 0 {
		t.Fatalf("empty server registry: pg_foreign_server has %d rows, want 0", len(rows))
	}

	// The server references an FDW by name; srvfdw must resolve to its OID.
	fdw := c.RegisterForeignDataWrapper("goopg_fdw")
	s := c.RegisterForeignServer("srv1", "goopg_fdw")
	if s.OID < FirstUserOID {
		t.Fatalf("RegisterForeignServer minted OID %d below FirstUserOID %d", s.OID, FirstUserOID)
	}
	// Idempotent: same name returns the same OID, no churn.
	if s2 := c.RegisterForeignServer("srv1", "goopg_fdw"); s2.OID != s.OID {
		t.Fatalf("re-register srv1 changed OID: %d → %d", s.OID, s2.OID)
	}

	// A second server gets a distinct OID; ListForeignServers is name-sorted.
	c.RegisterForeignServer("alpha_srv", "goopg_fdw")
	list := c.ListForeignServers()
	if len(list) != 2 || list[0].Name != "alpha_srv" || list[1].Name != "srv1" {
		t.Fatalf("ListForeignServers = %+v; want [alpha_srv, srv1]", list)
	}
	if list[0].OID == list[1].OID {
		t.Fatalf("distinct servers share OID %d", list[0].OID)
	}

	// The virtual view exposes the getForeignServers columns: srvfdw resolves to
	// the FDW OID, srvtype/srvversion/srvacl/srvoptions are NULL (empty), owner is
	// the bootstrap superuser (10).
	rows := c.tables["pg_catalog.pg_foreign_server"].VirtualRows()
	if len(rows) != 2 {
		t.Fatalf("pg_foreign_server has %d rows, want 2", len(rows))
	}
	srvrow := rows[1] // sorted: alpha_srv, srv1
	want := []string{
		strconv.FormatUint(uint64(s.OID), 10),   // oid
		"srv1",                                  // srvname
		"10",                                    // srvowner
		strconv.FormatUint(uint64(fdw.OID), 10), // srvfdw → FDW OID
		"", "", "", "",                          // srvtype, srvversion, srvacl, srvoptions (NULL)
	}
	if len(srvrow) != len(want) {
		t.Fatalf("server row width %d, want %d", len(srvrow), len(want))
	}
	for i := range want {
		if srvrow[i] != want[i] {
			t.Fatalf("server row col %d = %q, want %q (full row %+v)", i, srvrow[i], want[i], srvrow)
		}
	}

	// An unknown FDW name resolves to srvfdw 0 (the CREATE SERVER referenced a
	// wrapper goopg never registered — pg_dump's single-row subquery would then
	// fail, but goopg does not validate the reference, matching the dump-fidelity
	// scope).
	if oid := c.ForeignDataWrapperOID("nope"); oid != 0 {
		t.Fatalf("ForeignDataWrapperOID(nope) = %d, want 0", oid)
	}

	// Drop removes it from the view; dropping a missing name is a no-op.
	if !c.DropForeignServer("srv1") {
		t.Fatalf("DropForeignServer(srv1) returned false")
	}
	if c.DropForeignServer("srv1") {
		t.Fatalf("DropForeignServer(srv1) second call returned true")
	}
	if rows := c.tables["pg_catalog.pg_foreign_server"].VirtualRows(); len(rows) != 1 {
		t.Fatalf("after drop: pg_foreign_server has %d rows, want 1", len(rows))
	}
}
