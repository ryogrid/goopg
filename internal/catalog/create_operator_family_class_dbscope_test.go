package catalog

import "testing"

// TestOperatorFamilyAndClassCrossDatabaseIsolation verifies that CREATE
// OPERATOR FAMILY / CREATE OPERATOR CLASS under one database does not
// collide with a same-named family/class already created under a different
// database — the DU-002 dump+restore round-trip probe's next blocker after
// AccessMethod gained per-database scoping (TestAccessMethodCrossDatabase
// Isolation): restoring a dump into a fresh database re-issues `CREATE
// OPERATOR FAMILY public.op_family_loose USING btree` followed by `ALTER
// OPERATOR FAMILY ... ADD OPERATOR 1 ...`, which previously errored
// "operator 1(bigint,bigint) already exists in operator family
// op_family_loose" because every UserOperatorFamily/UserOperatorClass shared
// one flat, dbOid-less registry. Mirrors the AccessMethod cross-database
// isolation precedent (M0122-0007 4e follow-up).
func TestOperatorFamilyAndClassCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	famA := c.RegisterUserOperatorFamily("public", "op_family", PublicNamespaceOID, btreeAccessMethodOID, 0, DefaultDBOid)
	famB := c.RegisterUserOperatorFamily("public", "op_family", PublicNamespaceOID, btreeAccessMethodOID, 0, otherDB)
	if famA.OID == famB.OID {
		t.Errorf("cross-database operator families share OID %d, want distinct OIDs", famA.OID)
	}
	if famA.DBOid != DefaultDBOid || famB.DBOid != otherDB {
		t.Errorf("DBOid not recorded: famA.DBOid=%d famB.DBOid=%d", famA.DBOid, famB.DBOid)
	}

	if got, ok := c.LookupUserOperatorFamily("public", "op_family", btreeAccessMethodOID, DefaultDBOid); !ok || got.OID != famA.OID {
		t.Errorf("LookupUserOperatorFamily(DefaultDBOid) = %+v, %v; want famA", got, ok)
	}
	if got, ok := c.LookupUserOperatorFamily("public", "op_family", btreeAccessMethodOID, otherDB); !ok || got.OID != famB.OID {
		t.Errorf("LookupUserOperatorFamily(otherDB) = %+v, %v; want famB", got, ok)
	}

	// Dropping under one database must not remove the other's row.
	if !c.DropUserOperatorFamily("public", "op_family", btreeAccessMethodOID, DefaultDBOid) {
		t.Fatal("DropUserOperatorFamily(op_family) under DefaultDBOid: not found")
	}
	if _, ok := c.LookupUserOperatorFamily("public", "op_family", btreeAccessMethodOID, DefaultDBOid); ok {
		t.Error("DefaultDBOid's op_family still resolvable after DropUserOperatorFamily")
	}
	if _, ok := c.LookupUserOperatorFamily("public", "op_family", btreeAccessMethodOID, otherDB); !ok {
		t.Error("otherDB's op_family vanished after dropping DefaultDBOid's own copy")
	}

	// Same isolation for operator classes, keyed the same way.
	classA := c.RegisterUserOperatorClass("public", "op_class", PublicNamespaceOID, 0, btreeAccessMethodOID, famB.OID, OIDInt4, false, 0, DefaultDBOid)
	classB := c.RegisterUserOperatorClass("public", "op_class", PublicNamespaceOID, 0, btreeAccessMethodOID, famB.OID, OIDInt4, false, 0, otherDB)
	if classA.OID == classB.OID {
		t.Errorf("cross-database operator classes share OID %d, want distinct OIDs", classA.OID)
	}
	if classA.DBOid != DefaultDBOid || classB.DBOid != otherDB {
		t.Errorf("DBOid not recorded: classA.DBOid=%d classB.DBOid=%d", classA.DBOid, classB.DBOid)
	}

	if !c.DropUserOperatorClass("public", "op_class", btreeAccessMethodOID, DefaultDBOid) {
		t.Fatal("DropUserOperatorClass(op_class) under DefaultDBOid: not found")
	}
	if _, ok := c.LookupUserOperatorClass("public", "op_class", btreeAccessMethodOID, DefaultDBOid); ok {
		t.Error("DefaultDBOid's op_class still resolvable after DropUserOperatorClass")
	}
	if _, ok := c.LookupUserOperatorClass("public", "op_class", btreeAccessMethodOID, otherDB); !ok {
		t.Error("otherDB's op_class vanished after dropping DefaultDBOid's own copy")
	}
}
