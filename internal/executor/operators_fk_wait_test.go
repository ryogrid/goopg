package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEpqChainCheckMovedPartitionDirectSentinel — the simplest case:
// the recorded slot itself carries the cross-partition sentinel
// (single in-xact UPDATE that moved the row).  Mirrors the
// non-trigger leg of partition-key-update-1 (`s1u3pc` only).
// M0100-0005q.
func TestEpqChainCheckMovedPartitionDirectSentinel(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 2001}
	ctx := newPoolBackedContext(t, rel)

	pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	pin.Lock()
	tup := storage.NewHeapTuple(storage.TransactionID(100), storage.InvalidTransactionID, []byte("v"))
	slot, err := storage.PageAddHeapTuple(pin.Page(), tup)
	if err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}
	if err := storage.PageSetHeapTupleMovedPartition(pin.Page(), slot, storage.TransactionID(42)); err != nil {
		t.Fatalf("PageSetHeapTupleMovedPartition: %v", err)
	}
	pin.Unlock()
	ctx.Pool.Unpin(pin)

	if !epqChainCheckMovedPartition(ctx, rel, 0, slot) {
		t.Fatal("epqChainCheckMovedPartition = false on direct-sentinel slot")
	}
}

// TestEpqChainCheckMovedPartitionViaFallbackScan — non-HOT-style
// chain: the recorded slot's t_ctid is NOT updated (goopg's non-HOT
// UPDATE only stamps xmax, leaving t_ctid as-inserted), but a
// separate slot on the SAME relation carries the sentinel and shares
// the recorded slot's xmax.  The fallback scan in
// epqChainCheckMovedPartition must surface this.  Mirrors the
// `s1u3npc s1u3pc s2i` permutation where the first (in-partition)
// UPDATE leaves the original slot with xmax but no CTID redirect,
// and the second (cross-partition) UPDATE stamps the sentinel on
// the intermediate slot it just created.  M0100-0005q.
func TestEpqChainCheckMovedPartitionViaFallbackScan(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 2002}
	ctx := newPoolBackedContext(t, rel)

	const updaterXID storage.TransactionID = 77

	pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	pin.Lock()
	// Original slot: xmax stamped by updaterXID, CTID NOT updated
	// (the non-HOT goopg UPDATE path doesn't touch CTID).
	origTup := storage.NewHeapTuple(storage.TransactionID(50), storage.InvalidTransactionID, []byte("orig"))
	origSlot, err := storage.PageAddHeapTuple(pin.Page(), origTup)
	if err != nil {
		t.Fatalf("PageAddHeapTuple(orig): %v", err)
	}
	if err := storage.PageSetHeapTupleXmax(pin.Page(), origSlot, updaterXID); err != nil {
		t.Fatalf("PageSetHeapTupleXmax(orig): %v", err)
	}
	// Intermediate slot: created by the first (in-partition) UPDATE,
	// then stamped with sentinel by the second (cross-partition) UPDATE.
	midTup := storage.NewHeapTuple(updaterXID, storage.InvalidTransactionID, []byte("mid"))
	midSlot, err := storage.PageAddHeapTuple(pin.Page(), midTup)
	if err != nil {
		t.Fatalf("PageAddHeapTuple(mid): %v", err)
	}
	if err := storage.PageSetHeapTupleMovedPartition(pin.Page(), midSlot, updaterXID); err != nil {
		t.Fatalf("PageSetHeapTupleMovedPartition(mid): %v", err)
	}
	pin.Unlock()
	ctx.Pool.Unpin(pin)

	if !epqChainCheckMovedPartition(ctx, rel, 0, origSlot) {
		t.Fatal("epqChainCheckMovedPartition = false; fallback scan failed to find sibling sentinel slot stamped by same xmax")
	}
}

// TestEpqChainCheckMovedPartitionNoSentinel — control: no slot on
// the page carries the sentinel; expected false.  Guards against
// the fallback scan over-matching (e.g., on xmax alone).
func TestEpqChainCheckMovedPartitionNoSentinel(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 2003}
	ctx := newPoolBackedContext(t, rel)

	pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	pin.Lock()
	tup := storage.NewHeapTuple(storage.TransactionID(100), storage.InvalidTransactionID, []byte("v"))
	slot, err := storage.PageAddHeapTuple(pin.Page(), tup)
	if err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}
	// Plain xmax stamp — no sentinel anywhere.
	if err := storage.PageSetHeapTupleXmax(pin.Page(), slot, storage.TransactionID(42)); err != nil {
		t.Fatalf("PageSetHeapTupleXmax: %v", err)
	}
	pin.Unlock()
	ctx.Pool.Unpin(pin)

	if epqChainCheckMovedPartition(ctx, rel, 0, slot) {
		t.Fatal("epqChainCheckMovedPartition = true on plain-xmax slot; sentinel must be a hard requirement")
	}
}

// TestEpqChainCheckMovedPartitionFallbackIgnoresUnrelatedSentinel —
// the fallback scan filters by xmax; a sentinel-stamped tuple with
// a DIFFERENT xmax (an earlier, already-committed cross-partition
// UPDATE by a separate transaction) must NOT be reported as "the
// current updater moved this row".  Guards against
// cross-transaction false positives.
func TestEpqChainCheckMovedPartitionFallbackIgnoresUnrelatedSentinel(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 2004}
	ctx := newPoolBackedContext(t, rel)

	const updaterXID storage.TransactionID = 100
	const unrelatedXID storage.TransactionID = 200

	pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	pin.Lock()
	origTup := storage.NewHeapTuple(storage.TransactionID(50), storage.InvalidTransactionID, []byte("orig"))
	origSlot, err := storage.PageAddHeapTuple(pin.Page(), origTup)
	if err != nil {
		t.Fatalf("PageAddHeapTuple(orig): %v", err)
	}
	if err := storage.PageSetHeapTupleXmax(pin.Page(), origSlot, updaterXID); err != nil {
		t.Fatalf("PageSetHeapTupleXmax(orig): %v", err)
	}
	// A separate sentinel-stamped tuple from a DIFFERENT xact.
	otherTup := storage.NewHeapTuple(unrelatedXID, storage.InvalidTransactionID, []byte("other"))
	otherSlot, err := storage.PageAddHeapTuple(pin.Page(), otherTup)
	if err != nil {
		t.Fatalf("PageAddHeapTuple(other): %v", err)
	}
	if err := storage.PageSetHeapTupleMovedPartition(pin.Page(), otherSlot, unrelatedXID); err != nil {
		t.Fatalf("PageSetHeapTupleMovedPartition(other): %v", err)
	}
	pin.Unlock()
	ctx.Pool.Unpin(pin)

	if epqChainCheckMovedPartition(ctx, rel, 0, origSlot) {
		t.Fatal("epqChainCheckMovedPartition = true; must not surface a sentinel stamped by an unrelated xact")
	}
}
