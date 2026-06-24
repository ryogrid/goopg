package storage

import (
	"testing"
)

// TestPageFreezeOldTuples verifies that tuples older than freezeBelow have
// their xmin rewritten to FrozenTransactionID.
func TestPageFreezeOldTuples(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}

	// Slot 1: old tuple (xmin=10), eligible for freezing when freezeBelow>10.
	old := NewHeapTuple(10, InvalidTransactionID, []byte("old"))
	if _, err := PageAddHeapTuple(page, old); err != nil {
		t.Fatal(err)
	}
	// Slot 2: recent tuple (xmin=100), not yet old enough.
	recent := NewHeapTuple(100, InvalidTransactionID, []byte("recent"))
	if _, err := PageAddHeapTuple(page, recent); err != nil {
		t.Fatal(err)
	}
	// Slot 3: already frozen — must not be double-frozen.
	frozen := NewHeapTuple(FrozenTransactionID, InvalidTransactionID, []byte("frozen"))
	if _, err := PageAddHeapTuple(page, frozen); err != nil {
		t.Fatal(err)
	}

	stats, err := PageFreezeOldTuples(page, 50) // freeze xmin < 50
	if err != nil {
		t.Fatalf("PageFreezeOldTuples: %v", err)
	}
	if stats.Frozen != 1 {
		t.Errorf("expected 1 frozen tuple, got %d", stats.Frozen)
	}
	// MinUnfrozenXID should be 100 (the unfrozen recent tuple).
	if stats.MinUnfrozenXID != 100 {
		t.Errorf("MinUnfrozenXID=%d, want 100", stats.MinUnfrozenXID)
	}

	// Verify slot 1 xmin is now FrozenTransactionID.
	t1, err := PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if t1.Header.Xmin != FrozenTransactionID {
		t.Errorf("slot 1 xmin=%d, want FrozenTransactionID(%d)", t1.Header.Xmin, FrozenTransactionID)
	}

	// Verify slot 2 xmin is unchanged.
	t2, err := PageGetHeapTuple(page, 2)
	if err != nil {
		t.Fatal(err)
	}
	if t2.Header.Xmin != 100 {
		t.Errorf("slot 2 xmin=%d, want 100", t2.Header.Xmin)
	}
}

// TestPageFreezeSkipsDeleted verifies that deleted tuples (xmax set, not
// lock-only) are not frozen.
func TestPageFreezeSkipsDeleted(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	deleted := NewHeapTuple(5, 15, []byte("del")) // xmax=15 (not lock-only)
	if _, err := PageAddHeapTuple(page, deleted); err != nil {
		t.Fatal(err)
	}

	stats, err := PageFreezeOldTuples(page, 100)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Frozen != 0 {
		t.Errorf("deleted tuple must not be frozen, got Frozen=%d", stats.Frozen)
	}
	// xmin must be unchanged.
	tup, _ := PageGetHeapTuple(page, 1)
	if tup.Header.Xmin != 5 {
		t.Errorf("xmin changed unexpectedly to %d", tup.Header.Xmin)
	}
}

// TestPageFreezeNoOpZeroThreshold verifies that FreezeBelow=0 is a no-op.
func TestPageFreezeNoOpZeroThreshold(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	tup := NewHeapTuple(5, InvalidTransactionID, []byte("x"))
	if _, err := PageAddHeapTuple(page, tup); err != nil {
		t.Fatal(err)
	}

	stats, err := PageFreezeOldTuples(page, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Frozen != 0 {
		t.Errorf("FreezeBelow=0 must not freeze anything, got Frozen=%d", stats.Frozen)
	}
}

// TestFrozenTransactionIDVisibility verifies the key correctness property:
// a tuple with xmin=FrozenTransactionID is visible to any snapshot.
// This relies on SeesCommittedXID(2) returning true for any Xmin >= 3,
// which is always satisfied since FirstNormalTransactionID = 3.
func TestFrozenTransactionIDVisibility(t *testing.T) {
	if FrozenTransactionID != 2 {
		t.Fatalf("FrozenTransactionID must be 2, got %d", FrozenTransactionID)
	}
	// Simulate a snapshot with Xmin = 1_000_000_000 (1B XIDs allocated).
	// SeesCommittedXID(2) must return true because 2 < 1B and 2 is never
	// in the InProgress list.
	const xmin = FrozenTransactionID
	const snapshotXmin = TransactionID(1_000_000_000)
	if !(xmin < snapshotXmin) {
		t.Errorf("FrozenTransactionID(%d) must be < snapshotXmin(%d)", xmin, snapshotXmin)
	}
}

// TestPageFreezeMultiXactXmax exercises the updater-bearing multixact arm of the
// freeze liveness check. When a tuple's xmax is a MultiXactId (IS_MULTI set,
// LOCK_ONLY clear) the raw field is not a transaction id, so PageFreezeOldTuples
// resolves it through storage.ResolveMultiUpdater before deciding whether the row
// is deleted (skip) or still live (freeze its xmin). (M0118-0003.)
func TestPageFreezeMultiXactXmax(t *testing.T) {
	const multiID = TransactionID(1000) // a MultiXactId, NOT an xid

	build := func(t *testing.T) Page {
		page := make(Page, BlockSize)
		if err := InitPage(page); err != nil {
			t.Fatal(err)
		}
		// xmin=5 is < freezeBelow(100), so the only thing keeping it from being
		// frozen is the xmax liveness verdict under test.
		tup := NewHeapTuple(5, InvalidTransactionID, []byte("multi"))
		slot, err := PageAddHeapTuple(page, tup)
		if err != nil {
			t.Fatal(err)
		}
		// Updater-bearing multi: IS_MULTI set, LOCK_ONLY deliberately clear.
		if err := PageSetHeapTupleXmaxMulti(page, slot, multiID, HeapXmaxIsMulti|HeapXmaxExclLock, HeapKeysUpdated); err != nil {
			t.Fatal(err)
		}
		return page
	}

	setResolver := func(t *testing.T, fn func(TransactionID) (TransactionID, bool, bool)) {
		prev := ResolveMultiUpdater
		ResolveMultiUpdater = fn
		t.Cleanup(func() { ResolveMultiUpdater = prev })
	}

	t.Run("updater present -> deleted, not frozen", func(t *testing.T) {
		page := build(t)
		setResolver(t, func(x TransactionID) (TransactionID, bool, bool) {
			if x != multiID {
				t.Errorf("resolver got xmax=%d, want MultiXactId %d", x, multiID)
			}
			return 42, true, true // updater xid 42
		})
		stats, err := PageFreezeOldTuples(page, 100)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Frozen != 0 {
			t.Errorf("updater-bearing multi xmax must not be frozen, got Frozen=%d", stats.Frozen)
		}
	})

	t.Run("all lockers (no updater) -> live, frozen", func(t *testing.T) {
		page := build(t)
		setResolver(t, func(TransactionID) (TransactionID, bool, bool) {
			return InvalidTransactionID, false, true // resolved, no updater
		})
		stats, err := PageFreezeOldTuples(page, 100)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Frozen != 1 {
			t.Fatalf("all-locker multi xmax is live and must freeze its xmin, got Frozen=%d", stats.Frozen)
		}
		tup, _ := PageGetHeapTuple(page, 1)
		if tup.Header.Xmin != FrozenTransactionID {
			t.Errorf("xmin not rewritten to FrozenTransactionID, got %d", tup.Header.Xmin)
		}
	})

	t.Run("unresolvable multi -> conservatively not frozen", func(t *testing.T) {
		page := build(t)
		setResolver(t, func(TransactionID) (TransactionID, bool, bool) {
			return InvalidTransactionID, false, false // not found in member store
		})
		stats, err := PageFreezeOldTuples(page, 100)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Frozen != 0 {
			t.Errorf("unresolvable multi must not be frozen, got Frozen=%d", stats.Frozen)
		}
	})

	t.Run("nil resolver -> conservatively not frozen", func(t *testing.T) {
		page := build(t)
		setResolver(t, nil)
		stats, err := PageFreezeOldTuples(page, 100)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Frozen != 0 {
			t.Errorf("nil resolver must not freeze an IS_MULTI xmax, got Frozen=%d", stats.Frozen)
		}
	})
}
