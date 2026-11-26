package catalog

import "testing"

// TestOwnerACLPartialRevoke pins the owner-privilege model 027_nosuperuser.pl
// relies on (M0149-0057f): once the owner's default aclitem is materialized
// and partly revoked, the owner holds exactly what is left — the check must
// not treat the owner as unrestricted, and the owner entry (kept under the
// "postgres" bookkeeping key) must not inherit another grantee's privileges
// through the bootstrap superuser's has_privs_of_role.
func TestOwnerACLPartialRevoke(t *testing.T) {
	c := NewInMemory()
	const rel = 16500
	if c.IsOwnerACLRevoked(rel) {
		t.Fatal("untouched relation reported as owner-revoked")
	}
	c.MaterializeOwnerACL(rel, aclOwnerRole, []string{"INSERT", "SELECT", "UPDATE", "DELETE"})
	c.RevokeTablePrivilege(rel, aclOwnerRole, "SELECT")
	c.RevokeTablePrivilege(rel, aclOwnerRole, "INSERT")
	c.GrantTablePrivilege(rel, "regress_admin", "SELECT")
	if !c.IsOwnerACLRevoked(rel) {
		t.Fatal("a partly revoked owner entry is not reported, so the owner keeps every privilege")
	}
	if c.HasOwnerPrivilege(rel, "SELECT") {
		t.Fatal("owner holds SELECT through another grantee's grant")
	}
	if c.HasOwnerPrivilege(rel, "INSERT") {
		t.Fatal("owner still holds revoked INSERT")
	}
	if !c.HasOwnerPrivilege(rel, "UPDATE") {
		t.Fatal("owner lost UPDATE, which was not revoked")
	}
	c.GrantTablePrivilege(rel, publicPseudoRole, "SELECT")
	if !c.HasOwnerPrivilege(rel, "SELECT") {
		t.Fatal("a PUBLIC grant does not reach the owner")
	}
}
