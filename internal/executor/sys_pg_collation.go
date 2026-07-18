package executor

// B2.2 slice 4 (docs/design/wal-pg-identical-stream/02d §1 + the staged plan
// in IMPLEMENTATION-TODO): CREATE/ALTER/DROP COLLATION journal as real
// pg_collation heap rows with entries in both bootstrap-populated indexes,
// replacing the bespoke RecordKindCreateCollation(42)/DropCollation(43)/
// AlterCollationRename(44)/AlterCollationOwner(45)/AlterCollationSetSchema(93).
// ALTER variants ride a canonical non-HOT heap UPDATE at a TID cache keyed by
// the collation OID (the B2.2c operator pattern). Reload is fully physical —
// FormData_pg_collation (postgres/src/include/catalog/pg_collation.h) maps
// 1:1 onto UserCollation.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pg_collation relation + index OIDs (pg_collation.h).
const (
	pgCollationRelOID            = 3456
	pgCollationOidIndexOID       = 3085
	pgCollationNameEncNspIndexID = 3164
)

// PGCollationColumnsPG18 mirrors FormData_pg_collation (12 columns).
// Exported for the initdb reload.
func PGCollationColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "collname", Type: catalog.Type{Name: "name"}},
		{Name: "collnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "collowner", Type: catalog.Type{Name: "oid"}},
		{Name: "collprovider", Type: catalog.Type{Name: "char"}},
		{Name: "collisdeterministic", Type: catalog.Type{Name: "bool"}},
		{Name: "collencoding", Type: catalog.Type{Name: "int4"}},
		{Name: "collcollate", Type: catalog.Type{Name: "text"}},
		{Name: "collctype", Type: catalog.Type{Name: "text"}},
		{Name: "colllocale", Type: catalog.Type{Name: "text"}},
		{Name: "collicurules", Type: catalog.Type{Name: "text"}},
		{Name: "collversion", Type: catalog.Type{Name: "text"}},
	}
}

// buildPGCollationRow builds the pg_collation row for a user collation.
// Empty-string registry fields are genuinely NULL (BKI_DEFAULT(_null_) in
// pg_collation.h — the pg_class builder convention); collversion is always
// NULL: goopg records no provider version (virtual view parity).
func buildPGCollationRow(uc *catalog.UserCollation) Row {
	textOrNull := func(s string) Datum {
		if s == "" {
			return NullDatum
		}
		return NewStringDatum(s)
	}
	return Row{
		NewIntDatum(int64(uc.OID)),
		NewStringDatum(uc.Name),
		NewIntDatum(int64(uc.NamespaceOID)),
		NewIntDatum(int64(uc.Owner)),
		NewStringDatum(string(uc.Provider)),
		NewBoolDatum(uc.Deterministic),
		NewIntDatum(int64(uc.Encoding)),
		textOrNull(uc.Collate),
		textOrNull(uc.Ctype),
		textOrNull(uc.Locale),
		textOrNull(uc.Rules),
		NullDatum, // collversion
	}
}

func pgCollationRel() storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgCollationRelOID,
		Fork:   storage.MainFork,
	}
}

// buildIndexTupleNameInt4OidKey builds the 80-byte (name, int4, oid)
// IndexTuple for pg_collation_name_enc_nsp_index (3164) — executor twin of
// initdb's pgBuildIndexTupleNameInt4OidKey.
func buildIndexTupleNameInt4OidKey(heapBlk uint32, heapOff uint16, name string, enc int32, oid uint32) []byte {
	const (
		nameDataLen = 64
		hoff        = sysIndexTupleHoff
		size        = 80 // MAXALIGN(8 + 64 + 4 + 4)
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	nb := []byte(name)
	if len(nb) > nameDataLen-1 {
		nb = nb[:nameDataLen-1]
	}
	copy(out[hoff:], nb)
	le.PutUint32(out[hoff+nameDataLen:], uint32(enc))
	le.PutUint32(out[hoff+nameDataLen+4:], oid)
	return out
}

// cmpKeyInt32 compares one SIGNED int4 key attribute (int4_ops — collencoding
// is -1 for encoding-independent collations, which must sort BELOW every
// positive encoding, matching the bootstrap bulk-load's int32 sort).
func cmpKeyInt32(a, b []byte) int {
	av := int32(binary.LittleEndian.Uint32(a))
	bv := int32(binary.LittleEndian.Uint32(b))
	switch {
	case av < bv:
		return -1
	case av > bv:
		return 1
	default:
		return 0
	}
}

// cmpKeyNameInt4Oid compares (name, int4 signed, oid) keys.
func cmpKeyNameInt4Oid(a, b []byte) int {
	const nameDataLen = 64
	if c := cmpKeyName(a[:nameDataLen], b[:nameDataLen]); c != 0 {
		return c
	}
	if c := cmpKeyInt32(a[nameDataLen:], b[nameDataLen:]); c != 0 {
		return c
	}
	return cmpKeyUint32(a[nameDataLen+4:], b[nameDataLen+4:])
}

// upsertCollationCatalogRow journals one collation's CURRENT registry state:
// INSERT at CREATE COLLATION, canonical non-HOT heap UPDATE at the cached
// TID for ALTER ... RENAME/OWNER/SET SCHEMA. Fresh index entries land at the
// new TID either way.
func upsertCollationCatalogRow(ctx *Context, uc *catalog.UserCollation) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	row := buildPGCollationRow(uc)
	var tid storage.ItemPointer
	var err error
	if old, ok := im.CollationHeapTID(uc.OID); ok {
		oldTID := storage.ItemPointer{Block: storage.BlockNumber(old.Block), Offset: old.Offset}
		tid, err = updateHeapRowCanonicalPG(ctx, pgCollationRel(), PGCollationColumnsPG18(), oldTID, row)
	} else {
		tid, err = writeHeapRowCanonical(ctx, pgCollationRel(), PGCollationColumnsPG18(), row)
	}
	if err != nil {
		return err
	}
	im.SetCollationHeapTID(uc.OID, catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset})
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgCollationOidIndexOID,
		buildIndexTupleOidKey(blk, off, uc.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgCollationNameEncNspIndexID,
		buildIndexTupleNameInt4OidKey(blk, off, uc.Name, int32(uc.Encoding), uc.NamespaceOID),
		cmpKeyNameInt4Oid); err != nil {
		return err
	}
	mirrorCollationCatalogFiles(ctx)
	return nil
}

// deleteCollationCatalogRow stamps xmax on the collation's row (DROP
// COLLATION). MaterializeWriterXID first — an unmaterialized XID (0) makes
// the stamp a silent no-op (B2.2c lesson).
func deleteCollationCatalogRow(ctx *Context, collOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgCollationRel(), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == collOID
	})
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		im.DropCollationHeapTID(collOID)
	}
	mirrorCollationCatalogFiles(ctx)
}

// mirrorCollationCatalogFiles propagates the pg_collation heap + both
// indexes to the postgres DB's copies (reload reads base/5).
func mirrorCollationCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgCollationRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgCollationOidIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgCollationNameEncNspIndexID)
}
