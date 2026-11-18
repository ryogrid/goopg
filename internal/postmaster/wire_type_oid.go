package postmaster

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// wireTypeOID is the type OID a RowDescription field carries for a result
// column of type t: PG's SendRowDescriptionMessage (printtup.c) sends the
// column's own pg_type OID, with a domain replaced by its base type
// (getBaseTypeAndTypmod). Every RowDescription goes through here, so the
// simple, extended and FETCH paths and Describe report the same type.
func wireTypeOID(cat catalog.Catalog, dbOid uint32, t catalog.Type) uint32 {
	name := strings.ToLower(strings.TrimSpace(t.Name))
	isArray := t.IsArray
	if elem, ok := strings.CutSuffix(name, "[]"); ok {
		name, isArray = elem, true
	} else if elem, ok := strings.CutPrefix(name, "_"); ok && isBuiltinWireTypeName(elem) {
		name, isArray = elem, true
	}
	elem := t
	elem.Name = name

	oid := typeOIDFor(elem)
	if !isBuiltinWireTypeName(name) && cat != nil {
		if uoid, arrayOID, ok := userWireTypeOIDs(cat, dbOid, name); ok {
			if isArray {
				return arrayOID
			}
			return uoid
		}
	}
	if !isArray {
		return oid
	}
	if arr := catalog.ArrayOIDForBase(oid); arr != 0 {
		return arr
	}
	if elemName, ok := catalog.BuiltinFormatTypeName(oid); ok {
		if arr, ok := catalog.BuiltinTypeOIDByFormatName(elemName + "[]"); ok {
			return arr
		}
	}
	return catalog.OIDText
}

// userWireTypeOIDs resolves a user-defined type name to the OID a result
// column of that type reports, and to the OID of its array type. The array of
// a domain is a type of its own, so only the scalar form flattens to the base.
func userWireTypeOIDs(cat catalog.Catalog, dbOid uint32, name string) (scalar, array uint32, ok bool) {
	var db []uint32
	if dbOid != 0 {
		db = []uint32{dbOid}
	}
	if et, ok := cat.LookupEnum(name, db...); ok {
		return et.OID, et.ArrayOID, true
	}
	if d, ok := cat.LookupDomain(name, db...); ok {
		return domainBaseWireOID(cat, dbOid, d), d.ArrayOID, true
	}
	if ct := cat.LookupCompositeType(name, db...); ct != nil {
		return ct.OID, ct.ArrayOID, true
	}
	if rt, ok := cat.LookupRangeType(name, db...); ok {
		return rt.OID, rt.ArrayOID, true
	}
	if rt, ok := cat.LookupRangeTypeByMultirangeName(name); ok {
		return rt.MultirangeOID, rt.MultirangeArrayOID, true
	}
	if tbl, ok := cat.LookupTable(parser.ObjectName{Name: name}, db...); ok && tbl.RowTypeOID != 0 {
		return tbl.RowTypeOID, tbl.RowTypeOID - 1, true
	}
	return 0, 0, false
}

// domainBaseWireOID follows a domain to its non-domain base type.
func domainBaseWireOID(cat catalog.Catalog, dbOid uint32, d *catalog.Domain) uint32 {
	for depth := 0; depth < 64; depth++ {
		if d.BaseOID == 0 {
			return wireTypeOID(cat, dbOid, d.Base)
		}
		next, ok := cat.LookupDomainByOID(d.BaseOID)
		if !ok {
			return d.BaseOID
		}
		d = next
	}
	return catalog.OIDText
}

// builtinWireTypeOID resolves a lower-case type name the typeOIDFor switch does
// not list, by goopg's name table and then by pg_catalog's format_type
// spelling. A name that is not a type a value can have stays text: "unknown" is
// the planner's unresolved type (PG resolves such output to text), and the
// any* pseudo-types never describe a result column.
func builtinWireTypeOID(name string) uint32 {
	if name == "unknown" || isAnyPseudoType(name) {
		return catalog.OIDText
	}
	if oid := catalog.TypeNameToOID(name); oid != catalog.OIDText {
		return oid
	}
	if oid, ok := catalog.BuiltinTypeOIDByFormatName(name); ok {
		return oid
	}
	return catalog.OIDText
}

// isBuiltinWireTypeName reports whether name needs no user-type lookup.
func isBuiltinWireTypeName(name string) bool {
	switch name {
	case "", "text", "unknown":
		return true
	}
	return isAnyPseudoType(name) || typeOIDFor(catalog.Type{Name: name}) != catalog.OIDText
}

// isAnyPseudoType reports the polymorphic pseudo-types: "any", anyelement,
// anyarray and the rest of pg_type's any* family. A user type may also start
// with "any", so the name must be a built-in one.
func isAnyPseudoType(name string) bool {
	if name == "any" {
		return true
	}
	if !strings.HasPrefix(name, "any") {
		return false
	}
	_, ok := catalog.BuiltinTypeOIDByFormatName(name)
	return ok
}
