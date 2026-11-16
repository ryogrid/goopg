package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// Large objects (M0149-0001h1a), first slice: the pg_largeobject_metadata
// half — lo_create / lo_creat / lo_unlink and has_largeobject_privilege.
//
// PG keeps a large object's existence, owner and ACL in the
// pg_largeobject_metadata catalog (OID 2995: oid, lomowner, lomacl) and its
// data in pg_largeobject (2613). Both are ordinary MVCC heaps, so a large
// object created or unlinked by an aborted transaction never existed, and a
// restart or a PG node sees exactly the committed set
// (catalog/pg_largeobject.c, storage/large_object/inv_api.c).
//
// goopg does the same: initdb.loadSystemCatalogsIfPresentForDB registers
// pg_largeobject_metadata as a real heap-backed table, so
//   - inserts go through writeHeapRowCanonical (xmin = the writer XID,
//     XLOG_HEAP_INSERT, and the pg_largeobject_metadata_oid_index 2996 entry a
//     PG node's index scan needs);
//   - lookups and deletes run as internal SQL against that table, getting the
//     executor's ordinary snapshot visibility and rollback. (A catalog
//     xmax stamp — stampCatalogRows — is not transactional: it sets the
//     committed hint at once, so an aborted lo_unlink would still hide the
//     row.)

// loMetaTable returns the heap-backed pg_largeobject_metadata of the
// session's database.
// largeObjectRelationOID is LargeObjectRelationId (pg_largeobject): the
// classid of a large object's ObjectAddress, so of its pg_description row.
const largeObjectRelationOID = 2613

func loMetaTable(ctx *Context) (*catalog.Table, error) {
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_largeobject_metadata"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok || tbl == nil || tbl.Virtual {
		return nil, &ExecError{Code: "0A000", Message: "large objects are not supported in this database"}
	}
	return tbl, nil
}

// internalCatalogQuery runs one SQL statement against the catalogs on the
// session's snapshot, as backend code reads catalogs: without the session
// role's table privileges (the caller has done the large-object privilege
// check PG does).
func internalCatalogQuery(ctx *Context, sql string) ([]Row, error) {
	stmts, err := parser.Parse(sql)
	if err != nil || len(stmts) != 1 {
		return nil, &ExecError{Code: "XX000", Message: fmt.Sprintf("internal catalog query: %v", err)}
	}
	saved := ctx.NonSuperuserRole
	ctx.NonSuperuserRole = ""
	defer func() { ctx.NonSuperuserRole = saved }()
	plan, err := optimizer.Plan(stmts[0], ctxPlanCatalog(ctx))
	if err != nil {
		return nil, err
	}
	op, err := buildStatementScoped(plan)
	if err != nil {
		return nil, err
	}
	if err := op.Open(ctx); err != nil {
		op.Close()
		return nil, err
	}
	rows, err := drainRows(op)
	op.Close()
	return rows, err
}

// loLookup is LargeObjectExists plus the owner and ACL text of the visible
// pg_largeobject_metadata row.
func loLookup(ctx *Context, loid uint32) (owner uint32, acl string, found bool, err error) {
	if _, err := loMetaTable(ctx); err != nil {
		return 0, "", false, err
	}
	rows, err := internalCatalogQuery(ctx, fmt.Sprintf(
		"SELECT lomowner, lomacl::text FROM pg_catalog.pg_largeobject_metadata WHERE oid = %d::oid", loid))
	if err != nil || len(rows) == 0 {
		return 0, "", false, err
	}
	r := rows[0]
	if len(r) > 0 && !r[0].IsNull() {
		owner = uint32(r[0].Int)
	}
	if len(r) > 1 && !r[1].IsNull() {
		acl = r[1].StringValue()
	}
	return owner, acl, true, nil
}

// loCompatPrivileges reads the lo_compat_privileges GUC.
func loCompatPrivileges(ctx *Context) bool {
	if ctx.GetSetting == nil {
		return false
	}
	v, ok := ctx.GetSetting("lo_compat_privileges")
	return ok && (v == "on" || v == "true")
}

// loPreventReadOnly is PreventCommandIfReadOnly for the large-object writers.
func loPreventReadOnly(ctx *Context, fn string) error {
	if ctx.Session != nil && ctx.Session.IsReadOnlyTxn() {
		return &ExecError{Code: "25006", Message: fmt.Sprintf("cannot execute %s in a read-only transaction", fn)}
	}
	return nil
}

// evalLoCreate is be_lo_create / be_lo_creat → inv_create →
// LargeObjectCreate: a zero OID gets a fresh one (GetNewOidWithIndex); an OID
// already in use violates pg_largeobject_metadata_oid_index (23505). The
// owner is the current user; lomacl starts as the user's default ACL for
// large objects (get_user_default_acl), which is NULL until ALTER DEFAULT
// PRIVILEGES ... ON LARGE OBJECTS is modelled.
func evalLoCreate(fn string, args []Datum, ctx *Context) (Datum, error) {
	if len(args) == 1 && args[0].IsNull() {
		return NullDatum, nil
	}
	if err := loPreventReadOnly(ctx, fn+"()"); err != nil {
		return Datum{}, err
	}
	tbl, err := loMetaTable(ctx)
	if err != nil {
		return Datum{}, err
	}
	im, err := privilegeCatalog(fn, ctx, 0)
	if err != nil {
		return Datum{}, err
	}
	var loid uint32
	if fn == "lo_create" && len(args) == 1 {
		loid = uint32(args[0].Int)
	}
	if loid == 0 {
		for {
			loid = im.AllocOID()
			if loid < 16384 { // FirstNormalObjectId
				continue
			}
			_, _, found, lerr := loLookup(ctx, loid)
			if lerr != nil {
				return Datum{}, lerr
			}
			if !found {
				break
			}
		}
	} else if _, _, found, lerr := loLookup(ctx, loid); lerr != nil {
		return Datum{}, lerr
	} else if found {
		return Datum{}, &ExecError{Code: "23505",
			Message: "duplicate key value violates unique constraint \"pg_largeobject_metadata_oid_index\"",
			Detail:  fmt.Sprintf("Key (oid)=(%d) already exists.", loid)}
	}
	row := Row{NewIntDatum(int64(loid)), NewIntDatum(int64(currentRoleOID(im, ctx))), NullDatum}
	if _, err := writeHeapRowCanonical(ctx, ctx.Catalog.RelFileNode(tbl), tbl.Columns, row); err != nil {
		return Datum{}, err
	}
	return NewIntDatum(int64(loid)), nil
}

// evalLoUnlink is be_lo_unlink: 42704 for a missing object, 42501 unless the
// current user has the owner's privileges (or lo_compat_privileges), then
// inv_drop deletes the metadata row (and, once data pages exist, the
// pg_largeobject pages). Returns 1.
func evalLoUnlink(args []Datum, ctx *Context) (Datum, error) {
	if len(args) != 1 || args[0].IsNull() {
		return NullDatum, nil
	}
	if err := loPreventReadOnly(ctx, "lo_unlink()"); err != nil {
		return Datum{}, err
	}
	im, err := privilegeCatalog("lo_unlink", ctx, 0)
	if err != nil {
		return Datum{}, err
	}
	loid := uint32(args[0].Int)
	owner, _, found, err := loLookup(ctx, loid)
	if err != nil {
		return Datum{}, err
	}
	if !found {
		return Datum{}, &ExecError{Code: "42704", Message: fmt.Sprintf("large object %d does not exist", loid)}
	}
	role := currentRoleOID(im, ctx)
	if !loCompatPrivileges(ctx) && !im.IsSuperuser(role) && role != owner && !im.HasPrivsOfRole(role, owner) {
		return Datum{}, &ExecError{Code: "42501", Message: fmt.Sprintf("must be owner of large object %d", loid)}
	}
	// Close this session's descriptors on it, then inv_drop: the metadata
	// row and every data page.
	loCloseObjectFDs(ctx, loid)
	if _, err := internalCatalogQuery(ctx, fmt.Sprintf(
		"DELETE FROM pg_catalog.pg_largeobject_metadata WHERE oid = %d::oid", loid)); err != nil {
		return Datum{}, err
	}
	if _, err := loDataTable(ctx); err == nil {
		if err := loDeletePages(ctx, loid, 0, -1); err != nil {
			return Datum{}, err
		}
	}
	// inv_drop's performDeletion also removes the object's comment.
	im.SetComment(largeObjectRelationOID, loid, 0, "")
	return NewIntDatum(1), nil
}

// largeObjectPrivNames is convert_largeobject_priv_string's table.
var largeObjectPrivNames = map[string]byte{"SELECT": 'r', "UPDATE": 'w'}

// evalHasLargeObjectPrivilege is has_largeobject_privilege's three
// overloads (has_lo_priv_byid): a missing large object is NULL — for a
// superuser too, since LargeObjectExistsWithSnapshot runs first —
// lo_compat_privileges answers true, and otherwise
// pg_largeobject_aclmask_snapshot: superuser, owner, then lomacl with
// acldefault('L', owner) = owner rw and nothing for PUBLIC.
func evalHasLargeObjectPrivilege(args []Datum, ctx *Context, pos int) (Datum, error) {
	if anyNull(args) {
		return NullDatum, nil
	}
	im, err := privilegeCatalog("has_largeobject_privilege", ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	role, objArg, privs, err := privilegeArgs("has_largeobject_privilege", args, im, ctx, pos)
	if err != nil {
		return Datum{}, err
	}
	want, err := parsePrivList(privs, largeObjectPrivNames)
	if err != nil {
		return Datum{}, err
	}
	owner, acl, found, err := loLookup(ctx, uint32(objArg.Int))
	if err != nil {
		return Datum{}, err
	}
	if !found {
		return NullDatum, nil
	}
	if loCompatPrivileges(ctx) {
		return NewBoolDatum(true), nil
	}
	obj := aclObject{owner: owner, aclText: acl, letters: "rw", defaultOwner: "rw"}
	return NewBoolDatum(aclAnyHeld(im, role, obj, want)), nil
}
