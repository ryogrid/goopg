package postmaster

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/parser"
)

// wireType is what a RowDescription field says about a result column's type.
type wireType struct {
	oid    uint32
	length int16 // pg_type.typlen
	typmod int32
}

// wireFieldDescription builds the RowDescription field of one result column:
// PG's SendRowDescriptionMessage (printtup.c) sends the column's pg_type OID,
// typlen and typmod, with a domain replaced by its base type and base typmod
// (getBaseTypeAndTypmod). Every RowDescription goes through here, so the simple,
// extended and FETCH paths and Describe report the same type.
func wireFieldDescription(cat catalog.Catalog, dbOid uint32, name string, t catalog.Type) libpq.FieldDescription {
	w := resolveWireType(cat, dbOid, t)
	return libpq.FieldDescription{
		Name:         name,
		TypeOID:      w.oid,
		TypeSize:     w.length,
		TypeModifier: w.typmod,
		Format:       0,
	}
}

// wireTypeOID is the type OID of wireFieldDescription.
func wireTypeOID(cat catalog.Catalog, dbOid uint32, t catalog.Type) uint32 {
	return resolveWireType(cat, dbOid, t).oid
}

func resolveWireType(cat catalog.Catalog, dbOid uint32, t catalog.Type) wireType {
	name := strings.ToLower(strings.TrimSpace(t.Name))
	isArray := t.IsArray
	if elem, ok := strings.CutSuffix(name, "[]"); ok {
		name, isArray = elem, true
	} else if elem, ok := strings.CutPrefix(name, "_"); ok && isBuiltinWireTypeName(elem) {
		name, isArray = elem, true
	}
	elem := t
	elem.Name = name

	if !isBuiltinWireTypeName(name) && cat != nil {
		if u, ok := userWireType(cat, dbOid, name); ok {
			if isArray {
				return wireType{oid: u.arrayOID, length: -1, typmod: u.scalar.typmod}
			}
			return u.scalar
		}
	}
	oid := typeOIDFor(elem)
	typmod := executor.AttTypmodForOID(oid, t.Args)
	if !isArray {
		return wireType{oid: oid, length: executor.TypeLengthForOID(oid), typmod: typmod}
	}
	return wireType{oid: builtinArrayWireOID(oid), length: -1, typmod: typmod}
}

// builtinArrayWireOID is the array type of a built-in element type.
func builtinArrayWireOID(oid uint32) uint32 {
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

// userWireInfo is a user-defined type as a result column reports it, and the
// OID of its array type. The array of a domain is a type of its own, so only
// the scalar form flattens to the base type.
type userWireInfo struct {
	scalar   wireType
	arrayOID uint32
}

func userWireType(cat catalog.Catalog, dbOid uint32, name string) (userWireInfo, bool) {
	var db []uint32
	if dbOid != 0 {
		db = []uint32{dbOid}
	}
	varlena := func(oid, array uint32) (userWireInfo, bool) {
		return userWireInfo{wireType{oid: oid, length: -1, typmod: -1}, array}, true
	}
	if et, ok := cat.LookupEnum(name, db...); ok {
		return userWireInfo{wireType{oid: et.OID, length: 4, typmod: -1}, et.ArrayOID}, true
	}
	if d, ok := cat.LookupDomain(name, db...); ok {
		return userWireInfo{domainBaseWireType(cat, dbOid, d), d.ArrayOID}, true
	}
	if ct := cat.LookupCompositeType(name, db...); ct != nil {
		return varlena(ct.OID, ct.ArrayOID)
	}
	if rt, ok := cat.LookupRangeType(name, db...); ok {
		return varlena(rt.OID, rt.ArrayOID)
	}
	if rt, ok := cat.LookupRangeTypeByMultirangeName(name); ok {
		return varlena(rt.MultirangeOID, rt.MultirangeArrayOID)
	}
	if tbl, ok := cat.LookupTable(parser.ObjectName{Name: name}, db...); ok && tbl.RowTypeOID != 0 {
		return varlena(tbl.RowTypeOID, tbl.RowTypeOID-1)
	}
	return userWireInfo{}, false
}

// domainBaseWireType follows a domain to its non-domain base type
// and that type's declared typmod (getBaseTypeAndTypmod).
func domainBaseWireType(cat catalog.Catalog, dbOid uint32, d *catalog.Domain) wireType {
	for depth := 0; depth < 64; depth++ {
		if next, ok := cat.LookupDomainByOID(d.BaseOID); d.BaseOID != 0 && ok {
			d = next
			continue
		}
		w := resolveWireType(cat, dbOid, d.Base)
		if d.BaseOID != 0 {
			w.oid = d.BaseOID
		}
		return w
	}
	return wireType{oid: catalog.OIDText, length: -1, typmod: -1}
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
