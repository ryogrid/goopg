package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// M0148-0001j3 — USAGE privilege on types.
//
// PostgreSQL checks object_aclcheck(TypeRelationId, typid, GetUserId(),
// ACL_USAGE) wherever DDL makes an object depend on a type, and reports a
// failure through aclcheck_error_type ("permission denied for type %s", no
// error position):
//
//   - BuildDescForRelation (tablecmds.c): every column of CREATE TABLE, CREATE
//     TABLE AS, CREATE TYPE … AS (…), and ALTER TABLE / ALTER TYPE adding or
//     retyping a column/attribute; check_of_type for CREATE TABLE … OF;
//   - DefineDomain: the base type;
//   - interpret_function_parameter_list / compute_return_type: CREATE
//     FUNCTION argument and result types, and DefineAggregate's argument types;
//   - CreateCast: source and target; DefineOperator: left and right operand.
//
// The ACL of an array type is its element type's (pg_type_aclmask follows
// typelem), and a type whose typacl is still NULL has acldefault('T',
// owner): USAGE for the owner AND for PUBLIC — so only a type that a REVOKE
// has touched can refuse anyone. A superuser bypasses the check.

// typeUsageTarget resolves a type spelling (`public.t`, `t[]`, `_t`,
// `varchar(5)`) to the user type whose typacl governs it. ok is false for a
// built-in or unknown type (their typacl is the PUBLIC default).
func typeUsageTarget(im *catalog.InMemory, spelling string) (oid uint32, name string, owner uint32, ok bool) {
	base, _ := depTypeRef(spelling)
	if base == "" {
		return 0, "", 0, false
	}
	lookup := func(n string) (uint32, uint32, bool) {
		if et, ok := im.LookupEnum(n); ok && et != nil {
			return et.OID, et.OwnerOrDefault(), true
		}
		if d, ok := im.LookupDomain(n); ok && d != nil {
			return d.OID, d.OwnerOrDefault(), true
		}
		if ct := im.LookupCompositeType(n); ct != nil {
			return ct.OID, ct.OwnerOrDefault(), true
		}
		return 0, 0, false
	}
	if o, ow, found := lookup(base); found {
		return o, base, ow, true
	}
	// `_t` spells t's array type; its ACL is t's.
	if strings.HasPrefix(base, "_") {
		if o, ow, found := lookup(base[1:]); found {
			return o, base[1:], ow, true
		}
	}
	return 0, "", 0, false
}

// typeUsagePermitted reports whether the session's current role holds USAGE
// on the type named by spelling (pg_type_aclcheck(..., ACL_USAGE) == OK).
func typeUsagePermitted(ctx *Context, spelling string) (bool, string) {
	if ctx.NonSuperuserRole == "" {
		return true, ""
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return true, ""
	}
	oid, name, owner, found := typeUsageTarget(im, spelling)
	if !found {
		return true, ""
	}
	if im.TypeACLText(oid) == "" {
		return true, name // NULL typacl: acldefault grants PUBLIC USAGE
	}
	role := strings.ToLower(ctx.NonSuperuserRole)
	if roid, ok := im.RoleOID(role); ok && roid == owner {
		return true, name
	}
	return im.HasTablePrivilege(oid, role, "USAGE"), name
}

// checkTypeUsage is aclcheck_error_type for a failed USAGE check: 42501
// "permission denied for type %s" (format_type_be of the element type), no
// error position.
func checkTypeUsage(ctx *Context, spelling string) error {
	if ok, name := typeUsagePermitted(ctx, spelling); !ok {
		return &ExecError{Code: "42501", Message: fmt.Sprintf("permission denied for type %s", name)}
	}
	return nil
}
