package executor

// B3.1 (docs/design/wal-pg-identical-stream/02d §2): CREATE/DROP TRANSFORM
// journal as real pg_transform heap rows with entries in both indexes,
// replacing the bespoke RecordKindCreateTransform(36)/DropTransform(37).
// FormData_pg_transform (postgres/src/include/catalog/pg_transform.h) is five
// OIDs — the Transform registry's two name fields (TypeName/Lang) resolve to
// trftype/trflang via TypeNameToOID/LanguageNameToOID, and the reload
// reverses them, so the row is fully physical.
//
// Both indexes ship as empty metapage-only placeholders (no builtin
// transforms in pg_transform.dat), which the runtime lazily roots — the
// B2.1a machinery.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pg_transform relation + index OIDs (pg_transform.h).
const (
	pgTransformRelOID           = 3576
	pgTransformOidIndexOID      = 3574
	pgTransformTypeLangIndexOID = 3575
)

// PGTransformColumnsPG18 mirrors FormData_pg_transform (5 columns). Exported
// for the initdb reload.
func PGTransformColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "trftype", Type: catalog.Type{Name: "oid"}},
		{Name: "trflang", Type: catalog.Type{Name: "oid"}},
		{Name: "trffromsql", Type: catalog.Type{Name: "regproc"}},
		{Name: "trftosql", Type: catalog.Type{Name: "regproc"}},
	}
}

// buildPGTransformRow builds the pg_transform row for a user transform.
func buildPGTransformRow(tf *catalog.Transform) Row {
	return Row{
		NewIntDatum(int64(tf.OID)),
		NewIntDatum(int64(catalog.TypeNameToOID(tf.TypeName))),
		NewIntDatum(int64(catalog.LanguageNameToOID(tf.Lang))),
		NewIntDatum(int64(tf.FromFuncOID)),
		NewIntDatum(int64(tf.ToFuncOID)),
	}
}

func pgTransformRel() storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgTransformRelOID,
		Fork:   storage.MainFork,
	}
}

// writeTransformCatalogRow journals CREATE TRANSFORM: a pg_transform heap
// INSERT plus both index entries. RegisterTransform is idempotent per
// (type, lang) — a re-CREATE refreshes the function OIDs on the SAME OID —
// so the previous row version is stamped first, keeping exactly one live row
// per transform (PG's CREATE OR REPLACE TRANSFORM does a CatalogTupleUpdate;
// goopg has no TID cache for this low-traffic catalog, so delete+insert is
// the equivalent — the reload sees one live row either way).
func writeTransformCatalogRow(ctx *Context, tf *catalog.Transform) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampTransformRow(ctx, tf.OID)
	tid, err := writeHeapRowCanonical(ctx, pgTransformRel(), PGTransformColumnsPG18(), buildPGTransformRow(tf))
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgTransformOidIndexOID,
		buildIndexTupleOidKey(blk, off, tf.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgTransformTypeLangIndexOID,
		buildIndexTupleOidOidKey(blk, off,
			catalog.TypeNameToOID(tf.TypeName), catalog.LanguageNameToOID(tf.Lang)),
		cmpKeyOidOid); err != nil {
		return err
	}
	mirrorTransformCatalogFiles(ctx)
	return nil
}

// deleteTransformCatalogRow stamps xmax on the transform's row (DROP
// TRANSFORM).
func deleteTransformCatalogRow(ctx *Context, transformOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampTransformRow(ctx, transformOID)
	mirrorTransformCatalogFiles(ctx)
}

// stampTransformRow marks every live row for transformOID deleted. The
// caller must have materialized the writer XID.
func stampTransformRow(ctx *Context, transformOID uint32) {
	stampCatalogRows(ctx, pgTransformRel(), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == transformOID
	})
}

// mirrorTransformCatalogFiles propagates the pg_transform heap + both
// indexes to the postgres DB's copies (reload reads base/5).
func mirrorTransformCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgTransformRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgTransformOidIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgTransformTypeLangIndexOID)
}
