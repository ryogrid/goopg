package executor

import (
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
