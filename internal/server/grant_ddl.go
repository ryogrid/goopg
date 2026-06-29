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
	}
	// Bail on non-table object classes (ON DATABASE …, ON FUNCTION …, etc.).
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
			for _, p := range privs {
				s.cfg.Catalog.RevokeTablePrivilege(oid, role, p)
			}
		}
	}
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
