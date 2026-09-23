package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// databaseACLAllPrivs is the expansion of GRANT ALL [PRIVILEGES] ON DATABASE:
// the full ACL_ALL_RIGHTS_DATABASE set (acl.h), in PostgreSQL's canonical
// aclitemout letter order. Also the owner's implicit acldefault('d', owner)
// set, used to seed a materialized owner entry on the first owner-side
// REVOKE (mirrors typeACLAllPrivs / execTypeACLChange). M0119-0004-ACLHEAP
// (datacl half).
var databaseACLAllPrivs = []string{"CREATE", "TEMPORARY", "CONNECT"}

// databaseACLPublicDefaultPrivs is the world_default half of
// acldefault('d', owner) (acl.c): PUBLIC gets CREATE_TEMP + CONNECT but NOT
// CREATE — the one DATABASE-specific asymmetry vs TYPE/FUNCTION's uniform
// owner==PUBLIC default (typeACLAllPrivs seeds the identical set for both
// owner and PUBLIC there). M0119-0004-ACLHEAP (datacl half).
var databaseACLPublicDefaultPrivs = []string{"TEMPORARY", "CONNECT"}

// normalizeDatabasePriv maps a parsed GRANT/REVOKE ON DATABASE privilege
// keyword to its canonical form(s), expanding ALL/ALL PRIVILEGES to the full
// set and folding the TEMP alias to TEMPORARY (gram.y's privilege_target
// accepts either spelling). An unrecognised keyword yields nil (a no-op
// grant), mirroring expandColumnPrivs. M0119-0004-ACLHEAP (datacl half).
func normalizeDatabasePriv(priv string) []string {
	switch strings.ToUpper(strings.TrimSpace(priv)) {
	case "ALL", "ALL PRIVILEGES":
		return databaseACLAllPrivs
	case "CREATE":
		return []string{"CREATE"}
	case "TEMP", "TEMPORARY":
		return []string{"TEMPORARY"}
	case "CONNECT":
		return []string{"CONNECT"}
	default:
		return nil
	}
}

// execDatabaseACLChange applies a GRANT/REVOKE … ON DATABASE … to the
// OID-keyed ACL store and re-syncs the heap-backed pg_database.datacl row.
//
// Every named database is resolved through catalog.ResolveDatabaseOid, NOT
// just the connected one (M0122-0008). pg_database is a SHARED catalog, and
// upstream's ExecGrant_Database (`postgres/src/backend/catalog/aclchk.c`)
// looks each name up in it with no reference to MyDatabaseId — `GRANT CONNECT
// ON DATABASE otherdb TO r` from a session connected to `postgres` is
// ordinary, supported PostgreSQL. goopg used to compare each name against the
// live `CurrentDatabase` and return silently when it did not match, so the
// statement reported GRANT and changed nothing; measured against PG 18.3, the
// row that should have gained `r1=c/postgres` kept a NULL datacl.
//
// ResolveDatabaseOid is the correct key precisely because it returns DBOID()
// for "postgres" — the key the previous single-database code used — so the
// connected-database path is unchanged by construction rather than by
// coincidence. An unknown name now raises upstream's 3D000 instead of being
// ignored (`aclchk.c` → get_database_oid(..., false)).
//
// The grantor stamped on each grant is the
// session's current effective role (o.ctx.NonSuperuserRole, empty meaning the
// bootstrap superuser), mirroring tryRecordTableGrant's/execTypeACLChange's
// grantor attribution — datacl shares the same tableACLs/tableACLGrantor
// store via relaclTextLockedFor, so no separate grantor map was needed here.
// M0119-0004-ACLHEAP (datacl half).
func (o *ddlOp) execDatabaseACLChange(dc *parser.DatabaseACLChange) error {
	if err := checkGrantedByCurrentUser(o.ctx.NonSuperuserRole, dc.GrantedBy); err != nil {
		return err
	}
	if err := checkGrantOptionToPublic(dc.Revoke, dc.WithGrantOption, dc.Grantees); err != nil {
		return err
	}
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	// Resolve every named database FIRST, so a statement naming one good and
	// one bad name changes nothing before it errors — upstream resolves the
	// whole list in ExecGrant_Database's objects loop under one transaction.
	oids := make([]uint32, 0, len(dc.DatabaseNames))
	for _, name := range dc.DatabaseNames {
		bare := strings.Trim(name, `"`)
		oid, found := im.ResolveDatabaseOid(bare)
		if !found || oid == 0 {
			return &ExecError{
				Code:    "3D000",
				Message: fmt.Sprintf("database %q does not exist", bare),
			}
		}
		oids = append(oids, oid)
	}
	if len(oids) == 0 {
		return nil
	}
	var privs []string
	for _, p := range dc.Privileges {
		privs = append(privs, normalizeDatabasePriv(p)...)
	}
	if len(privs) == 0 {
		return nil
	}
	for _, dbOid := range oids {
		if err := o.applyDatabaseACLChange(im, dc, dbOid, privs); err != nil {
			return err
		}
	}
	return nil
}

// applyDatabaseACLChange applies one resolved database's half of a
// GRANT/REVOKE … ON DATABASE. Split out of execDatabaseACLChange so the
// per-database body is identical for the connected database and for any other
// one — the single-database version of this code lived inline, which is how
// "only the live database" became a property of the statement rather than of
// the loop that drives it.
func (o *ddlOp) applyDatabaseACLChange(im *catalog.InMemory, dc *parser.DatabaseACLChange, dbOid uint32, privs []string) error {
	if dc.Revoke {
		// Seed both implicit acldefault('d', …) halves while datacl is still
		// NULL so an owner-side REVOKE leaves the surviving privileges
		// explicit (mirrors execTypeACLChange's REVOKE branch).
		if im.DatabaseACLText(dbOid) == "" {
			im.MaterializeOwnerACL(dbOid, "postgres", databaseACLAllPrivs)
			for _, p := range databaseACLPublicDefaultPrivs {
				im.GrantTablePrivilege(dbOid, "PUBLIC", p)
			}
		}
		for _, role := range dc.Grantees {
			for _, p := range privs {
				im.RevokeTablePrivilege(dbOid, role, p)
			}
		}
	} else {
		// Seed the implicit PUBLIC TEMPORARY+CONNECT that acldefault('d', …)
		// carries so the materialized datacl matches PG's array; the owner
		// entry is supplied by the renderer's owner branch (DatabaseACLText /
		// relaclTextLockedFor).
		for _, p := range databaseACLPublicDefaultPrivs {
			im.GrantTablePrivilege(dbOid, "PUBLIC", p)
		}
		for _, role := range dc.Grantees {
			for _, p := range privs {
				im.GrantTablePrivilegeAs(dbOid, role, p, dc.WithGrantOption, o.ctx.NonSuperuserRole)
			}
		}
	}
	return o.resyncDatabaseACLHeapRow(im, dbOid)
}

// resyncDatabaseACLHeapRow rewrites the heap-backed pg_database row for dbOid
// so its datacl column carries the projected ACL as a PG-native _aclitem
// ArrayType (read back by goopg's own SELECT path via seqScanOp /
// renderHeapACLColumnInto, and by an attached PG18 standby / basebackup).
// pg_database is one of PostgreSQL's SHARED (cluster-wide) catalogs — a
// single relfilenode under global/, not duplicated per connected database —
// so unlike resyncTypeACLHeapRow there is no per-DB mirror step. Uses the
// same delete-old(xmax stamp)+insert-new MVCC row-version pattern as
// resyncTypeACLHeapRow — NOT persistDatFrozenXID's in-place overwrite, which
// intentionally violates MVCC to mirror PG's own non-transactional
// vac_update_datfrozenxid; a GRANT is an ordinary transactional DDL
// statement and must produce a fresh row version like any other UPDATE. A
// no-op when the catalog heap relfiles are absent (in-memory test fixture).
// M0119-0004-ACLHEAP (datacl half).
func (o *ddlOp) resyncDatabaseACLHeapRow(im *catalog.InMemory, dbOid uint32) error {
	ctx := o.ctx
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	rel := catalog.SharedCatalogRelFileNode(catalog.PgDatabaseRelationOID)
	cols := catalog.PgDatabaseColumnsPG18()
	row, found, err := readLiveDatabaseRow(ctx, rel, cols, dbOid)
	if err != nil || !found {
		return err
	}
	// Fill datacl (the final pg_database column) from the projected ACL text.
	// PG keeps datacl NULL until the array is non-empty; an owner-side
	// REVOKE ALL yields the non-NULL empty array "{}" (encoded as an empty
	// _aclitem).
	aclText := im.DatabaseACLText(dbOid)
	daclIdx := len(row) - 1
	if aclText == "" {
		row[daclIdx] = NullDatum
	} else {
		blob, encErr := encodeAclItemArrayText(aclText, func(roleName string) uint32 {
			if roid, ok := im.RoleOID(roleName); ok {
				return roid
			}
			return 0
		})
		if encErr != nil {
			return encErr
		}
		row[daclIdx] = NewBytesDatum(blob)
	}
	// Stamp the stale row's xmax with this txn's writer XID, then append the
	// rebuilt row as a fresh MVCC version.
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	deleteDatabaseFromCatalogHeap(ctx, dbOid, ctx.Tx.XID)
	if _, err := writeHeapRowCanonical(ctx, rel, cols, row); err != nil {
		return err
	}
	return nil
}

// deleteDatabaseFromCatalogHeap stamps xmax on the live pg_database tuple
// whose oid column matches dbOid, mirroring deleteTypeFromCatalogHeap. It
// reuses pgDatabaseTupleOID (operators_vacuum_datfrozenxid.go) to read the
// oid column (ordinal 0, offset 0) directly from raw physical tuple data.
// M0119-0004-ACLHEAP (datacl half).
func deleteDatabaseFromCatalogHeap(ctx *Context, dbOid uint32, xmax storage.TransactionID) {
	rel := catalog.SharedCatalogRelFileNode(catalog.PgDatabaseRelationOID)
	stampCatalogRows(ctx, rel, xmax, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return pgDatabaseTupleOID(data) == dbOid
	})
}

// readLiveDatabaseRow scans the pg_database heap for the live tuple whose oid
// column matches dbOid and decodes it into a Row using cols, mirroring
// updatePgDatabaseTupleOnPage's page-scan/liveness test
// (operators_vacuum_datfrozenxid.go). Returns found=false if no live row
// exists (defensive; v0's bootstrap always has template0/template1/postgres).
// M0119-0004-ACLHEAP (datacl half).
func readLiveDatabaseRow(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, dbOid uint32) (Row, bool, error) {
	nblocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return nil, false, err
	}
	for blk := storage.BlockNumber(0); blk < nblocks; blk++ {
		slot, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if perr != nil {
			return nil, false, perr
		}
		row, found, rerr := readDatabaseRowOnPage(slot, cols, dbOid)
		ctx.Pool.Unpin(slot)
		if rerr != nil {
			return nil, false, rerr
		}
		if found {
			return row, true, nil
		}
	}
	return nil, false, nil
}

// readDatabaseRowOnPage scans one pg_database page under a shared content
// lock for the live tuple whose oid matches dbOid and decodes it. Companion
// read-only half of updatePgDatabaseTupleOnPage's scan loop.
// M0119-0004-ACLHEAP (datacl half).
func readDatabaseRowOnPage(slot *storage.Slot, cols []catalog.Column, dbOid uint32) (Row, bool, error) {
	slot.RLock()
	defer slot.RUnlock()
	page := slot.Page()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		return nil, false, err
	}
	for s := uint16(1); s <= uint16(count); s++ {
		tup, terr := storage.PageGetHeapTuple(page, s)
		if terr != nil {
			continue
		}
		if tup.Header.Xmin == storage.InvalidTransactionID || tup.Header.Xmax != storage.InvalidTransactionID {
			continue
		}
		if len(tup.Data) < 4 || pgDatabaseTupleOID(tup.Data) != dbOid {
			continue
		}
		natts := int(tup.Header.Infomask2 & 0x07FF)
		row := make(Row, len(cols))
		if derr := DecodeRowIntoMctxPGTuple(row, cols, tup.Data, tup.Bitmap, natts, nil); derr != nil {
			return nil, false, derr
		}
		return row, true, nil
	}
	return nil, false, nil
}
