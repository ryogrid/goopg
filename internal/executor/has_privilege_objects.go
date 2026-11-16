package executor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// The has_*_privilege functions over relations' columns, sequences,
// functions and types (acl.c; M0149-0001h1 slice 2). Each builds the
// object's aclmask view (aclObject) and asks aclAnyHeld, as
// has_table_privilege does in has_privilege_common.go.

// resolveRoleArg is get_role_oid_or_public for a name argument, or the OID as
// given.
func resolveRoleArg(d Datum, im *catalog.InMemory) (uint32, error) {
	if d.Kind == KindInt {
		return uint32(d.Int), nil
	}
	name := d.StringValue()
	if strings.EqualFold(name, "public") {
		return aclPublicRole, nil
	}
	oid, ok := im.RoleOID(name)
	if !ok {
		return 0, &ExecError{Code: "42704", Message: fmt.Sprintf("role \"%s\" does not exist", name)}
	}
	return oid, nil
}

// currentRoleOID is GetUserId() for the forms without a role argument.
func currentRoleOID(im *catalog.InMemory, ctx *Context) uint32 {
	if oid, ok := im.RoleOID(ctx.EffectiveUserName()); ok {
		return oid
	}
	return catalog.BootstrapSuperuserOID
}

// missingObjectResult is object_aclcheck_ext's answer for an OID naming no
// object: a superuser is bypassed before the catalog lookup (aclchk.c
// object_aclmask_ext, pg_namespace_aclmask_ext, pg_type_aclmask_ext), so it
// holds every privilege; anyone else sees is_missing and gets NULL. Relations
// (pg_class_aclmask_ext) look the tuple up first and always return NULL.
func missingObjectResult(im *catalog.InMemory, role uint32, privs string, names map[string]byte) (Datum, error) {
	if _, err := parsePrivList(privs, names); err != nil {
		return Datum{}, err
	}
	if role != aclPublicRole && im.IsSuperuser(role) {
		return NewBoolDatum(true), nil
	}
	return NullDatum, nil
}

// relationACLObject is pg_class_aclmask's view of a relation: a sequence
// carries only rwU; the predefined data roles imply SELECT / write rights.
func relationACLObject(im *catalog.InMemory, tbl *catalog.Table, relOID uint32) aclObject {
	owner := catalog.BootstrapSuperuserOID
	if tbl.Owner != "" {
		if o, ok := im.RoleOID(tbl.Owner); ok {
			owner = o
		}
	}
	obj := aclObject{owner: owner, letters: "arwdDxtm", defaultOwner: "arwdDxtm",
		implied: map[uint32]string{roleReadAllData: "r", roleWriteAllData: "awd", roleMaintain: "m"}}
	if tbl.IsSequence {
		obj.aclText, obj.letters, obj.defaultOwner = im.SequenceRelaclText(relOID), "rwU", "rwU"
		obj.implied = map[uint32]string{roleReadAllData: "r", roleWriteAllData: "w"}
	} else {
		obj.aclText = im.RelaclText(relOID)
	}
	return obj
}

// relationByArg is convert_table_name (regclassin; 42P01 when missing) for a
// text argument, or the OID as given. ok is false for an OID naming no
// relation (the _id variants return NULL).
func relationByArg(d Datum, im *catalog.InMemory, ctx *Context) (*catalog.Table, uint32, bool, error) {
	var relOID uint32
	if d.Kind == KindInt {
		relOID = uint32(d.Int)
	} else {
		r, err := regIdentifierInput(d, "regclass", ctx, 0)
		if err != nil {
			return nil, 0, false, err
		}
		relOID = uint32(r.Int)
	}
	tbl, ok := im.LookupTableByOID(relOID)
	if !ok {
		if d.Kind == KindInt {
			return nil, relOID, false, nil
		}
		return nil, relOID, false, &ExecError{Code: "42P01", Message: fmt.Sprintf("relation with OID %d does not exist", relOID)}
	}
	return tbl, relOID, true, nil
}

// columnPrivNames is convert_column_priv_string's table.
var columnPrivNames = map[string]byte{"SELECT": 'r', "INSERT": 'a', "UPDATE": 'w', "REFERENCES": 'x'}

// systemAttNums are the system columns' attnums (sysattr.h).
var systemAttNums = map[string]int16{"ctid": -1, "xmin": -2, "cmin": -3, "xmax": -4, "cmax": -5, "tableoid": -6}

// droppedColumnName is the name RemoveAttributeById gives a dropped column.
func droppedColumnName(attnum int16) string {
	return fmt.Sprintf("........pg.dropped.%d........", attnum)
}

// columnAttnumState resolves a column of tbl: by name through
// convert_column_name (a dropped column's placeholder name is InvalidAttrNumber,
// an unknown name is 42703) or by attnum. exists is false when
// pg_attribute_aclcheck_ext would report the attribute missing (no such
// attnum, or a dropped column).
func columnAttnumState(tbl *catalog.Table, col Datum) (attnum int16, exists bool, err error) {
	if col.Kind == KindInt {
		attnum = int16(col.Int)
		if attnum < 0 {
			for _, n := range systemAttNums {
				if n == attnum {
					return attnum, true, nil
				}
			}
			return attnum, false, nil
		}
		for _, c := range tbl.Columns {
			if int16(c.Ordinal+1) == attnum {
				return attnum, !c.Dropped, nil
			}
		}
		return attnum, false, nil
	}
	name := col.StringValue()
	for _, c := range tbl.Columns {
		if c.Dropped {
			if name == droppedColumnName(int16(c.Ordinal+1)) {
				return 0, false, nil
			}
			continue
		}
		if c.Name == name {
			return int16(c.Ordinal + 1), true, nil
		}
	}
	if n, ok := systemAttNums[name]; ok {
		return n, true, nil
	}
	return 0, false, &ExecError{Code: "42703", Message: fmt.Sprintf("column \"%s\" of relation \"%s\" does not exist", name, tbl.Name)}
}

// columnACLObject is pg_attribute_aclmask's view: acldefault('c') is empty
// and the table owner holds the grant options.
func columnACLObject(im *catalog.InMemory, tblObj aclObject, relOID uint32, attnum int16) aclObject {
	return aclObject{owner: tblObj.owner, aclText: im.AttrACLText(relOID, attnum), letters: "arwx",
		defaultOwner: "", defaultPublic: ""}
}

// evalHasColumnPrivilege is has_column_privilege's twelve overloads
// (column_privilege_check): column-level privilege first, then table-level;
// NULL when the table OID or the attribute is missing.
func evalHasColumnPrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if len(args) != 3 && len(args) != 4 {
		return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "function has_column_privilege does not exist"}
	}
	if anyNull(args) {
		return NullDatum, nil
	}
	im, err := privilegeCatalog("has_column_privilege", ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	var role uint32
	if len(args) == 4 {
		if role, err = resolveRoleArg(args[0], im); err != nil {
			return Datum{}, err
		}
		args = args[1:]
	} else {
		role = currentRoleOID(im, ctx)
	}
	tbl, relOID, found, err := relationByArg(args[0], im, ctx)
	if err != nil {
		return Datum{}, err
	}
	attnum, exists := int16(0), false
	if found {
		if attnum, exists, err = columnAttnumState(tbl, args[1]); err != nil {
			return Datum{}, err
		}
	}
	want, err := parsePrivList(args[2].StringValue(), columnPrivNames)
	if err != nil {
		return Datum{}, err
	}
	if !found || !exists {
		return NullDatum, nil
	}
	tblObj := relationACLObject(im, tbl, relOID)
	if attnum > 0 && aclAnyHeld(im, role, columnACLObject(im, tblObj, relOID, attnum), want) {
		return NewBoolDatum(true), nil
	}
	return NewBoolDatum(aclAnyHeld(im, role, tblObj, want)), nil
}

// evalHasAnyColumnPrivilege is has_any_column_privilege's six overloads: the
// table-level privilege, else any non-dropped column's (pg_attribute_aclcheck_all
// with ACLMASK_ANY).
func evalHasAnyColumnPrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if anyNull(args) {
		return NullDatum, nil
	}
	im, err := privilegeCatalog("has_any_column_privilege", ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	role, objArg, privs, err := privilegeArgs("has_any_column_privilege", args, im, ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	tbl, relOID, found, err := relationByArg(objArg, im, ctx)
	if err != nil {
		return Datum{}, err
	}
	want, err := parsePrivList(privs, columnPrivNames)
	if err != nil {
		return Datum{}, err
	}
	if !found {
		return NullDatum, nil
	}
	tblObj := relationACLObject(im, tbl, relOID)
	if aclAnyHeld(im, role, tblObj, want) {
		return NewBoolDatum(true), nil
	}
	for _, c := range tbl.Columns {
		if c.Dropped {
			continue
		}
		if aclAnyHeld(im, role, columnACLObject(im, tblObj, relOID, int16(c.Ordinal+1)), want) {
			return NewBoolDatum(true), nil
		}
	}
	return NewBoolDatum(false), nil
}

// sequencePrivNames is convert_sequence_priv_string's table.
var sequencePrivNames = map[string]byte{"USAGE": 'U', "SELECT": 'r', "UPDATE": 'w'}

// evalHasSequencePrivilege is has_sequence_privilege's six overloads: the
// privilege string is parsed before the name; a relation that is not a
// sequence is 42809.
func evalHasSequencePrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if anyNull(args) {
		return NullDatum, nil
	}
	im, err := privilegeCatalog("has_sequence_privilege", ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	role, objArg, privs, err := privilegeArgs("has_sequence_privilege", args, im, ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	want, err := parsePrivList(privs, sequencePrivNames)
	if err != nil {
		return Datum{}, err
	}
	tbl, relOID, found, err := relationByArg(objArg, im, ctx)
	if err != nil {
		return Datum{}, err
	}
	if !found {
		return NullDatum, nil
	}
	if !tbl.IsSequence {
		name := tbl.Name
		if objArg.Kind != KindInt {
			name = objArg.StringValue()
		}
		return Datum{}, &ExecError{Code: "42809", Message: fmt.Sprintf("\"%s\" is not a sequence", name)}
	}
	return NewBoolDatum(aclAnyHeld(im, role, relationACLObject(im, tbl, relOID), want)), nil
}

// functionPrivNames is convert_function_priv_string's table.
var functionPrivNames = map[string]byte{"EXECUTE": 'X'}

// evalHasFunctionPrivilege is has_function_privilege's six overloads: the
// function by regprocedure text (42883 when missing) or by OID (NULL when
// missing). acldefault('f', owner) gives EXECUTE to the owner and PUBLIC.
func evalHasFunctionPrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if anyNull(args) {
		return NullDatum, nil
	}
	im, err := privilegeCatalog("has_function_privilege", ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	role, objArg, privs, err := privilegeArgs("has_function_privilege", args, im, ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	var procOID uint32
	if objArg.Kind == KindInt {
		procOID = uint32(objArg.Int)
	} else {
		d, rerr := regIdentifierInput(objArg, "regprocedure", ctx, 0)
		if rerr != nil {
			return Datum{}, rerr
		}
		procOID = uint32(d.Int)
	}
	owner, found := catalog.BootstrapSuperuserOID, false
	if r := im.Routines().LookupByOID(procOID); r != nil {
		owner, found = r.OwnerOrDefault(), true
	} else if agg, ok := im.LookupUserAggregateByOID(procOID); ok {
		owner, found = agg.OwnerOrDefault(), true
	} else if _, ok := catalog.RegprocName(procOID); ok {
		found = true
	}
	if !found {
		if objArg.Kind == KindInt {
			return missingObjectResult(im, role, privs, functionPrivNames)
		}
		return Datum{}, &ExecError{Code: "42883", Message: fmt.Sprintf("function \"%s\" does not exist", objArg.StringValue())}
	}
	want, err := parsePrivList(privs, functionPrivNames)
	if err != nil {
		return Datum{}, err
	}
	obj := aclObject{owner: owner, aclText: im.ProcACLText(procOID), letters: "X",
		defaultOwner: "X", defaultPublic: "X"}
	return NewBoolDatum(aclAnyHeld(im, role, obj, want)), nil
}

// typePrivNames is convert_type_priv_string's table.
var typePrivNames = map[string]byte{"USAGE": 'U'}

// userTypeOwner resolves a user type OID — or its array type's OID, whose
// ACL is the element type's (pg_type_aclmask follows typelem) — to the OID
// whose typacl governs it and that type's owner.
func userTypeOwner(im *catalog.InMemory, oid uint32) (uint32, uint32, bool) {
	if et, ok := im.LookupEnumByOID(oid); ok && et != nil {
		return et.OID, et.OwnerOrDefault(), true
	}
	if d, ok := im.LookupDomainByOID(oid); ok && d != nil {
		return d.OID, d.OwnerOrDefault(), true
	}
	if ct, ok := im.LookupCompositeTypeByOID(oid); ok && ct != nil {
		return ct.OID, ct.OwnerOrDefault(), true
	}
	if rt, ok := im.LookupRangeTypeByOID(oid); ok && rt != nil {
		return rt.OID, rt.OwnerOrDefault(), true
	}
	if et, ok := im.LookupEnumByArrayOID(oid); ok && et != nil {
		return et.OID, et.OwnerOrDefault(), true
	}
	if d, ok := im.LookupDomainByArrayOID(oid); ok && d != nil {
		return d.OID, d.OwnerOrDefault(), true
	}
	if ct, ok := im.LookupCompositeTypeByArrayOID(oid); ok && ct != nil {
		return ct.OID, ct.OwnerOrDefault(), true
	}
	if rt, ok := im.LookupRangeTypeByArrayOID(oid); ok && rt != nil {
		return rt.OID, rt.OwnerOrDefault(), true
	}
	return 0, 0, false
}

// evalHasTypePrivilege is has_type_privilege's six overloads: the type by
// regtype text (42704 when missing) or by OID (NULL when missing).
// acldefault('T', owner) gives USAGE to the owner and PUBLIC.
func evalHasTypePrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if anyNull(args) {
		return NullDatum, nil
	}
	im, err := privilegeCatalog("has_type_privilege", ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	role, objArg, privs, err := privilegeArgs("has_type_privilege", args, im, ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	var typeOID uint32
	if objArg.Kind == KindInt {
		typeOID = uint32(objArg.Int)
	} else {
		d, rerr := regIdentifierInput(objArg, "regtype", ctx, 0)
		if rerr != nil {
			return Datum{}, rerr
		}
		typeOID = uint32(d.Int)
	}
	aclOID, owner, found := userTypeOwner(im, typeOID)
	if !found {
		// A built-in type: owned by the bootstrap superuser. regtypeout
		// renders an unknown OID as its number.
		if RegOut("regtype", typeOID, im, false) == strconv.FormatUint(uint64(typeOID), 10) {
			return missingObjectResult(im, role, privs, typePrivNames)
		}
		aclOID, owner = typeOID, catalog.BootstrapSuperuserOID
	}
	want, err := parsePrivList(privs, typePrivNames)
	if err != nil {
		return Datum{}, err
	}
	obj := aclObject{owner: owner, aclText: im.TypeACLText(aclOID), letters: "U",
		defaultOwner: "U", defaultPublic: "U"}
	return NewBoolDatum(aclAnyHeld(im, role, obj, want)), nil
}

// isHasPrivilegeBuiltin reports the has_*_privilege names evalFuncCall
// dispatches to the evaluators above.
func isHasPrivilegeBuiltin(name string) bool {
	switch name {
	case "has_database_privilege", "has_table_privilege", "has_schema_privilege",
		"has_column_privilege", "has_any_column_privilege", "has_sequence_privilege",
		"has_function_privilege", "has_type_privilege", "has_largeobject_privilege",
		"lo_create", "lo_creat", "lo_unlink":
		return true
	}
	return isLargeObjectDataFunc(name)
}
