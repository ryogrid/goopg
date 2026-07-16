package executor

// B2.1c (docs/design/wal-pg-identical-stream/02d §1): range types journal as
// real catalog heap rows — the 4 pg_type rows (range/array/multirange/
// multirange-array, written by syncRangeTypeToCatalogHeap since DU-002) plus
// a pg_range row (this file) with entries in both pg_range indexes — so the
// startup reload reconstructs the registry from the heaps and the bespoke
// range RecordKinds (81/82/117/118) retire. rngcanonical/rngsubdiff stay 0
// (goopg defines no canonical/subdiff procs for user ranges).

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pg_range relation + index OIDs (postgres/src/include/catalog/pg_range.h).
const (
	pgRangeRelOID                = 3541
	pgRangeRngtypidIndexOID      = 3542
	pgRangeRngmultitypidIndexOID = 2228
)

// PGRangeColumnsPG18 mirrors initdb's pgRangeColDefs (FormData_pg_range):
// 7 columns, no oid system column — rngtypid is the primary key. Exported
// for the initdb reload.
func PGRangeColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "rngtypid", Type: catalog.Type{Name: "oid"}},
		{Name: "rngsubtype", Type: catalog.Type{Name: "oid"}},
		{Name: "rngmultitypid", Type: catalog.Type{Name: "oid"}},
		{Name: "rngcollation", Type: catalog.Type{Name: "oid"}},
		{Name: "rngsubopc", Type: catalog.Type{Name: "oid"}},
		{Name: "rngcanonical", Type: catalog.Type{Name: "regproc"}},
		{Name: "rngsubdiff", Type: catalog.Type{Name: "regproc"}},
	}
}

// buildPGRangeRow builds the pg_range row for a user range type. Mirrors
// initdb's pgRangeRow value semantics.
func buildPGRangeRow(rt *catalog.RangeType) Row {
	return Row{
		NewIntDatum(int64(rt.OID)),                                // 1 rngtypid
		NewIntDatum(int64(catalog.TypeNameToOID(rt.SubtypeName))), // 2 rngsubtype
		NewIntDatum(int64(rt.MultirangeOID)),                      // 3 rngmultitypid
		NewIntDatum(int64(rt.CollationOID)),                       // 4 rngcollation
		NewIntDatum(int64(rt.OpclassOID)),                         // 5 rngsubopc
		NewIntDatum(0),                                            // 6 rngcanonical
		NewIntDatum(0),                                            // 7 rngsubdiff
	}
}

func pgRangeRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgRangeRelOID,
		Fork:   storage.MainFork,
	}
}

// writeRangeCatalogRow journals the pg_range row (XLOG_HEAP_INSERT) and
// maintains both pg_range indexes (3542 rngtypid PKEY, 2228 rngmultitypid).
func writeRangeCatalogRow(ctx *Context, rt *catalog.RangeType) error {
	tid, err := writeHeapRowCanonical(ctx, pgRangeRel(ctx), PGRangeColumnsPG18(), buildPGRangeRow(rt))
	if err != nil {
		return err
	}
	tup := buildIndexTupleOidKey(uint32(tid.Block), tid.Offset, rt.OID)
	if err := insertCanonicalSysBtreeLeaf(ctx, pgRangeRngtypidIndexOID, tup, cmpKeyUint32); err != nil {
		return err
	}
	mtup := buildIndexTupleOidKey(uint32(tid.Block), tid.Offset, rt.MultirangeOID)
	return insertCanonicalSysBtreeLeaf(ctx, pgRangeRngmultitypidIndexOID, mtup, cmpKeyUint32)
}

// deleteRangeCatalogRow stamps xmax on the pg_range row keyed by rngtypid
// (col 0). Index entries die with their heap row, PG-style.
func deleteRangeCatalogRow(ctx *Context, rngtypid uint32, xmax storage.TransactionID) {
	stampCatalogRows(ctx, pgRangeRel(ctx), xmax, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == rngtypid
	})
}

// mirrorRangeCatalogFiles propagates the pg_range heap + both indexes to
// the postgres DB's copies (reload reads base/5).
func mirrorRangeCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgRangeRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgRangeRngtypidIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgRangeRngmultitypidIndexOID)
}
