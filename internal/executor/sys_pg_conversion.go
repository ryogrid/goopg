package executor

// B2.2 slice 4: CREATE/ALTER/DROP CONVERSION journal as real pg_conversion
// heap rows, replacing the bespoke RecordKindCreateConversion(40)/
// DropConversion(41)/AlterConversionRename(130)/AlterConversionOwner(131)/
// AlterConversionSetSchema(132). Same upsert/TID-cache shape as the
// pg_collation twin. FormData_pg_conversion:
// postgres/src/include/catalog/pg_conversion.h.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pg_conversion relation + index OIDs (pg_conversion.h).
const (
	pgConversionRelOID          = 2607
	pgConversionDefaultIndexOID = 2668
	pgConversionNameNspIndexOID = 2669
	pgConversionOidIndexOID     = 2670
)

// PGConversionColumnsPG18 mirrors FormData_pg_conversion (8 columns).
// Exported for the initdb reload.
func PGConversionColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "conname", Type: catalog.Type{Name: "name"}},
		{Name: "connamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "conowner", Type: catalog.Type{Name: "oid"}},
		{Name: "conforencoding", Type: catalog.Type{Name: "int4"}},
		{Name: "contoencoding", Type: catalog.Type{Name: "int4"}},
		{Name: "conproc", Type: catalog.Type{Name: "regproc"}},
		{Name: "condefault", Type: catalog.Type{Name: "bool"}},
	}
}

// buildPGConversionRow builds the pg_conversion row for a user conversion.
func buildPGConversionRow(uc *catalog.UserConversion) Row {
	return Row{
		NewIntDatum(int64(uc.OID)),
		NewStringDatum(uc.Name),
		NewIntDatum(int64(uc.NamespaceOID)),
		NewIntDatum(int64(uc.Owner)),
		NewIntDatum(int64(uc.ForEncoding)),
		NewIntDatum(int64(uc.ToEncoding)),
		NewIntDatum(int64(uc.FuncOID)),
		NewBoolDatum(uc.Default),
	}
}

func pgConversionRel() storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgConversionRelOID,
		Fork:   storage.MainFork,
	}
}

// buildIndexTupleOidInt4Int4OidKey builds the 24-byte (oid, int4, int4, oid)
// IndexTuple for pg_conversion_default_index (2668: connamespace,
// conforencoding, contoencoding, oid).
func buildIndexTupleOidInt4Int4OidKey(heapBlk uint32, heapOff uint16, nsp uint32, forEnc, toEnc int32, oid uint32) []byte {
	const (
		hoff = sysIndexTupleHoff
		size = 24 // MAXALIGN(8 + 4 + 4 + 4 + 4)
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:], nsp)
	le.PutUint32(out[hoff+4:], uint32(forEnc))
	le.PutUint32(out[hoff+8:], uint32(toEnc))
	le.PutUint32(out[hoff+12:], oid)
	return out
}

// cmpKeyOidInt4Int4Oid compares (oid, int4 signed, int4 signed, oid) keys.
func cmpKeyOidInt4Int4Oid(a, b []byte) int {
	if c := cmpKeyUint32(a, b); c != 0 {
		return c
	}
	if c := cmpKeyInt32(a[4:], b[4:]); c != 0 {
		return c
	}
	if c := cmpKeyInt32(a[8:], b[8:]); c != 0 {
		return c
	}
	return cmpKeyUint32(a[12:], b[12:])
}

// upsertConversionCatalogRow journals one conversion's CURRENT registry
// state: INSERT at CREATE CONVERSION, canonical non-HOT heap UPDATE at the
// cached TID for ALTER ... RENAME/OWNER/SET SCHEMA.
func upsertConversionCatalogRow(ctx *Context, uc *catalog.UserConversion) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	row := buildPGConversionRow(uc)
	var tid storage.ItemPointer
	var err error
	if old, ok := im.ConversionHeapTID(uc.OID); ok {
		oldTID := storage.ItemPointer{Block: storage.BlockNumber(old.Block), Offset: old.Offset}
		tid, err = updateHeapRowCanonicalPG(ctx, pgConversionRel(), PGConversionColumnsPG18(), oldTID, row)
	} else {
		tid, err = writeHeapRowCanonical(ctx, pgConversionRel(), PGConversionColumnsPG18(), row)
	}
	if err != nil {
		return err
	}
	im.SetConversionHeapTID(uc.OID, catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset})
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgConversionOidIndexOID,
		buildIndexTupleOidKey(blk, off, uc.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgConversionNameNspIndexOID,
		buildIndexTupleNameOidKey(blk, off, uc.Name, uc.NamespaceOID), cmpKeyNameOid); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgConversionDefaultIndexOID,
		buildIndexTupleOidInt4Int4OidKey(blk, off, uc.NamespaceOID, uc.ForEncoding, uc.ToEncoding, uc.OID),
		cmpKeyOidInt4Int4Oid); err != nil {
		return err
	}
	mirrorConversionCatalogFiles(ctx)
	return nil
}

// deleteConversionCatalogRow stamps xmax on the conversion's row (DROP
// CONVERSION). MaterializeWriterXID first (B2.2c lesson).
func deleteConversionCatalogRow(ctx *Context, convOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgConversionRel(), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == convOID
	})
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		im.DropConversionHeapTID(convOID)
	}
	mirrorConversionCatalogFiles(ctx)
}

// mirrorConversionCatalogFiles propagates the pg_conversion heap + all three
// indexes to the postgres DB's copies (reload reads base/5).
func mirrorConversionCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgConversionRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgConversionDefaultIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgConversionNameNspIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgConversionOidIndexOID)
}
