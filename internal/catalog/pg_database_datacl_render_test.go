package catalog

import "testing"

// TestPGDatabaseRendersNonLiveDatabaseDatacl is the reader half of the
// M0122-0008 sibling pair (Hard-won Rule #2). The pg_database virtual row
// builder used to render datacl only for the "postgres" row, keyed by
// c.DBOID(), because execDatabaseACLChange could only ever write that one.
// Teaching the writer to reach other databases without this half would have
// stored an ACL that no SELECT could show — a green writer test beside a
// column that is always NULL.
//
// The "postgres" assertion is not redundant: ResolveDatabaseOid returns
// DBOID() for that name, which is what makes the fix leave the live row's
// rendering byte-identical rather than merely similar.
func TestPGDatabaseRendersNonLiveDatabaseDatacl(t *testing.T) {
	c := NewInMemory()
	otherOid, err := c.CreateDatabase("otherdb", BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	// Seed the same shape execDatabaseACLChange produces for a GRANT.
	for _, p := range []string{"TEMPORARY", "CONNECT"} {
		c.GrantTablePrivilege(otherOid, "PUBLIC", p)
	}
	c.GrantTablePrivilege(otherOid, "r1", "CONNECT")
	c.GrantTablePrivilege(c.DBOID(), "PUBLIC", "CONNECT")

	tbl, ok := c.ns(DefaultDBOid).tables["pg_catalog.pg_database"]
	if !ok {
		t.Fatalf("pg_database virtual table not registered")
	}
	const datnameIdx, dataclIdx = 1, 16
	got := map[string]string{}
	for _, row := range tbl.VirtualRows() {
		got[row[datnameIdx]] = row[dataclIdx]
	}

	if want := c.DatabaseACLText(otherOid); got["otherdb"] != want || want == "" {
		t.Errorf("otherdb datacl rendered %q; want the projected ACL %q", got["otherdb"], want)
	}
	if want := c.DatabaseACLText(c.DBOID()); got["postgres"] != want || want == "" {
		t.Errorf("postgres datacl rendered %q; want %q", got["postgres"], want)
	}
	// A database with no ACL must stay NULL, not render an empty array:
	// PG keeps datacl NULL until something is granted.
	if got["template0"] != VirtualNull {
		t.Errorf("template0 datacl = %q; want NULL", got["template0"])
	}
}
