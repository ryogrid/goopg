package catalog

import (
	"sort"
	"strings"
)

// PostgreSQL's DEFACLOBJ_* object-type letters (pg_default_acl.h). goopg
// tracks all six syntactically (ALTER DEFAULT PRIVILEGES parses every
// defacl_privilege_target) but only stores/renders ACLs for the five that
// have a real privilege set modeled elsewhere in this package
// (DefaultACLObjTypeLargeObject has no backing pg_largeobject subsystem at
// all — see the deferral ledger — so it is accepted and silently dropped by
// the executor rather than materialized here). M0110-0001 (DU-002 slice 438
// follow-up).
const (
	DefaultACLObjTypeRelation    byte = 'r' // TABLES
	DefaultACLObjTypeSequence    byte = 'S' // SEQUENCES
	DefaultACLObjTypeFunction    byte = 'f' // FUNCTIONS / ROUTINES
	DefaultACLObjTypeType        byte = 'T' // TYPES
	DefaultACLObjTypeNamespace   byte = 'n' // SCHEMAS
	DefaultACLObjTypeLargeObject byte = 'L' // LARGE OBJECTS (not modeled)
)

// defaultACLKey identifies one pg_default_acl row: the target role, the
// target schema (0 = no IN SCHEMA, a global default), and the object type.
// Mirrors pg_default_acl's own (defaclrole, defaclnamespace, defaclobjtype)
// uniqueness. M0110-0001 (DU-002 slice 438 follow-up).
type defaultACLKey struct {
	RoleOID   uint32
	SchemaOID uint32
	ObjType   byte
}

// DefaultACLEntry is one row DefaultACLEntries returns, for pg_default_acl's
// VirtualRows.
type DefaultACLEntry struct {
	OID       uint32
	RoleOID   uint32
	SchemaOID uint32 // 0 = global
	ObjType   byte
}

// defaultACLPrivInfo returns the canonical aclitemout privilege-letter order
// for objType, reusing the exact same tables pg_class/pg_proc/pg_type/
// pg_namespace ACL rendering already uses (tableACLPrivOrder etc — a default
// ACL for TABLES grants the identical privilege set a real table does, and
// so on for the other four modeled object types). ok is false for an
// unrecognized or unmodeled (large object) objType.
func defaultACLPrivInfo(objType byte) (privOrder []aclPrivLetter, ok bool) {
	switch objType {
	case DefaultACLObjTypeRelation:
		return tableACLPrivOrder, true
	case DefaultACLObjTypeSequence:
		return sequenceACLPrivOrder, true
	case DefaultACLObjTypeFunction:
		return functionACLPrivOrder, true
	case DefaultACLObjTypeType:
		return typeACLPrivOrder, true
	case DefaultACLObjTypeNamespace:
		return schemaACLPrivOrder, true
	}
	return nil, false
}

// DefaultACLObjTypeFromParserObjType maps the parser's
// AlterDefaultPrivilegesStmt.ObjType string ("table"/"sequence"/"function"/
// "type"/"schema"/"largeobject") to the DEFACLOBJ_* byte pg_default_acl
// stores. ok is false for an unrecognized string (should not happen — the
// parser only ever produces one of the six).
func DefaultACLObjTypeFromParserObjType(objType string) (byte, bool) {
	switch objType {
	case "table":
		return DefaultACLObjTypeRelation, true
	case "sequence":
		return DefaultACLObjTypeSequence, true
	case "function":
		return DefaultACLObjTypeFunction, true
	case "type":
		return DefaultACLObjTypeType, true
	case "schema":
		return DefaultACLObjTypeNamespace, true
	case "largeobject":
		return DefaultACLObjTypeLargeObject, true
	}
	return 0, false
}

// DefaultACLNormalizePriv maps a parsed ALTER DEFAULT PRIVILEGES privilege
// keyword to its canonical form — "ALL"/"ALL PRIVILEGES" expands to every
// right modeled for objType. An unrecognized keyword, or an objType this
// package does not model (DefaultACLObjTypeLargeObject), yields nil; the
// caller silently skips it, matching normalizeParameterPriv/
// normalizeDatabasePriv's existing convention elsewhere in this codebase.
func DefaultACLNormalizePriv(objType byte, priv string) []string {
	privOrder, ok := defaultACLPrivInfo(objType)
	if !ok {
		return nil
	}
	priv = strings.ToUpper(strings.TrimSpace(priv))
	if priv == "ALL" || priv == "ALL PRIVILEGES" {
		out := make([]string, len(privOrder))
		for i, pl := range privOrder {
			out[i] = pl.keyword
		}
		return out
	}
	for _, pl := range privOrder {
		if pl.keyword == priv {
			return []string{priv}
		}
	}
	return nil
}

// defaultACLOwnerString returns the owner's full privilege-letter set for
// objType (the baseline a fresh *global* default-ACL entry seeds from —
// see DefaultACLText), reusing the same owner-string constants relacl/typacl/
// proacl/nspacl rendering already uses.
func defaultACLOwnerString(objType byte) string {
	switch objType {
	case DefaultACLObjTypeRelation:
		return ownerTableACLString
	case DefaultACLObjTypeSequence:
		return ownerSequenceACLString
	case DefaultACLObjTypeFunction:
		return ownerFunctionACLString
	case DefaultACLObjTypeType:
		return ownerTypeACLString
	case DefaultACLObjTypeNamespace:
		return ownerSchemaACLString
	}
	return ""
}

// DefaultACLOID returns the synthetic pg_default_acl.oid for the
// (roleOID, schemaOID, objType) triple, minting one from the shared nextOID
// counter on first use — mirrors ParameterACLOID's lazy-create pattern (real
// PostgreSQL likewise only rows a pg_default_acl tuple once a GRANT
// materializes one, ParameterAclCreate/SetDefaultACL). schemaOID 0 means "no
// IN SCHEMA" (a global default); this is recorded per-OID so DefaultACLText
// can select the right rendering (a global entry has an implicit
// acldefault() baseline, a schema-scoped one does not — aclchk.c's
// SetDefaultACL). M0110-0001 (DU-002 slice 438 follow-up).
func (c *InMemory) DefaultACLOID(roleOID, schemaOID uint32, objType byte) uint32 {
	key := defaultACLKey{RoleOID: roleOID, SchemaOID: schemaOID, ObjType: objType}
	c.mu.Lock()
	defer c.mu.Unlock()
	if oid, ok := c.defaultACLOIDs[key]; ok {
		return oid
	}
	oid := c.allocOIDLocked()
	c.defaultACLOIDs[key] = oid
	c.defaultACLKeys[oid] = key
	c.defaultACLGlobal[oid] = schemaOID == 0
	return oid
}

// HasDefaultACL reports whether the (roleOID, schemaOID, objType) triple
// currently has a materialized, non-empty pg_default_acl row, without
// minting one — mirrors HasParameterACL. REVOKE against a triple that either
// never had a row or whose ACL has already returned to its implicit default
// (at which point real PostgreSQL deletes the row) is a no-op; callers gate
// on this before calling RevokeDefaultACLPrivilege so a bare REVOKE never
// mints a hollow entry.
func (c *InMemory) HasDefaultACL(roleOID, schemaOID uint32, objType byte) bool {
	key := defaultACLKey{RoleOID: roleOID, SchemaOID: schemaOID, ObjType: objType}
	c.mu.RLock()
	defer c.mu.RUnlock()
	oid, ok := c.defaultACLOIDs[key]
	if !ok {
		return false
	}
	return len(c.tableACLs[oid]) > 0
}

// GrantDefaultACLPrivilege records that grantee ("public" for PUBLIC) is
// granted priv on the default-ACL entry aclOID (minted by DefaultACLOID).
// This is a thin alias for GrantTablePrivilegeWithGrantOption: a default-ACL
// entry shares the exact same synthetic-OID-keyed store every other
// virtual-only ACL class (parameter, and — for the global-entry case — the
// owner-injection rendering) already uses, so no new storage engine is
// needed, only a dedicated renderer (DefaultACLText). M0110-0001 (DU-002
// slice 438 follow-up).
func (c *InMemory) GrantDefaultACLPrivilege(aclOID uint32, grantee, priv string, withGrantOption bool) {
	c.GrantTablePrivilegeWithGrantOption(aclOID, grantee, priv, withGrantOption)
}

// RevokeDefaultACLPrivilege is GrantDefaultACLPrivilege's inverse.
func (c *InMemory) RevokeDefaultACLPrivilege(aclOID uint32, grantee, priv string) {
	c.RevokeTablePrivilege(aclOID, grantee, priv)
}

// DefaultACLText renders the pg_default_acl.defaclacl aclitem[] text for the
// default-ACL entry aclOID, or "" (SQL NULL) once no grant survives.
//
// A *global* entry (no IN SCHEMA) reuses relaclTextLockedFor's owner-
// injection engine as-is: PostgreSQL's SetDefaultACL (aclchk.c) seeds a fresh
// global entry's baseline from acldefault(objtype, roleid) — the exact same
// "owner implicitly holds full rights until explicitly touched" rule
// pg_class.relacl/pg_proc.proacl/pg_type.typacl/pg_namespace.nspacl already
// implement here — under the simplifying assumption shared by every other
// ACL store in this package that the owning role is always "postgres"
// (goopg's current_user resolves to "postgres" unconditionally; a
// `FOR ROLE <other>` target's *implicit* baseline is not separately modeled,
// though grants explicitly naming that role as a grantee still render
// correctly — see the deferral ledger).
//
// A *schema-scoped* entry (IN SCHEMA given) has no implicit baseline at all
// (aclchk.c's make_empty_acl() branch for a valid nspid), so it is rendered
// with no synthetic owner line — every grantee, including "postgres" if
// explicitly granted, appears uniformly.
func (c *InMemory) DefaultACLText(aclOID uint32, objType byte) string {
	privOrder, ok := defaultACLPrivInfo(objType)
	if !ok {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.defaultACLGlobal[aclOID] {
		return c.relaclTextLockedFor(aclOID, privOrder, defaultACLOwnerString(objType))
	}
	return c.defaultACLTextNoOwnerLocked(aclOID, privOrder)
}

// defaultACLTextNoOwnerLocked renders aclOID's grantees with no owner
// injection at all — the schema-scoped counterpart of relaclTextLockedFor's
// tail (grantee ordering/quoting/PUBLIC-mapping logic is identical; only the
// leading implicit-owner item is omitted). Caller must hold c.mu.
func (c *InMemory) defaultACLTextNoOwnerLocked(aclOID uint32, privOrder []aclPrivLetter) string {
	byRole := c.tableACLs[aclOID]
	if len(byRole) == 0 {
		return ""
	}
	roles := make([]string, 0, len(byRole))
	seen := make(map[string]bool, len(byRole))
	for _, role := range c.tableACLOrder[aclOID] {
		if seen[role] {
			continue
		}
		if _, ok := byRole[role]; !ok {
			continue
		}
		seen[role] = true
		roles = append(roles, role)
	}
	var missing []string
	for role := range byRole {
		if seen[role] {
			continue
		}
		missing = append(missing, role)
	}
	sort.Strings(missing)
	roles = append(roles, missing...)
	var items []string
	for _, role := range roles {
		letters := renderACLLetters(byRole[role], privOrder)
		if letters == "" {
			continue
		}
		grantee := role
		if grantee == publicPseudoRole {
			grantee = ""
		} else if disp, ok := c.roleACLDisplay[role]; ok {
			grantee = disp
		}
		items = append(items, aclQuoteName(grantee)+"="+letters+"/postgres")
	}
	if len(items) == 0 {
		return ""
	}
	return "{" + strings.Join(items, ",") + "}"
}

// DefaultACLEntries returns every default-ACL triple that currently has a
// materialized, non-empty row, sorted by OID for determinism — mirrors
// ParameterACLEntries. A triple whose ACL has fully returned to its implicit
// default (its tableACLs entry emptied by RevokeTablePrivilege) is omitted,
// matching PostgreSQL's own row deletion in that case (SetDefaultACL,
// aclchk.c). M0110-0001 (DU-002 slice 438 follow-up).
func (c *InMemory) DefaultACLEntries() []DefaultACLEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]DefaultACLEntry, 0, len(c.defaultACLKeys))
	for oid, key := range c.defaultACLKeys {
		if len(c.tableACLs[oid]) == 0 {
			continue
		}
		out = append(out, DefaultACLEntry{OID: oid, RoleOID: key.RoleOID, SchemaOID: key.SchemaOID, ObjType: key.ObjType})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}
