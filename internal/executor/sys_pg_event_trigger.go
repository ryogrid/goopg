package executor

// B3.2 (docs/design/wal-pg-identical-stream/02d §2): CREATE/ALTER/DROP EVENT
// TRIGGER journal as real pg_event_trigger heap rows with entries in both
// indexes, replacing the bespoke RecordKindCreateEventTrigger(56)/
// DropEventTrigger(57)/AlterEventTriggerEnabled(58)/AlterEventTriggerRename(59)/
// AlterEventTriggerOwner(60). FormData_pg_event_trigger
// (postgres/src/include/catalog/pg_event_trigger.h) is six scalar columns
// plus the evttags text[] array (the WHEN TAG IN (...) filter — NULL when
// absent, event_trigger.c:307). ALTER RENAME/ENABLE/OWNER ride a canonical
// non-HOT heap UPDATE at a TID cache keyed by the trigger OID (the B2.2c
// pattern). Both indexes ship as empty placeholders (no builtin event
// triggers) that the runtime lazily roots.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pg_event_trigger relation + index OIDs (pg_event_trigger.h).
const (
	pgEventTriggerRelOID          = 3466
	pgEventTriggerEvtnameIndexOID = 3467
	pgEventTriggerOidIndexOID     = 3468
)

// PGEventTriggerColumnsPG18 mirrors FormData_pg_event_trigger (7 columns).
// Exported for the initdb reload.
func PGEventTriggerColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "evtname", Type: catalog.Type{Name: "name"}},
		{Name: "evtevent", Type: catalog.Type{Name: "name"}},
		{Name: "evtowner", Type: catalog.Type{Name: "oid"}},
		{Name: "evtfoid", Type: catalog.Type{Name: "regproc"}},
		{Name: "evtenabled", Type: catalog.Type{Name: "char"}},
		{Name: "evttags", Type: catalog.Type{Name: "text", IsArray: true}},
	}
}

// buildPGEventTriggerRow builds the pg_event_trigger row for a user event
// trigger. evtenabled defaults to 'O' (origin) — the CREATE-time default —
// when the registry field is empty. evttags is NULL when the trigger has no
// WHEN TAG filter (matching event_trigger.c, which sets attisnull there).
func buildPGEventTriggerRow(et *catalog.EventTrigger) Row {
	enabled := et.Enabled
	if enabled == "" {
		enabled = "O"
	}
	tags := NullDatum
	if len(et.Tags) > 0 {
		tags = NewStringDatum(formatTextArray(et.Tags))
	}
	owner := et.Owner
	if owner == 0 {
		owner = 10 // bootstrap superuser, matching the render-time default
	}
	return Row{
		NewIntDatum(int64(et.OID)),
		NewStringDatum(et.Name),
		NewStringDatum(et.Event),
		NewIntDatum(int64(owner)),
		NewIntDatum(int64(et.FuncOID)),
		NewStringDatum(enabled),
		tags,
	}
}

func pgEventTriggerRel() storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgEventTriggerRelOID,
		Fork:   storage.MainFork,
	}
}

// upsertEventTriggerCatalogRow journals one event trigger's CURRENT registry
// state: INSERT at CREATE, canonical non-HOT heap UPDATE at the cached TID
// for ALTER ... RENAME/ENABLE/OWNER.
func upsertEventTriggerCatalogRow(ctx *Context, et *catalog.EventTrigger) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	row := buildPGEventTriggerRow(et)
	var tid storage.ItemPointer
	var err error
	if old, ok := im.EventTriggerHeapTID(et.OID); ok {
		oldTID := storage.ItemPointer{Block: storage.BlockNumber(old.Block), Offset: old.Offset}
		tid, err = updateHeapRowCanonicalPG(ctx, pgEventTriggerRel(), PGEventTriggerColumnsPG18(), oldTID, row)
	} else {
		tid, err = writeHeapRowCanonical(ctx, pgEventTriggerRel(), PGEventTriggerColumnsPG18(), row)
	}
	if err != nil {
		return err
	}
	im.SetEventTriggerHeapTID(et.OID, catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset})
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgEventTriggerOidIndexOID,
		buildIndexTupleOidKey(blk, off, et.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgEventTriggerEvtnameIndexOID,
		buildIndexTupleNameKey(blk, off, et.Name), cmpKeyName); err != nil {
		return err
	}
	mirrorEventTriggerCatalogFiles(ctx)
	return nil
}

// deleteEventTriggerCatalogRow stamps xmax on the trigger's row (DROP EVENT
// TRIGGER). MaterializeWriterXID first — an unmaterialized XID (0) makes the
// stamp a silent no-op (B2.2c lesson).
func deleteEventTriggerCatalogRow(ctx *Context, evtOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgEventTriggerRel(), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == evtOID
	})
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		im.DropEventTriggerHeapTID(evtOID)
	}
	mirrorEventTriggerCatalogFiles(ctx)
}

// mirrorEventTriggerCatalogFiles propagates the pg_event_trigger heap + both
// indexes to the postgres DB's copies (reload reads base/5).
func mirrorEventTriggerCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgEventTriggerRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgEventTriggerEvtnameIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgEventTriggerOidIndexOID)
}
