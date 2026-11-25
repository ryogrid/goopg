package catalog

import "testing"

// TestLargeObjectDefaultACLAndRollbackSnapshot pins LARGE OBJECTS as a
// modelled pg_default_acl class (privileges SELECT 'r' / UPDATE 'w', owner
// baseline "rw", no PUBLIC world default) and SnapshotACLEntry restoring an
// entry to its pre-change state, which ALTER DEFAULT PRIVILEGES records so a
// ROLLBACK undoes it.
func TestLargeObjectDefaultACLAndRollbackSnapshot(t *testing.T) {
	c := NewInMemory()
	if got := DefaultACLNormalizePriv(DefaultACLObjTypeLargeObject, "ALL"); len(got) != 2 || got[0] != "SELECT" || got[1] != "UPDATE" {
		t.Fatalf("ALL on LARGE OBJECTS = %v", got)
	}
	oid := c.DefaultACLOID(10, 0, DefaultACLObjTypeLargeObject)
	undo := c.SnapshotACLEntry(oid)
	c.GrantDefaultACLPrivilege(oid, "alice", "SELECT", false)
	if got := c.DefaultACLText(oid, DefaultACLObjTypeLargeObject); got != "{postgres=rw/postgres,alice=r/postgres}" {
		t.Fatalf("defaclacl = %q", got)
	}
	if !c.HasDefaultACL(10, 0, DefaultACLObjTypeLargeObject) {
		t.Fatal("entry not materialised")
	}
	undo()
	if c.HasDefaultACL(10, 0, DefaultACLObjTypeLargeObject) {
		t.Fatal("snapshot restore left the entry in place")
	}
	c.GrantDefaultACLPrivilege(oid, "alice", "SELECT", false)
	undo2 := c.SnapshotACLEntry(oid)
	c.GrantDefaultACLPrivilege(oid, "bob", "UPDATE", false)
	undo2()
	if got := c.DefaultACLText(oid, DefaultACLObjTypeLargeObject); got != "{postgres=rw/postgres,alice=r/postgres}" {
		t.Fatalf("after restore defaclacl = %q", got)
	}
}
