package catalog

import (
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// TestGrantRoleMembershipUpsertsInPlace verifies GrantRoleMembership mints a
// fresh OID for a new (roleOid, memberOid, grantorOid) triple and a re-grant
// BY THE SAME GRANTOR keeps the same OID while updating admin_option: an
// unspecified (nil) WITH ADMIN clause never touches an existing row's
// admin_option (a plain re-grant never downgrades or upgrades it), but an
// explicit WITH ADMIN FALSE/TRUE always applies, matching PG's
// GRANT_ROLE_SPECIFIED_ADMIN bitmask semantics. M0119-0004-ACLHEAP.
func TestGrantRoleMembershipUpsertsInPlace(t *testing.T) {
	c := NewInMemory()
	oid1 := c.GrantRoleMembership(100, 200, 10, nil, nil, nil)
	if oid1 == 0 {
		t.Fatalf("GrantRoleMembership returned zero OID")
	}
	entries := c.RoleMembershipEntries()
	if len(entries) != 1 || entries[0].AdminOption {
		t.Fatalf("unexpected entries after first grant: %+v", entries)
	}

	// Re-grant BY THE SAME GRANTOR (10) WITH ADMIN OPTION: OID stays the
	// same, admin_option upgrades to true.
	oid2 := c.GrantRoleMembership(100, 200, 10, boolPtr(true), nil, nil)
	if oid2 != oid1 {
		t.Errorf("re-grant minted a new OID: %d != %d", oid2, oid1)
	}
	entries = c.RoleMembershipEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].GrantorOID != 10 || !entries[0].AdminOption {
		t.Errorf("re-grant did not update admin_option: %+v", entries[0])
	}

	// A further plain re-grant BY THE SAME GRANTOR (no WITH clause at all —
	// nil admin) must NOT downgrade admin_option back to false.
	c.GrantRoleMembership(100, 200, 10, nil, nil, nil)
	entries = c.RoleMembershipEntries()
	if len(entries) != 1 || !entries[0].AdminOption {
		t.Errorf("plain re-grant downgraded admin_option: %+v", entries)
	}

	// An explicit WITH ADMIN FALSE, unlike an unspecified clause, DOES
	// downgrade — PG allows an explicit request to override the current
	// value in either direction (GrantRole, user.c).
	c.GrantRoleMembership(100, 200, 10, boolPtr(false), nil, nil)
	entries = c.RoleMembershipEntries()
	if len(entries) != 1 || entries[0].AdminOption {
		t.Errorf("explicit WITH ADMIN FALSE did not downgrade admin_option: %+v", entries)
	}

	// A DIFFERENT grantor granting the SAME (role, member) pair mints an
	// independent second row — real PG's (roleid, member, grantor) unique
	// index (pg_auth_members_role_member_index) allows exactly this.
	oid3 := c.GrantRoleMembership(100, 200, 20, boolPtr(true), nil, nil)
	if oid3 == oid1 {
		t.Errorf("a different grantor must mint a new OID, not reuse %d", oid1)
	}
	entries = c.RoleMembershipEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 independent grantor rows, got %d: %+v", len(entries), entries)
	}
	byGrantor := map[uint32]RoleMembership{}
	for _, e := range entries {
		byGrantor[e.GrantorOID] = e
	}
	if byGrantor[10].AdminOption {
		t.Errorf("grantor 10's row must be untouched by grantor 20's grant: %+v", byGrantor[10])
	}
	if !byGrantor[20].AdminOption {
		t.Errorf("grantor 20's new row must carry its own WITH ADMIN OPTION: %+v", byGrantor[20])
	}
}

// TestGrantRoleMembershipInheritSetDefaults verifies InitGrantRoleOptions'
// insert-time defaults (user.c): admin=false, set=true, inherit=true (goopg
// has no per-role NOINHERIT tracking, so every role's rolinherit is always
// true — the PG default of "inherit the grantee's rolinherit" coincides with
// a flat default of true here), and that an explicit WITH clause overrides
// them on both insert and a later re-grant. M0119-0004-ACLHEAP.
func TestGrantRoleMembershipInheritSetDefaults(t *testing.T) {
	c := NewInMemory()
	c.GrantRoleMembership(100, 200, 10, nil, nil, nil)
	entries := c.RoleMembershipEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !entries[0].InheritOption || !entries[0].SetOption || entries[0].AdminOption {
		t.Fatalf("unexpected defaults on fresh grant: %+v", entries[0])
	}

	// WITH INHERIT FALSE, SET FALSE on a fresh grant applies immediately.
	c.GrantRoleMembership(300, 400, 10, nil, boolPtr(false), boolPtr(false))
	entries = c.RoleMembershipEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	row := entries[0]
	if row.RoleOID != 300 {
		row = entries[1]
	}
	if row.InheritOption || row.SetOption {
		t.Errorf("explicit WITH INHERIT FALSE, SET FALSE not applied: %+v", row)
	}

	// A later re-grant BY THE SAME GRANTOR with an unspecified INHERIT/SET
	// leaves them untouched (and stays the same row, not a new grantor row).
	c.GrantRoleMembership(300, 400, 10, nil, nil, nil)
	entries = c.RoleMembershipEntries()
	if len(entries) != 2 {
		t.Fatalf("same-grantor re-grant must not mint a new row, got %d entries: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.RoleOID == 300 && e.MemberOID == 400 {
			if e.InheritOption || e.SetOption {
				t.Errorf("unspecified re-grant touched inherit/set: %+v", e)
			}
		}
	}
}

// TestRevokeRoleMembership verifies all four REVOKE forms: a full revoke
// deletes the row, and each of REVOKE ADMIN/INHERIT/SET OPTION FOR only
// clears its own flag and leaves the row (and the other two flags) in place.
func TestRevokeRoleMembership(t *testing.T) {
	c := NewInMemory()
	c.GrantRoleMembership(100, 200, 10, boolPtr(true), nil, nil)

	if !c.RevokeRoleMembership(100, 200, 10, "admin") {
		t.Fatalf("RevokeRoleMembership(admin) reported no existing row")
	}
	entries := c.RoleMembershipEntries()
	if len(entries) != 1 || entries[0].AdminOption {
		t.Fatalf("admin-option-only revoke should keep the row with AdminOption=false: %+v", entries)
	}
	if !entries[0].InheritOption || !entries[0].SetOption {
		t.Fatalf("admin-option-only revoke must not touch inherit/set: %+v", entries)
	}

	if !c.RevokeRoleMembership(100, 200, 10, "inherit") {
		t.Fatalf("RevokeRoleMembership(inherit) reported no existing row")
	}
	if !c.RevokeRoleMembership(100, 200, 10, "set") {
		t.Fatalf("RevokeRoleMembership(set) reported no existing row")
	}
	entries = c.RoleMembershipEntries()
	if len(entries) != 1 || entries[0].InheritOption || entries[0].SetOption {
		t.Fatalf("inherit/set-option-only revokes should keep the row with both flags false: %+v", entries)
	}

	if !c.RevokeRoleMembership(100, 200, 10, "") {
		t.Fatalf("full RevokeRoleMembership reported no existing row")
	}
	if entries := c.RoleMembershipEntries(); len(entries) != 0 {
		t.Fatalf("full revoke should delete the row: %+v", entries)
	}

	// Revoking a non-existent membership is a silent no-op (matches this
	// codebase's other ACL REVOKE paths).
	if c.RevokeRoleMembership(999, 999, 999, "") {
		t.Errorf("RevokeRoleMembership on a non-existent row reported success")
	}
}

// TestRevokeRoleMembershipTargetsOnlyItsOwnGrantorRow verifies that REVOKE
// only ever touches the ONE (roleOid, memberOid, grantorOid) row it names —
// an independent row granted by a DIFFERENT grantor on the same (role,
// member) pair survives untouched, matching real PG's (roleid, member,
// grantor) unique index (a REVOKE naming the wrong grantor is a no-op, same
// as revoking a membership that was never granted at all).
func TestRevokeRoleMembershipTargetsOnlyItsOwnGrantorRow(t *testing.T) {
	c := NewInMemory()
	c.GrantRoleMembership(100, 200, 10, nil, nil, nil)
	c.GrantRoleMembership(100, 200, 20, nil, nil, nil)

	if !c.RevokeRoleMembership(100, 200, 10, "") {
		t.Fatalf("revoking grantor 10's row reported no existing row")
	}
	entries := c.RoleMembershipEntries()
	if len(entries) != 1 || entries[0].GrantorOID != 20 {
		t.Fatalf("grantor 20's independent row must survive: %+v", entries)
	}

	if c.RevokeRoleMembership(100, 200, 10, "") {
		t.Errorf("re-revoking the already-removed grantor-10 row must be a no-op")
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
	c.GrantRoleMembership(100, 200, 10, nil, nil, nil)
	if !c.RoleIsMemberOf(200, 100) {
		t.Errorf("direct membership not detected")
	}
	if c.RoleIsMemberOf(100, 200) {
		t.Errorf("membership direction must not be symmetric")
	}

	// 300 is a member of 200 -> 300 is transitively a member of 100.
	c.GrantRoleMembership(200, 300, 10, nil, nil, nil)
	if !c.RoleIsMemberOf(300, 100) {
		t.Errorf("transitive membership not detected")
	}
}

// TestRoleMembershipEntriesDeterministicOrder verifies sort order for
// deterministic pg_auth_members virtual-row output.
func TestRoleMembershipEntriesDeterministicOrder(t *testing.T) {
	c := NewInMemory()
	c.GrantRoleMembership(200, 10, 10, nil, nil, nil)
	c.GrantRoleMembership(100, 20, 10, nil, nil, nil)
	c.GrantRoleMembership(100, 10, 10, nil, nil, nil)

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

// TestGrantRoleWouldCreateGrantorCycle verifies AddRoleMems' member-grantor
// circularity guard (user.c ~1751): a DIFFERENT check from RoleIsMemberOf's
// role-member loop. A single direct grant (A granted ADMIN on X to B) does
// NOT by itself block B from granting X back to A WITH ADMIN OPTION — A has
// no existing membership-in-X row for the simulated revoke to touch, so the
// grantor-chain check is a no-op (matches PG's literal algorithm: only a
// grant that would cascade-revoke the CURRENT grantor's own admin-option row
// creates the circularity).
func TestGrantRoleWouldCreateGrantorCycle(t *testing.T) {
	c := NewInMemory()
	const roleX = 500

	// B (700) got ADMIN on X FROM A (600) — but A itself holds no
	// membership-in-X row (A only appears as a grantor, never as a member).
	c.GrantRoleMembership(roleX, 700, 600, boolPtr(true), nil, nil)

	// "B tries to grant X back to A" with no chain to cascade through is NOT
	// circular: A holds no membership-in-X row, so revoking it (as this
	// grant would simulate) touches nothing, and B's own admin-option row
	// (granted by A) is left untouched.
	if c.GrantRoleWouldCreateGrantorCycle(roleX, []uint32{600}, 700) {
		t.Errorf("a one-hop grant-back with no existing member row for the target should not be circular")
	}

	// Now also grant X to A (A becomes a genuine member of X, admin granted
	// by some third party). B granting X back to A WITH ADMIN is now
	// circular: revoking A's new membership-in-X row cascades (A is the
	// grantor of B's admin-option row) to also revoke B's own admin_option,
	// leaving B with no surviving source of ADMIN OPTION on X to perform the
	// grant with.
	c.GrantRoleMembership(roleX, 600, 999, boolPtr(true), nil, nil)
	if !c.GrantRoleWouldCreateGrantorCycle(roleX, []uint32{600}, 700) {
		t.Errorf("a chained grant-back that would cascade-revoke the grantor's own admin option must be circular")
	}
}

// TestGrantRoleWouldCreateGrantorCycleRetainsUntouchedAdmin verifies that a
// grantor keeps the ability to grant when the new grantee set does not
// implicate the grantor's own admin-option row at all.
func TestGrantRoleWouldCreateGrantorCycleRetainsUntouchedAdmin(t *testing.T) {
	c := NewInMemory()
	const roleX = 500

	// B (700) holds ADMIN on X, granted by an unrelated role (999).
	c.GrantRoleMembership(roleX, 700, 999, boolPtr(true), nil, nil)

	// B grants X to a third, unrelated role (800): nothing about B's own
	// row is implicated by revoking 800's (nonexistent) membership-in-X row.
	if c.GrantRoleWouldCreateGrantorCycle(roleX, []uint32{800}, 700) {
		t.Errorf("granting to an unrelated member must not implicate the grantor's untouched admin row")
	}
}

// TestGrantRoleWouldCreateGrantorCycleRejectsBootstrapSuperuserGrantee
// verifies PG's unconditional rule: ADMIN OPTION can never be (re-)granted
// to the bootstrap superuser (it needs no source grantor), regardless of
// any existing pg_auth_members rows.
func TestGrantRoleWouldCreateGrantorCycleRejectsBootstrapSuperuserGrantee(t *testing.T) {
	c := NewInMemory()
	if !c.GrantRoleWouldCreateGrantorCycle(500, []uint32{BootstrapSuperuserOID}, 700) {
		t.Errorf("granting ADMIN OPTION to the bootstrap superuser must always be rejected")
	}
}

// TestRevokeRoleMembershipCascadeSetNoAdminOptionNeverCascades verifies PG's
// early-return: a member with no ADMIN OPTION on roleOid could not have
// re-granted it to anyone, so revoking it never produces dependents —
// regardless of what other rows exist for roleOid.
func TestRevokeRoleMembershipCascadeSetNoAdminOptionNeverCascades(t *testing.T) {
	c := NewInMemory()
	const roleX = 500
	c.GrantRoleMembership(roleX, 700, 10, boolPtr(false), nil, nil)
	// 700 (no admin) itself re-"granted" (recorded, not really possible
	// without admin, but exercises the walk) roleX to 800.
	c.GrantRoleMembership(roleX, 800, 700, nil, nil, nil)

	deps, blocked := c.RevokeRoleMembershipCascadeSet(roleX, 700, 10, false)
	if deps != nil || blocked {
		t.Errorf("revoking a non-admin row must never cascade or block: deps=%v blocked=%v", deps, blocked)
	}
}

// TestRevokeRoleMembershipCascadeSetBlocksWithoutCascade verifies RESTRICT
// (cascade=false, including REVOKE's unwritten default) reports blocked=true
// and produces no dependents to apply when a dependent grant exists.
func TestRevokeRoleMembershipCascadeSetBlocksWithoutCascade(t *testing.T) {
	c := NewInMemory()
	const roleX = 500
	c.GrantRoleMembership(roleX, 700, 10, boolPtr(true), nil, nil) // 700 has ADMIN on X
	c.GrantRoleMembership(roleX, 800, 700, nil, nil, nil)          // 700 granted X to 800

	deps, blocked := c.RevokeRoleMembershipCascadeSet(roleX, 700, 10, false)
	if !blocked {
		t.Errorf("RESTRICT with a dependent grant must report blocked=true")
	}
	if deps != nil {
		t.Errorf("blocked revoke must not report a dependent set to apply: %v", deps)
	}
}

// TestRevokeRoleMembershipCascadeSetWalksTransitiveChain verifies CASCADE
// (cascade=true) collects the full transitive chain of dependents, stopping
// the walk past a dependent row only when that row's own AdminOption is
// false, and never descending into a sibling grant unrelated to the chain.
func TestRevokeRoleMembershipCascadeSetWalksTransitiveChain(t *testing.T) {
	c := NewInMemory()
	const roleX = 500
	c.GrantRoleMembership(roleX, 700, 10, boolPtr(true), nil, nil)  // 700: ADMIN, granted by 10
	c.GrantRoleMembership(roleX, 800, 700, boolPtr(true), nil, nil) // 800: ADMIN, granted by 700
	c.GrantRoleMembership(roleX, 900, 800, nil, nil, nil)           // 900: no admin, granted by 800 -> chain stops past here
	c.GrantRoleMembership(roleX, 950, 10, boolPtr(true), nil, nil)  // unrelated sibling, granted by 10 directly

	deps, blocked := c.RevokeRoleMembershipCascadeSet(roleX, 700, 10, true)
	if blocked {
		t.Fatalf("CASCADE must never report blocked")
	}
	want := map[uint32]bool{800: true, 900: true}
	if len(deps) != len(want) {
		t.Fatalf("deps = %v, want members %v", deps, want)
	}
	for _, d := range deps {
		if !want[d.MemberOID] {
			t.Errorf("unexpected dependent member %d in cascade set: %v", d.MemberOID, deps)
		}
		delete(want, d.MemberOID)
	}
	if len(want) != 0 {
		t.Errorf("cascade set missing expected members: %v", want)
	}
}

// TestUnregisterRoleDropsMembershipRows verifies DROP ROLE cascades removal
// of any pg_auth_members row referencing the role's OID on either side,
// mirroring PostgreSQL's DropRole (user.c).
func TestUnregisterRoleDropsMembershipRows(t *testing.T) {
	c := NewInMemory()
	c.RegisterRoleWithOID("admin", 100)
	c.RegisterRoleWithOID("alice", 200)
	c.GrantRoleMembership(100, 200, 10, nil, nil, nil)

	c.UnregisterRole("alice")
	if entries := c.RoleMembershipEntries(); len(entries) != 0 {
		t.Errorf("dropping the member role should remove its membership row: %+v", entries)
	}

	c.RegisterRoleWithOID("alice", 200)
	c.GrantRoleMembership(100, 200, 10, nil, nil, nil)
	c.UnregisterRole("admin")
	if entries := c.RoleMembershipEntries(); len(entries) != 0 {
		t.Errorf("dropping the granted role should remove its membership row: %+v", entries)
	}
}

// TestIsSuperuser verifies IsSuperuser: the bootstrap superuser (OID 10) is
// always superuser; an unregistered/unknown OID and an ordinary registered
// role default to false; a role with RoleAttrs.Superuser set reports true.
// M0119-0004-ACLHEAP.
func TestIsSuperuser(t *testing.T) {
	c := NewInMemory()
	if !c.IsSuperuser(BootstrapSuperuserOID) {
		t.Errorf("bootstrap superuser (OID 10) must always report superuser")
	}
	if c.IsSuperuser(999) {
		t.Errorf("unknown OID must not report superuser")
	}

	c.RegisterRole("alice")
	aliceOid, _ := c.RoleOID("alice")
	if c.IsSuperuser(aliceOid) {
		t.Errorf("a plain registered role must not report superuser by default")
	}

	c.SetRoleAttrs("alice", RoleAttrs{Superuser: true})
	if !c.IsSuperuser(aliceOid) {
		t.Errorf("a role with RoleAttrs.Superuser=true must report superuser")
	}
}

// TestIsAdminOfRole verifies IsAdminOfRole mirrors is_admin_of_role
// (acl.c): a superuser member is always admin of any role; a role is never
// its own admin (the self-membership carve-out, unlike RoleIsMemberOf); a
// direct WITH ADMIN OPTION grant makes memberOid an admin; ADMIN OPTION is
// inherited transitively through ANY membership chain, not just inheritable
// ones; and a chain with no ADMIN OPTION anywhere along it reports false.
// M0119-0004-ACLHEAP.
func TestIsAdminOfRole(t *testing.T) {
	c := NewInMemory()
	if !c.IsAdminOfRole(BootstrapSuperuserOID, 12345) {
		t.Errorf("the bootstrap superuser must be admin of every role")
	}
	if c.IsAdminOfRole(100, 100) {
		t.Errorf("a role must never be its own admin (self-membership carve-out)")
	}
	if c.IsAdminOfRole(100, 200) {
		t.Errorf("unrelated roles must report false before any grant")
	}

	// 100 holds a direct WITH ADMIN OPTION grant on 200.
	c.GrantRoleMembership(200, 100, 10, boolPtr(true), nil, nil)
	if !c.IsAdminOfRole(100, 200) {
		t.Errorf("direct ADMIN OPTION grant not detected")
	}

	// 300 is a plain (non-admin) member of 100, which itself holds ADMIN
	// OPTION on 200 (via the row above) — 300 must inherit admin-of-200
	// transitively even though its own membership in 100 carries no ADMIN
	// OPTION (ROLERECURSE_MEMBERS ignores the option flags while walking).
	c.GrantRoleMembership(100, 300, 10, nil, nil, nil)
	if !c.IsAdminOfRole(300, 200) {
		t.Errorf("transitive ADMIN OPTION inheritance not detected")
	}

	// 400 is a plain member of 300, but 300 has no ADMIN OPTION on 200 (only
	// on nothing) — no path to admin-of-200 exists.
	c.GrantRoleMembership(300, 400, 10, nil, nil, nil)
	if c.IsAdminOfRole(400, 100) {
		t.Errorf("a chain with no ADMIN OPTION anywhere must report false")
	}
}

// TestHasPrivsOfRole verifies HasPrivsOfRole mirrors has_privs_of_role
// (acl.c): a role always has privileges of itself, a superuser has
// privileges of every role, a role is reachable via a chain of INHERIT-true
// pg_auth_members rows, and a non-inheritable link blocks the walk past that
// point — unlike RoleIsMemberOf (which ignores INHERIT entirely). M0119-0004-
// ACLHEAP (check_role_grantor follow-up).
func TestHasPrivsOfRole(t *testing.T) {
	c := NewInMemory()
	if !c.HasPrivsOfRole(100, 100) {
		t.Errorf("a role must always have privileges of itself")
	}
	if !c.HasPrivsOfRole(BootstrapSuperuserOID, 12345) {
		t.Errorf("the bootstrap superuser must have privileges of every role")
	}
	if c.HasPrivsOfRole(100, 200) {
		t.Errorf("unrelated roles must report false before any grant")
	}

	// 100 is an INHERIT member of 200 (no admin option needed).
	c.GrantRoleMembership(200, 100, 10, nil, boolPtr(true), nil)
	if !c.HasPrivsOfRole(100, 200) {
		t.Errorf("a direct INHERIT membership must grant privileges of the role")
	}

	// 300 is a transitive INHERIT member of 200, via 100.
	c.GrantRoleMembership(100, 300, 10, nil, boolPtr(true), nil)
	if !c.HasPrivsOfRole(300, 200) {
		t.Errorf("a transitive INHERIT chain must grant privileges of the role")
	}

	// 400 is a member of 300, but WITHOUT INHERIT — the walk must not pass
	// through this edge, so 400 must not inherit 300's (or 200's) privileges.
	c.GrantRoleMembership(300, 400, 10, nil, boolPtr(false), nil)
	if c.HasPrivsOfRole(400, 300) {
		t.Errorf("a non-inheritable membership must not grant privileges of the role")
	}
	if c.HasPrivsOfRole(400, 200) {
		t.Errorf("a non-inheritable link must block privilege inheritance past it")
	}
}

// TestSelectBestAdmin verifies SelectBestAdmin mirrors select_best_admin
// (acl.c): a role can never be its own admin, a direct ADMIN OPTION grant is
// preferred (returns memberOid itself), ADMIN OPTION reachable via an
// INHERIT-true chain resolves to the closest role that directly holds it,
// and a non-inheritable link blocks the walk from reaching an ancestor's
// ADMIN OPTION. M0119-0004-ACLHEAP (check_role_grantor follow-up).
func TestSelectBestAdmin(t *testing.T) {
	c := NewInMemory()
	if got := c.SelectBestAdmin(100, 100); got != 0 {
		t.Errorf("a role must never be its own admin, got %d", got)
	}
	if got := c.SelectBestAdmin(100, 200); got != 0 {
		t.Errorf("unrelated roles must report 0 before any grant, got %d", got)
	}

	// 100 holds a direct WITH ADMIN OPTION grant on 200.
	c.GrantRoleMembership(200, 100, 10, boolPtr(true), nil, nil)
	if got := c.SelectBestAdmin(100, 200); got != 100 {
		t.Errorf("direct ADMIN OPTION must resolve to memberOid itself, got %d", got)
	}

	// 300 is an INHERIT member of 100, which itself holds ADMIN OPTION on
	// 200 — 300's best admin for 200 must resolve to 100, the closest
	// ancestor that directly holds it.
	c.GrantRoleMembership(100, 300, 10, nil, boolPtr(true), nil)
	if got := c.SelectBestAdmin(300, 200); got != 100 {
		t.Errorf("transitive ADMIN OPTION must resolve to the closest holder (100), got %d", got)
	}

	// 400 is a member of 300, but WITHOUT INHERIT — the walk must not reach
	// 100's ADMIN OPTION on 200 through this non-inheritable edge.
	c.GrantRoleMembership(300, 400, 10, nil, boolPtr(false), nil)
	if got := c.SelectBestAdmin(400, 200); got != 0 {
		t.Errorf("a non-inheritable link must block reaching an ancestor's ADMIN OPTION, got %d", got)
	}
}

// TestPredefinedRoleResolution verifies PG18's 16 built-in "pg_*" predefined
// roles resolve through RoleOID/RoleExists exactly like a registered role
// (needed so `GRANT pg_read_all_data TO alice` and similar work), report
// true from IsPredefinedRole, and are immune to the mutations that operate
// only on the user-created `roles` registry (RegisterRole re-registration
// under the same name must not clobber the predefined OID; UnregisterRole
// must not remove it). M0119-0004-ACLHEAP.
func TestPredefinedRoleResolution(t *testing.T) {
	c := NewInMemory()

	oid, ok := c.RoleOID("pg_database_owner")
	if !ok || oid != RoleOIDPgDatabaseOwner {
		t.Fatalf("RoleOID(pg_database_owner) = (%d, %v), want (%d, true)", oid, ok, RoleOIDPgDatabaseOwner)
	}
	if !c.RoleExists("pg_database_owner") {
		t.Errorf("RoleExists(pg_database_owner) = false, want true")
	}
	if !c.IsPredefinedRole("pg_database_owner") {
		t.Errorf("IsPredefinedRole(pg_database_owner) = false, want true")
	}
	// Case-insensitive, matching every other role-name lookup in this file.
	if !c.IsPredefinedRole("PG_DATABASE_OWNER") {
		t.Errorf("IsPredefinedRole must be case-insensitive")
	}

	oid, ok = c.RoleOID("pg_read_all_data")
	if !ok || oid != 6181 {
		t.Fatalf("RoleOID(pg_read_all_data) = (%d, %v), want (6181, true)", oid, ok)
	}

	if c.RoleExists("pg_not_a_real_role") {
		t.Errorf("RoleExists(pg_not_a_real_role) = true, want false")
	}
	if c.IsPredefinedRole("pg_not_a_real_role") {
		t.Errorf("IsPredefinedRole(pg_not_a_real_role) = true, want false")
	}

	// Not part of the user-role registry: RegisterRole/UnregisterRole must
	// not disturb it.
	c.RegisterRole("pg_database_owner")
	oid, ok = c.RoleOID("pg_database_owner")
	if !ok || oid != RoleOIDPgDatabaseOwner {
		t.Errorf("RegisterRole must not shadow the predefined OID: got (%d, %v)", oid, ok)
	}
	c.UnregisterRole("pg_database_owner")
	if !c.RoleExists("pg_database_owner") {
		t.Errorf("UnregisterRole must not remove a predefined role")
	}

	// Excluded from AllRoleStates so pg_authid heap sync's own dedicated
	// predefined-role writer never sees a duplicate.
	for _, s := range c.AllRoleStates() {
		if s.Name == "pg_database_owner" || strings.HasPrefix(s.Name, "pg_") {
			t.Errorf("AllRoleStates must exclude predefined roles, found %q", s.Name)
		}
	}
}
