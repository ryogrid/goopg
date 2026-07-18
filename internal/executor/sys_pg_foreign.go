package executor

// B3.4 (docs/design/wal-pg-identical-stream/02d §2): CREATE/DROP FOREIGN DATA
// WRAPPER / SERVER / USER MAPPING journal as real pg_foreign_data_wrapper /
// pg_foreign_server / pg_user_mapping heap rows, replacing the bespoke
// RecordKindCreateForeignServer(126)/DropForeignServer(127)/
// CreateUserMapping(128)/DropUserMapping(129). The FDW itself had NO
// durability record before this slice — it gains one here (a real heap row +
// reload), which the server's srvfdw OID column requires.
//
// Cross-references resolve name→OID at write time and reverse OID→name on
// reload: pg_foreign_server.srvfdw = the FDW's OID; pg_user_mapping.umserver
// = the server's OID, umuser = the role's OID (0 for PUBLIC). OPTIONS are
// text[] arrays (the evttags codec pattern). These are per-database catalogs
// (base/<db>), NOT shared. Column layouts:
// postgres/src/include/catalog/pg_foreign_data_wrapper.h, pg_foreign_server.h,
// pg_user_mapping.h.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const (
	pgFdwRelOID             = 2328
	pgFdwOidIndexOID        = 112
	pgFdwNameIndexOID       = 548
	pgForeignServerRelOID   = 1417
	pgForeignServerOidIdxID = 113
	pgForeignServerNameIdx  = 549
	pgUserMappingRelOID     = 1418
	pgUserMappingOidIdxID   = 174
	pgUserMappingUserSrvIdx = 175
)

// PGForeignDataWrapperColumnsPG18 mirrors FormData_pg_foreign_data_wrapper
// (7 columns). Exported for the initdb reload.
func PGForeignDataWrapperColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "fdwname", Type: catalog.Type{Name: "name"}},
		{Name: "fdwowner", Type: catalog.Type{Name: "oid"}},
		{Name: "fdwhandler", Type: catalog.Type{Name: "regproc"}},
		{Name: "fdwvalidator", Type: catalog.Type{Name: "regproc"}},
		{Name: "fdwacl", Type: catalog.Type{Name: "aclitem", IsArray: true}},
		{Name: "fdwoptions", Type: catalog.Type{Name: "text", IsArray: true}},
	}
}

// PGForeignServerColumnsPG18 mirrors FormData_pg_foreign_server (8 columns).
func PGForeignServerColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "srvname", Type: catalog.Type{Name: "name"}},
		{Name: "srvowner", Type: catalog.Type{Name: "oid"}},
		{Name: "srvfdw", Type: catalog.Type{Name: "oid"}},
		{Name: "srvtype", Type: catalog.Type{Name: "text"}},
		{Name: "srvversion", Type: catalog.Type{Name: "text"}},
		{Name: "srvacl", Type: catalog.Type{Name: "aclitem", IsArray: true}},
		{Name: "srvoptions", Type: catalog.Type{Name: "text", IsArray: true}},
	}
}

// PGUserMappingColumnsPG18 mirrors FormData_pg_user_mapping (4 columns).
func PGUserMappingColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "umuser", Type: catalog.Type{Name: "oid"}},
		{Name: "umserver", Type: catalog.Type{Name: "oid"}},
		{Name: "umoptions", Type: catalog.Type{Name: "text", IsArray: true}},
	}
}

func optionsDatum(opts []string) Datum {
	if len(opts) == 0 {
		return NullDatum
	}
	return NewStringDatum(formatTextArray(opts))
}

func sysForeignRel(relOID uint32) storage.RelFileNode {
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: relOID, Fork: storage.MainFork}
}

// writeFdwCatalogRow journals CREATE FOREIGN DATA WRAPPER: a heap INSERT + a
// pre-INSERT xmax stamp of any prior version (RegisterForeignDataWrapper is
// create-or-fetch on a stable OID — a re-CREATE keeps the OID; no TID cache
// for this low-traffic catalog, so delete+insert keeps one live row).
func writeFdwCatalogRow(ctx *Context, fdw *catalog.ForeignDataWrapper) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampByOID(ctx, sysForeignRel(pgFdwRelOID), fdw.OID)
	owner := fdw.Owner
	if owner == 0 {
		owner = 10
	}
	row := Row{
		NewIntDatum(int64(fdw.OID)),
		NewStringDatum(fdw.Name),
		NewIntDatum(int64(owner)),
		NewIntDatum(int64(fdw.HandlerOID)),
		NewIntDatum(int64(fdw.ValidatorOID)),
		NullDatum, // fdwacl
		optionsDatum(fdw.Options),
	}
	tid, err := writeHeapRowCanonical(ctx, sysForeignRel(pgFdwRelOID), PGForeignDataWrapperColumnsPG18(), row)
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgFdwOidIndexOID, buildIndexTupleOidKey(blk, off, fdw.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgFdwNameIndexOID, buildIndexTupleNameKey(blk, off, fdw.Name), cmpKeyName); err != nil {
		return err
	}
	mirrorForeignCatalogFiles(ctx)
	return nil
}

// writeForeignServerCatalogRow journals CREATE SERVER: a heap INSERT with
// srvfdw resolved to the FDW's OID, plus the FDW's own row (CREATE SERVER
// implies the FDW exists but goopg never wrote its row before this slice —
// ensure it is present so srvfdw resolves on a standby / reload).
func writeForeignServerCatalogRow(ctx *Context, srv *catalog.ForeignServer) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	if fdw, found := im.LookupForeignDataWrapper(srv.FdwName); found && fdw != nil {
		if werr := writeFdwCatalogRow(ctx, fdw); werr != nil {
			return werr
		}
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampByOID(ctx, sysForeignRel(pgForeignServerRelOID), srv.OID)
	owner := srv.Owner
	if owner == 0 {
		owner = 10
	}
	row := Row{
		NewIntDatum(int64(srv.OID)),
		NewStringDatum(srv.Name),
		NewIntDatum(int64(owner)),
		NewIntDatum(int64(im.ForeignDataWrapperOID(srv.FdwName))),
		textOrNullDatum(srv.Type),
		textOrNullDatum(srv.Version),
		NullDatum, // srvacl
		optionsDatum(srv.Options),
	}
	tid, err := writeHeapRowCanonical(ctx, sysForeignRel(pgForeignServerRelOID), PGForeignServerColumnsPG18(), row)
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgForeignServerOidIdxID, buildIndexTupleOidKey(blk, off, srv.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgForeignServerNameIdx, buildIndexTupleNameKey(blk, off, srv.Name), cmpKeyName); err != nil {
		return err
	}
	mirrorForeignCatalogFiles(ctx)
	return nil
}

// writeUserMappingCatalogRow journals CREATE USER MAPPING: umuser = the
// role's OID (0 for PUBLIC), umserver = the server's OID.
func writeUserMappingCatalogRow(ctx *Context, um *catalog.UserMapping) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	umuser := uint32(0)
	if um.UmUser != "" && um.UmUser != "public" {
		if roleOID, found := im.RoleOID(um.UmUser); found {
			umuser = roleOID
		}
	}
	srvOID := uint32(0)
	if srv, found := im.LookupForeignServer(um.SrvName, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)); found && srv != nil {
		srvOID = srv.OID
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampByOID(ctx, sysForeignRel(pgUserMappingRelOID), um.OID)
	row := Row{
		NewIntDatum(int64(um.OID)),
		NewIntDatum(int64(umuser)),
		NewIntDatum(int64(srvOID)),
		optionsDatum(um.Options),
	}
	tid, err := writeHeapRowCanonical(ctx, sysForeignRel(pgUserMappingRelOID), PGUserMappingColumnsPG18(), row)
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgUserMappingOidIdxID, buildIndexTupleOidKey(blk, off, um.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgUserMappingUserSrvIdx,
		buildIndexTupleOidOidKey(blk, off, umuser, srvOID), cmpKeyOidOid); err != nil {
		return err
	}
	mirrorForeignCatalogFiles(ctx)
	return nil
}

// deleteForeignRowByOID stamps xmax on the row of rel whose oid column
// matches (DROP SERVER / DROP USER MAPPING).
func deleteForeignRowByOID(ctx *Context, relOID, rowOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampByOID(ctx, sysForeignRel(relOID), rowOID)
	mirrorForeignCatalogFiles(ctx)
}

// stampByOID marks every live row of rel whose oid column (col 0) equals
// rowOID deleted. The caller must have materialized the writer XID.
func stampByOID(ctx *Context, rel storage.RelFileNode, rowOID uint32) {
	stampCatalogRows(ctx, rel, ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == rowOID
	})
}

// textOrNullDatum returns a NULL datum for an empty string, else the string.
func textOrNullDatum(s string) Datum {
	if s == "" {
		return NullDatum
	}
	return NewStringDatum(s)
}

// mirrorForeignCatalogFiles propagates all three heaps + their indexes to the
// postgres DB's copies (reload reads base/5).
func mirrorForeignCatalogFiles(ctx *Context) {
	for _, oid := range []uint32{
		pgFdwRelOID, pgFdwOidIndexOID, pgFdwNameIndexOID,
		pgForeignServerRelOID, pgForeignServerOidIdxID, pgForeignServerNameIdx,
		pgUserMappingRelOID, pgUserMappingOidIdxID, pgUserMappingUserSrvIdx,
	} {
		_ = mirrorCatalogRelToPostgresDB(ctx, oid)
	}
}
