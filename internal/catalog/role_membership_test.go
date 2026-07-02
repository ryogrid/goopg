package catalog

import "testing"

// TestGrantRoleMembershipUpsertsInPlace verifies GrantRoleMembership mints a
// fresh OID for a new (roleOid, memberOid) pair and re-grants keep the same
// OID while updating grantor and admin_option (WITH ADMIN OPTION never
// downgrades on a plain re-grant). M0119-0004-ACLHEAP.
func TestGrantRoleMembershipUpsertsInPlace(t *testing.T) {
	c := NewInMemory()
	oid1 := c.GrantRoleMembership(100, 200, 10, false)
	if oid1 == 0 {
		t.Fatalf("GrantRoleMembership returned zero OID")
	}
	entries := c.RoleMembershipEntries()
	if len(entries) != 1 || entries[0].AdminOption {
		t.Fatalf("unexpected entries after first grant: %+v", entries)
	}

	// Re-grant with a different grantor and WITH ADMIN OPTION: OID stays the
	// same, grantor updates, admin_option upgrades to true.
	oid2 := c.GrantRoleMembership(100, 200, 20, true)
	if oid2 != oid1 {
		t.Errorf("re-grant minted a new OID: %d != %d", oid2, oid1)
	}
	entries = c.RoleMembershipEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].GrantorOID != 20 || !entries[0].AdminOption {
		t.Errorf("re-grant did not update grantor/admin_option: %+v", entries[0])
	}

	// A further plain re-grant (no WITH ADMIN OPTION) must NOT downgrade
	// admin_option back to false.
	c.GrantRoleMembership(100, 200, 30, false)
	entries = c.RoleMembershipEntries()
	if !entries[0].AdminOption {
		t.Errorf("plain re-grant downgraded admin_option: %+v", entries[0])
	}
	if entries[0].GrantorOID != 30 {
		t.Errorf("plain re-grant did not update grantor: %+v", entries[0])
	}
}

// TestRevokeRoleMembership verifies both REVOKE forms: a full revoke deletes
// the row, and REVOKE ADMIN OPTION FOR only clears the flag.
func TestRevokeRoleMembership(t *testing.T) {
	c := NewInMemory()
	c.GrantRoleMembership(100, 200, 10, true)

	if !c.RevokeRoleMembership(100, 200, true) {
		t.Fatalf("RevokeRoleMembership(adminOptionOnly) reported no existing row")
	}
	entries := c.RoleMembershipEntries()
	if len(entries) != 1 || entries[0].AdminOption {
		t.Fatalf("admin-option-only revoke should keep the row with AdminOption=false: %+v", entries)
	}

	if !c.RevokeRoleMembership(100, 200, false) {
		t.Fatalf("full RevokeRoleMembership reported no existing row")
	}
	if entries := c.RoleMembershipEntries(); len(entries) != 0 {
		t.Fatalf("full revoke should delete the row: %+v", entries)
	}

	// Revoking a non-existent membership is a silent no-op (matches this
	// codebase's other ACL REVOKE paths).
	if c.RevokeRoleMembership(999, 999, false) {
		t.Errorf("RevokeRoleMembership on a non-existent row reported success")
	}
}

// TestRoleIsMemberOfDetectsSelfAndTransitiveCycles verifies the traversal
// GRANT ROLE's circularity check relies on.
func TestRoleIsMemberOfDetectsSelfAndTransitiveCycles(t *testing.T) {
	c := NewInMemory()
	if !c.RoleIsMemberOf(100, 100) {
		t.Errorf("self-membership must report true")
	}
	if c.RoleIsMemberOf(100, 200) {
		t.Errorf("unrelated roles must report false before any grant")
	}

	// 200 is a member of 100 (role=100, member=200).
	c.GrantRoleMembership(100, 200, 10, false)
	if !c.RoleIsMemberOf(200, 100) {
		t.Errorf("direct membership not detected")
	}
	if c.RoleIsMemberOf(100, 200) {
		t.Errorf("membership direction must not be symmetric")
	}

	// 300 is a member of 200 -> 300 is transitively a member of 100.
	c.GrantRoleMembership(200, 300, 10, false)
	if !c.RoleIsMemberOf(300, 100) {
		t.Errorf("transitive membership not detected")
	}
}

// TestRoleMembershipEntriesDeterministicOrder verifies sort order for
// deterministic pg_auth_members virtual-row output.
func TestRoleMembershipEntriesDeterministicOrder(t *testing.T) {
	c := NewInMemory()
	c.GrantRoleMembership(200, 10, 10, false)
	c.GrantRoleMembership(100, 20, 10, false)
	c.GrantRoleMembership(100, 10, 10, false)

	entries := c.RoleMembershipEntries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	want := [][2]uint32{{100, 10}, {100, 20}, {200, 10}}
	for i, w := range want {
		if entries[i].RoleOID != w[0] || entries[i].MemberOID != w[1] {
			t.Errorf("entry %d = (role=%d, member=%d), want (role=%d, member=%d)",
				i, entries[i].RoleOID, entries[i].MemberOID, w[0], w[1])
		}
	}
}

// TestUnregisterRoleDropsMembershipRows verifies DROP ROLE cascades removal
// of any pg_auth_members row referencing the role's OID on either side,
// mirroring PostgreSQL's DropRole (user.c).
func TestUnregisterRoleDropsMembershipRows(t *testing.T) {
	c := NewInMemory()
	c.RegisterRoleWithOID("admin", 100)
	c.RegisterRoleWithOID("alice", 200)
	c.GrantRoleMembership(100, 200, 10, false)

	c.UnregisterRole("alice")
	if entries := c.RoleMembershipEntries(); len(entries) != 0 {
		t.Errorf("dropping the member role should remove its membership row: %+v", entries)
	}

	c.RegisterRoleWithOID("alice", 200)
	c.GrantRoleMembership(100, 200, 10, false)
	c.UnregisterRole("admin")
	if entries := c.RoleMembershipEntries(); len(entries) != 0 {
		t.Errorf("dropping the granted role should remove its membership row: %+v", entries)
	}
}
