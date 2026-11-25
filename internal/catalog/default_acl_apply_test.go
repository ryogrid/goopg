package catalog

import "testing"

// TestApplyDefaultACLMergesGlobalAndSchema pins get_user_default_acl for
// relations: no entry leaves the hardwired default (false); a schema-only
// entry merges into acldefault (owner baseline kept); a global entry's
// grants and a schema entry's grants are both applied.
func TestApplyDefaultACLMergesGlobalAndSchema(t *testing.T) {
	c := NewInMemory()
	const ns = 2200
	if c.ApplyDefaultACL(50001, 10, ns, DefaultACLObjTypeRelation) {
		t.Fatal("applied a default with no pg_default_acl entry")
	}
	sOID := c.DefaultACLOID(10, ns, DefaultACLObjTypeRelation)
	c.GrantDefaultACLPrivilege(sOID, "public", "SELECT", false)
	if !c.ApplyDefaultACL(50002, 10, ns, DefaultACLObjTypeRelation) {
		t.Fatal("schema-scoped default not applied")
	}
	if got := c.RelaclText(50002); got != "{postgres=arwdDxtm/postgres,=r/postgres}" {
		t.Fatalf("schema-only relacl = %q", got)
	}
	gOID := c.DefaultACLOID(10, 0, DefaultACLObjTypeRelation)
	c.GrantDefaultACLPrivilege(gOID, "alice", "INSERT", false)
	if !c.ApplyDefaultACL(50003, 10, ns, DefaultACLObjTypeRelation) {
		t.Fatal("global + schema default not applied")
	}
	if got := c.RelaclText(50003); got != "{postgres=arwdDxtm/postgres,alice=a/postgres,=r/postgres}" {
		t.Fatalf("merged relacl = %q", got)
	}
	if !c.HasTablePrivilege(50003, "alice", "INSERT") || c.HasTablePrivilege(50003, "alice", "UPDATE") {
		t.Fatal("merged grants not effective")
	}
}
