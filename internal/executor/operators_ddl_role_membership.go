package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// execRoleMembershipChange applies a `GRANT <role> TO <role>`/`REVOKE ...
// FROM <role>` role-membership statement (pg_auth_members), resolving role
// names to their stable catalog OIDs and updating the InMemory roleMembers
// registry. Unlike every other ACL variant execCompatNoop dispatches, role
// membership has no object to re-sync a heap row for — pg_auth_members is
// virtual, sourced entirely from this registry (RoleMembershipEntries).
// M0119-0004-ACLHEAP.
func (o *ddlOp) execRoleMembershipChange(rc *parser.RoleMembershipChange) error {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	resolveRole := func(name string) (uint32, error) {
		// A role-membership grantee/grantor has no PUBLIC special case —
		// unlike an object-privilege ACL, PostgreSQL rejects PUBLIC here
		// with the SAME "role does not exist" error as any other unknown
		// name (get_rolespec_oid, acl.c), which RoleOID's plain lookup
		// already produces since "public" is never registered as a role.
		oid, ok := im.RoleOID(name)
		if !ok {
			return 0, &ExecError{Code: "42704", Message: fmt.Sprintf("role %q does not exist", name)}
		}
		return oid, nil
	}
	if rc.Revoke {
		for _, roleName := range rc.Roles {
			roleOid, err := resolveRole(roleName)
			if err != nil {
				return err
			}
			for _, memberName := range rc.Grantees {
				memberOid, err := resolveRole(memberName)
				if err != nil {
					return err
				}
				im.RevokeRoleMembership(roleOid, memberOid, rc.RevokeOption)
				if o.ctx.WAL != nil {
					_, _, _ = o.ctx.WAL.Append(wal.EncodeRevokeRoleMembership(roleOid, memberOid, rc.RevokeOption))
				}
			}
		}
		return nil
	}
	grantorOid := o.currentDDLOwnerOID()
	if rc.GrantedBy != "" {
		if oid, err := resolveRole(rc.GrantedBy); err == nil {
			grantorOid = oid
		}
	}
	for _, roleName := range rc.Roles {
		roleOid, err := resolveRole(roleName)
		if err != nil {
			return err
		}
		for _, memberName := range rc.Grantees {
			memberOid, err := resolveRole(memberName)
			if err != nil {
				return err
			}
			// Reject a membership that would create a cycle (including the
			// trivial self-grant): roleOid must not already be a
			// (transitive) member of memberOid, mirroring
			// is_member_of_role_nosuper's check in AddRoleMems (user.c).
			if im.RoleIsMemberOf(roleOid, memberOid) {
				return &ExecError{Code: "0LP01", Message: fmt.Sprintf(
					"role %q is a member of role %q", roleName, memberName)}
			}
			im.GrantRoleMembership(roleOid, memberOid, grantorOid, rc.AdminOption, rc.InheritOption, rc.SetOption)
			if o.ctx.WAL != nil {
				_, _, _ = o.ctx.WAL.Append(wal.EncodeGrantRoleMembership(roleOid, memberOid, grantorOid, rc.AdminOption, rc.InheritOption, rc.SetOption))
			}
		}
	}
	return nil
}
