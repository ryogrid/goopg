package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func adminOptTrue() *bool { v := true; return &v }

// TestExecRoleMembershipChangeRequiresAdminOption verifies
// check_role_membership_authorization's "otherwise, must have admin option
// on the role to be changed" branch: a non-superuser currentUserID with no
// ADMIN OPTION on the target role is rejected (42501), the same currentUserID
// after being granted ADMIN OPTION succeeds, and the bootstrap superuser
// always succeeds regardless of admin option. M0119-0004-ACLHEAP.
func TestExecRoleMembershipChangeRequiresAdminOption(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRole("alice")
	cat.RegisterRole("grp")
	cat.RegisterRole("newmember")
	aliceOid, _ := cat.RoleOID("alice")
	grpOid, _ := cat.RoleOID("grp")

	ctx := &Context{Catalog: cat, NonSuperuserRole: "alice"}
	op := &ddlOp{ctx: ctx}
	rc := &parser.RoleMembershipChange{
		Roles:    []string{"grp"},
		Grantees: []string{"newmember"},
	}

	// alice has no ADMIN OPTION on grp yet: rejected.
	err := op.execRoleMembershipChange(rc)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "42501" {
		t.Fatalf("expected 42501 permission-denied error, got %#v", err)
	}
	if got := cat.RoleMembershipEntries(); len(got) != 0 {
		t.Fatalf("rejected grant must not mutate state, got %+v", got)
	}

	// Grant alice ADMIN OPTION on grp (by the bootstrap superuser): now the
	// same GRANT statement must succeed.
	cat.GrantRoleMembership(grpOid, aliceOid, catalog.BootstrapSuperuserOID, adminOptTrue(), nil, nil)
	if err := op.execRoleMembershipChange(rc); err != nil {
		t.Fatalf("expected grant to succeed once alice holds ADMIN OPTION on grp, got %v", err)
	}

	// The bootstrap superuser (no NonSuperuserRole override) always
	// succeeds, admin option or not.
	cat2 := catalog.NewInMemory()
	cat2.RegisterRole("grp")
	cat2.RegisterRole("newmember")
	superCtx := &Context{Catalog: cat2}
	superOp := &ddlOp{ctx: superCtx}
	if err := superOp.execRoleMembershipChange(rc); err != nil {
		t.Fatalf("expected bootstrap superuser grant to succeed, got %v", err)
	}
}

// TestExecRoleMembershipChangeSuperuserRoleRequiresSuperuserGrantor verifies
// check_role_membership_authorization's "to mess with a superuser role, you
// gotta be superuser" branch: even a currentUserID holding ADMIN OPTION on a
// superuser-flagged role is rejected; only another superuser may grant or
// revoke membership in it. M0119-0004-ACLHEAP.
func TestExecRoleMembershipChangeSuperuserRoleRequiresSuperuserGrantor(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRole("alice")
	cat.RegisterRole("superrole")
	cat.RegisterRole("newmember")
	aliceOid, _ := cat.RoleOID("alice")
	superroleOid, _ := cat.RoleOID("superrole")
	cat.SetRoleAttrs("superrole", catalog.RoleAttrs{Superuser: true})
	// alice even holds ADMIN OPTION on superrole — must still be rejected,
	// since superuser-ness overrides the ordinary admin-option path entirely.
	cat.GrantRoleMembership(superroleOid, aliceOid, catalog.BootstrapSuperuserOID, adminOptTrue(), nil, nil)

	ctx := &Context{Catalog: cat, NonSuperuserRole: "alice"}
	op := &ddlOp{ctx: ctx}
	rc := &parser.RoleMembershipChange{
		Roles:    []string{"superrole"},
		Grantees: []string{"newmember"},
	}
	err := op.execRoleMembershipChange(rc)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "42501" {
		t.Fatalf("expected 42501 permission-denied error, got %#v", err)
	}

	// The bootstrap superuser succeeds.
	superCtx := &Context{Catalog: cat}
	superOp := &ddlOp{ctx: superCtx}
	if err := superOp.execRoleMembershipChange(rc); err != nil {
		t.Fatalf("expected bootstrap superuser grant to succeed, got %v", err)
	}
}

// TestExecRoleMembershipChangeRevokeRequiresAdminOption mirrors the GRANT
// case for REVOKE: a non-superuser, non-admin currentUserID cannot revoke a
// role's membership either. M0119-0004-ACLHEAP.
func TestExecRoleMembershipChangeRevokeRequiresAdminOption(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRole("alice")
	cat.RegisterRole("grp")
	cat.RegisterRole("member")
	aliceOid, _ := cat.RoleOID("alice")
	grpOid, _ := cat.RoleOID("grp")
	memberOid, _ := cat.RoleOID("member")
	cat.GrantRoleMembership(grpOid, memberOid, catalog.BootstrapSuperuserOID, nil, nil, nil)

	ctx := &Context{Catalog: cat, NonSuperuserRole: "alice"}
	op := &ddlOp{ctx: ctx}
	rc := &parser.RoleMembershipChange{
		Revoke:   true,
		Roles:    []string{"grp"},
		Grantees: []string{"member"},
	}
	err := op.execRoleMembershipChange(rc)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "42501" {
		t.Fatalf("expected 42501 permission-denied error, got %#v", err)
	}
	if got := cat.RoleMembershipEntries(); len(got) != 1 {
		t.Fatalf("rejected revoke must not mutate state, got %+v", got)
	}

	// Grant alice ADMIN OPTION on grp: the same REVOKE now succeeds.
	cat.GrantRoleMembership(grpOid, aliceOid, catalog.BootstrapSuperuserOID, adminOptTrue(), nil, nil)
	if err := op.execRoleMembershipChange(rc); err != nil {
		t.Fatalf("expected revoke to succeed once alice holds ADMIN OPTION on grp, got %v", err)
	}
}

// TestExecRoleMembershipChangeInfersGrantorViaInheritedAdmin verifies
// check_role_grantor's implicit-grantor inference (no GRANTED BY): a
// non-superuser currentUserID who holds ADMIN OPTION on the target role only
// indirectly — by INHERITing (WITH INHERIT TRUE, the default) the privileges
// of an intermediate role that itself directly holds ADMIN OPTION — is
// recorded as having granted via that intermediate role
// (catalog.SelectBestAdmin), not as the current user itself. M0119-0004-
// ACLHEAP (check_role_grantor follow-up).
func TestExecRoleMembershipChangeInfersGrantorViaInheritedAdmin(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRole("manager")
	cat.RegisterRole("alice")
	cat.RegisterRole("grp")
	cat.RegisterRole("newmember")
	managerOid, _ := cat.RoleOID("manager")
	aliceOid, _ := cat.RoleOID("alice")
	grpOid, _ := cat.RoleOID("grp")
	newmemberOid, _ := cat.RoleOID("newmember")

	// manager directly holds ADMIN OPTION on grp; alice INHERITs manager's
	// privileges (default WITH INHERIT TRUE) but has no direct grant on grp.
	cat.GrantRoleMembership(grpOid, managerOid, catalog.BootstrapSuperuserOID, adminOptTrue(), nil, nil)
	cat.GrantRoleMembership(managerOid, aliceOid, catalog.BootstrapSuperuserOID, nil, nil, nil)

	ctx := &Context{Catalog: cat, NonSuperuserRole: "alice"}
	op := &ddlOp{ctx: ctx}
	rc := &parser.RoleMembershipChange{
		Roles:    []string{"grp"},
		Grantees: []string{"newmember"},
	}
	if err := op.execRoleMembershipChange(rc); err != nil {
		t.Fatalf("expected grant to succeed via inherited ADMIN OPTION, got %v", err)
	}

	entries := cat.RoleMembershipEntries()
	var found *catalog.RoleMembership
	for i := range entries {
		if entries[i].RoleOID == grpOid && entries[i].MemberOID == newmemberOid {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a grp->newmember row, got %+v", entries)
	}
	if found.GrantorOID != managerOid {
		t.Errorf("expected grantor to be inferred as manager (%d), got %d (alice=%d)", managerOid, found.GrantorOID, aliceOid)
	}
}

// TestExecRoleMembershipChangeGrantedByRequiresPrivsOfGrantor verifies
// check_role_grantor's explicit-GRANTED-BY impersonation guard: naming a
// GRANTED BY role whose privileges the current user does not possess is
// rejected (42501) regardless of whether that role itself holds ADMIN
// OPTION on the target; once the current user is made an (inheriting)
// member of the named grantor, the same statement succeeds and records that
// grantor, not the current user. M0119-0004-ACLHEAP.
func TestExecRoleMembershipChangeGrantedByRequiresPrivsOfGrantor(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRole("alice")
	cat.RegisterRole("bob")
	cat.RegisterRole("grp")
	cat.RegisterRole("newmember")
	aliceOid, _ := cat.RoleOID("alice")
	bobOid, _ := cat.RoleOID("bob")
	grpOid, _ := cat.RoleOID("grp")
	newmemberOid, _ := cat.RoleOID("newmember")

	// bob directly holds ADMIN OPTION on grp; alice does NOT have privileges
	// of bob yet, but does have her own direct ADMIN OPTION on grp (so
	// checkRoleMembershipAuthorization passes) — isolating the impersonation
	// guard from the authorization check.
	cat.GrantRoleMembership(grpOid, bobOid, catalog.BootstrapSuperuserOID, adminOptTrue(), nil, nil)
	cat.GrantRoleMembership(grpOid, aliceOid, catalog.BootstrapSuperuserOID, adminOptTrue(), nil, nil)

	ctx := &Context{Catalog: cat, NonSuperuserRole: "alice"}
	op := &ddlOp{ctx: ctx}
	rc := &parser.RoleMembershipChange{
		Roles:     []string{"grp"},
		Grantees:  []string{"newmember"},
		GrantedBy: "bob",
	}
	err := op.execRoleMembershipChange(rc)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "42501" {
		t.Fatalf("expected 42501 impersonation error, got %#v", err)
	}

	// Make alice an (inheriting) member of bob: the same statement now
	// succeeds and records bob, not alice, as grantor.
	cat.GrantRoleMembership(bobOid, aliceOid, catalog.BootstrapSuperuserOID, nil, nil, nil)
	if err := op.execRoleMembershipChange(rc); err != nil {
		t.Fatalf("expected grant to succeed once alice has privileges of bob, got %v", err)
	}
	entries := cat.RoleMembershipEntries()
	var found *catalog.RoleMembership
	for i := range entries {
		if entries[i].RoleOID == grpOid && entries[i].MemberOID == newmemberOid {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a grp->newmember row, got %+v", entries)
	}
	if found.GrantorOID != bobOid {
		t.Errorf("expected grantor to be the explicit GRANTED BY role (bob=%d), got %d", bobOid, found.GrantorOID)
	}
}

// TestExecRoleMembershipChangeGrantedByRequiresDirectAdminOption verifies
// check_role_grantor's GRANT-specific rule that an explicit GRANTED BY role
// must itself directly hold ADMIN OPTION on the target — merely inheriting
// it from a further ancestor is not enough (that would let a GRANT claim to
// flow from a grantor who never actually held the option). M0119-0004-
// ACLHEAP.
func TestExecRoleMembershipChangeGrantedByRequiresDirectAdminOption(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRole("root")
	cat.RegisterRole("bob")
	cat.RegisterRole("alice")
	cat.RegisterRole("grp")
	cat.RegisterRole("newmember")
	rootOid, _ := cat.RoleOID("root")
	bobOid, _ := cat.RoleOID("bob")
	aliceOid, _ := cat.RoleOID("alice")
	grpOid, _ := cat.RoleOID("grp")

	// root directly holds ADMIN OPTION on grp; bob only INHERITs it from
	// root (no direct grant on grp); alice inherits bob's (and thus root's)
	// privileges too, and also holds a direct ADMIN OPTION on grp so
	// checkRoleMembershipAuthorization passes.
	cat.GrantRoleMembership(grpOid, rootOid, catalog.BootstrapSuperuserOID, adminOptTrue(), nil, nil)
	cat.GrantRoleMembership(rootOid, bobOid, catalog.BootstrapSuperuserOID, nil, nil, nil)
	cat.GrantRoleMembership(bobOid, aliceOid, catalog.BootstrapSuperuserOID, nil, nil, nil)
	cat.GrantRoleMembership(grpOid, aliceOid, catalog.BootstrapSuperuserOID, adminOptTrue(), nil, nil)

	ctx := &Context{Catalog: cat, NonSuperuserRole: "alice"}
	op := &ddlOp{ctx: ctx}
	rc := &parser.RoleMembershipChange{
		Roles:     []string{"grp"},
		Grantees:  []string{"newmember"},
		GrantedBy: "bob",
	}
	err := op.execRoleMembershipChange(rc)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "42501" {
		t.Fatalf("expected 42501 (grantor must directly hold ADMIN OPTION), got %#v", err)
	}
	if !strings.Contains(execErr.Detail, "ADMIN option on role") {
		t.Errorf("expected detail to name the ADMIN OPTION requirement, got %q", execErr.Detail)
	}
}

// TestExecRoleMembershipChangeUnresolvableGrantedByErrors verifies GRANTED
// BY naming an unknown role is a hard "role does not exist" error, not a
// silent fall-back to the current user as grantor. M0119-0004-ACLHEAP.
func TestExecRoleMembershipChangeUnresolvableGrantedByErrors(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRole("grp")
	cat.RegisterRole("newmember")

	op := &ddlOp{ctx: &Context{Catalog: cat}}
	rc := &parser.RoleMembershipChange{
		Roles:     []string{"grp"},
		Grantees:  []string{"newmember"},
		GrantedBy: "nosuchrole",
	}
	err := op.execRoleMembershipChange(rc)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "42704" {
		t.Fatalf("expected 42704 role-does-not-exist error, got %#v", err)
	}
	if got := cat.RoleMembershipEntries(); len(got) != 0 {
		t.Fatalf("rejected grant must not mutate state, got %+v", got)
	}
}
