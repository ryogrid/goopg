package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// execAlterDefaultPrivileges applies `ALTER DEFAULT PRIVILEGES [FOR ROLE|USER
// ...] [IN SCHEMA ...] {GRANT|REVOKE} ...` to the synthetic-OID-keyed
// pg_default_acl store (catalog.InMemory.DefaultACLOID/Grant.../Revoke...).
// Mirrors ExecAlterDefaultPrivilegesStmt (aclchk.c) at the same level of
// detail this codebase's other virtual-only ACL classes (pg_parameter_acl,
// execParameterACLChange) already operate at.
//
// Scope note: goopg does not yet *apply* a stored default-ACL entry to an
// object created afterwards — no CREATE TABLE/SEQUENCE/FUNCTION/TYPE/SCHEMA
// call site consults this store (a separately bounded, larger gap; see the
// deferral ledger). This loop's scope is catalog/dump fidelity only: the
// statement is parsed, validated, and round-tripped through pg_default_acl
// exactly as PostgreSQL stores it, which is what pg_dump's
// getDefaultACLs/dumpDefaultACL query against. M0110-0001 (DU-002 slice 438
// follow-up).
func (o *ddlOp) execAlterDefaultPrivileges(s *parser.AlterDefaultPrivilegesStmt) error {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	objType, ok := catalog.DefaultACLObjTypeFromParserObjType(s.ObjType)
	if !ok {
		return nil
	}
	// Real PostgreSQL rejects IN SCHEMA combined with ON SCHEMAS/LARGE
	// OBJECTS outright (SetDefaultACL, aclchk.c: "cannot use IN SCHEMA
	// clause when using ...") — both object classes are inherently
	// namespace-level or namespace-independent, so a schema-scoped default
	// for either is meaningless.
	if len(s.Schemas) > 0 {
		switch s.ObjType {
		case "schema":
			return &ExecError{Code: "0LP01", Pos: s.Pos(), Message: "cannot use IN SCHEMA clause when using GRANT/REVOKE ON SCHEMAS"}
		case "largeobject":
			return &ExecError{Code: "0LP01", Pos: s.Pos(), Message: "cannot use IN SCHEMA clause when using GRANT/REVOKE ON LARGE OBJECTS"}
		}
	}
	// goopg has no pg_largeobject subsystem at all (no lo_create/lo_open/
	// loread/lowrite/..., no heap-backed pg_largeobject(_metadata) storage —
	// a separate, materially larger Effort-L feature per the deferral
	// ledger), so a LARGE OBJECTS default is accepted syntactically (the
	// parser already validated the grammar) but never materialized: there
	// is no object kind here for a resulting row to describe.
	if s.ObjType == "largeobject" {
		return nil
	}
	var privs []string
	for _, p := range s.Privileges {
		privs = append(privs, catalog.DefaultACLNormalizePriv(objType, p)...)
	}
	if len(privs) == 0 {
		return nil
	}
	roleOIDs, err := o.resolveDefaultPrivilegeRoleOIDs(s)
	if err != nil {
		return err
	}
	schemaOIDs, err := o.resolveDefaultPrivilegeSchemaOIDs(s)
	if err != nil {
		return err
	}
	for _, roleOID := range roleOIDs {
		for _, schemaOID := range schemaOIDs {
			if s.Revoke {
				// A REVOKE against a triple with no materialized row (never
				// granted, or already back at its implicit default) is a
				// pure no-op — mirrors execParameterACLChange's identical
				// REVOKE-side gate, and never mints a hollow pg_default_acl
				// row. GrantOptionFor ("REVOKE GRANT OPTION FOR ...") is
				// parsed and validated but, like every other GRANT-family
				// ACL change in this codebase (buildDatabaseACLChange et
				// al.), not distinctly narrowed to just the grant-option
				// bit — it revokes the privilege outright, the same
				// simplification already accepted elsewhere.
				if !im.HasDefaultACL(roleOID, schemaOID, objType) {
					continue
				}
				oid := im.DefaultACLOID(roleOID, schemaOID, objType)
				for _, grantee := range s.Grantees {
					for _, p := range privs {
						im.RevokeDefaultACLPrivilege(oid, grantee, p)
					}
				}
			} else {
				oid := im.DefaultACLOID(roleOID, schemaOID, objType)
				for _, grantee := range s.Grantees {
					for _, p := range privs {
						im.GrantDefaultACLPrivilege(oid, grantee, p, s.WithGrantOption)
					}
				}
			}
		}
	}
	return nil
}

// resolveDefaultPrivilegeRoleOIDs resolves the FOR ROLE|USER role_list to
// role OIDs, defaulting to the bootstrap superuser (OID 10) when the clause
// is omitted — PostgreSQL defaults to GetUserId() (the session's current
// effective user), which goopg's current_user always resolves to "postgres"
// unconditionally (see current_user's own hardcoded evalExpr case and
// resolveNewOwnerOID's identical CURRENT_USER/SESSION_USER/CURRENT_ROLE
// shortcut). An explicitly named, unregistered role raises 42704
// (role_specification_does_not_exist), matching get_rolespec_oid.
func (o *ddlOp) resolveDefaultPrivilegeRoleOIDs(s *parser.AlterDefaultPrivilegesStmt) ([]uint32, error) {
	im := o.ctx.Catalog.(*catalog.InMemory)
	if len(s.Roles) == 0 {
		return []uint32{10}, nil
	}
	out := make([]uint32, 0, len(s.Roles))
	for _, name := range s.Roles {
		if strings.EqualFold(name, "current_user") || strings.EqualFold(name, "session_user") || strings.EqualFold(name, "current_role") {
			out = append(out, 10)
			continue
		}
		oid, found := im.RoleOID(name)
		if !found {
			return nil, &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("role %q does not exist", name)}
		}
		out = append(out, oid)
	}
	return out, nil
}

// resolveDefaultPrivilegeSchemaOIDs resolves the IN SCHEMA schema_list to
// namespace OIDs, defaulting to a single global entry (schemaOID 0) when
// the clause is omitted. An unrecognized schema raises 3F000
// (undefined_schema), matching every other schema-name lookup in this
// codebase (e.g. execAlterSchema).
func (o *ddlOp) resolveDefaultPrivilegeSchemaOIDs(s *parser.AlterDefaultPrivilegesStmt) ([]uint32, error) {
	if len(s.Schemas) == 0 {
		return []uint32{0}, nil
	}
	im := o.ctx.Catalog.(*catalog.InMemory)
	out := make([]uint32, 0, len(s.Schemas))
	for _, name := range s.Schemas {
		if !im.SchemaExists(name) {
			return nil, &ExecError{Code: "3F000", Pos: s.Pos(), Message: fmt.Sprintf("schema %q does not exist", name)}
		}
		out = append(out, im.SchemaOID(name))
	}
	return out, nil
}
