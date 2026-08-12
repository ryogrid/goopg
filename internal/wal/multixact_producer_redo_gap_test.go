package wal

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S24: the producer-side multixact redo gap -------------------------
//
// S24 (durable pg_multixact SLRU + multixact_redo) is DEFERRED out of M0131 —
// the re-arm trigger is the skipped
// TestE2E_GoopgCrashStartOnPGDataDir_Concurrent. That deferral is about
// *consuming* a PG stream's RM_MULTIXACT records.
//
// This file pins the separate, already-live half that the deferral does NOT
// cover: goopg's OWN crash recovery loses a multixact xmax it produced itself.
//
// M0118-0004/-0009 taught the UPDATE/DELETE producers to preserve a concurrent
// non-conflicting locker by stamping the old tuple's xmax with an
// {updater + surviving lockers} MultiXactId
// (executor.stampUpdaterXmaxNonHOT → storage.PageSetHeapTupleXmaxMulti), and to
// carry lock-only lockers onto the new version
// (executor.carryForwardLockersToNewTuple). Both stamps ride an ordinary
// WAL-logged delete/update — but the record they ride carries
// `xmax = effectiveWriterXID(ctx)` (a plain xid; operators_storage.go's five
// markHeapDeleteDirtyAndClearVM call sites) with `infobits_set = 0` hardcoded
// (EncodeHeapDeletePG / EncodeHeapUpdatePG). Upstream's XLHL_XMAX_IS_MULTI is
// exactly the byte that would say otherwise (S21h taught redo to honour it, but
// no goopg emitter sets it).
//
// So redo faithfully reproduces what the record says and not what the page
// said: a plain single-xid xmax with HEAP_XMAX_IS_MULTI cleared. Recovery does
// not fail — it silently drops the preserved lockers, and the multixact-
// no-forget property M0118-0009 exists to uphold does not survive a crash.
//
// These tests assert the CURRENT (defective) behaviour deliberately, so that
// the day an emitter learns to set XLHL_XMAX_IS_MULTI they fail and force the
// author to re-read this comment. They are the executable form of the S24
// producer-side ledger row.

// TestGoopgHeapDeleteRedoDropsProducedMultiXmax: the old-tuple half. A page
// tuple stamped with a {updater + locker} multi, logged the only way goopg logs
// a delete, comes back from redo as a bare xid.
func TestGoopgHeapDeleteRedoDropsProducedMultiXmax(t *testing.T) {
	mgr, rel := seedHeapTupleForRedo(t, 42, "row")

	// What the executor writes to the page: xmax is a MultiXactId naming the
	// updater plus a surviving FOR KEY SHARE locker.
	const producedMulti = uint32(7)
	const writerXID = uint32(99)
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	if err := storage.PageSetHeapTupleXmaxMulti(page, 1, storage.TransactionID(producedMulti),
		storage.HeapXmaxIsMulti|storage.HeapXmaxKeyShrLock, storage.HeapKeysUpdated); err != nil {
		t.Fatal(err)
	}
	if err := mgr.WriteBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}

	// What the executor writes to WAL for that same mutation: the plain writer
	// xid, infobits_set = 0.
	framed, err := EncodeHeapDeletePG(rel, 0, 1, storage.TransactionID(writerXID), nil)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 200)

	_, after := readRedoTuple(t, mgr, rel, 1)
	if after.Header.Xmax != storage.TransactionID(writerXID) {
		t.Fatalf("t_xmax = %d, want the plain writer xid %d — if this is now the "+
			"MultiXactId %d the emit side learned XLHL_XMAX_IS_MULTI; re-read the "+
			"file comment and close the M0131-S24 producer-side ledger row",
			after.Header.Xmax, writerXID, producedMulti)
	}
	if storage.IsHeapTupleXmaxMulti(after.Header.Infomask) {
		t.Fatalf("HEAP_XMAX_IS_MULTI survived redo (infomask=%#x) — see above", after.Header.Infomask)
	}
	if after.Header.Infomask&storage.HeapXmaxKeyShrLock != 0 {
		t.Fatalf("the preserved FOR KEY SHARE locker's strength bit survived redo: infomask=%#x",
			after.Header.Infomask)
	}
}

// TestGoopgHeapUpdateRedoDropsCarriedForwardLockers: the new-tuple half.
// carryForwardLockersToNewTuple stamps the *successor* version with a lock-only
// multi so an inherited FOR KEY SHARE outlives the updater's commit. The update
// record carries the new tuple's bytes, so whatever xmax those bytes hold is
// what redo lands — and the tuple image the executor hands the logger is built
// before the carry-forward stamp, i.e. with xmax = 0.
func TestGoopgHeapUpdateRedoDropsCarriedForwardLockers(t *testing.T) {
	mgr, rel := seedHeapTupleForRedo(t, 42, "old")

	// The new version as the logger sees it: a fresh tuple, no xmax yet.
	newTup := storage.NewHeapTuple(100, storage.InvalidTransactionID, []byte("new"))
	newTup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2}
	newBytes, err := newTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	framed, err := EncodeHeapUpdatePG(rel, 0, 1, 0, 2, 100, newBytes)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 200)

	_, after := readRedoTuple(t, mgr, rel, 2)
	if storage.IsHeapTupleXmaxMulti(after.Header.Infomask) {
		t.Fatalf("the successor version came back from redo carrying a lock-only "+
			"multi (infomask=%#x) — carryForwardLockersToNewTuple is now WAL-visible; "+
			"close the M0131-S24 producer-side ledger row", after.Header.Infomask)
	}
	if after.Header.Xmax != storage.InvalidTransactionID {
		t.Fatalf("successor t_xmax = %d, want 0 (inherited lockers are not logged)", after.Header.Xmax)
	}
}
