package executor

// B3.6 (docs/design/wal-pg-identical-stream/02d §2): CREATE/ALTER/DROP TEXT
// SEARCH CONFIGURATION journal as real pg_ts_config + pg_ts_config_map heap
// rows, replacing the bespoke RecordKindCreateTSConfig(106)/AddTSConfigMapping
// (107)/DropTSConfig(108)/DropTSConfigMapping(109)/RenameTSConfig(110)/
// SetTSConfigSchema(111)/ReplaceTSConfigMappingDict(112)/AlterTSConfigMapping
// (113). The base pg_ts_config row is all-scalar (name + owner + cfgparser
// OID). Its ADD MAPPING entries live in pg_ts_config_map, one row per
// (token type, dictionary) — mapcfg = the config's OID, maptokentype = the
// numeric token type (catalog.TSTokenTypeID), mapseqno = the dictionary's
// index in the token's DictOIDs run, mapdict = the pg_ts_dict OID (durable
// since B3.5). goopg stores the mappings inline on UserTSConfig, so any
// mapping mutation re-syncs the whole config_map row-set. Column layouts:
// postgres/src/include/catalog/pg_ts_config.h, pg_ts_config_map.h.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const (
	pgTSConfigRelOID          = 3602
	pgTSConfigNameNspIndexOID = 3608
	pgTSConfigOidIndexOID     = 3712
	pgTSConfigMapRelOID       = 3603
	pgTSConfigMapIndexOID     = 3609
)

// PGTSConfigColumnsPG18 mirrors FormData_pg_ts_config (5 columns). Exported
// for the initdb reload.
func PGTSConfigColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "cfgname", Type: catalog.Type{Name: "name"}},
		{Name: "cfgnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "cfgowner", Type: catalog.Type{Name: "oid"}},
		{Name: "cfgparser", Type: catalog.Type{Name: "oid"}},
	}
}

// PGTSConfigMapColumnsPG18 mirrors FormData_pg_ts_config_map (4 columns, no
// oid). Exported for the initdb reload.
func PGTSConfigMapColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "mapcfg", Type: catalog.Type{Name: "oid"}},
		{Name: "maptokentype", Type: catalog.Type{Name: "int4"}},
		{Name: "mapseqno", Type: catalog.Type{Name: "int4"}},
		{Name: "mapdict", Type: catalog.Type{Name: "oid"}},
	}
}

func buildPGTSConfigRow(uc *catalog.UserTSConfig) Row {
	owner := uc.Owner
	if owner == 0 {
		owner = 10
	}
	return Row{
		NewIntDatum(int64(uc.OID)),
		NewStringDatum(uc.Name),
		NewIntDatum(int64(uc.NamespaceOID)),
		NewIntDatum(int64(owner)),
		NewIntDatum(int64(uc.Parser)),
	}
}

func pgTSConfigRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgTSConfigRelOID, Fork: storage.MainFork}
}

func pgTSConfigMapRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgTSConfigMapRelOID, Fork: storage.MainFork}
}

// buildIndexTupleOidInt4Int4Key builds the 24-byte (oid, int4, int4)
// IndexTuple for pg_ts_config_map_index (3609: mapcfg, maptokentype,
// mapseqno).
func buildIndexTupleOidInt4Int4Key(heapBlk uint32, heapOff uint16, oid uint32, i1, i2 int32) []byte {
	const (
		hoff = sysIndexTupleHoff
		size = 24 // MAXALIGN(8 + 4 + 4 + 4)
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:], oid)
	le.PutUint32(out[hoff+4:], uint32(i1))
	le.PutUint32(out[hoff+8:], uint32(i2))
	return out
}

// cmpKeyOidInt4Int4 compares (oid, int4 signed, int4 signed) keys.
func cmpKeyOidInt4Int4(a, b []byte) int {
	if c := cmpKeyUint32(a, b); c != 0 {
		return c
	}
	if c := cmpKeyInt32(a[4:], b[4:]); c != 0 {
		return c
	}
	return cmpKeyInt32(a[8:], b[8:])
}

// upsertTSConfigCatalogRow journals one configuration's CURRENT state: the
// base pg_ts_config row (INSERT at CREATE, heap UPDATE at the cached TID for
// RENAME / SET SCHEMA) plus a full re-sync of its pg_ts_config_map rows
// (delete every row for this config's OID, then write one per (token type,
// dictionary) from the inline Mappings). The re-sync covers every mapping
// mutation (ADD / DROP MAPPING / REPLACE / ALTER) uniformly.
func upsertTSConfigCatalogRow(ctx *Context, uc *catalog.UserTSConfig) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	row := buildPGTSConfigRow(uc)
	var tid storage.ItemPointer
	var err error
	if old, ok := im.TSConfigHeapTID(uc.OID); ok {
		oldTID := storage.ItemPointer{Block: storage.BlockNumber(old.Block), Offset: old.Offset}
		tid, err = updateHeapRowCanonicalPG(ctx, pgTSConfigRel(), PGTSConfigColumnsPG18(), oldTID, row)
	} else {
		tid, err = writeHeapRowCanonical(ctx, pgTSConfigRel(), PGTSConfigColumnsPG18(), row)
	}
	if err != nil {
		return err
	}
	im.SetTSConfigHeapTID(uc.OID, catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset})
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgTSConfigOidIndexOID,
		buildIndexTupleOidKey(blk, off, uc.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgTSConfigNameNspIndexOID,
		buildIndexTupleNameOidKey(blk, off, uc.Name, uc.NamespaceOID), cmpKeyNameOid); err != nil {
		return err
	}
	if err := syncTSConfigMapRows(ctx, uc); err != nil {
		return err
	}
	mirrorTSConfigCatalogFiles(ctx)
	return nil
}

// syncTSConfigMapRows re-writes the pg_ts_config_map rows for uc: it stamps
// every existing row for uc.OID, then INSERTs one per (token type,
// dictionary) from the inline Mappings. The caller has materialized the
// writer XID.
func syncTSConfigMapRows(ctx *Context, uc *catalog.UserTSConfig) error {
	stampTSConfigMapRows(ctx, uc.OID)
	for _, m := range uc.Mappings {
		tokID, ok := catalog.TSTokenTypeID(m.TokenType)
		if !ok {
			continue
		}
		for seq, dictOID := range m.DictOIDs {
			row := Row{
				NewIntDatum(int64(uc.OID)),
				NewIntDatum(int64(tokID)),
				NewIntDatum(int64(seq)),
				NewIntDatum(int64(dictOID)),
			}
			tid, err := writeHeapRowCanonical(ctx, pgTSConfigMapRel(), PGTSConfigMapColumnsPG18(), row)
			if err != nil {
				return err
			}
			if err := insertCanonicalSysBtreeLeaf(ctx, pgTSConfigMapIndexOID,
				buildIndexTupleOidInt4Int4Key(uint32(tid.Block), tid.Offset, uc.OID, int32(tokID), int32(seq)),
				cmpKeyOidInt4Int4); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteTSConfigCatalogRows stamps xmax on the config's base row and all its
// pg_ts_config_map rows (DROP TEXT SEARCH CONFIGURATION).
func deleteTSConfigCatalogRows(ctx *Context, cfgOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgTSConfigRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == cfgOID
	})
	stampTSConfigMapRows(ctx, cfgOID)
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		im.DropTSConfigHeapTID(cfgOID)
	}
	mirrorTSConfigCatalogFiles(ctx)
}

// stampTSConfigMapRows marks every live pg_ts_config_map row for cfgOID
// (mapcfg = column 0) deleted. The caller has materialized the writer XID.
func stampTSConfigMapRows(ctx *Context, cfgOID uint32) {
	stampCatalogRows(ctx, pgTSConfigMapRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == cfgOID
	})
}

// mirrorTSConfigCatalogFiles propagates both heaps + their indexes to the
// postgres DB's copies (reload reads base/5).
func mirrorTSConfigCatalogFiles(ctx *Context) {
	for _, oid := range []uint32{
		pgTSConfigRelOID, pgTSConfigNameNspIndexOID, pgTSConfigOidIndexOID,
		pgTSConfigMapRelOID, pgTSConfigMapIndexOID,
	} {
		_ = mirrorCatalogRelToPostgresDB(ctx, oid)
	}
}
