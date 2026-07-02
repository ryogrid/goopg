package wal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestCanonicalHeapInsertWALRoundTrip verifies that a PG-canonical
// XLOG_HEAP_INSERT record with FPI can be written to a WAL segment,
// read back, decoded, and replayed via ApplyRecord to restore the page.
func TestCanonicalHeapInsertWALRoundTrip(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: pickPageSegSize,
		PageHeaders: true,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	rel := storage.RelFileNode{DBOid: 5, RelOid: 1234, Fork: storage.MainFork}
	// Build a page with one tuple.
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte{0x01, 0x02, 0x03, 0x04})
	slot, err := storage.PageAddHeapTuple(page, tup)
	if err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}
	t.Logf("tuple inserted at slot %d", slot)

	// Emit canonical XLOG_HEAP_INSERT.
	payload := catalog.BuildCanonicalHeapInsertPayload(rel, 0, page, slot, 7)
	if payload[0] != RecordKindCanonical {
		t.Fatalf("payload[0]=0x%02x, want 0xFE", payload[0])
	}
	_, end, err := w.Append(payload)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	t.Logf("canonical INSERT WAL written, EndLSN=%d", end)

	it, err := NewRecordIterator(w, walDir, pickPageSegSize, 0)
	if err != nil {
		t.Fatalf("NewRecordIterator: %v", err)
	}
	defer it.Close()

	ctx := context.Background()
	rec, err := it.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	t.Logf("decoded: Payload=%v XLog=%v", rec.Payload == nil, rec.XLog != nil)
	if rec.XLog == nil {
		t.Fatal("rec.XLog is nil — PageHeaders mode should produce XLog")
	}
	if len(rec.Payload) != 0 {
		t.Fatalf("rec.Payload should be nil for FPI record, got len=%d (first byte=0x%02x)", len(rec.Payload), rec.Payload[0])
	}
	if len(rec.XLog.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(rec.XLog.Blocks))
	}
	block := rec.XLog.Blocks[0]
	t.Logf("block: HasImage=%v ImageApply=%v", block.HasImage, block.ImageApply)
	if !block.HasImage || !block.ImageApply {
		t.Fatal("block should have FPI with apply=true")
	}

	// Apply the record to a fresh storage manager.
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	applied, err := ApplyRecord(mgr, rec)
	if err != nil {
		t.Fatalf("ApplyRecord: %v", err)
	}
	if !applied {
		t.Fatal("ApplyRecord applied=false, want true")
	}

	// Read back and verify the tuple is present.
	gotPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, gotPage); err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	gotTup, err := storage.PageGetHeapTuple(gotPage, slot)
	if err != nil {
		t.Fatalf("PageGetHeapTuple slot=%d: %v", slot, err)
	}
	if gotTup.Header.Xmin != 7 {
		t.Fatalf("xmin = %d, want 7", gotTup.Header.Xmin)
	}
	t.Logf("tuple verified: xmin=%d data=%x", gotTup.Header.Xmin, gotTup.Data)
}

// TestCanonicalHeapInplaceWALRoundTrip mirrors
// TestCanonicalHeapInsertWALRoundTrip for the XLOG_HEAP_INPLACE record
// (M0117-0008 Part B, pg_database.datfrozenxid). Uses DBOid 0 — the
// shared-catalog sentinel — so the round trip also exercises
// storage.Manager's global/ path resolution rather than base/<dbOid>/.
func TestCanonicalHeapInplaceWALRoundTrip(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: pickPageSegSize,
		PageHeaders: true,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	rel := catalog.SharedCatalogRelFileNode(catalog.PgDatabaseRelationOID)
	if rel.DBOid != 0 {
		t.Fatalf("SharedCatalogRelFileNode.DBOid = %d, want 0", rel.DBOid)
	}

	// Build a page with one "before" tuple, then mutate it in place
	// (same-length overwrite) to model the datfrozenxid advance.
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	before := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte{3, 0, 0, 0})
	slot, err := storage.PageAddHeapTuple(page, before)
	if err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}
	after := storage.HeapTuple{Header: before.Header, Data: []byte{200, 0, 0, 0}}
	raw, err := after.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if err := storage.PageReplaceItemRaw(page, slot, raw); err != nil {
		t.Fatalf("PageReplaceItemRaw: %v", err)
	}

	// Emit canonical XLOG_HEAP_INPLACE with the post-overwrite page image.
	payload := catalog.BuildCanonicalHeapInplacePayload(rel, 0, page, slot, 7)
	if payload[0] != RecordKindCanonical {
		t.Fatalf("payload[0]=0x%02x, want 0xFE", payload[0])
	}
	if _, _, err := w.Append(payload); err != nil {
		t.Fatalf("Append: %v", err)
	}

	it, err := NewRecordIterator(w, walDir, pickPageSegSize, 0)
	if err != nil {
		t.Fatalf("NewRecordIterator: %v", err)
	}
	defer it.Close()

	rec, err := it.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if rec.XLog == nil {
		t.Fatal("rec.XLog is nil — PageHeaders mode should produce XLog")
	}

	// Apply to a fresh standby-style manager and confirm the mutated
	// tuple bytes land at global/1262, not base/0/1262.
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	applied, err := ApplyRecord(mgr, rec)
	if err != nil {
		t.Fatalf("ApplyRecord: %v", err)
	}
	if !applied {
		t.Fatal("ApplyRecord applied=false, want true")
	}

	globalPath := filepath.Join(dataDir, "global", "1262")
	if _, err := os.Stat(globalPath); err != nil {
		t.Fatalf("expected %s to exist after replay: %v", globalPath, err)
	}

	gotPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, gotPage); err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	gotTup, err := storage.PageGetHeapTuple(gotPage, slot)
	if err != nil {
		t.Fatalf("PageGetHeapTuple slot=%d: %v", slot, err)
	}
	if len(gotTup.Data) != 4 || gotTup.Data[0] != 200 {
		t.Fatalf("replayed tuple data = %x, want overwritten value with data[0]=200", gotTup.Data)
	}
	t.Logf("in-place update verified at global/1262: data=%x", gotTup.Data)
}
