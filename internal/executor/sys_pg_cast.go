package executor

// B2.2a (docs/design/wal-pg-identical-stream/02d §1 + the staged plan in
// IMPLEMENTATION-TODO): CREATE/DROP CAST journal as real pg_cast heap rows
// with entries in both bootstrap-populated indexes, replacing the bespoke
// RecordKindCreateCast(38)/DropCast(39). Reload is fully physical (the Cast
// registry's 6 scalars map 1:1 onto the pg_cast columns; type names revive
// via the OID→name reversal the pg_range reload uses).

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pg_cast relation + index OIDs (postgres/src/include/catalog/pg_cast.h).
const (
	pgCastRelOID               = 2605
	pgCastOidIndexOID          = 2660
	pgCastSourceTargetIndexOID = 2661
)

// PGCastColumnsPG18 mirrors FormData_pg_cast (6 columns). Exported for the
// initdb reload.
func PGCastColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "castsource", Type: catalog.Type{Name: "oid"}},
		{Name: "casttarget", Type: catalog.Type{Name: "oid"}},
		{Name: "castfunc", Type: catalog.Type{Name: "oid"}},
		{Name: "castcontext", Type: catalog.Type{Name: "char"}},
		{Name: "castmethod", Type: catalog.Type{Name: "char"}},
	}
}

// buildPGCastRow builds the pg_cast row for a user cast. Context/Method are
// already stored in the registry as the single-char catalog codes.
func buildPGCastRow(c *catalog.Cast) Row {
	return Row{
		NewIntDatum(int64(c.OID)),
		NewIntDatum(int64(catalog.TypeNameToOID(c.SourceType))),
		NewIntDatum(int64(catalog.TypeNameToOID(c.TargetType))),
		NewIntDatum(int64(c.FuncOID)),
		NewStringDatum(c.Context),
		NewStringDatum(c.Method),
	}
}

func pgCastRel() storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgCastRelOID,
		Fork:   storage.MainFork,
	}
}

// buildIndexTupleOidOidKey builds the 16-byte (oid, oid) IndexTuple for
// pg_cast_source_target_index (2661) — executor twin of initdb's
// pgBuildIndexTupleOidOidKey.
func buildIndexTupleOidOidKey(heapBlk uint32, heapOff uint16, oid1, oid2 uint32) []byte {
	const (
		hoff = sysIndexTupleHoff
		size = 16
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:hoff+4], oid1)
	le.PutUint32(out[hoff+4:hoff+8], oid2)
	return out
}

// cmpKeyOidOid compares (uint32, uint32) keys.
func cmpKeyOidOid(a, b []byte) int {
	if c := cmpKeyUint32(a, b); c != 0 {
		return c
	}
	return cmpKeyUint32(a[4:], b[4:])
}

// writeCastCatalogRow journals one cast as a pg_cast heap INSERT plus both
// index entries.
func writeCastCatalogRow(ctx *Context, c *catalog.Cast) error {
	tid, err := writeHeapRowCanonical(ctx, pgCastRel(), PGCastColumnsPG18(), buildPGCastRow(c))
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgCastOidIndexOID,
		buildIndexTupleOidKey(blk, off, c.OID), cmpKeyUint32); err != nil {
		return err
	}
	return insertCanonicalSysBtreeLeaf(ctx, pgCastSourceTargetIndexOID,
		buildIndexTupleOidOidKey(blk, off,
			catalog.TypeNameToOID(c.SourceType), catalog.TypeNameToOID(c.TargetType)), cmpKeyOidOid)
}

// deleteCastCatalogRow stamps xmax on the cast's row (DROP CAST).
func deleteCastCatalogRow(ctx *Context, castOID uint32, xmax storage.TransactionID) {
	stampCatalogRows(ctx, pgCastRel(), xmax, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == castOID
	})
}

// mirrorCastCatalogFiles propagates the pg_cast heap + both indexes to the
// postgres DB's copies (reload reads base/5).
func mirrorCastCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgCastRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgCastOidIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgCastSourceTargetIndexOID)
}
