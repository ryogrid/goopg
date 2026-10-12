package catalog

import "testing"

// TestForeignDataWrappersArePerDatabase pins M0122-0015e: an FDW belongs to
// the database that created it. One created in the default database used to
// show in every database's pg_foreign_data_wrapper (and its pg_dump), and a
// same-named FDW in a second database collided with it.
func TestForeignDataWrappersArePerDatabase(t *testing.T) {
	c := NewInMemory()
	const other = uint32(16500)
	a := c.RegisterForeignDataWrapper("dummy", nil)
	if _, ok := c.LookupForeignDataWrapper("dummy", other); ok {
		t.Fatalf("default database's FDW visible in another database")
	}
	b := c.RegisterForeignDataWrapper("dummy", []string{"k=v"}, other)
	if a.OID == b.OID || a.DBOid != DefaultDBOid || b.DBOid != other {
		t.Fatalf("same-named FDWs in two databases: %+v / %+v", a, b)
	}
	if c.ForeignDataWrapperOID("dummy") != a.OID || c.ForeignDataWrapperOID("dummy", other) != b.OID {
		t.Fatalf("ForeignDataWrapperOID does not resolve per database")
	}
	if rows := c.PGForeignDataWrapperRowsForDBOid(other); len(rows) != 1 || rows[0][0] == "" || rows[0][6] != "{k=v}" {
		t.Fatalf("other database's rows: %v", rows)
	}
	if rows := c.PGForeignDataWrapperRowsForDBOid(PostgresDBOid); len(rows) != 1 || rows[0][6] != "" {
		t.Fatalf("a postgres connection reads the default database's rows: %v", rows)
	}
	if !c.DropForeignDataWrapper("dummy") {
		t.Fatalf("drop in the default database")
	}
	if _, ok := c.LookupForeignDataWrapper("dummy", other); !ok {
		t.Fatalf("dropping the default database's FDW removed the other database's")
	}
	if got := c.LookupForeignDataWrapperByOID(b.OID); got == nil || got.DBOid != other {
		t.Fatalf("LookupForeignDataWrapperByOID across databases: %+v", got)
	}
}
