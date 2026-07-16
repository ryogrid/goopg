package executor

// B1.3 (docs/design/wal-pg-identical-stream/02c §3): pg_sequence CATALOG-ROW
// heap journaling. The sequence DEFINITION (Form_pg_sequence: seqrelid,
// seqtypid, seqstart, seqincrement, seqmax, seqmin, seqcache, seqcycle) now
// lives as a real heap row in base/<db>/2224 + an entry in
// pg_sequence_seqrelid_index (5002) — CREATE/ALTER SEQUENCE journal
// XLOG_HEAP_INSERT/UPDATE, DROP journals XLOG_HEAP_DELETE, exactly PG's
// shape. The COUNTER (last_value/log_cnt/is_called) deliberately stays on
// the name-keyed RecordKindSequenceState(65)/DropSequence(66) records until
// B1.3b's XLOG_SEQ_LOG flip (see the 02c scope decision); kind 65 also still
// carries the definition for goopg's own replay, so the heap row is
// PG-parity surface, not goopg's recovery source, this phase.

import (
	"fmt"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// pgSequenceRelOID / pgSequenceSeqrelidIndexOID are pg_sequence's relation
// and pkey-index OIDs (postgres/src/include/catalog/pg_sequence.h).
const (
	pgSequenceRelOID            = 2224
	pgSequenceSeqrelidIndexOID  = 5002
)

// PGSequenceColumnsPG18 mirrors Form_pg_sequence (8 columns, all fixed).
// Exported for the initdb TID-seeding reload.
func PGSequenceColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "seqrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "seqtypid", Type: catalog.Type{Name: "oid"}},
		{Name: "seqstart", Type: catalog.Type{Name: "int8"}},
		{Name: "seqincrement", Type: catalog.Type{Name: "int8"}},
		{Name: "seqmax", Type: catalog.Type{Name: "int8"}},
		{Name: "seqmin", Type: catalog.Type{Name: "int8"}},
		{Name: "seqcache", Type: catalog.Type{Name: "int8"}},
		{Name: "seqcycle", Type: catalog.Type{Name: "bool"}},
	}
}

// seqTypIDForDataType maps the sequence AS type to seqtypid.
func seqTypIDForDataType(dataType string) int64 {
	switch dataType {
	case "smallint":
		return 21
	case "integer":
		return 23
	default: // bigint (PG default)
		return 20
	}
}

// buildPGSequenceRow builds one Form_pg_sequence row from the definition
// snapshot the kind-65 payload carries.
func buildPGSequenceRow(seqrelid uint32, p wal.SequenceStatePayload) Row {
	return Row{
		NewIntDatum(int64(seqrelid)),               // seqrelid
		NewIntDatum(seqTypIDForDataType(p.DataType)), // seqtypid
		NewIntDatum(p.Start),                       // seqstart
		NewIntDatum(p.Increment),                   // seqincrement
		NewIntDatum(p.Max),                         // seqmax
		NewIntDatum(p.Min),                         // seqmin
		NewIntDatum(p.Cache),                       // seqcache
		NewBoolDatum(p.Cycle),                      // seqcycle
	}
}

// seqHeapEntry is the TID-carrying cache entry per sequence (doc 02a §3.3),
// keyed by seqKey(name, dbOid) — the same key the counter registry uses.
// defSig fingerprints the definition so counter-only WALLogSequenceState
// calls (setval, TRUNCATE RESTART pre-logs) skip redundant heap UPDATEs
// (PG does not touch pg_sequence on setval).
type seqHeapEntry struct {
	tid    catalog.SchemaHeapTID
	defSig string
}

var seqHeapTIDs sync.Map // seqKey → seqHeapEntry

func seqDefSig(seqrelid uint32, p wal.SequenceStatePayload) string {
	return fmt.Sprintf("%d|%s|%d|%d|%d|%d|%d|%v", seqrelid, p.DataType, p.Start, p.Increment, p.Max, p.Min, p.Cache, p.Cycle)
}

// pgSequenceRel returns the pg_sequence heap relfile for the catalog-write DB.
func pgSequenceRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  tableCatalogHeapDBOid(ctx),
		RelOid: pgSequenceRelOID,
		Fork:   storage.MainFork,
	}
}

// sequenceRelOIDForName resolves the sequence RELATION's pg_class OID
// (Form_pg_sequence.seqrelid) from the catalog registration
// CreateSequenceCatalogRelation performed. Empty schema resolves via the
// catalog's usual lookup rules. Returns 0 when unknown (temp sequences,
// embedded setups) — no heap row is written then.
func sequenceRelOIDForName(cat catalog.Catalog, name string, dbOid uint32) uint32 {
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		return 0
	}
	schema, bare := "", name
	if i := lastDotIndex(name); i >= 0 {
		schema, bare = name[:i], name[i+1:]
	}
	if tbl, ok := im.LookupTable(parser.ObjectName{Schema: schema, Name: bare}, dbOid); ok && tbl != nil && tbl.IsSequence {
		return tbl.OID
	}
	return 0
}

func lastDotIndex(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// syncSequenceDefinitionToCatalogHeap upserts the pg_sequence row for the
// definition carried by p — called from WALLogSequenceState (the definition
// funnel) beside the kind-65 emit. Counter-only snapshots (unchanged
// definition) are skipped via the fingerprint.
func syncSequenceDefinitionToCatalogHeap(ctx *Context, name string, p wal.SequenceStatePayload) {
	if ctx == nil || ctx.Pool == nil || ctx.Catalog == nil {
		return
	}
	seqrelid := sequenceRelOIDForName(ctx.Catalog, name, p.DBOid)
	if seqrelid == 0 {
		return
	}
	key := seqKey(name, p.DBOid)
	sig := seqDefSig(seqrelid, p)
	if v, ok := seqHeapTIDs.Load(key); ok && v.(seqHeapEntry).defSig == sig {
		return // definition unchanged — counter-only snapshot
	}
	row := buildPGSequenceRow(seqrelid, p)
	if v, ok := seqHeapTIDs.Load(key); ok {
		e := v.(seqHeapEntry)
		oldTID := storage.ItemPointer{Block: storage.BlockNumber(e.tid.Block), Offset: e.tid.Offset}
		newTID, err := updateHeapRowCanonicalPG(ctx, pgSequenceRel(ctx), PGSequenceColumnsPG18(), oldTID, row)
		if err != nil {
			return // parity surface only this phase — never fail the DDL
		}
		seqHeapTIDs.Store(key, seqHeapEntry{tid: catalog.SchemaHeapTID{Block: uint32(newTID.Block), Offset: newTID.Offset}, defSig: sig})
		_ = mirrorProcCatalogFiles(ctx)
		return
	}
	tid, err := writeHeapRowCanonical(ctx, pgSequenceRel(ctx), PGSequenceColumnsPG18(), row)
	if err != nil {
		return
	}
	_ = insertCanonicalSysBtreeLeaf(ctx, pgSequenceSeqrelidIndexOID,
		buildIndexTupleOidKey(uint32(tid.Block), tid.Offset, seqrelid), cmpKeyUint32)
	seqHeapTIDs.Store(key, seqHeapEntry{tid: catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset}, defSig: sig})
	_ = mirrorProcCatalogFiles(ctx)
}

// dropSequenceCatalogHeapRow journals the pg_sequence heap DELETE for a
// dropped/renamed-away sequence key. Missing TID is a no-op.
func dropSequenceCatalogHeapRow(ctx *Context, name string, dbOid uint32) {
	if ctx == nil || ctx.Pool == nil {
		return
	}
	key := seqKey(name, resolveSeqDBOid([]uint32{dbOid}))
	v, ok := seqHeapTIDs.Load(key)
	if !ok {
		return
	}
	e := v.(seqHeapEntry)
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	rel := pgSequenceRel(ctx)
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(e.tid.Block)})
	if err != nil {
		return
	}
	slot.Lock()
	ht, herr := storage.PageGetHeapTuple(slot.Page(), e.tid.Offset)
	if herr != nil || ht.Header.Xmax != storage.InvalidTransactionID {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		seqHeapTIDs.Delete(key)
		return
	}
	oldTuple, merr := ht.MarshalBinary()
	if merr != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return
	}
	xmax := effectiveWriterXID(ctx)
	if err := storage.PageSetHeapTupleXmax(slot.Page(), e.tid.Offset, xmax); err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return
	}
	_ = markHeapDeleteDirty(ctx.Pool, slot, rel, storage.BlockNumber(e.tid.Block), e.tid.Offset, xmax, oldTuple)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	seqHeapTIDs.Delete(key)
	_ = mirrorProcCatalogFiles(ctx)
}

// SeedSequenceHeapTID is the startup reload hook (initdb): records the live
// pg_sequence row TID for a restored sequence so post-restart ALTERs update
// in place instead of inserting duplicates.
func SeedSequenceHeapTID(name string, dbOid uint32, seqrelid uint32, tid catalog.SchemaHeapTID, p wal.SequenceStatePayload) {
	seqHeapTIDs.Store(seqKey(name, resolveSeqDBOid([]uint32{dbOid})), seqHeapEntry{tid: tid, defSig: seqDefSig(seqrelid, p)})
}