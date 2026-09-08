package executor

// B-05a: pg_statistic_ext_data (3429) write path + reload decoder.
//
// Oracle: postgres/src/backend/statistics/extended_stats.c statext_store
// (writes one row per (stxoid, stxdinherit), deleting the old row first) and
// postgres/src/include/catalog/pg_statistic_ext_data.h (6-column layout, no
// oid system column — attnums start at 1 = stxoid).
//
// Column layout (PG physical attnum order):
//   stxoid oid, stxdinherit bool, stxdndistinct pg_ndistinct,
//   stxddependencies pg_dependencies, stxdmcv pg_mcv_list,
//   stxdexpr _pg_statistic.
//
// The writer mirrors sys_pg_statistic_ext.go:73-183 (per-DB heap routing via
// tableCatalogHeapDBOid, base/1→base/5 mirror, xmax-stamp re-sync).
// The decoder (DecodeStatisticExtDataPayloads) mirrors initdb's
// loadStatisticsExtFromHeapForDB scan-callback style so the future 3429
// startup-reload pass can reuse it directly.

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pgStatisticExtDataRelOID mirrors StatisticExtDataRelationId (3429).
const pgStatisticExtDataRelOID = 3429

// PGStatisticExtDataColumnsPG18 mirrors FormData_pg_statistic_ext_data in PG
// physical attnum order. The Type names drive goopg's heap codec: the three
// built kinds hit the B-05a KindBytes-passthrough arms (full varlena,
// header included); stxdexpr is always written NULL (expression statistics
// are deferred — see buildAndStoreExtStatistics), so its arm never fires on
// the write path.
func PGStatisticExtDataColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "stxoid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "stxdinherit", Type: catalog.Type{Name: "bool"}, Ordinal: 1},
		{Name: "stxdndistinct", Type: catalog.Type{Name: "pg_ndistinct"}, Ordinal: 2},
		{Name: "stxddependencies", Type: catalog.Type{Name: "pg_dependencies"}, Ordinal: 3},
		{Name: "stxdmcv", Type: catalog.Type{Name: "pg_mcv_list"}, Ordinal: 4},
		{Name: "stxdexpr", Type: catalog.Type{Name: "_pg_statistic"}, Ordinal: 5},
	}
}

// pgStatisticExtDataRel is the connecting database's own pg_statistic_ext_data
// heap (base/<dbOid>/3429 via tableCatalogHeapDBOid — same routing as the
// pg_statistic_ext / pg_statistic rows).
func pgStatisticExtDataRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{DBOid: tableCatalogHeapDBOid(ctx), RelOid: pgStatisticExtDataRelOID, Fork: storage.MainFork}
}

// buildStatisticExtDataRow constructs one pg_statistic_ext_data row for the
// non-inherited (stxdinherit=false) statistics of stxOID. ndistinct and deps
// are complete varlenas (header included) or nil for NULL (kind not requested
// or not buildable). Pure constructor — no heap IO — so tests can drive the
// exact encode→decode path without a pool.
//
// B-05a ledger-defer, MCV: stxdmcv is ALWAYS NULL here. Upstream serializes
// the MCV list (up to statistics-target entries of full multi-column datums),
// whose size is bounded only by the target — persisting it needs TOAST, and
// goopg's catalog heap does not toast (same wall as pg_statistic's wide rows
// in persistStatsToPGStatistic, which truncates instead). ndistinct and
// dependencies are combinatorially capped (247 ndistinct items at 8 columns;
// dependencies size-guarded by the caller), so they fit; MCV does not fit
// the guarantee and waits on real TOAST. The skip point is this NULL.
//
// B-05a ledger-defer, expression statistics: stxdexpr is ALWAYS NULL here.
// Building it needs per-expression ANALYZE (examine_expression /
// compute_expr_stats over evaluated expression values), which goopg's
// single-relation sample scan does not produce. Same deferral class as MCV.
func buildStatisticExtDataRow(stxOID uint32, ndistinct, deps []byte) Row {
	ndDatum := NullDatum
	if len(ndistinct) > 0 {
		ndDatum = NewBytesDatum(ndistinct)
	}
	depDatum := NullDatum
	if len(deps) > 0 {
		depDatum = NewBytesDatum(deps)
	}
	return Row{
		NewIntDatum(int64(stxOID)), // stxoid
		NewBoolDatum(false),        // stxdinherit (see below)
		ndDatum,                    // stxdndistinct
		depDatum,                   // stxddependencies
		NullDatum,                  // stxdmcv — B-05a defer (TOAST wall), see above
		NullDatum,                  // stxdexpr — B-05a defer (no expr ANALYZE), see above
	}
}

// syncStatisticExtDataRow stores the built statistics for stxOID, mirroring
// statext_store (extended_stats.c:758-825): delete the old (stxoid,
// stxdinherit) tuple if present, then insert the new one.
//
// Only stxdinherit=false rows are ever written: goopg's ANALYZE is a single
// non-inherited scan (there is no inheritance-tree statistics pass), so the
// inh=true variant upstream builds for partitioned parents has no input here.
// A future inheritance pass must write (stxoid, true) rows through this same
// function with an inherit flag.
func syncStatisticExtDataRow(ctx *Context, stxOID uint32, ndistinct, deps []byte) error {
	if ctx == nil || ctx.Pool == nil {
		return fmt.Errorf("pg_statistic_ext_data write requires Pool in Context")
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	// RemoveStatisticsDataById(stxOID, false): stamp the live predecessor so
	// the reload's liveness filter skips it — the heap grows monotonically,
	// the same pattern as stampStatisticExtRows / persistStatsToPGStatistic.
	stampStatisticExtDataRows(ctx, stxOID, effectiveWriterXID(ctx))
	row := buildStatisticExtDataRow(stxOID, ndistinct, deps)
	if _, err := writeHeapRowCanonical(ctx, pgStatisticExtDataRel(ctx), PGStatisticExtDataColumnsPG18(), row); err != nil {
		return err
	}
	return mirrorStatisticExtDataToPostgresDB(ctx)
}

// mirrorStatisticExtDataToPostgresDB copies the base/1/3429 heap into
// base/5/3429 so a real PG standby connecting via dbname=postgres reads the
// runtime-written rows — the twin of mirrorStatisticExtToPostgresDB (only
// needed when the write targeted the DefaultDBOid heap).
func mirrorStatisticExtDataToPostgresDB(ctx *Context) error {
	if tableCatalogHeapDBOid(ctx) != catalog.DefaultDBOid {
		return nil
	}
	return mirrorCatalogRelToPostgresDB(ctx, pgStatisticExtDataRelOID)
}

// stampStatisticExtDataRows stamps xmax on every live pg_statistic_ext_data
// row for (stxOID, stxdinherit=false): bytes 0:4 are the LE stxoid, byte 4 is
// the stxdinherit bool. Used before each re-sync so the reload skips the old
// version (twin of stampStatisticExtRows).
func stampStatisticExtDataRows(ctx *Context, stxOID uint32, xmax storage.TransactionID) {
	rel := pgStatisticExtDataRel(ctx)
	stampCatalogRows(ctx, rel, xmax, func(data []byte) bool {
		return len(data) >= 5 &&
			binary.LittleEndian.Uint32(data[0:4]) == stxOID &&
			data[4] == 0 // stxdinherit=false; inh=true rows are never ours
	})
	// Carry the delete across to the base/5 mirror like the pg_statistic_ext
	// stamp path does (the re-sync's sync mirrors afterward anyway).
	_ = mirrorStatisticExtDataToPostgresDB(ctx)
}

// DecodeStatisticExtDataPayloads decodes one pg_statistic_ext_data heap row
// (decoded with PGStatisticExtDataColumnsPG18) back into its kind blobs.
// Returned slices are the complete varlenas (header included), ready for
// deserializeExtNDistinct / deserializeExtDependencies, or nil when the kind
// is NULL (not built — statext_is_kind_built's answer for this row).
//
// Reload-decoder twin: initdb's loadStatisticsExtFromHeapForDB scan callback
// decodes heap tuples the same way (DecodeRowIntoMctxPGTuple over the
// physical columns) and then parses payloads; the future 3429 startup-reload
// pass reuses this function for the payload half. stxdmcv/stxdexpr decode to
// their KindBytes forms when non-NULL (a PG-built heap's rows); goopg-written
// rows always carry NULL there.
func DecodeStatisticExtDataPayloads(row Row) (ndistinct, dependencies, mcv, expr []byte, err error) {
	if len(row) != 6 {
		return nil, nil, nil, nil, fmt.Errorf("pg_statistic_ext_data: %d columns, want 6", len(row))
	}
	payload := func(d Datum, name string) ([]byte, error) {
		if d.IsNull() {
			return nil, nil
		}
		// The B-05a codec arms decode these varlenas to KindBytes holding
		// the FULL varlena (header included); the default text arm would
		// have produced KindString instead, which can never be a valid
		// magic-bearing blob.
		if d.Kind != KindBytes {
			return nil, fmt.Errorf("pg_statistic_ext_data.%s: expected varlena bytes, got kind %d", name, d.Kind)
		}
		return append([]byte(nil), d.BytesValue()...), nil
	}
	if ndistinct, err = payload(row[2], "stxdndistinct"); err != nil {
		return nil, nil, nil, nil, err
	}
	if dependencies, err = payload(row[3], "stxddependencies"); err != nil {
		return nil, nil, nil, nil, err
	}
	if mcv, err = payload(row[4], "stxdmcv"); err != nil {
		return nil, nil, nil, nil, err
	}
	if expr, err = payload(row[5], "stxdexpr"); err != nil {
		return nil, nil, nil, nil, err
	}
	return ndistinct, dependencies, mcv, expr, nil
}
