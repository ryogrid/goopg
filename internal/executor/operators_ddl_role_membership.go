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
	// The grantor row a bare REVOKE (no GRANTED BY) targets is, in real PG,
	// whatever check_role_grantor(is_grant=false) infers for the current
	// session (usually the current user, falling back to an inherited/
	// superuser path this codebase does not model — see the deferral
	// ledger). goopg's simplified session model reuses the SAME resolution
	// GRANT already applies: the effective DDL-owner role, or an explicit
	// GRANTED BY override. Shared across both branches below so REVOKE only
	// ever touches the specific (role, member, grantor) row that grantor
	// actually owns, leaving any OTHER grantor's independent row on the same
	// (role, member) pair untouched — real PG's (roleid, member, grantor)
	// unique index. M0119-0004-ACLHEAP.
	grantorOid := o.currentDDLOwnerOID()
	if rc.GrantedBy != "" {
		if oid, err := resolveRole(rc.GrantedBy); err == nil {
			grantorOid = oid
		}
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
				// A whole-row revoke ("") or `ADMIN OPTION FOR` ("admin")
				// can cascade to grants memberOid itself made (as grantor)
				// using the ADMIN OPTION being taken away; INHERIT/SET
				// OPTION FOR never cascade (plan_single_revoke, user.c).
				if rc.RevokeOption == "" || rc.RevokeOption == "admin" {
					deps, blocked := im.RevokeRoleMembershipCascadeSet(roleOid, memberOid, grantorOid, rc.Cascade)
					if blocked {
						return &ExecError{Code: "2BP01", Message: "dependent privileges exist",
							Hint: "Use CASCADE to revoke them too."}
					}
					for _, dep := range deps {
						im.RevokeRoleMembership(roleOid, dep.MemberOID, dep.GrantorOID, "")
						if o.ctx.WAL != nil {
							_, _, _ = o.ctx.WAL.Append(wal.EncodeRevokeRoleMembership(roleOid, dep.MemberOID, dep.GrantorOID, ""))
						}
					}
				}
				im.RevokeRoleMembership(roleOid, memberOid, grantorOid, rc.RevokeOption)
				if o.ctx.WAL != nil {
					_, _, _ = o.ctx.WAL.Append(wal.EncodeRevokeRoleMembership(roleOid, memberOid, grantorOid, rc.RevokeOption))
				}
			}
		}
		return nil
	}
	for _, roleName := range rc.Roles {
		roleOid, err := resolveRole(roleName)
		if err != nil {
			return err
		}
		memberOids := make([]uint32, 0, len(rc.Grantees))
		for _, memberName := range rc.Grantees {
			memberOid, err := resolveRole(memberName)
			if err != nil {
				return err
			}
			// Reject a membership that would create a role-member cycle
			// (including the trivial self-grant): roleOid must not already
			// be a (transitive) member of memberOid, mirroring
			// is_member_of_role_nosuper's check in AddRoleMems (user.c).
			if im.RoleIsMemberOf(roleOid, memberOid) {
				return &ExecError{Code: "0LP01", Message: fmt.Sprintf(
					"role %q is a member of role %q", roleName, memberName)}
			}
			memberOids = append(memberOids, memberOid)
		}

		// Reject a WITH ADMIN TRUE grant that would create a member-grantor
		// loop (AddRoleMems, user.c ~1751): a DIFFERENT circularity than the
		// role-member check above — this one is about the ADMIN OPTION grant
		// chain, checked once for the whole grantee batch, before any of
		// this roleOid's grants are applied (matching AddRoleMems' ordering:
		// per-member sanity checks, then one whole-batch admin check, then
		// the catalog update loop). The bootstrap superuser can never be on
		// either side of a circular grant.
		if rc.AdminOption != nil && *rc.AdminOption && grantorOid != catalog.BootstrapSuperuserOID {
			if im.GrantRoleWouldCreateGrantorCycle(roleOid, memberOids, grantorOid) {
				return &ExecError{Code: "0LP01", Message: "ADMIN option cannot be granted back to your own grantor"}
			}
		}

		for _, memberOid := range memberOids {
			im.GrantRoleMembership(roleOid, memberOid, grantorOid, rc.AdminOption, rc.InheritOption, rc.SetOption)
			if o.ctx.WAL != nil {
				_, _, _ = o.ctx.WAL.Append(wal.EncodeGrantRoleMembership(roleOid, memberOid, grantorOid, rc.AdminOption, rc.InheritOption, rc.SetOption))
			}
		}
	}
	return nil
}
