package catalog

import "testing"

// TestRegisterRealTablePerDatabaseRelFileNode is the read-side gate for
// M0143-0002e (docs/design/0100-0149/m0143-0002d-per-database-type-catalog.md):
// before this change, pg_type/pg_attribute were registered exactly once via
// RegisterRealTable(t) with no dbOid argument, leaving Table.DBOid at its Go
// zero value — RelFileNode's `table.DBOid != 0` branch then never fired, so
// every connection resolved the SAME physical file (the process-wide
// c.dbOid) regardless of which database it was on. This test confirms
// RegisterRealTable's new trailing dbOid argument breaks that: a Table
// registered for a distinct database now carries its own DBOid and
// RelFileNode routes it to that database's own base/<dbOid>/<OID> file,
// while the original no-dbOid-argument call keeps resolving through the
// process-wide c.dbOid exactly as before (byte-identical to every existing
// caller/test).
func TestRegisterRealTablePerDatabaseRelFileNode(t *testing.T) {
	c := NewInMemory()

	otherOid, err := c.CreateDatabase("typedb", BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if otherOid == 0 || otherOid == DefaultDBOid || otherOid == c.dbOid {
		t.Fatalf("CreateDatabase returned oid %d, want distinct from DefaultDBOid(%d)/c.dbOid(%d)", otherOid, DefaultDBOid, c.dbOid)
	}

	defaultTbl := &Table{
		Schema:  "pg_catalog",
		Name:    "pg_type",
		Columns: PGTypeColumns(),
		OID:     TypeRelationId,
	}
	if err := c.RegisterRealTable(defaultTbl); err != nil {
		t.Fatalf("RegisterRealTable(default, no dbOid): %v", err)
	}
	if defaultTbl.DBOid != 0 {
		t.Errorf("no-dbOid RegisterRealTable call set Table.DBOid = %d, want 0 (pre-existing behavior must be unchanged)", defaultTbl.DBOid)
	}

	otherTbl := &Table{
		Schema:  "pg_catalog",
		Name:    "pg_type",
		Columns: PGTypeColumns(),
		OID:     TypeRelationId,
	}
	if err := c.RegisterRealTable(otherTbl, otherOid); err != nil {
		t.Fatalf("RegisterRealTable(other, dbOid=%d): %v", otherOid, err)
	}
	if otherTbl.DBOid != otherOid {
		t.Errorf("RegisterRealTable(t, %d) left Table.DBOid = %d, want %d", otherOid, otherTbl.DBOid, otherOid)
	}

	// Each registration must live in its OWN namespace, not collide.
	if got := c.ns(DefaultDBOid).tables["pg_catalog.pg_type"]; got != defaultTbl {
		t.Fatalf("DefaultDBOid namespace does not hold defaultTbl")
	}
	if got := c.ns(otherOid).tables["pg_catalog.pg_type"]; got != otherTbl {
		t.Fatalf("otherOid namespace does not hold otherTbl")
	}

	defaultRel := c.RelFileNode(defaultTbl)
	otherRel := c.RelFileNode(otherTbl)
	if defaultRel.DBOid != c.dbOid {
		t.Errorf("RelFileNode(defaultTbl).DBOid = %d, want process-wide c.dbOid %d (unchanged)", defaultRel.DBOid, c.dbOid)
	}
	if otherRel.DBOid != otherOid {
		t.Errorf("RelFileNode(otherTbl).DBOid = %d, want %d (the table's own database)", otherRel.DBOid, otherOid)
	}
	if defaultRel.DBOid == otherRel.DBOid {
		t.Fatalf("both pg_type Tables resolved to the SAME physical DBOid (%d) — the cross-database union bug M0143-0002d/e fixes", defaultRel.DBOid)
	}

	// Idempotent re-registration (mirrors the doc comment's Restore()-then-
	// loadSystemCatalogsIfPresent race) must still no-op per namespace.
	again := &Table{
		Schema:  "pg_catalog",
		Name:    "pg_type",
		Columns: PGTypeColumns(),
		OID:     TypeRelationId,
	}
	if err := c.RegisterRealTable(again, otherOid); err != nil {
		t.Fatalf("idempotent re-registration under otherOid: %v", err)
	}
	if got := c.ns(otherOid).tables["pg_catalog.pg_type"]; got != otherTbl {
		t.Fatalf("idempotent re-registration replaced the original Table object")
	}
}
