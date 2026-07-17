package executor

// B2.2 slice 2 (docs/design/wal-pg-identical-stream/02d §1 + the staged plan
// in IMPLEMENTATION-TODO): CREATE/DROP/ALTER AGGREGATE journal as real
// pg_proc (prokind='a') + pg_aggregate heap rows, replacing the bespoke
// RecordKindCreateAggregate(46)/AlterAggregateRename(47)/DropAggregate(48)/
// AlterAggregateOwner(49). The pg_proc half rides the B1.2 routine funnel
// (syncRoutineToCatalogHeap: heap row + 2690/2691 entries + JSON arg-meta);
// the pg_aggregate half extends DU-002 slice 405's write-only heap append
// with pg_aggregate_fnoid_index (2650) maintenance. Reload joins both heaps
// physically (aggfnoid = pg_proc.oid).

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pg_aggregate index OID (postgres/src/include/catalog/pg_aggregate.h):
// DECLARE_UNIQUE_INDEX_PKEY(pg_aggregate_fnoid_index, 2650, on aggfnoid).
const pgAggregateFnoidIndexOID = 2650

// PGAggregateColumnsPG18 exposes the 22-column FormData_pg_aggregate layout
// for the initdb reload (executor-internal builder: pgAggregateColumnsPG18).
func PGAggregateColumnsPG18() []catalog.Column {
	return pgAggregateColumnsPG18()
}

// routineForAggregate synthesizes the *catalog.Routine whose pg_proc row
// represents this aggregate (prokind='a'). Field choices mirror the virtual
// pg_proc view's aggregate rows (initdb/pg_proc_view.go DU-002 slice 405):
// prolang=internal (cost 1), prosrc=aggregate_dummy (PG's real stub),
// provolatile='i', proparallel='u', proisstrict=false (NULL handling lives
// in the transfn). prorettype follows PG's DefineAggregate: the finalfn's
// return type when one is given, else the state type.
func routineForAggregate(cat catalog.Catalog, agg *catalog.UserAggregate, schema string) *catalog.Routine {
	rettype := agg.SType
	if _, ft := ResolveAggFuncOIDAndRetType(cat, agg.FinalFunc); ft != "" {
		rettype = ft
	}
	argTypes := make([]catalog.Type, len(agg.ArgTypes))
	for i, t := range agg.ArgTypes {
		argTypes[i] = catalog.Type{Name: t}
	}
	return &catalog.Routine{
		OID:        agg.OID,
		Name:       agg.Name,
		Schema:     schema,
		KindChar:   "a",
		Language:   "internal",
		Body:       "aggregate_dummy",
		Volatile:   "i",
		Parallel:   "u",
		ArgTypes:   argTypes,
		ReturnType: catalog.Type{Name: rettype},
		Owner:      agg.Owner,
		Aggregate:  agg,
	}
}

// aggregateSchemaName reverses agg.NamespaceOID to its schema name for the
// synthesized pg_proc row ("public" fallback matches NamespaceOIDOrDefault).
func aggregateSchemaName(cat catalog.Catalog, agg *catalog.UserAggregate) string {
	if im, ok := cat.(*catalog.InMemory); ok {
		if name := im.SchemaNameForOID(agg.NamespaceOIDOrDefault()); name != "" {
			return name
		}
	}
	return "public"
}

func pgAggregateRel() storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AggregateRelationId,
		Fork:   storage.MainFork,
	}
}

// writeAggregateCatalogRows journals CREATE AGGREGATE: the prokind='a'
// pg_proc row (heap + 2690/2691 via the routine funnel, which also caches
// the TID for later ALTER updates) plus the pg_aggregate row with its 2650
// entry.
func writeAggregateCatalogRows(ctx *Context, agg *catalog.UserAggregate, schema string) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := syncRoutineToCatalogHeap(ctx, routineForAggregate(ctx.Catalog, agg, schema)); err != nil {
		return err
	}
	tid, err := writeHeapRowCanonical(ctx, pgAggregateRel(), pgAggregateColumnsPG18(), buildUserPGAggregateRow(ctx, agg))
	if err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgAggregateFnoidIndexOID,
		buildIndexTupleOidKey(uint32(tid.Block), tid.Offset, agg.OID), cmpKeyUint32); err != nil {
		return err
	}
	mirrorAggregateCatalogFiles(ctx)
	return nil
}

// updateAggregateProcRow journals ALTER AGGREGATE ... RENAME TO / OWNER TO:
// only the pg_proc half changes (pg_aggregate carries no name/owner), and
// the routine funnel's cached TID turns it into a canonical non-HOT UPDATE.
func updateAggregateProcRow(ctx *Context, agg *catalog.UserAggregate) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := syncRoutineToCatalogHeap(ctx, routineForAggregate(ctx.Catalog, agg, aggregateSchemaName(ctx.Catalog, agg))); err != nil {
		return
	}
	mirrorAggregateCatalogFiles(ctx)
}

// deleteAggregateCatalogRows journals DROP AGGREGATE: xmax on the pg_proc
// row (routine funnel) and on the pg_aggregate row.
func deleteAggregateCatalogRows(ctx *Context, aggOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	_ = deleteRoutineCatalogHeapRow(ctx, aggOID)
	stampCatalogRows(ctx, pgAggregateRel(), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == aggOID
	})
	mirrorAggregateCatalogFiles(ctx)
}

// mirrorAggregateCatalogFiles propagates the pg_aggregate heap + fnoid
// index to the postgres DB's copies (reload reads base/5); the pg_proc
// files mirror inside the routine funnel.
func mirrorAggregateCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, catalog.AggregateRelationId)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgAggregateFnoidIndexOID)
}
