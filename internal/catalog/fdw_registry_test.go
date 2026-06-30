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

	f := c.RegisterForeignDataWrapper("myfdw", []string{"debug=true"})
	if f.OID < FirstUserOID {
		t.Fatalf("RegisterForeignDataWrapper minted OID %d below FirstUserOID %d", f.OID, FirstUserOID)
	}
	// Idempotent: same name returns the same OID, no churn. Empty options on
	// re-registration do not clobber the stored OPTIONS.
	if f2 := c.RegisterForeignDataWrapper("myfdw", nil); f2.OID != f.OID {
		t.Fatalf("re-register myfdw changed OID: %d → %d", f.OID, f2.OID)
	}
	if len(f.Options) != 1 || f.Options[0] != "debug=true" {
		t.Fatalf("re-register with nil options clobbered OPTIONS: %+v", f.Options)
	}

	// A second FDW gets a distinct OID; ListForeignDataWrappers is name-sorted.
	c.RegisterForeignDataWrapper("alpha_fdw", nil)
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
	// fdwoptions renders the text[] external literal "{debug=true}" that
	// pg_dump's pg_options_to_table(fdwoptions) expands back into an OPTIONS clause.
	want := []string{strconv.FormatUint(uint64(f.OID), 10), "myfdw", "10", "0", "0", "", "{debug=true}"}
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
	fdw := c.RegisterForeignDataWrapper("goopg_fdw", nil)
	s := c.RegisterForeignServer("srv1", "goopg_fdw", "", "", nil)
	if s.OID < FirstUserOID {
		t.Fatalf("RegisterForeignServer minted OID %d below FirstUserOID %d", s.OID, FirstUserOID)
	}
	// Idempotent: same name returns the same OID, no churn.
	if s2 := c.RegisterForeignServer("srv1", "goopg_fdw", "", "", nil); s2.OID != s.OID {
		t.Fatalf("re-register srv1 changed OID: %d → %d", s.OID, s2.OID)
	}

	// A second server gets a distinct OID; ListForeignServers is name-sorted.
	c.RegisterForeignServer("alpha_srv", "goopg_fdw", "", "", nil)
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

	// A server created WITH OPTIONS surfaces them as the srvoptions text[] literal
	// ("{name=value,…}") that pg_dump's pg_options_to_table(srvoptions) expands.
	// DU-002 slice 378.
	c.RegisterForeignServer("opt_srv", "goopg_fdw", "", "", []string{"host=localhost", "dbname=mydb"})
	optView := c.tables["pg_catalog.pg_foreign_server"].VirtualRows()
	var optRow []string
	for _, r := range optView {
		if r[1] == "opt_srv" {
			optRow = r
			break
		}
	}
	if optRow == nil {
		t.Fatalf("opt_srv not found in pg_foreign_server view")
	}
	if got := optRow[7]; got != "{host=localhost,dbname=mydb}" {
		t.Fatalf("opt_srv srvoptions = %q, want %q", got, "{host=localhost,dbname=mydb}")
	}
	// Re-registering with new options refreshes them; nil options leave them intact.
	c.RegisterForeignServer("opt_srv", "goopg_fdw", "", "", nil)
	if list := c.ListForeignServers(); func() bool {
		for _, sv := range list {
			if sv.Name == "opt_srv" && len(sv.Options) == 2 {
				return false
			}
		}
		return true
	}() {
		t.Fatalf("nil re-register dropped opt_srv options")
	}
	c.DropForeignServer("opt_srv")

	// A server created WITH TYPE / VERSION surfaces them in srvtype / srvversion
	// so pg_dump's dumpForeignServer re-emits the TYPE 'x' / VERSION 'y' clauses.
	// DU-002 slice 381.
	c.RegisterForeignServer("tv_srv", "goopg_fdw", "oracle", "12.2", nil)
	tvView := c.tables["pg_catalog.pg_foreign_server"].VirtualRows()
	var tvRow []string
	for _, r := range tvView {
		if r[1] == "tv_srv" {
			tvRow = r
			break
		}
	}
	if tvRow == nil {
		t.Fatalf("tv_srv not found in pg_foreign_server view")
	}
	if tvRow[4] != "oracle" || tvRow[5] != "12.2" {
		t.Fatalf("tv_srv srvtype/srvversion = %q/%q, want %q/%q", tvRow[4], tvRow[5], "oracle", "12.2")
	}
	// nil/empty re-register leaves TYPE/VERSION intact; non-empty refreshes them.
	c.RegisterForeignServer("tv_srv", "goopg_fdw", "", "", nil)
	c.RegisterForeignServer("tv_srv", "goopg_fdw", "mysql", "8.0", nil)
	tvView = c.tables["pg_catalog.pg_foreign_server"].VirtualRows()
	for _, r := range tvView {
		if r[1] == "tv_srv" {
			if r[4] != "mysql" || r[5] != "8.0" {
				t.Fatalf("tv_srv after refresh srvtype/srvversion = %q/%q, want mysql/8.0", r[4], r[5])
			}
		}
	}
	c.DropForeignServer("tv_srv")

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

// TestUserMappingRegistry verifies the user-mapping registry and the
// pg_user_mappings virtual view that pg_dump's dumpUserMappings reads. The view
// MUST exist (returning 0 rows by default) so that creating any foreign server
// does not make pg_dump abort on a missing relation. DU-002 slice 377.
func TestUserMappingRegistry(t *testing.T) {
	c := NewInMemory()

	// The view exists and is empty on a fresh server (this is the exit-0 guard:
	// pg_dump queries pg_user_mappings for every foreign server).
	umView, ok := c.tables["pg_catalog.pg_user_mappings"]
	if !ok {
		t.Fatalf("pg_user_mappings virtual view is not registered")
	}
	if rows := umView.VirtualRows(); len(rows) != 0 {
		t.Fatalf("empty mapping registry: pg_user_mappings has %d rows, want 0", len(rows))
	}

	// Register a server + role so the mapping resolves srvid and umuser.
	c.RegisterForeignDataWrapper("goopg_fdw", nil)
	srv := c.RegisterForeignServer("goopg_srv", "goopg_fdw", "", "", nil)
	c.RegisterRole("um_role")
	roleOID, _ := c.RoleOID("um_role")

	m := c.RegisterUserMapping("um_role", "goopg_srv", []string{"user=remote", "password=secret"})
	if m.OID < FirstUserOID {
		t.Fatalf("RegisterUserMapping minted OID %d below FirstUserOID %d", m.OID, FirstUserOID)
	}
	// Idempotent: same (user, server) returns the same OID; passing nil options on
	// re-register does NOT clear the stored options.
	if m2 := c.RegisterUserMapping("um_role", "goopg_srv", nil); m2.OID != m.OID {
		t.Fatalf("re-register mapping changed OID: %d → %d", m.OID, m2.OID)
	}

	// The view row mirrors dumpUserMappings' inputs: srvid resolves to the server
	// OID, usename is the role name, umuser its OID, umoptions the text[] literal
	// that pg_options_to_table(umoptions) expands.
	rows := umView.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_user_mappings has %d rows, want 1", len(rows))
	}
	want := []string{
		strconv.FormatUint(uint64(m.OID), 10),   // umid
		strconv.FormatUint(uint64(srv.OID), 10), // srvid → server OID
		"goopg_srv",                             // srvname
		strconv.FormatUint(uint64(roleOID), 10), // umuser → role OID
		"um_role",                               // usename
		"{user=remote,password=secret}",         // umoptions text[]
	}
	if len(rows[0]) != len(want) {
		t.Fatalf("mapping row width %d, want %d", len(rows[0]), len(want))
	}
	for i := range want {
		if rows[0][i] != want[i] {
			t.Fatalf("mapping row col %d = %q, want %q (full row %+v)", i, rows[0][i], want[i], rows[0])
		}
	}

	// A PUBLIC mapping renders usename='public' with umuser=0 (ACL_ID_PUBLIC).
	c.RegisterUserMapping("public", "goopg_srv", nil)
	pubRows := umView.VirtualRows()
	var foundPublic bool
	for _, r := range pubRows {
		if r[4] == "public" {
			foundPublic = true
			if r[3] != "0" {
				t.Fatalf("PUBLIC mapping umuser = %q, want 0", r[3])
			}
		}
	}
	if !foundPublic {
		t.Fatalf("PUBLIC mapping missing from pg_user_mappings: %+v", pubRows)
	}

	// Drop removes it; dropping a missing pair is a no-op.
	if !c.DropUserMapping("um_role", "goopg_srv") {
		t.Fatalf("DropUserMapping(um_role, goopg_srv) returned false")
	}
	if c.DropUserMapping("um_role", "goopg_srv") {
		t.Fatalf("DropUserMapping second call returned true")
	}
	if rows := umView.VirtualRows(); len(rows) != 1 { // only the PUBLIC mapping remains
		t.Fatalf("after drop: pg_user_mappings has %d rows, want 1", len(rows))
	}
}
