package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// Shared machinery for acl.c's has_*_privilege family (M0149-0001h /
// M0149-0001h1): role resolution, privilege-string parsing and aclmask
// evaluation over goopg's rendered ACL texts.

// aclPublicRole stands for ACL_ID_PUBLIC as a role argument: it matches only
// PUBLIC entries and is never a superuser or a member of anything.
const aclPublicRole = ^uint32(0)

// Predefined roles aclmask consults (pg_authid.dat).
const (
	roleReadAllData  = 6181
	roleWriteAllData = 6182
	roleMaintain     = 6337
)

type privWant struct {
	letter byte
	gopt   bool
}

// parsePrivList is acl.c's convert_any_priv_string: a comma-separated,
// case-insensitive list of the kind's privilege names, each optionally WITH
// GRANT OPTION; an unknown name is 22023.
func parsePrivList(s string, names map[string]byte) ([]privWant, error) {
	var out []privWant
	for _, part := range strings.Split(s, ",") {
		p := strings.ToUpper(strings.Join(strings.Fields(part), " "))
		gopt := false
		if base, ok := strings.CutSuffix(p, " WITH GRANT OPTION"); ok {
			p, gopt = base, true
		}
		l, ok := names[p]
		if !ok {
			return nil, &ExecError{Code: "22023", Message: fmt.Sprintf("unrecognized privilege type: \"%s\"", strings.TrimSpace(part))}
		}
		out = append(out, privWant{l, gopt})
	}
	return out, nil
}

// privilegeArgs splits a has_*_privilege call into (role, object, privilege)
// and resolves the role: a name through get_role_oid_or_public (42704), an
// OID as given, or the effective user for the two-argument forms.
func privilegeArgs(fn string, args []Datum, im *catalog.InMemory, ctx *Context, pos int) (uint32, Datum, string, error) {
	switch len(args) {
	case 2:
		oid, ok := im.RoleOID(ctx.EffectiveUserName())
		if !ok {
			oid = catalog.BootstrapSuperuserOID
		}
		return oid, args[0], args[1].StringValue(), nil
	case 3:
		if args[0].Kind == KindInt {
			return uint32(args[0].Int), args[1], args[2].StringValue(), nil
		}
		name := args[0].StringValue()
		if strings.EqualFold(name, "public") {
			return aclPublicRole, args[1], args[2].StringValue(), nil
		}
		oid, ok := im.RoleOID(name)
		if !ok {
			return 0, Datum{}, "", &ExecError{Code: "42704", Message: fmt.Sprintf("role \"%s\" does not exist", name)}
		}
		return oid, args[1], args[2].StringValue(), nil
	}
	return 0, Datum{}, "", &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("function %s does not exist", fn)}
}

// aclObject is one object's privilege state as aclmask sees it.
type aclObject struct {
	owner   uint32
	aclText string // rendered aclitem[]; "" = NULL (acldefault)
	// defaultOwner / defaultPublic are acldefault's letters for the kind.
	defaultOwner, defaultPublic string
	letters                     string // every privilege letter of the kind
	// implied are privileges granted by membership in a predefined role
	// (aclmask's pg_read_all_data / pg_write_all_data / pg_maintain arms).
	implied map[uint32]string
}

// aclAnyHeld is object_aclcheck with ACLMASK_ANY: true when role holds any
// wanted privilege. A superuser holds everything; a role with the owner's
// privileges holds every grant option; privileges come from the entries for
// PUBLIC, the role and the roles it has the privileges of.
func aclAnyHeld(im *catalog.InMemory, role uint32, obj aclObject, want []privWant) bool {
	if role != aclPublicRole && im.IsSuperuser(role) {
		return true
	}
	held, gopts := map[byte]bool{}, map[byte]bool{}
	if role != aclPublicRole && (role == obj.owner || im.HasPrivsOfRole(role, obj.owner)) {
		for i := 0; i < len(obj.letters); i++ {
			gopts[obj.letters[i]] = true
		}
	}
	if role != aclPublicRole {
		for pre, letters := range obj.implied {
			if im.HasPrivsOfRole(role, pre) {
				for i := 0; i < len(letters); i++ {
					held[letters[i]] = true
				}
			}
		}
	}
	text := obj.aclText
	if text == "" {
		ownerName := aclPutid(im.RoleNameForOID(obj.owner))
		items := []string{}
		if obj.defaultPublic != "" {
			items = append(items, "="+obj.defaultPublic+"/"+ownerName)
		}
		if obj.defaultOwner != "" {
			items = append(items, ownerName+"="+obj.defaultOwner+"/"+ownerName)
		}
		text = "{" + strings.Join(items, ",") + "}"
	}
	body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "{"), "}")
	for _, item := range splitTopLevelAclItems(body) {
		grantee, mode, _, perr := parseAclItemText(item)
		if perr != nil {
			continue
		}
		applies := grantee == ""
		if !applies && role != aclPublicRole {
			if goid, ok := im.RoleOID(grantee); ok {
				applies = goid == role || im.HasPrivsOfRole(role, goid)
			}
		}
		if !applies {
			continue
		}
		for i := 0; i < len(obj.letters); i++ {
			l := obj.letters[i]
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
			return true
		}
	}
	return false
}

// privilegeCatalog returns the session's in-memory catalog.
func privilegeCatalog(fn string, ctx *Context, pos int) (*catalog.InMemory, error) {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil, &ExecError{Code: "XX000", Pos: pos, Message: fn + ": catalog unavailable"}
	}
	return im, nil
}

func anyNull(args []Datum) bool {
	for _, a := range args {
		if a.IsNull() {
			return true
		}
	}
	return false
}

// tablePrivNames is convert_table_priv_string's table.
var tablePrivNames = map[string]byte{
	"SELECT": 'r', "INSERT": 'a', "UPDATE": 'w', "DELETE": 'd', "TRUNCATE": 'D',
	"REFERENCES": 'x', "TRIGGER": 't', "MAINTAIN": 'm',
}

// evalHasTablePrivilege is has_table_privilege's six overloads
// (acl.c has_table_privilege_*): the table by name through regclass input
// (42P01 when missing) or by OID (NULL when missing).
func evalHasTablePrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if anyNull(args) {
		return NullDatum, nil
	}
	im, err := privilegeCatalog("has_table_privilege", ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	role, objArg, privs, err := privilegeArgs("has_table_privilege", args, im, ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	var relOID uint32
	if objArg.Kind == KindInt {
		relOID = uint32(objArg.Int)
	} else {
		d, rerr := regIdentifierInput(objArg, "regclass", ctx, 0) // regclassin reports no cursor
		if rerr != nil {
			return Datum{}, rerr
		}
		relOID = uint32(d.Int)
	}
	tbl, ok := im.LookupTableByOID(relOID)
	if !ok {
		if objArg.Kind == KindInt {
			return NullDatum, nil
		}
		return Datum{}, &ExecError{Code: "42P01", Message: fmt.Sprintf("relation with OID %d does not exist", relOID)}
	}
	want, err := parsePrivList(privs, tablePrivNames)
	if err != nil {
		return Datum{}, err
	}
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
	return NewBoolDatum(aclAnyHeld(im, role, obj, want)), nil
}

// schemaPrivNames is convert_schema_priv_string's table.
var schemaPrivNames = map[string]byte{"CREATE": 'C', "USAGE": 'U'}

// evalHasSchemaPrivilege is has_schema_privilege's six overloads: the schema
// by name (3F000 when missing) or by OID (NULL when missing).
// pg_read_all_data / pg_write_all_data imply USAGE (aclmask).
func evalHasSchemaPrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if anyNull(args) {
		return NullDatum, nil
	}
	im, err := privilegeCatalog("has_schema_privilege", ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	role, objArg, privs, err := privilegeArgs("has_schema_privilege", args, im, ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	var name string
	var oid uint32
	if objArg.Kind == KindInt {
		oid = uint32(objArg.Int)
		name = im.SchemaNameForOID(oid)
		if name == "" {
			return NullDatum, nil
		}
	} else {
		name = objArg.StringValue()
		oid = im.SchemaOID(name)
		if oid == 0 {
			return Datum{}, &ExecError{Code: "3F000", Message: fmt.Sprintf("schema \"%s\" does not exist", name)}
		}
	}
	want, err := parsePrivList(privs, schemaPrivNames)
	if err != nil {
		return Datum{}, err
	}
	obj := aclObject{owner: im.SchemaOwnerOID(name), aclText: im.NamespaceACLText(oid), letters: "UC",
		defaultOwner: "UC", implied: map[uint32]string{roleReadAllData: "U", roleWriteAllData: "U"}}
	return NewBoolDatum(aclAnyHeld(im, role, obj, want)), nil
}
