package catalog

import "testing"

// TestMemberCanSetRole pins member_can_set_role as 033_run_as_table_owner.pl
// uses it (M0149-0058a): only memberships carrying the SET option let a role
// SET ROLE to another, while privileges are inherited only through INHERIT
// memberships (HasPrivsOfRole) — the two are independent.
func TestMemberCanSetRole(t *testing.T) {
	c := NewInMemory()
	c.RegisterRoleWithOID("regress_admin", 20001)
	c.RegisterRoleWithOID("regress_alice", 20002)
	no, yes := false, true

	c.GrantRoleMembership(20002, 20001, BootstrapSuperuserOID, &no, &no, &yes) // INHERIT FALSE, SET TRUE
	if !c.MemberCanSetRole(20001, 20002) {
		t.Fatal("SET TRUE membership does not allow SET ROLE")
	}
	if c.HasPrivsOfRole(20001, 20002) {
		t.Fatal("INHERIT FALSE membership passes privileges")
	}

	c.GrantRoleMembership(20002, 20001, BootstrapSuperuserOID, &no, &yes, &no) // INHERIT TRUE, SET FALSE
	if c.MemberCanSetRole(20001, 20002) {
		t.Fatal("SET FALSE membership still allows SET ROLE")
	}
	if !c.HasPrivsOfRole(20001, 20002) {
		t.Fatal("INHERIT TRUE membership does not pass privileges")
	}
	if !c.MemberCanSetRole(20001, 20001) || !c.MemberCanSetRole(BootstrapSuperuserOID, 20002) {
		t.Fatal("a role itself or a superuser cannot SET ROLE")
	}
	if c.MemberCanSetRole(20002, 20001) {
		t.Fatal("SET ROLE allowed against the membership direction")
	}
}
