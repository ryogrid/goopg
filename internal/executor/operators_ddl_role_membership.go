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
	// currentUserID is check_role_membership_authorization's currentUserId
	// (user.c: GetUserId(), captured once before any GRANTED BY override) —
	// the actual invoking session role whose OWN privileges gate whether
	// this GRANT/REVOKE is even allowed, as opposed to grantorOid below
	// (who gets RECORDED as grantor-of-record, which GRANTED BY can
	// redirect). goopg's simplified session model has no separate "current
	// user" from "effective DDL-owner role", so both start from the same
	// resolution. M0119-0004-ACLHEAP.
	currentUserID := o.currentDDLOwnerOID()
	// GRANTED BY's spec is resolved to an OID exactly once, up front, shared
	// across every target role in this statement — mirrors
	// roleSpecsToIds/get_rolespec_oid(missing_ok=false) running ahead of the
	// per-role AddRoleMems/DelRoleMems loop in ExecGrantRoleStmt (user.c): an
	// unresolvable GRANTED BY name is a hard "role does not exist" error, not
	// a silent fall-back to the current user.
	explicitGrantorOid := uint32(0)
	haveExplicitGrantor := rc.GrantedBy != ""
	if haveExplicitGrantor {
		oid, err := resolveRole(rc.GrantedBy)
		if err != nil {
			return err
		}
		explicitGrantorOid = oid
	}
	if rc.Revoke {
		for _, roleName := range rc.Roles {
			roleOid, err := resolveRole(roleName)
			if err != nil {
				return err
			}
			if err := o.checkRoleMembershipAuthorization(im, currentUserID, roleOid, roleName, false); err != nil {
				return err
			}
			grantorOid, err := o.checkRoleGrantor(im, currentUserID, roleOid, roleName, explicitGrantorOid, haveExplicitGrantor, false)
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
		if err := o.checkRoleMembershipAuthorization(im, currentUserID, roleOid, roleName, true); err != nil {
			return err
		}
		grantorOid, err := o.checkRoleGrantor(im, currentUserID, roleOid, roleName, explicitGrantorOid, haveExplicitGrantor, true)
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

// checkRoleMembershipAuthorization ports check_role_membership_authorization
// (postgres/src/backend/commands/user.c): currentUserID must have permission
// to modify roleOid's membership list. A superuser roleOid can only be
// touched by a superuser currentUserID; otherwise currentUserID must hold
// ADMIN OPTION on roleOid (directly or transitively — catalog.IsAdminOfRole).
// roleName is only used for the error message. Called once per roleOid in
// the outer loop of both the GRANT and REVOKE branches, before any grantee is
// resolved — matching user.c's per-statement-role check, which runs before
// AddRoleMems/DelRoleMems are ever invoked for that role. M0119-0004-ACLHEAP.
//
// Not yet ported: the ROLE_PG_DATABASE_OWNER "cannot have explicit members"
// carve-out (unreachable today since predefined roles like
// pg_database_owner are never registered in the live role-name registry —
// resolveRole already fails them with "role does not exist", a separate,
// out-of-scope gap) — see the deferral ledger.
func (o *ddlOp) checkRoleMembershipAuthorization(im *catalog.InMemory, currentUserID, roleOid uint32, roleName string, isGrant bool) error {
	verb := "grant"
	if !isGrant {
		verb = "revoke"
	}
	if im.IsSuperuser(roleOid) {
		if !im.IsSuperuser(currentUserID) {
			return &ExecError{
				Code:    "42501",
				Message: fmt.Sprintf("permission denied to %s role %q", verb, roleName),
				Detail:  fmt.Sprintf("Only roles with the SUPERUSER attribute may %s roles with the SUPERUSER attribute.", verb),
			}
		}
		return nil
	}
	if !im.IsAdminOfRole(currentUserID, roleOid) {
		return &ExecError{
			Code:    "42501",
			Message: fmt.Sprintf("permission denied to %s role %q", verb, roleName),
			Detail:  fmt.Sprintf("Only roles with the ADMIN option on role %q may %s this role.", roleName, verb),
		}
	}
	return nil
}

// checkRoleGrantor ports check_role_grantor (postgres/src/backend/commands/
// user.c): resolves the OID to be RECORDED as grantor-of-record for a
// GRANT/REVOKE of roleOid's membership, called once per target roleOid AFTER
// checkRoleMembershipAuthorization has already confirmed currentUserID may
// touch roleOid's membership list at all (matching AddRoleMems/DelRoleMems'
// call ordering — check_role_membership_authorization runs in
// ExecGrantRoleStmt before AddRoleMems/DelRoleMems, and check_role_grantor
// runs as the first thing inside those).
//
// No explicit GRANTED BY (explicitGrantorOid invalid): a superuser
// currentUserID always grants/revokes as the bootstrap superuser (recording
// against a fixed identity that never depends on any existing grant row);
// otherwise the grantor is inferred as the closest role (fewest hops,
// preferring currentUserID's own direct ADMIN OPTION) whose privileges
// currentUserID inherits and which itself directly holds ADMIN OPTION on
// roleOid (catalog.SelectBestAdmin). No best-admin candidate is guaranteed
// to exist even though checkRoleMembershipAuthorization already approved
// the operation: that check (IsAdminOfRole, ROLERECURSE_MEMBERS) walks ANY
// membership chain, while SelectBestAdmin (ROLERECURSE_PRIVS) only walks
// INHERIT-marked edges — a WITH INHERIT FALSE membership to an admin-bearing
// role authorizes but yields no grantor, raising XX000 "no possible
// grantors" exactly as upstream's elog does (user.c:2231).
//
// Explicit GRANTED BY: the named role must be one whose privileges
// currentUserID possesses (catalog.HasPrivsOfRole) — otherwise this is an
// impersonation attempt and is rejected regardless of is_grant. For GRANT
// specifically (REVOKE gets a pass — "no matching grant should exist
// anyway, but if it somehow does, let the user get rid of it"), the named
// grantor must ALSO directly hold ADMIN OPTION on roleOid itself — not
// merely inherit it from a further role — unless it is the bootstrap
// superuser, which is exempt from that requirement. M0119-0004-ACLHEAP.
func (o *ddlOp) checkRoleGrantor(im *catalog.InMemory, currentUserID, roleOid uint32, roleName string, explicitGrantorOid uint32, haveExplicitGrantor, isGrant bool) (uint32, error) {
	if !haveExplicitGrantor {
		if im.IsSuperuser(currentUserID) {
			return catalog.BootstrapSuperuserOID, nil
		}
		best := im.SelectBestAdmin(currentUserID, roleOid)
		if best == 0 {
			// Reachable despite checkRoleMembershipAuthorization's prior
			// approval: IsAdminOfRole (is_admin_of_role, ROLERECURSE_MEMBERS)
			// walks ANY membership chain regardless of INHERIT, but
			// SelectBestAdmin (select_best_admin, ROLERECURSE_PRIVS) only
			// walks INHERIT-marked edges — a WITH INHERIT FALSE membership
			// to an admin-bearing intermediate role authorizes the op but
			// yields no inheritable grantor. Confirmed against live PG 18.3:
			// GRANT tgt TO mid WITH ADMIN OPTION; GRANT mid TO cur WITH
			// INHERIT FALSE; then cur running GRANT tgt TO x raises this
			// exact elog. Mirrors user.c:2231 verbatim (SQLSTATE XX000, not
			// translated/localized in upstream either).
			return 0, &ExecError{Code: "XX000", Message: "no possible grantors"}
		}
		return best, nil
	}
	if !im.HasPrivsOfRole(currentUserID, explicitGrantorOid) {
		grantorName := im.RoleNameForOID(explicitGrantorOid)
		verb := "grant privileges as"
		if !isGrant {
			verb = "revoke privileges granted by"
		}
		return 0, &ExecError{
			Code:    "42501",
			Message: fmt.Sprintf("permission denied to %s role %q", verb, grantorName),
			Detail:  fmt.Sprintf("Only roles with privileges of role %q may %s this role.", grantorName, verb),
		}
	}
	if isGrant && explicitGrantorOid != catalog.BootstrapSuperuserOID {
		if im.SelectBestAdmin(explicitGrantorOid, roleOid) != explicitGrantorOid {
			grantorName := im.RoleNameForOID(explicitGrantorOid)
			return 0, &ExecError{
				Code:    "42501",
				Message: fmt.Sprintf("permission denied to grant privileges as role %q", grantorName),
				Detail:  fmt.Sprintf("The grantor must have the ADMIN option on role %q.", roleName),
			}
		}
	}
	return explicitGrantorOid, nil
}
