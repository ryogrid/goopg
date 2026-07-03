package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/parser"
)

// parameterACLAllPrivs is the expansion of GRANT ALL [PRIVILEGES] ON
// PARAMETER: the full ACL_ALL_RIGHTS_PARAMETER_ACL set (acl.h) — SET then
// ALTER SYSTEM, PostgreSQL's canonical aclitemout letter order. Also the
// owner's implicit acldefault('p', BOOTSTRAP_SUPERUSERID) set, used to seed a
// materialized owner entry on the first owner-side REVOKE (mirrors
// databaseACLAllPrivs / execDatabaseACLChange). M0119-0004-ACLHEAP (parameter
// ACL half).
var parameterACLAllPrivs = []string{"SET", "ALTER SYSTEM"}

// normalizeParameterPriv maps a parsed GRANT/REVOKE ON PARAMETER privilege
// keyword to its canonical form(s), expanding ALL/ALL PRIVILEGES to the full
// set. An unrecognised keyword yields nil (a no-op grant), mirroring
// normalizeDatabasePriv. M0119-0004-ACLHEAP (parameter ACL half).
func normalizeParameterPriv(priv string) []string {
	switch strings.ToUpper(strings.TrimSpace(priv)) {
	case "ALL", "ALL PRIVILEGES":
		return parameterACLAllPrivs
	case "SET":
		return []string{"SET"}
	case "ALTER SYSTEM":
		return []string{"ALTER SYSTEM"}
	default:
		return nil
	}
}

// checkParameterACLName ports check_GUC_name_for_parameter_acl (guc.c): a
// brand-new pg_parameter_acl entry may only be minted for a name that is
// either a real (compiled-in) GUC or syntactically a valid custom/extension
// GUC name (config.IsCustomGUCName — two or more dot-separated simple
// identifiers). Real PostgreSQL additionally rejects a custom name that
// collides with a currently loaded extension's reserved namespace prefix
// (reserved_class_prefix); goopg does not track loaded-extension GUC
// namespaces, so that half is not ported (see deferral ledger). A single-part
// unrecognized name raises 42704 (undefined_object, matching
// assignable_custom_variable_name's "no separator" branch); a dotted name
// that fails the custom-name syntax check raises 42602 (invalid_name,
// matching its "invalid configuration parameter name" branch).
// M0119-0004-ACLHEAP (parameter ACL half).
func (o *ddlOp) checkParameterACLName(name string) error {
	if o.ctx.GetSetting != nil {
		if _, ok := o.ctx.GetSetting(name); ok {
			return nil
		}
	}
	if config.IsCustomGUCName(name) {
		return nil
	}
	if strings.Contains(name, ".") {
		return &ExecError{
			Code:    "42602",
			Message: fmt.Sprintf("invalid configuration parameter name %q", name),
			Detail:  "Custom parameter names must be two or more simple identifiers separated by dots.",
		}
	}
	return &ExecError{Code: "42704", Message: fmt.Sprintf("unrecognized configuration parameter %q", name)}
}

// execParameterACLChange applies a GRANT/REVOKE … ON PARAMETER … to the
// OID-keyed ACL store. Unlike execTypeACLChange/execDatabaseACLChange,
// pg_parameter_acl has no heap relfilenode to re-sync — it is a
// goopg-virtual-only catalog (registered purely so pg_dumpall's
// getParameterACLs query resolves), and PostgreSQL itself treats every
// parameter ACL as owned by the bootstrap superuser regardless of who issues
// the GRANT (ExecGrant_Parameter, aclchk.c), so no ownership/name resolution
// against a real object is needed either. Entry materialization mirrors
// objectNamesToOids' OBJECT_PARAMETER_ACL case exactly: GRANT looks up the
// name (ParameterAclLookup, missing_ok=true) and, only if that misses, mints
// a brand-new row (ParameterAclCreate) after running checkParameterACLName
// (check_GUC_name_for_parameter_acl — real PG never validates an
// already-materialized name again); REVOKE also just looks up — a REVOKE
// naming a GUC with no ACL entry has nothing to remove and is a pure no-op,
// never creating a row (goopg previously materialized one unconditionally,
// a divergence fixed alongside this validation).
// M0119-0004-ACLHEAP (parameter ACL half).
func (o *ddlOp) execParameterACLChange(pc *parser.ParameterACLChange) error {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	var privs []string
	for _, p := range pc.Privileges {
		privs = append(privs, normalizeParameterPriv(p)...)
	}
	if len(privs) == 0 {
		return nil
	}
	for _, name := range pc.ParamNames {
		if pc.Revoke {
			if !im.HasParameterACL(name) {
				continue
			}
			oid := im.ParameterACLOID(name)
			// Seed the owner's implicit acldefault('p', …) set while paracl is
			// still NULL so an owner-side REVOKE leaves the surviving privileges
			// explicit (mirrors execDatabaseACLChange's REVOKE branch). Unlike
			// DATABASE, PUBLIC's default is ACL_NO_RIGHTS, so no PUBLIC seed is
			// needed here.
			if im.ParameterACLText(oid) == "" {
				im.MaterializeOwnerACL(oid, "postgres", parameterACLAllPrivs)
			}
			for _, role := range pc.Grantees {
				for _, p := range privs {
					im.RevokeTablePrivilege(oid, role, p)
				}
			}
		} else {
			if !im.HasParameterACL(name) {
				if err := o.checkParameterACLName(name); err != nil {
					return err
				}
			}
			oid := im.ParameterACLOID(name)
			for _, role := range pc.Grantees {
				for _, p := range privs {
					im.GrantTablePrivilegeWithGrantOption(oid, role, p, pc.WithGrantOption)
				}
			}
		}
	}
	return nil
}
