package executor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/adt/aclitem"
)

// GRANT / REVOKE … ON LARGE OBJECT (M0149-0001h1a slice 3): a port of
// aclchk.c's ExecGrant_Largeobject. A large object's ACL lives only in its
// pg_largeobject_metadata row (lomacl) — large-object OIDs are chosen by the
// user and may equal any other object's OID, so goopg's OID-keyed ACL store
// cannot hold them. Each statement:
//
//  1. resolves the objects (42704 "large object %u does not exist"), then the
//     grantees (42704 role), then the privileges (0LP01 "invalid privilege
//     type %s for large object") — ExecuteGrantStmt's order;
//  2. per object, starts from lomacl or acldefault('L', owner) = owner rw;
//  3. select_best_grantor: a role with the owner's privileges grants as the
//     owner; anyone else grants as itself with the grant options it holds;
//  4. restrict_and_check_grant: no grant options and no privilege at all is
//     42501; a partial grant / revoke is a WARNING;
//  5. merge_acl_with_grant / aclupdate per grantee — item key (grantee,
//     grantor), an emptied item is removed, revoking a grant option that was
//     passed on is 2BP01 under RESTRICT and recursive under CASCADE;
//  6. rewrites the row (internal-SQL DELETE + writeHeapRowCanonical, so the
//     change is transactional and index 2996 stays correct).

const (
	loPrivRead   = uint64(1) << 1 // ACL_SELECT ('r')
	loPrivUpdate = uint64(1) << 2 // ACL_UPDATE ('w')
	loPrivAll    = loPrivRead | loPrivUpdate
)

// loACLItem is one aclitem; grantee / grantor 0 = PUBLIC / unset.
type loACLItem struct {
	grantee, grantor uint32
	public           bool
	privs, goptions  uint64
}

// loParseACL parses lomacl text, or builds acldefault('L', owner).
func loParseACL(im *catalog.InMemory, text string, owner uint32) ([]loACLItem, error) {
	if text == "" {
		return []loACLItem{{grantee: owner, grantor: owner, privs: loPrivAll}}, nil
	}
	body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "{"), "}")
	var out []loACLItem
	for _, item := range splitTopLevelAclItems(body) {
		grantee, mode, grantor, err := parseAclItemText(item)
		if err != nil {
			return nil, err
		}
		it := loACLItem{privs: mode & 0xffffffff, goptions: mode >> aclGrantOptionShift}
		if grantee == "" {
			it.public = true
		} else if oid, ok := im.RoleOID(grantee); ok {
			it.grantee = oid
		}
		if oid, ok := im.RoleOID(grantor); ok {
			it.grantor = oid
		}
		out = append(out, it)
	}
	return out, nil
}

// loFormatACL renders items as aclitem[] text (aclitemout).
func loFormatACL(im *catalog.InMemory, items []loACLItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		grantee := ""
		if !it.public {
			grantee = aclPutid(im.RoleNameForOID(it.grantee))
		}
		mode := it.privs | it.goptions<<aclGrantOptionShift
		parts = append(parts, grantee+"="+aclitem.PrivLetters(mode)+"/"+aclPutid(im.RoleNameForOID(it.grantor)))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// loHasPrivsOf is has_privs_of_role, with PUBLIC entries applying to all.
func loHasPrivsOf(im *catalog.InMemory, role uint32, it loACLItem) bool {
	if it.public {
		return true
	}
	return it.grantee == role || im.HasPrivsOfRole(role, it.grantee)
}

// loAclMask is aclmask over items for role: the privileges (and, shifted,
// grant options) role holds directly or through roles it has the
// privileges of.
func loAclMask(im *catalog.InMemory, items []loACLItem, role, owner uint32) (privs, goptions uint64) {
	if role == owner || im.HasPrivsOfRole(role, owner) {
		goptions = loPrivAll
	}
	for _, it := range items {
		if loHasPrivsOf(im, role, it) {
			privs |= it.privs
			goptions |= it.goptions
		}
	}
	return privs, goptions
}

// loSelectBestGrantor is select_best_grantor for large objects.
func loSelectBestGrantor(im *catalog.InMemory, role uint32, need uint64, items []loACLItem, owner uint32) (grantor uint32, avail uint64) {
	if role == owner || im.IsSuperuser(role) || im.HasPrivsOfRole(role, owner) {
		return owner, need
	}
	// The role itself if it holds every needed grant option; otherwise the
	// role (direct holder first) holding the most of them.
	_, own := loAclMask(im, items, role, owner)
	return role, own & need
}

// loACLUpdate is aclupdate for one grantee: add or remove privs / goptions
// on the (grantee, grantor) item. Revoking grant options that were passed
// on recurses (CASCADE) or fails (RESTRICT).
func loACLUpdate(im *catalog.InMemory, items []loACLItem, mod loACLItem, add, cascade bool, loname string) ([]loACLItem, error) {
	idx := -1
	for i, it := range items {
		if it.public == mod.public && it.grantee == mod.grantee && it.grantor == mod.grantor {
			idx = i
			break
		}
	}
	if add {
		if idx < 0 {
			return append(items, mod), nil
		}
		items[idx].privs |= mod.privs
		items[idx].goptions |= mod.goptions
		return items, nil
	}
	if idx < 0 {
		return items, nil
	}
	oldGopts := items[idx].goptions
	items[idx].privs &^= mod.privs
	items[idx].goptions &^= mod.goptions
	lost := oldGopts &^ items[idx].goptions
	if items[idx].privs == 0 && items[idx].goptions == 0 {
		items = append(items[:idx], items[idx+1:]...)
	}
	if lost == 0 || mod.public {
		return items, nil
	}
	// Privileges the grantee passed on with the option it just lost.
	for i := 0; i < len(items); i++ {
		it := items[i]
		if it.public || it.grantor != mod.grantee || (it.privs|it.goptions)&lost == 0 {
			continue
		}
		if !cascade {
			return nil, &ExecError{Code: "2BP01", Message: "dependent privileges exist",
				Hint: "Use CASCADE to revoke them too."}
		}
		var err error
		items, err = loACLUpdate(im, items, loACLItem{grantee: it.grantee, public: it.public, grantor: it.grantor,
			privs: lost, goptions: lost}, false, true, loname)
		if err != nil {
			return nil, err
		}
		i = -1 // the slice changed; rescan
	}
	return items, nil
}

// execLargeObjectACLChange runs GRANT / REVOKE … ON LARGE OBJECT.
func (o *ddlOp) execLargeObjectACLChange(lc *parser.LargeObjectACLChange) error {
	if err := checkGrantedByCurrentUser(o.ctx.NonSuperuserRole, lc.GrantedBy); err != nil {
		return err
	}
	ctx := o.ctx
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	tbl, err := loMetaTable(ctx)
	if err != nil {
		return err
	}
	// 1. Objects.
	type loObj struct {
		oid   uint32
		owner uint32
		acl   string
	}
	var objs []loObj
	for _, s := range lc.Objects {
		v, perr := strconv.ParseInt(s, 10, 64)
		if perr != nil || v < 0 || v > 0xffffffff {
			return &ExecError{Code: "22P02", Message: fmt.Sprintf("invalid input syntax for type oid: \"%s\"", s)}
		}
		owner, acl, found, lerr := loLookup(ctx, uint32(v))
		if lerr != nil {
			return lerr
		}
		if !found {
			return &ExecError{Code: "42704", Message: fmt.Sprintf("large object %d does not exist", v)}
		}
		objs = append(objs, loObj{uint32(v), owner, acl})
	}
	// 2. Grantees.
	var grantees []loACLItem
	for _, g := range lc.Grantees {
		if strings.EqualFold(g, "public") {
			grantees = append(grantees, loACLItem{public: true})
			continue
		}
		oid, ok := im.RoleOID(g)
		if !ok {
			return &ExecError{Code: "42704", Message: fmt.Sprintf("role \"%s\" does not exist", g)}
		}
		grantees = append(grantees, loACLItem{grantee: oid})
	}
	// 3. Privileges.
	var privs uint64
	allPrivs := false
	for _, p := range lc.Privileges {
		switch p {
		case "ALL", "ALL PRIVILEGES":
			privs, allPrivs = loPrivAll, true
		case "SELECT":
			privs |= loPrivRead
		case "UPDATE":
			privs |= loPrivUpdate
		default:
			return &ExecError{Code: "0LP01", Message: fmt.Sprintf("invalid privilege type %s for large object", p)}
		}
	}
	if !lc.Revoke && lc.WithGrantOption {
		for _, g := range grantees {
			if g.public {
				return &ExecError{Code: "0LP01", Message: "grant options can only be granted to roles"}
			}
		}
	}
	role := currentRoleOID(im, ctx)
	for _, ob := range objs {
		items, err := loParseACL(im, ob.acl, ob.owner)
		if err != nil {
			return err
		}
		loname := fmt.Sprintf("large object %d", ob.oid)
		grantor, avail := loSelectBestGrantor(im, role, privs, items, ob.owner)
		// restrict_and_check_grant.
		if avail == 0 {
			p, g := loAclMask(im, items, grantor, ob.owner)
			if p == 0 && g == 0 {
				// aclcheck_error formats "permission denied for large object
				// %s" with ExecGrant_Largeobject's objname "large object %u",
				// so PG really prints the phrase twice.
				return &ExecError{Code: "42501", Message: "permission denied for large object " + loname}
			}
		}
		this := privs & avail
		if !lc.Revoke {
			if this == 0 {
				ctx.AddWarningWithHint("01007", fmt.Sprintf("no privileges were granted for \"%s\"", loname), "")
			} else if !allPrivs && this != privs {
				ctx.AddWarningWithHint("01007", fmt.Sprintf("not all privileges were granted for \"%s\"", loname), "")
			}
		} else {
			if this == 0 {
				ctx.AddWarningWithHint("01006", fmt.Sprintf("no privileges could be revoked for \"%s\"", loname), "")
			} else if !allPrivs && this != privs {
				ctx.AddWarningWithHint("01006", fmt.Sprintf("not all privileges could be revoked for \"%s\"", loname), "")
			}
		}
		// merge_acl_with_grant.
		for _, g := range grantees {
			mod := g
			mod.grantor = grantor
			if !lc.Revoke || !lc.GrantOptionFor {
				mod.privs = this
			}
			if lc.Revoke || lc.WithGrantOption {
				mod.goptions = this
			}
			items, err = loACLUpdate(im, items, mod, !lc.Revoke, lc.Cascade, loname)
			if err != nil {
				return err
			}
		}
		if err := loRewriteMetadata(ctx, tbl, ob.oid, ob.owner, loFormatACL(im, items)); err != nil {
			return err
		}
	}
	return nil
}

// loRewriteMetadata replaces loid's pg_largeobject_metadata row
// (CatalogTupleUpdate): the old version is deleted through internal SQL and
// the new one written with writeHeapRowCanonical, so the change follows the
// transaction and pg_largeobject_metadata_oid_index points at it.
func loRewriteMetadata(ctx *Context, tbl *catalog.Table, loid, owner uint32, aclText string) error {
	if _, err := internalCatalogQuery(ctx, fmt.Sprintf(
		"DELETE FROM pg_catalog.pg_largeobject_metadata WHERE oid = %d::oid", loid)); err != nil {
		return err
	}
	acl, err := loACLDatum(ctx, aclText)
	if err != nil {
		return err
	}
	row := Row{NewIntDatum(int64(loid)), NewIntDatum(int64(owner)), acl}
	_, err = writeHeapRowCanonical(ctx, ctx.Catalog.RelFileNode(tbl), tbl.Columns, row)
	return err
}

// loACLDatum is the lomacl cell for aclText ("" = NULL): a PG-native
// aclitem[] (element type aclitem, 1033) — the string form would be stored
// with the wrong element type, which PG's check_acl rejects ("ACL array
// contains wrong data type").
func loACLDatum(ctx *Context, aclText string) (Datum, error) {
	if aclText == "" {
		return NullDatum, nil
	}
	im, _ := ctx.Catalog.(*catalog.InMemory)
	blob, err := encodeAclItemArrayText(aclText, func(roleName string) uint32 {
		if im != nil {
			if oid, ok := im.RoleOID(roleName); ok {
				return oid
			}
		}
		return 0
	})
	if err != nil {
		return Datum{}, err
	}
	return NewBytesDatum(blob), nil
}

// loDefaultACLText is get_user_default_acl(OBJECT_LARGEOBJECT, owner,
// InvalidOid) (aclchk.c): the owner's global pg_default_acl entry for
// LARGE OBJECTS, or "" (NULL lomacl) when there is none or it equals
// acldefault('L', owner). goopg renders a global entry's implicit owner
// item and grantors as "postgres" (DefaultACLText); for another owner those
// are re-pointed at the owner, as SetDefaultACL would have recorded them.
func loDefaultACLText(im *catalog.InMemory, owner uint32) (string, error) {
	if !im.HasDefaultACL(owner, 0, catalog.DefaultACLObjTypeLargeObject) {
		return "", nil
	}
	text := im.DefaultACLText(im.DefaultACLOID(owner, 0, catalog.DefaultACLObjTypeLargeObject), catalog.DefaultACLObjTypeLargeObject)
	if text == "" {
		return "", nil
	}
	items, err := loParseACL(im, text, owner)
	if err != nil {
		return "", err
	}
	if owner != 10 {
		for i := range items {
			if items[i].grantor == 10 {
				items[i].grantor = owner
			}
			if !items[i].public && items[i].grantee == 10 && i == 0 {
				items[i].grantee = owner
			}
		}
	}
	if len(items) == 1 && !items[0].public && items[0].grantee == owner && items[0].grantor == owner &&
		items[0].privs == loPrivAll && items[0].goptions == 0 {
		return "", nil // equal to acldefault: the hardwired default (NULL)
	}
	return loFormatACL(im, items), nil
}

// execAlterLargeObjectOwner is AlterObjectOwner_internal for a large object:
// 42704 for a missing object or role; a non-superuser must have the old
// owner's privileges ("must be owner of large object %u") and be able to SET
// ROLE to the new owner; lomacl is carried over by aclnewowner (every
// grantee / grantor entry of the old owner becomes the new owner, merging
// any entries that then coincide). M0149-0001h1a.
func (o *ddlOp) execAlterLargeObjectOwner(s *parser.AlterLargeObjectOwnerStmt) error {
	ctx := o.ctx
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	tbl, err := loMetaTable(ctx)
	if err != nil {
		return err
	}
	if s.OID < 0 || s.OID > 0xffffffff {
		return &ExecError{Code: "42704", Message: fmt.Sprintf("large object %d does not exist", s.OID)}
	}
	loid := uint32(s.OID)
	owner, acl, found, err := loLookup(ctx, loid)
	if err != nil {
		return err
	}
	if !found {
		return &ExecError{Code: "42704", Message: fmt.Sprintf("large object %d does not exist", loid)}
	}
	name := s.NewOwner
	if name == "current_user" {
		name = ctx.EffectiveUserName()
	}
	newOwner, ok := im.RoleOID(name)
	if !ok {
		return &ExecError{Code: "42704", Message: fmt.Sprintf("role \"%s\" does not exist", name)}
	}
	if newOwner == owner {
		return nil
	}
	role := currentRoleOID(im, ctx)
	if !im.IsSuperuser(role) {
		if role != owner && !im.HasPrivsOfRole(role, owner) {
			return &ExecError{Code: "42501", Message: fmt.Sprintf("must be owner of large object %d", loid)}
		}
		if role != newOwner && !im.HasPrivsOfRole(role, newOwner) {
			return &ExecError{Code: "42501", Message: fmt.Sprintf("must be able to SET ROLE \"%s\"", im.RoleNameForOID(newOwner))}
		}
	}
	newACL := ""
	if acl != "" {
		items, err := loParseACL(im, acl, owner)
		if err != nil {
			return err
		}
		var merged []loACLItem
		for _, it := range items {
			if !it.public && it.grantee == owner {
				it.grantee = newOwner
			}
			if it.grantor == owner {
				it.grantor = newOwner
			}
			dup := false
			for i := range merged {
				if merged[i].public == it.public && merged[i].grantee == it.grantee && merged[i].grantor == it.grantor {
					merged[i].privs |= it.privs
					merged[i].goptions |= it.goptions
					dup = true
					break
				}
			}
			if !dup {
				merged = append(merged, it)
			}
		}
		newACL = loFormatACL(im, merged)
	}
	return loRewriteMetadata(ctx, tbl, loid, newOwner, newACL)
}
