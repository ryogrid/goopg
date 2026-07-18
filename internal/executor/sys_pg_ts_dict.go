package executor

// B3.5 (docs/design/wal-pg-identical-stream/02d §2): CREATE/ALTER/DROP TEXT
// SEARCH DICTIONARY journal as real pg_ts_dict heap rows, replacing the
// bespoke RecordKindCreateTSDict(104)/DropTSDict(105)/RenameTSDict(114)/
// SetTSDictSchema(115)/AlterTSDictOptions(116). FormData_pg_ts_dict
// (postgres/src/include/catalog/pg_ts_dict.h) maps 1:1 onto UserTSDict — the
// registry already stores dicttemplate as an OID and dictinitoption as the
// serialized text, so the row is fully physical with no cross-resolution.
// ALTER RENAME/SET SCHEMA/ALTER OPTIONS ride a canonical non-HOT heap UPDATE
// at a TID cache keyed by the dictionary OID. pg_ts_config + config_map
// (kinds 106-113) convert in B3.6.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const (
	pgTSDictRelOID          = 3600
	pgTSDictNameNspIndexOID = 3604
	pgTSDictOidIndexOID     = 3605
)

// PGTSDictColumnsPG18 mirrors FormData_pg_ts_dict (6 columns). Exported for
// the initdb reload.
func PGTSDictColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "dictname", Type: catalog.Type{Name: "name"}},
		{Name: "dictnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "dictowner", Type: catalog.Type{Name: "oid"}},
		{Name: "dicttemplate", Type: catalog.Type{Name: "oid"}},
		{Name: "dictinitoption", Type: catalog.Type{Name: "text"}},
	}
}

func buildPGTSDictRow(ud *catalog.UserTSDict) Row {
	owner := ud.Owner
	if owner == 0 {
		owner = 10
	}
	init := NullDatum
	if ud.InitOption != "" {
		init = NewStringDatum(ud.InitOption)
	}
	return Row{
		NewIntDatum(int64(ud.OID)),
		NewStringDatum(ud.Name),
		NewIntDatum(int64(ud.NamespaceOID)),
		NewIntDatum(int64(owner)),
		NewIntDatum(int64(ud.Template)),
		init,
	}
}

func pgTSDictRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgTSDictRelOID, Fork: storage.MainFork}
}

// upsertTSDictCatalogRow journals one dictionary's CURRENT registry state:
// INSERT at CREATE, canonical non-HOT heap UPDATE at the cached TID for
// ALTER ... RENAME / SET SCHEMA / ALTER OPTIONS.
func upsertTSDictCatalogRow(ctx *Context, ud *catalog.UserTSDict) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	row := buildPGTSDictRow(ud)
	var tid storage.ItemPointer
	var err error
	if old, ok := im.TSDictHeapTID(ud.OID); ok {
		oldTID := storage.ItemPointer{Block: storage.BlockNumber(old.Block), Offset: old.Offset}
		tid, err = updateHeapRowCanonicalPG(ctx, pgTSDictRel(), PGTSDictColumnsPG18(), oldTID, row)
	} else {
		tid, err = writeHeapRowCanonical(ctx, pgTSDictRel(), PGTSDictColumnsPG18(), row)
	}
	if err != nil {
		return err
	}
	im.SetTSDictHeapTID(ud.OID, catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset})
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgTSDictOidIndexOID,
		buildIndexTupleOidKey(blk, off, ud.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgTSDictNameNspIndexOID,
		buildIndexTupleNameOidKey(blk, off, ud.Name, ud.NamespaceOID), cmpKeyNameOid); err != nil {
		return err
	}
	mirrorTSDictCatalogFiles(ctx)
	return nil
}

// deleteTSDictCatalogRow stamps xmax on the dictionary's row (DROP).
func deleteTSDictCatalogRow(ctx *Context, dictOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgTSDictRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == dictOID
	})
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		im.DropTSDictHeapTID(dictOID)
	}
	mirrorTSDictCatalogFiles(ctx)
}

// mirrorTSDictCatalogFiles propagates the pg_ts_dict heap + both indexes to
// the postgres DB's copies (reload reads base/5).
func mirrorTSDictCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgTSDictRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgTSDictNameNspIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgTSDictOidIndexOID)
}
