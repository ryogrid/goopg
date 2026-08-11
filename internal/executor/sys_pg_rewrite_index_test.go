package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestWriteViewRewriteRowMaintainsPgRewriteIndexes is M0131-S5's component
// gate. `writeViewRewriteRow` used to write the _RETURN rule's pg_rewrite heap
// row and DISCARD the TID `writeHeapRowCanonical` returned, leaving both
// declared indexes empty:
//
//	2692 pg_rewrite_oid_index          btree(oid oid_ops)
//	2693 pg_rewrite_rel_rulename_index btree(ev_class oid_ops, rulename name_ops)
//
// 2693 is load-bearing for a real PG 18.3 hosted on a goopg catalog.
// `RelationBuildRuleLock` (postgres/src/backend/utils/cache/relcache.c:785-806)
// reaches a relation's rules ONLY through it, with `indexOK = true` as a hard
// constant — `systable_beginscan` (access/index/genam.c:397-401) takes the
// heap-scan branch only for indexOK=false / IgnoreSystemIndexes /
// ReindexIsProcessingIndex, none of which an ordinary backend hits. An empty
// 2693 therefore left `rd_rules == NULL`, the view kept `rd_tableam == NULL`,
// and the planner raised 42809 at optimizer/util/plancat.c:139-147 — even
// though `pg_get_viewdef` worked, because ruleutils.c reaches pg_rewrite over
// SPI (a planned seq-scan) instead.
//
// The assertions here are the cheap half of the guard; the expensive half is
// the promoted `SELECT count(*) FROM public.b5c_view` assertion on the
// promoted standby in TestE2E_FailoverGoopgToPG.
func TestWriteViewRewriteRowMaintainsPgRewriteIndexes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// syncTableToCatalogHeap touches the pg_class / pg_attribute btrees too;
	// stub every leaf-root it will insert into so the sync completes.
	for _, idxOID := range []uint32{
		pgClassOidIndexOID, pgClassRelnameNspIndexOID, pgAttributeRelidAttnumIndexOID,
		pgRewriteOidIndexOID, pgRewriteRelRulenameIndexOID,
	} {
		if err := setupStubSysBtree(ctx, idxOID, nil); err != nil {
			t.Fatalf("stub index %d: %v", idxOID, err)
		}
	}

	// A materialized view is the cheapest shape that reaches
	// writeViewRewriteRow: the SQL-text ev_action fallback needs no resolvable
	// base relation, and the index maintenance under test is independent of
	// which of the two ev_action forms the row carries.
	tbl := &catalog.Table{
		Schema:    "public",
		Name:      "b5c_mv",
		OID:       16500,
		IsMatView: true,
		ViewDef:   "SELECT client FROM public.bench_log WHERE client > 0",
		Columns: []catalog.Column{
			{Name: "client", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
		},
	}
	if err := syncTableToCatalogHeap(ctx, tbl); err != nil {
		t.Fatalf("syncTableToCatalogHeap: %v", err)
	}

	relTuples := readSysBtreeLeaf(t, ctx, pgRewriteRelRulenameIndexOID)
	if len(relTuples) != 1 {
		t.Fatalf("pg_rewrite_rel_rulename_index: got %d tuples, want 1", len(relTuples))
	}
	relTup := relTuples[0]
	// 80-byte (oid, NameData) key: MAXALIGN(8 + 4 + 64). buildIndexTupleOidNameKey.
	if len(relTup) != 80 {
		t.Errorf("pg_rewrite_rel_rulename_index tuple len = %d, want 80", len(relTup))
	}
	if got := binary.LittleEndian.Uint32(relTup[sysIndexTupleHoff : sysIndexTupleHoff+4]); got != tbl.OID {
		t.Errorf("pg_rewrite_rel_rulename_index: ev_class = %d, want %d", got, tbl.OID)
	}
	if got := trimNameDataBytes(relTup[sysIndexTupleHoff+4 : sysIndexTupleHoff+68]); got != viewRuleName {
		t.Errorf("pg_rewrite_rel_rulename_index: rulename = %q, want %q", got, viewRuleName)
	}

	oidTuples := readSysBtreeLeaf(t, ctx, pgRewriteOidIndexOID)
	if len(oidTuples) != 1 {
		t.Fatalf("pg_rewrite_oid_index: got %d tuples, want 1", len(oidTuples))
	}

	// Both leaves must point at the SAME live heap row, and that row must be
	// the _RETURN rule for this relation — a leaf whose TID resolves elsewhere
	// is exactly as useless to RelationBuildRuleLock as no leaf at all.
	relTID := indexTupleHeapTID(relTup)
	if oidTID := indexTupleHeapTID(oidTuples[0]); oidTID != relTID {
		t.Errorf("pg_rewrite index TIDs disagree: 2693 → %v, 2692 → %v", relTID, oidTID)
	}
	rel := storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgRewriteRelOID, Fork: storage.MainFork}
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: relTID.Block})
	if err != nil {
		t.Fatalf("pin pg_rewrite block %d (the TID the 2693 leaf points at): %v", relTID.Block, err)
	}
	defer ctx.Pool.Unpin(slot)
	ht, err := storage.PageGetHeapTuple(slot.Page(), relTID.Offset)
	if err != nil {
		t.Fatalf("pg_rewrite heap tuple at %v: %v", relTID, err)
	}
	if ht.Header.Xmax != storage.InvalidTransactionID {
		t.Errorf("pg_rewrite row at %v is dead (xmax=%d); the leaf must point at a LIVE row", relTID, ht.Header.Xmax)
	}
	if len(ht.Data) < pgRewriteEvClassOffset+4 {
		t.Fatalf("pg_rewrite tuple data too short: %d bytes", len(ht.Data))
	}
	gotEvClass := binary.LittleEndian.Uint32(ht.Data[pgRewriteEvClassOffset : pgRewriteEvClassOffset+4])
	if gotEvClass != tbl.OID {
		t.Errorf("heap row the 2693 leaf resolves to has ev_class = %d, want %d", gotEvClass, tbl.OID)
	}

	// M0131-S5.5 — the DROP/recreate cycle, VERIFIED rather than assumed.
	// stampViewRewriteRows (the deleteCatalogRowsForOID path) stamps xmax on the
	// old rule row and does NOT touch the leaves, so a re-created view leaves a
	// stale leaf beside the fresh one. That matches PG: systable_getnext fetches
	// the heap tuple through the index TID and applies the snapshot, so the dead
	// row filters out and RelationBuildRuleLock still sees exactly one rule.
	// What must hold on our side is that the FRESH row is indexed too — a
	// re-created view whose only leaf points at a dead row is invisible.
	stampViewRewriteRows(ctx, catalog.DefaultDBOid, tbl.OID, storage.TransactionID(9999))
	if err := syncTableToCatalogHeap(ctx, tbl); err != nil {
		t.Fatalf("syncTableToCatalogHeap (recreate): %v", err)
	}
	after := readSysBtreeLeaf(t, ctx, pgRewriteRelRulenameIndexOID)
	if len(after) != 2 {
		t.Fatalf("pg_rewrite_rel_rulename_index after recreate: got %d tuples, want 2 (stale + fresh)", len(after))
	}
	live := 0
	for i, tup := range after {
		tid := indexTupleHeapTID(tup)
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: tid.Block})
		if err != nil {
			t.Fatalf("pin pg_rewrite block %d for leaf %d: %v", tid.Block, i+1, err)
		}
		h, err := storage.PageGetHeapTuple(s.Page(), tid.Offset)
		ctx.Pool.Unpin(s)
		if err != nil {
			t.Fatalf("pg_rewrite heap tuple at %v (leaf %d): %v", tid, i+1, err)
		}
		if h.Header.Xmax == storage.InvalidTransactionID {
			live++
		}
	}
	if live != 1 {
		t.Errorf("after recreate: %d of %d 2693 leaves resolve to a LIVE pg_rewrite row, want exactly 1",
			live, len(after))
	}
}

// TestPgRewriteIndexesAreMirroredToPostgresDB pins M0131-S5.4. Runtime rule
// rows are written to base/<tableCatalogHeapDBOid>, but an attached PG
// connects `dbname=postgres` and reads base/5. Omitting either index from
// mirrorTouchedCatalogsToPostgresDB repeats blocker #8 (pg_index 2678/2679)
// exactly: the standby sees an EMPTY index and 2693 has no seq-scan fallback.
func TestPgRewriteIndexesAreMirroredToPostgresDB(t *testing.T) {
	want := map[uint32]string{
		pgRewriteRelRulenameIndexOID: "pg_rewrite_rel_rulename_index (2693)",
		pgRewriteOidIndexOID:         "pg_rewrite_oid_index (2692)",
	}
	for _, oid := range mirroredCatalogOIDs() {
		delete(want, oid)
	}
	for oid, name := range want {
		t.Errorf("%s (%d) is not in mirroredOIDs — a hosted PG reading base/5 would find it empty", name, oid)
	}
}

// indexTupleHeapTID decodes the ItemPointerData an IndexTuple leads with.
func indexTupleHeapTID(tup []byte) storage.ItemPointer {
	le := binary.LittleEndian
	blk := uint32(le.Uint16(tup[0:2]))<<16 | uint32(le.Uint16(tup[2:4]))
	return storage.ItemPointer{Block: storage.BlockNumber(blk), Offset: le.Uint16(tup[4:6])}
}
