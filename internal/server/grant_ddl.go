package server

// grant_ddl.go — minimal in-process GRANT recorder for object (table)
// privileges (M0118-0008, design 0118-0039).
//
// goopg parses GRANT/REVOKE as a no-op CompatNoopStmt, which is sufficient for
// the many tests that only need GRANT to *succeed*. The *-conflict isolation
// specs (truncate-conflict, …) additionally need GRANT to actually take effect
// so that a role impersonated via SET ROLE either is or is not allowed to run a
// privileged command (e.g. TRUNCATE). tryRecordTableGrant intercepts an
// autocommit, single-statement, table-level GRANT and records the granted
// privileges in the catalog ACL store (Catalog.GrantTablePrivilege); the
// executor's per-command privilege checks consult it.
//
// Scope is deliberately narrow: only `GRANT <privs> ON [TABLE] <tables> TO
// <roles>` is recorded. Anything we cannot confidently parse (column-level
// grants, GRANT ON SCHEMA/DATABASE/SEQUENCE/…, role-membership GRANT, GRANT …
// TO PUBLIC) is left to the existing permissive no-op path — the command still
// reports success, it just records nothing. REVOKE is likewise left as a no-op.

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// tableObjectKeywords are the first words after ON that denote a non-table
// object class we do not model; such grants are left to the no-op path.
// `sequence` is NOT here: a sequence GRANT is recorded (DU-002 slice 333) so it
// round-trips through pg_dump, sharing the relation ACL store keyed by OID.
var nonTableGrantObjects = map[string]struct{}{
	"schema": {}, "database": {}, "function": {},
	"procedure": {}, "routine": {}, "domain": {}, "type": {},
	"tablespace": {}, "language": {}, "foreign": {}, "large": {},
	"all": {}, "parameter": {},
}

// allTablePrivileges is the expansion of GRANT ALL [PRIVILEGES] ON a table.
var allTablePrivileges = []string{
	"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE",
	"REFERENCES", "TRIGGER", "MAINTAIN",
}

// allSequencePrivileges is the expansion of GRANT ALL [PRIVILEGES] ON a
// sequence (USAGE/SELECT/UPDATE — pg_dump renders these as "rwU"). DU-002
// slice 333.
var allSequencePrivileges = []string{"USAGE", "SELECT", "UPDATE"}

// allSchemaPrivileges is the expansion of GRANT ALL [PRIVILEGES] ON a schema
// (USAGE/CREATE — pg_dump renders these as "UC"). DU-002 slice 335.
var allSchemaPrivileges = []string{"USAGE", "CREATE"}

// allFunctionPrivileges is the expansion of GRANT ALL [PRIVILEGES] ON a
// function/procedure/routine. EXECUTE is the only function privilege, which
// pg_dump renders as the aclitem letter "X". DU-002 slice 345.
var allFunctionPrivileges = []string{"EXECUTE"}

// allForeignServerPrivileges is the expansion of GRANT ALL [PRIVILEGES] ON a
// FOREIGN SERVER. USAGE is the only foreign-server privilege, which pg_dump
// renders as the aclitem letter "U". DU-002 slice 427.
var allForeignServerPrivileges = []string{"USAGE"}

// allForeignDataWrapperPrivileges is the expansion of GRANT ALL [PRIVILEGES]
// ON a FOREIGN DATA WRAPPER. USAGE is the only FDW privilege (ACL_ALL_RIGHTS_FDW
// == ACL_USAGE), which pg_dump renders as the aclitem letter "U". DU-002
// slice 428.
var allForeignDataWrapperPrivileges = []string{"USAGE"}

// aclOwnerRole is the object owner in goopg's single bootstrap-superuser model.
// A REVOKE whose grantee is the owner is an owner-side revoke-of-default, which
// must first materialize the owner's implicit default ACL. DU-002 slice 340.
const aclOwnerRole = "postgres"

// tryRecordTableGrant parses a table-level GRANT and records its privileges in
// the catalog ACL store. It is a best-effort side effect: on any form it does
// not recognise it simply returns, leaving the statement as a successful no-op.
func (s *Server) tryRecordTableGrant(stmt string) {
	if s.cfg.Catalog == nil {
		return
	}
	lower := strings.ToLower(stmt)
	if !strings.HasPrefix(lower, "grant ") {
		return
	}
	// A column-level grant carries a parenthesised column list before ON; we do
	// not model column privileges, so bail.
	onIdx := strings.Index(lower, " on ")
	toIdx := strings.LastIndex(lower, " to ")
	if onIdx < 0 || toIdx < 0 || toIdx <= onIdx {
		return
	}
	privPart := strings.TrimSpace(stmt[len("grant "):onIdx])
	objPart := strings.TrimSpace(stmt[onIdx+len(" on ") : toIdx])
	rolePart := strings.TrimSpace(stmt[toIdx+len(" to "):])
	if strings.ContainsRune(privPart, '(') {
		return // column-level grant
	}

	// Strip a trailing WITH GRANT OPTION / GRANTED BY clause from the role list,
	// remembering whether WITH GRANT OPTION was present so the recorded ACL can
	// render the privilege letter with a trailing `*` (DU-002 slice 332).
	withGrantOption := false
	if i := strings.Index(strings.ToLower(rolePart), " with "); i >= 0 {
		tail := strings.ToLower(strings.TrimSpace(rolePart[i+len(" with "):]))
		if tail == "grant option" {
			withGrantOption = true
		}
		rolePart = strings.TrimSpace(rolePart[:i])
	}
	if i := strings.Index(strings.ToLower(rolePart), " granted by "); i >= 0 {
		rolePart = strings.TrimSpace(rolePart[:i])
	}

	// Optional leading TABLE / SEQUENCE keyword on the object list, or a required
	// SCHEMA keyword for a namespace grant. A SEQUENCE grant is recorded too
	// (DU-002 slice 333) and a SCHEMA grant (DU-002 slice 335) so each
	// round-trips through pg_dump; their ALL expansions and rendered privilege
	// letters differ.
	isSequence := false
	if rest, ok := cutLeadingKeyword(objPart, "table"); ok {
		objPart = rest
	} else if rest, ok := cutLeadingKeyword(objPart, "sequence"); ok {
		objPart = rest
		isSequence = true
	} else if rest, ok := cutLeadingKeyword(objPart, "schema"); ok {
		// GRANT … ON SCHEMA <names> TO <roles>. Schemas live in pg_namespace and
		// share the OID-keyed ACL store with relations; record under each schema's
		// OID so pg_dump's getNamespaces/dumpACL re-emits the GRANT from nspacl.
		s.recordSchemaGrant(rest, rolePart, privPart, withGrantOption)
		return
	} else if rest, ok := cutLeadingKeyword(objPart, "function"); ok {
		// GRANT EXECUTE ON FUNCTION <signature> TO <roles>. Functions live in
		// pg_proc and share the OID-keyed ACL store; record under each routine's
		// OID so pg_dump's getFuncs/dumpACL re-emits the GRANT from proacl.
		s.recordFunctionGrant(rest, rolePart, privPart, withGrantOption)
		return
	} else if rest, ok := cutLeadingKeyword(objPart, "procedure"); ok {
		// GRANT EXECUTE ON PROCEDURE … shares the routine ACL path with FUNCTION.
		s.recordFunctionGrant(rest, rolePart, privPart, withGrantOption)
		return
	} else if rest, ok := cutLeadingKeyword(objPart, "routine"); ok {
		// GRANT EXECUTE ON ROUTINE … shares the routine ACL path with FUNCTION.
		s.recordFunctionGrant(rest, rolePart, privPart, withGrantOption)
		return
	} else if rest, ok := cutLeadingKeyword(objPart, "foreign"); ok {
		if rest2, ok2 := cutLeadingKeyword(rest, "server"); ok2 {
			// GRANT USAGE ON FOREIGN SERVER <name> TO <roles>. Foreign servers live
			// in pg_foreign_server and share the OID-keyed ACL store; record under
			// each server's OID so pg_dump's getForeignServers/dumpACL re-emits the
			// GRANT from srvacl.
			s.recordForeignServerGrant(rest2, rolePart, privPart, withGrantOption)
			return
		}
		if rest2, ok2 := cutLeadingKeyword(rest, "data"); ok2 {
			if rest3, ok3 := cutLeadingKeyword(rest2, "wrapper"); ok3 {
				// GRANT USAGE ON FOREIGN DATA WRAPPER <name> TO <roles>. FDWs live in
				// pg_foreign_data_wrapper and share the OID-keyed ACL store; record
				// under each FDW's OID so pg_dump's getForeignDataWrappers/dumpACL
				// re-emits the GRANT from fdwacl. DU-002 slice 428.
				s.recordForeignDataWrapperGrant(rest3, rolePart, privPart, withGrantOption)
				return
			}
		}
	}
	// Bail on non-table object classes (ON DATABASE …, etc.).
	if _, isNonTable := nonTableGrantObjects[firstWordLower(objPart)]; isNonTable {
		return
	}

	allPrivs := allTablePrivileges
	if isSequence {
		allPrivs = allSequencePrivileges
	}
	privs := parseGrantPrivileges(privPart, allPrivs)
	if len(privs) == 0 {
		return
	}
	tables := splitGrantList(objPart)
	roles := splitGrantList(rolePart)
	for _, t := range tables {
		on := objectNameFromIdent(t)
		tbl, ok := s.cfg.Catalog.LookupTable(on)
		if !ok {
			continue
		}
		for _, role := range roles {
			for _, p := range privs {
				s.cfg.Catalog.GrantTablePrivilegeWithGrantOption(tbl.OID, role, p, withGrantOption)
			}
		}
	}
}

// tryRecordTableRevoke parses an autocommit table/sequence-level REVOKE and
// removes the named privileges from the catalog ACL store, mirroring
// tryRecordTableGrant. Like the grant recorder it is best-effort: any form it
// does not recognise (column-level, ON SCHEMA/DATABASE/FUNCTION/…, role
// membership, REVOKE GRANT OPTION FOR) simply returns, leaving the statement as
// a successful no-op. Recording the revoke keeps the materialized relacl in
// sync so pg_dump re-emits only the privileges that actually remain. DU-002
// slice 338.
func (s *Server) tryRecordTableRevoke(stmt string) {
	if s.cfg.Catalog == nil {
		return
	}
	lower := strings.ToLower(stmt)
	if !strings.HasPrefix(lower, "revoke ") {
		return
	}
	onIdx := strings.Index(lower, " on ")
	fromIdx := strings.LastIndex(lower, " from ")
	if onIdx < 0 || fromIdx < 0 || fromIdx <= onIdx {
		return
	}
	privPart := strings.TrimSpace(stmt[len("revoke "):onIdx])
	objPart := strings.TrimSpace(stmt[onIdx+len(" on ") : fromIdx])
	rolePart := strings.TrimSpace(stmt[fromIdx+len(" from "):])
	if strings.ContainsRune(privPart, '(') {
		return // column-level revoke
	}
	// We do not model REVOKE GRANT OPTION FOR (it only clears the grant-option
	// flag, not the privilege); leave such a form to the no-op path.
	if strings.HasPrefix(strings.ToLower(privPart), "grant option for") {
		return
	}
	// Strip a trailing CASCADE / RESTRICT drop-behaviour keyword from the role
	// list (REVOKE … FROM <roles> [CASCADE|RESTRICT]).
	if i := strings.LastIndex(strings.ToLower(rolePart), " cascade"); i >= 0 {
		rolePart = strings.TrimSpace(rolePart[:i])
	} else if i := strings.LastIndex(strings.ToLower(rolePart), " restrict"); i >= 0 {
		rolePart = strings.TrimSpace(rolePart[:i])
	}

	isSequence := false
	if rest, ok := cutLeadingKeyword(objPart, "table"); ok {
		objPart = rest
	} else if rest, ok := cutLeadingKeyword(objPart, "sequence"); ok {
		objPart = rest
		isSequence = true
	} else if rest, ok := cutLeadingKeyword(objPart, "schema"); ok {
		// REVOKE … ON SCHEMA <names> FROM <roles>. Mirror the grant recorder's
		// SCHEMA branch: schemas share the OID-keyed ACL store with relations, so
		// remove the named privileges from each schema's nspacl. DU-002 slice 339.
		s.recordSchemaRevoke(rest, rolePart, privPart)
		return
	} else if rest, ok := cutLeadingKeyword(objPart, "function"); ok {
		// REVOKE EXECUTE ON FUNCTION <signature> FROM <roles>. Mirror the grant
		// recorder's FUNCTION branch: routines share the OID-keyed ACL store, so
		// remove the named privileges from each routine's proacl. DU-002 slice 346.
		s.recordFunctionRevoke(rest, rolePart, privPart)
		return
	} else if rest, ok := cutLeadingKeyword(objPart, "procedure"); ok {
		// REVOKE EXECUTE ON PROCEDURE … shares the routine ACL path with FUNCTION.
		s.recordFunctionRevoke(rest, rolePart, privPart)
		return
	} else if rest, ok := cutLeadingKeyword(objPart, "routine"); ok {
		// REVOKE EXECUTE ON ROUTINE … shares the routine ACL path with FUNCTION.
		s.recordFunctionRevoke(rest, rolePart, privPart)
		return
	} else if rest, ok := cutLeadingKeyword(objPart, "foreign"); ok {
		if rest2, ok2 := cutLeadingKeyword(rest, "server"); ok2 {
			// REVOKE USAGE ON FOREIGN SERVER <name> FROM <roles>. Mirror the grant
			// recorder's FOREIGN SERVER branch: servers share the OID-keyed ACL
			// store, so remove the named privileges from each server's srvacl.
			// DU-002 slice 427.
			s.recordForeignServerRevoke(rest2, rolePart, privPart)
			return
		}
		if rest2, ok2 := cutLeadingKeyword(rest, "data"); ok2 {
			if rest3, ok3 := cutLeadingKeyword(rest2, "wrapper"); ok3 {
				// REVOKE USAGE ON FOREIGN DATA WRAPPER <name> FROM <roles>. Mirror the
				// grant recorder's FOREIGN DATA WRAPPER branch: FDWs share the
				// OID-keyed ACL store, so remove the named privileges from each FDW's
				// fdwacl. DU-002 slice 428.
				s.recordForeignDataWrapperRevoke(rest3, rolePart, privPart)
				return
			}
		}
	}
	// Only table/sequence relacl is modelled here; bail on every other object
	// class (ON DATABASE/FUNCTION/…), matching the grant recorder's scope.
	if _, isNonTable := nonTableGrantObjects[firstWordLower(objPart)]; isNonTable {
		return
	}

	allPrivs := allTablePrivileges
	if isSequence {
		allPrivs = allSequencePrivileges
	}
	privs := parseGrantPrivileges(privPart, allPrivs)
	if len(privs) == 0 {
		return
	}
	tables := splitGrantList(objPart)
	roles := splitGrantList(rolePart)
	for _, t := range tables {
		on := objectNameFromIdent(t)
		tbl, ok := s.cfg.Catalog.LookupTable(on)
		if !ok {
			continue
		}
		for _, role := range roles {
			// An owner-side REVOKE (`REVOKE … FROM postgres`) materializes the
			// owner's full default ACL first so the surviving privileges render
			// explicitly; PostgreSQL leaves relacl NULL until then. allPrivs is the
			// owner's full default set for the object class (table or sequence), so
			// the subsequent RevokeTablePrivilege yields relacl = default − revoked.
			// DU-002 slice 340.
			if strings.EqualFold(role, aclOwnerRole) {
				s.cfg.Catalog.MaterializeOwnerACL(tbl.OID, aclOwnerRole, allPrivs)
			}
			for _, p := range privs {
				s.cfg.Catalog.RevokeTablePrivilege(tbl.OID, role, p)
			}
		}
	}
}

// recordSchemaGrant records a `GRANT <privs> ON SCHEMA <names> TO <roles>` in
// the catalog ACL store, keyed by each schema's pg_namespace OID. Unknown
// schemas and an empty/unparseable privilege list are skipped, leaving the
// statement as a successful no-op. DU-002 slice 335.
func (s *Server) recordSchemaGrant(objPart, rolePart, privPart string, withGrantOption bool) {
	privs := parseGrantPrivileges(privPart, allSchemaPrivileges)
	if len(privs) == 0 {
		return
	}
	schemas := splitGrantList(objPart)
	roles := splitGrantList(rolePart)
	for _, sc := range schemas {
		oid := s.cfg.Catalog.SchemaOID(strings.Trim(strings.TrimSpace(sc), `"`))
		if oid == 0 {
			continue
		}
		for _, role := range roles {
			for _, p := range privs {
				s.cfg.Catalog.GrantTablePrivilegeWithGrantOption(oid, role, p, withGrantOption)
			}
		}
	}
}

// recordSchemaRevoke removes the named privileges from each schema's nspacl in
// the catalog ACL store, mirroring recordSchemaGrant. Unknown schemas and an
// empty/unparseable privilege list are skipped, leaving the statement as a
// successful no-op. Keeping nspacl in sync lets pg_dump re-emit only the schema
// privileges that actually remain after the revoke. DU-002 slice 339.
func (s *Server) recordSchemaRevoke(objPart, rolePart, privPart string) {
	privs := parseGrantPrivileges(privPart, allSchemaPrivileges)
	if len(privs) == 0 {
		return
	}
	schemas := splitGrantList(objPart)
	roles := splitGrantList(rolePart)
	for _, sc := range schemas {
		oid := s.cfg.Catalog.SchemaOID(strings.Trim(strings.TrimSpace(sc), `"`))
		if oid == 0 {
			continue
		}
		for _, role := range roles {
			// An owner-side REVOKE (`REVOKE … FROM postgres`) materializes the
			// owner's full default schema ACL first so the surviving privileges
			// render explicitly; PostgreSQL leaves nspacl NULL until then.
			// allSchemaPrivileges is the owner's full default set for a namespace
			// (USAGE, CREATE), so a `REVOKE ALL … ON SCHEMA … FROM postgres` empties
			// the materialized owner entry and the catalog records nspacl = {} (the
			// type-agnostic relACLEmptied path), making pg_dump re-emit a bare
			// `REVOKE ALL ON SCHEMA … FROM postgres;`. DU-002 slice 342.
			if strings.EqualFold(role, aclOwnerRole) {
				s.cfg.Catalog.MaterializeOwnerACL(oid, aclOwnerRole, allSchemaPrivileges)
			}
			for _, p := range privs {
				s.cfg.Catalog.RevokeTablePrivilege(oid, role, p)
			}
		}
	}
}

// recordForeignServerGrant records a `GRANT USAGE ON FOREIGN SERVER <names>
// TO <roles>` in the catalog ACL store, keyed by each server's pg_foreign_server
// OID. Foreign servers share the OID-keyed ACL store with relations, schemas,
// and routines. Unlike a function/type, a foreign server's acldefault('S',
// owner) grants USAGE to the owner ONLY (world default ACL_NO_RIGHTS), so no
// implicit PUBLIC entry is seeded here — mirroring recordSchemaGrant, not
// recordFunctionGrant. Unknown servers and an empty/unparseable privilege list
// are skipped, leaving the statement a successful no-op. DU-002 slice 427.
func (s *Server) recordForeignServerGrant(objPart, rolePart, privPart string, withGrantOption bool) {
	privs := parseGrantPrivileges(privPart, allForeignServerPrivileges)
	if len(privs) == 0 {
		return
	}
	servers := splitGrantList(objPart)
	roles := splitGrantList(rolePart)
	for _, srv := range servers {
		oid := s.cfg.Catalog.ForeignServerOID(strings.Trim(strings.TrimSpace(srv), `"`))
		if oid == 0 {
			continue
		}
		for _, role := range roles {
			for _, p := range privs {
				s.cfg.Catalog.GrantTablePrivilegeWithGrantOption(oid, role, p, withGrantOption)
			}
		}
	}
}

// recordForeignServerRevoke removes the named privileges from each foreign
// server's srvacl in the catalog ACL store, mirroring recordForeignServerGrant.
// Unknown servers and an empty/unparseable privilege list are skipped, leaving
// the statement as a successful no-op. Keeping srvacl in sync lets pg_dump
// re-emit only the server privileges that actually remain after the revoke.
// DU-002 slice 427.
func (s *Server) recordForeignServerRevoke(objPart, rolePart, privPart string) {
	privs := parseGrantPrivileges(privPart, allForeignServerPrivileges)
	if len(privs) == 0 {
		return
	}
	servers := splitGrantList(objPart)
	roles := splitGrantList(rolePart)
	for _, srv := range servers {
		oid := s.cfg.Catalog.ForeignServerOID(strings.Trim(strings.TrimSpace(srv), `"`))
		if oid == 0 {
			continue
		}
		for _, role := range roles {
			// An owner-side REVOKE (`REVOKE … FROM postgres`) materializes the
			// owner's full default foreign-server ACL first so the surviving
			// privileges render explicitly; PostgreSQL leaves srvacl NULL until
			// then. allForeignServerPrivileges is the owner's full default set
			// (USAGE only), mirroring recordSchemaRevoke's owner-materialization.
			if strings.EqualFold(role, aclOwnerRole) {
				s.cfg.Catalog.MaterializeOwnerACL(oid, aclOwnerRole, allForeignServerPrivileges)
			}
			for _, p := range privs {
				s.cfg.Catalog.RevokeTablePrivilege(oid, role, p)
			}
		}
	}
}

// recordForeignDataWrapperGrant records a `GRANT USAGE ON FOREIGN DATA
// WRAPPER <names> TO <roles>` in the catalog ACL store, keyed by each FDW's
// pg_foreign_data_wrapper OID. FDWs share the OID-keyed ACL store with
// relations, schemas, routines, and foreign servers. Like a foreign server, an
// FDW's acldefault('F', owner) grants USAGE to the owner ONLY (world default
// ACL_NO_RIGHTS), so no implicit PUBLIC entry is seeded here — mirroring
// recordForeignServerGrant. Unknown FDWs and an empty/unparseable privilege
// list are skipped, leaving the statement a successful no-op. DU-002 slice
// 428.
func (s *Server) recordForeignDataWrapperGrant(objPart, rolePart, privPart string, withGrantOption bool) {
	privs := parseGrantPrivileges(privPart, allForeignDataWrapperPrivileges)
	if len(privs) == 0 {
		return
	}
	fdws := splitGrantList(objPart)
	roles := splitGrantList(rolePart)
	for _, fdw := range fdws {
		oid := s.cfg.Catalog.ForeignDataWrapperOID(strings.Trim(strings.TrimSpace(fdw), `"`))
		if oid == 0 {
			continue
		}
		for _, role := range roles {
			for _, p := range privs {
				s.cfg.Catalog.GrantTablePrivilegeWithGrantOption(oid, role, p, withGrantOption)
			}
		}
	}
}

// recordForeignDataWrapperRevoke removes the named privileges from each FDW's
// fdwacl in the catalog ACL store, mirroring recordForeignDataWrapperGrant.
// Unknown FDWs and an empty/unparseable privilege list are skipped, leaving
// the statement as a successful no-op. Keeping fdwacl in sync lets pg_dump
// re-emit only the FDW privileges that actually remain after the revoke.
// DU-002 slice 428.
func (s *Server) recordForeignDataWrapperRevoke(objPart, rolePart, privPart string) {
	privs := parseGrantPrivileges(privPart, allForeignDataWrapperPrivileges)
	if len(privs) == 0 {
		return
	}
	fdws := splitGrantList(objPart)
	roles := splitGrantList(rolePart)
	for _, fdw := range fdws {
		oid := s.cfg.Catalog.ForeignDataWrapperOID(strings.Trim(strings.TrimSpace(fdw), `"`))
		if oid == 0 {
			continue
		}
		for _, role := range roles {
			// An owner-side REVOKE (`REVOKE … FROM postgres`) materializes the
			// owner's full default FDW ACL first so the surviving privileges
			// render explicitly; PostgreSQL leaves fdwacl NULL until then.
			// allForeignDataWrapperPrivileges is the owner's full default set
			// (USAGE only), mirroring recordForeignServerRevoke's owner
			// materialization.
			if strings.EqualFold(role, aclOwnerRole) {
				s.cfg.Catalog.MaterializeOwnerACL(oid, aclOwnerRole, allForeignDataWrapperPrivileges)
			}
			for _, p := range privs {
				s.cfg.Catalog.RevokeTablePrivilege(oid, role, p)
			}
		}
	}
}

// recordFunctionGrant records a `GRANT EXECUTE ON FUNCTION <signature> TO
// <roles>` in the catalog ACL store, keyed by each routine's pg_proc OID.
// PostgreSQL's acldefault for a function grants EXECUTE to BOTH the owner and
// PUBLIC, so the first grant materializes proacl as
// "{=X/postgres,postgres=X/postgres,<grantee>=X/postgres}". goopg seeds the
// implicit PUBLIC EXECUTE entry explicitly (the owner entry is supplied by the
// relacl renderer's owner branch via ownerFunctionACLString), so the projected
// proacl reproduces both default entries plus the grantee and pg_dump's
// getFuncs/buildACLCommands diffs it against acldefault('f', 10) to re-emit
// exactly the new grant. Unknown routines, a non-EXECUTE privilege list, or an
// empty role list are skipped, leaving the statement a successful no-op. When
// withGrantOption is set the grantee's EXECUTE is recorded with the grant-option
// flag so the materialized proacl renders "<grantee>=X*/postgres" and pg_dump
// re-emits the trailing `WITH GRANT OPTION` (DU-002 slice 348). The implicit
// owner/PUBLIC default entries are always plain (acldefault carries no grant
// option). DU-002 slice 345.
func (s *Server) recordFunctionGrant(objPart, rolePart, privPart string, withGrantOption bool) {
	privs := parseGrantPrivileges(privPart, allFunctionPrivileges)
	hasExecute := false
	for _, p := range privs {
		if p == "EXECUTE" {
			hasExecute = true
			break
		}
	}
	if !hasExecute {
		return
	}
	roles := splitGrantList(rolePart)
	if len(roles) == 0 {
		return
	}
	for _, sig := range splitFunctionList(objPart) {
		oid := s.lookupFunctionOID(sig)
		if oid == 0 {
			continue
		}
		// Seed the implicit PUBLIC EXECUTE that acldefault('f', …) carries so the
		// materialized proacl matches PostgreSQL's array (the catalog lower-cases
		// "PUBLIC" to the reserved pseudo-role and renders it as the empty grantee).
		s.cfg.Catalog.GrantTablePrivilege(oid, "PUBLIC", "EXECUTE")
		for _, role := range roles {
			s.cfg.Catalog.GrantTablePrivilegeWithGrantOption(oid, role, "EXECUTE", withGrantOption)
		}
	}
}

// recordFunctionRevoke removes the named privileges from each routine's proacl
// in the catalog ACL store, mirroring recordFunctionGrant. A function's implicit
// default proacl grants EXECUTE to BOTH the owner and PUBLIC
// (acldefault('f', proowner) = "{=X/postgres,postgres=X/postgres}"), and
// PostgreSQL leaves proacl NULL until the first GRANT/REVOKE materializes it. A
// REVOKE therefore materializes the owner's default entry first (the
// type-agnostic MaterializeOwnerACL) so the surviving owner EXECUTE renders
// explicitly; the PUBLIC half of the default is implicit and is simply omitted
// once revoked. The common case `REVOKE EXECUTE … FROM PUBLIC` thus yields
// proacl = "{postgres=X/postgres}", which pg_dump diffs against acldefault('f',
// 10) to emit `REVOKE ALL ON FUNCTION … FROM PUBLIC;`. Unknown routines, a
// non-EXECUTE privilege list, or an empty role list are skipped, leaving the
// statement a successful no-op. DU-002 slice 346.
func (s *Server) recordFunctionRevoke(objPart, rolePart, privPart string) {
	privs := parseGrantPrivileges(privPart, allFunctionPrivileges)
	hasExecute := false
	for _, p := range privs {
		if p == "EXECUTE" {
			hasExecute = true
			break
		}
	}
	if !hasExecute {
		return
	}
	roles := splitGrantList(rolePart)
	if len(roles) == 0 {
		return
	}
	for _, sig := range splitFunctionList(objPart) {
		oid := s.lookupFunctionOID(sig)
		if oid == 0 {
			continue
		}
		// A function's acldefault('f', owner) grants EXECUTE to BOTH the owner and
		// PUBLIC. PostgreSQL materializes that full array on the first GRANT/REVOKE,
		// then mutates it — so an owner-side REVOKE leaves PUBLIC's `{=X/owner}`
		// (slice 347) and a PUBLIC-side REVOKE leaves the owner's `{postgres=X/…}`
		// (slice 346). Seed both implicit entries here, but only while proacl is
		// still NULL (no prior mutation): ProcACLText returns "" for NULL. Re-seeding
		// on a second REVOKE would resurrect an already-revoked grantee (e.g.
		// `REVOKE … FROM PUBLIC` then `… FROM postgres` must reach `{}`, not
		// `{=X/postgres}`). The catalog lower-cases "PUBLIC" to the reserved
		// pseudo-role and renders it as the empty grantee, matching the grant recorder.
		if s.cfg.Catalog.ProcACLText(oid) == "" {
			s.cfg.Catalog.MaterializeOwnerACL(oid, aclOwnerRole, allFunctionPrivileges)
			s.cfg.Catalog.GrantTablePrivilege(oid, "PUBLIC", "EXECUTE")
		}
		for _, role := range roles {
			for _, p := range privs {
				s.cfg.Catalog.RevokeTablePrivilege(oid, role, p)
			}
		}
	}
}

// lookupFunctionOID resolves a routine signature token such as
// "public.grantfn(integer)" to its pg_proc OID, returning 0 when it does not
// resolve. It first tries an exact name+argument-type match (mirroring
// COMMENT ON / DROP FUNCTION resolution); when the parenthesised arg-type
// spelling differs from the stored canonical name it falls back to a unique
// by-name match. DU-002 slice 345.
func (s *Server) lookupFunctionOID(sig string) uint32 {
	rs := s.cfg.Catalog.Routines()
	if rs == nil {
		return 0
	}
	name := strings.TrimSpace(sig)
	var argTypes []catalog.Type
	if lp := strings.IndexByte(name, '('); lp >= 0 {
		argList := name[lp+1:]
		name = strings.TrimSpace(name[:lp])
		if rp := strings.LastIndexByte(argList, ')'); rp >= 0 {
			argList = argList[:rp]
		}
		for _, a := range strings.Split(argList, ",") {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			argTypes = append(argTypes, catalog.Type{Name: strings.ToLower(a)})
		}
	}
	on := objectNameFromIdent(name)
	if r, ok := rs.Lookup(on, argTypes); ok && r != nil {
		return r.OID
	}
	if cands := rs.LookupByName(on); len(cands) == 1 {
		return cands[0].OID
	}
	return 0
}

// parseGrantPrivileges splits a comma-separated privilege list into upper-cased
// keywords, expanding ALL [PRIVILEGES] to allPrivs (the full privilege set for
// the object class being granted — table or sequence).
func parseGrantPrivileges(privPart string, allPrivs []string) []string {
	out := make([]string, 0, 4)
	for _, raw := range strings.Split(privPart, ",") {
		p := strings.ToUpper(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		if p == "ALL" || p == "ALL PRIVILEGES" {
			return append([]string(nil), allPrivs...)
		}
		out = append(out, p)
	}
	return out
}

// splitGrantList splits a comma-separated identifier list, trimming whitespace
// and surrounding quotes from each element.
func splitGrantList(list string) []string {
	out := make([]string, 0, 2)
	for _, raw := range strings.Split(list, ",") {
		v := strings.TrimSpace(raw)
		v = strings.Trim(v, `"`)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// splitFunctionList splits a comma-separated list of routine signatures while
// respecting parentheses, so a multi-argument signature such as
// `f(integer, text)` is kept intact instead of being torn at the inner comma.
// Each element is trimmed of surrounding whitespace; empty elements are dropped.
// DU-002 slice 345.
func splitFunctionList(list string) []string {
	out := make([]string, 0, 2)
	depth := 0
	start := 0
	for i := 0; i < len(list); i++ {
		switch list[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				if v := strings.TrimSpace(list[start:i]); v != "" {
					out = append(out, v)
				}
				start = i + 1
			}
		}
	}
	if v := strings.TrimSpace(list[start:]); v != "" {
		out = append(out, v)
	}
	return out
}

// objectNameFromIdent builds an ObjectName from a possibly schema-qualified
// identifier token (e.g. "public.t" or "t").
func objectNameFromIdent(ident string) parser.ObjectName {
	ident = strings.TrimSpace(ident)
	if dot := strings.IndexByte(ident, '.'); dot >= 0 {
		return parser.ObjectName{Schema: strings.Trim(ident[:dot], `"`), Name: strings.Trim(ident[dot+1:], `"`)}
	}
	return parser.ObjectName{Name: strings.Trim(ident, `"`)}
}

// firstWordLower returns the first whitespace-delimited token of s, lower-cased.
func firstWordLower(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n\r"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// cutLeadingKeyword strips a leading whole-word keyword (case-insensitive) plus
// the following whitespace, returning (rest, true) when present.
func cutLeadingKeyword(s, kw string) (string, bool) {
	if len(s) <= len(kw) {
		return s, false
	}
	if strings.EqualFold(s[:len(kw)], kw) && (s[len(kw)] == ' ' || s[len(kw)] == '\t') {
		return strings.TrimSpace(s[len(kw):]), true
	}
	return s, false
}
