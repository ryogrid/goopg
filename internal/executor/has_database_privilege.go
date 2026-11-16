package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
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

// dbPrivLetter maps a database privilege keyword to its aclitem letter.
var dbPrivLetter = map[string]byte{"CREATE": 'C', "TEMPORARY": 'T', "TEMP": 'T', "CONNECT": 'c'}

type privWant struct {
	letter byte
	gopt   bool
}

// parseDatabasePrivs is convert_database_priv_string.
func parseDatabasePrivs(s string) ([]privWant, error) {
	var out []privWant
	for _, part := range strings.Split(s, ",") {
		p := strings.ToUpper(strings.Join(strings.Fields(part), " "))
		gopt := false
		if base, ok := strings.CutSuffix(p, " WITH GRANT OPTION"); ok {
			p, gopt = base, true
		}
		l, ok := dbPrivLetter[p]
		if !ok {
			return nil, &ExecError{Code: "22023", Message: fmt.Sprintf("unrecognized privilege type: \"%s\"", strings.TrimSpace(part))}
		}
		out = append(out, privWant{l, gopt})
	}
	return out, nil
}

// evalHasDatabasePrivilege evaluates one call; args are the evaluated
// arguments (2 or 3).
func evalHasDatabasePrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if len(args) != 2 && len(args) != 3 {
		return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "function has_database_privilege does not exist"}
	}
	for _, a := range args {
		if a.IsNull() {
			return NullDatum, nil
		}
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return Datum{}, &ExecError{Code: "XX000", Pos: pos, Message: "has_database_privilege: catalog unavailable"}
	}
	const public = ^uint32(0) // ACL_ID_PUBLIC stand-in: matches only PUBLIC entries
	var role uint32
	dbArg, privArg := args[0], args[1]
	if len(args) == 3 {
		dbArg, privArg = args[1], args[2]
		if args[0].Kind == KindInt {
			role = uint32(args[0].Int)
		} else {
			name := args[0].StringValue()
			if strings.EqualFold(name, "public") {
				role = public
			} else if oid, ok := im.RoleOID(name); ok {
				role = oid
			} else {
				return Datum{}, &ExecError{Code: "42704", Message: fmt.Sprintf("role \"%s\" does not exist", name)}
			}
		}
	} else {
		oid, ok := im.RoleOID(ctx.EffectiveUserName())
		if !ok {
			oid = catalog.BootstrapSuperuserOID
		}
		role = oid
	}
	var dbName string
	if dbArg.Kind == KindInt {
		n, ok := im.DatabaseNameByDisplayOID(uint32(dbArg.Int))
		if !ok {
			return NullDatum, nil
		}
		dbName = n
	} else {
		dbName = dbArg.StringValue()
		if _, ok := im.ResolveDatabaseOid(dbName); !ok {
			return Datum{}, &ExecError{Code: "3D000", Message: fmt.Sprintf("database \"%s\" does not exist", dbName)}
		}
	}
	want, err := parseDatabasePrivs(privArg.StringValue())
	if err != nil {
		return Datum{}, err
	}
	if role != public && im.IsSuperuser(role) {
		return NewBoolDatum(true), nil
	}
	owner := im.DatabaseOwner(dbName)
	held, gopts := map[byte]bool{}, map[byte]bool{}
	if role != public && (role == owner || im.HasPrivsOfRole(role, owner)) {
		for _, l := range []byte{'C', 'T', 'c'} {
			gopts[l] = true
		}
	}
	dbOid, _ := im.ResolveDatabaseOid(dbName)
	aclText := im.DatabaseACLText(dbOid)
	if aclText == "" {
		aclText = "{" + aclPutid(im.RoleNameForOID(owner)) + "=CTc/" + aclPutid(im.RoleNameForOID(owner)) +
			",=Tc/" + aclPutid(im.RoleNameForOID(owner)) + "}"
	}
	body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(aclText), "{"), "}")
	for _, item := range splitTopLevelAclItems(body) {
		grantee, mode, _, perr := parseAclItemText(item)
		if perr != nil {
			continue
		}
		applies := grantee == ""
		if !applies && role != public {
			if goid, ok := im.RoleOID(grantee); ok {
				applies = goid == role || im.HasPrivsOfRole(role, goid)
			}
		}
		if !applies {
			continue
		}
		for _, l := range []byte{'C', 'T', 'c'} {
			bit := uint64(1) << uint(strings.IndexByte(aclRightsChars, l))
			if mode&bit != 0 {
				held[l] = true
			}
			if mode&(bit<<aclGrantOptionShift) != 0 {
				gopts[l] = true
			}
		}
	}
	for _, w := range want {
		if (w.gopt && gopts[w.letter]) || (!w.gopt && held[w.letter]) {
			return NewBoolDatum(true), nil
		}
	}
	return NewBoolDatum(false), nil
}
