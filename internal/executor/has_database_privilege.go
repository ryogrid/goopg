package executor

import (
	"fmt"
)

// has_database_privilege (M0149-0001h): a port of the acl.c family
// (has_database_privilege_{name_name,name_id,id_name,id_id,name,id}) over
// goopg's database ACL store — the same source pg_database.datacl renders,
// so `\l+` and the privilege test agree.
//
//   - role: a name goes through get_role_oid_or_public ("public" is
//     ACL_ID_PUBLIC, an unknown name is 42704); an OID is taken as given; the
//     two-argument forms use the current (effective) user.
//   - database: a name goes through get_database_oid (unknown is 3D000); an
//     OID that names no database returns NULL, as the _id variants do.
//   - privilege: convert_database_priv_string — a comma-separated,
//     case-insensitive list of CREATE / TEMPORARY / TEMP / CONNECT, each
//     optionally WITH GRANT OPTION; anything else is 22023.
//   - result: object_aclcheck with ACLMASK_ANY — true when any listed
//     privilege is held. pg_database_aclmask → aclmask: a superuser holds
//     everything; a role with the privileges of the owner holds every grant
//     option; otherwise the privileges come from the ACL entries whose
//     grantee is PUBLIC, the role, or a role it has the privileges of. A NULL
//     datacl is acldefault('d', owner): owner CTc, PUBLIC Tc.

// dbPrivNames is convert_database_priv_string's table.
var dbPrivNames = map[string]byte{"CREATE": 'C', "TEMPORARY": 'T', "TEMP": 'T', "CONNECT": 'c'}

// evalHasDatabasePrivilege evaluates one call; args are the evaluated
// arguments (2 or 3).
func evalHasDatabasePrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if len(args) != 2 && len(args) != 3 {
		return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "function has_database_privilege does not exist"}
	}
	if anyNull(args) {
		return NullDatum, nil
	}
	im, err := privilegeCatalog("has_database_privilege", ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	role, dbArg, privs, err := privilegeArgs("has_database_privilege", args, im, ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	var dbName string
	if dbArg.Kind == KindInt {
		n, ok := im.DatabaseNameByDisplayOID(uint32(dbArg.Int))
		if !ok {
			return missingObjectResult(im, role, privs, dbPrivNames)
		}
		dbName = n
	} else {
		dbName = dbArg.StringValue()
		if _, ok := im.ResolveDatabaseOid(dbName); !ok {
			return Datum{}, &ExecError{Code: "3D000", Message: fmt.Sprintf("database \"%s\" does not exist", dbName)}
		}
	}
	want, err := parsePrivList(privs, dbPrivNames)
	if err != nil {
		return Datum{}, err
	}
	dbOid, _ := im.ResolveDatabaseOid(dbName)
	obj := aclObject{owner: im.DatabaseOwner(dbName), aclText: im.DatabaseACLText(dbOid),
		letters: "CTc", defaultOwner: "CTc", defaultPublic: "Tc"}
	return NewBoolDatum(aclAnyHeld(im, role, obj, want)), nil
}
