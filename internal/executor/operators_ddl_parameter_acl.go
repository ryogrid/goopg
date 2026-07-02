package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
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

// execParameterACLChange applies a GRANT/REVOKE … ON PARAMETER … to the
// OID-keyed ACL store. Unlike execTypeACLChange/execDatabaseACLChange,
// pg_parameter_acl has no heap relfilenode to re-sync — it is a
// goopg-virtual-only catalog (registered purely so pg_dumpall's
// getParameterACLs query resolves), and PostgreSQL itself treats every
// parameter ACL as owned by the bootstrap superuser regardless of who issues
// the GRANT (ExecGrant_Parameter, aclchk.c), so no ownership/name resolution
// against a real object is needed either — goopg accepts any parameter name
// unconditionally (a bounded simplification vs. PG's
// check_GUC_name_for_parameter_acl, which validates against the compiled-in
// GUC table; see deferral ledger). Each named GUC lazily mints a synthetic
// pg_parameter_acl.oid on first use via catalog.ParameterACLOID, mirroring
// PostgreSQL's lazy ParameterAclCreate. M0119-0004-ACLHEAP (parameter ACL
// half).
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
		oid := im.ParameterACLOID(name)
		if pc.Revoke {
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
			for _, role := range pc.Grantees {
				for _, p := range privs {
					im.GrantTablePrivilegeWithGrantOption(oid, role, p, pc.WithGrantOption)
				}
			}
		}
	}
	return nil
}
