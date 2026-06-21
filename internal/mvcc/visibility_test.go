package mvcc

import (
	"testing"

	"github.com/goopg/goopg/internal/multixact"
	"github.com/goopg/goopg/internal/storage"
)

func TestTupleVisibleBasicCases(t *testing.T) {
	snap := Snapshot{
		Xmin:       storage.TransactionID(10),
		Xmax:       storage.TransactionID(20),
		InProgress: []storage.TransactionID{12, 15},
	}
	current := storage.TransactionID(18)

	tests := []struct {
		name string
		h    storage.HeapTupleHeader
		want bool
	}{
		{
			name: "committed insert not deleted",
			h: storage.HeapTupleHeader{
				Xmin: 8,
				Xmax: storage.InvalidTransactionID,
			},
			want: true,
		},
		{
			name: "insert in progress invisible",
			h: storage.HeapTupleHeader{
				Xmin: 12,
				Xmax: storage.InvalidTransactionID,
			},
			want: false,
		},
		{
			name: "future insert invisible",
			h: storage.HeapTupleHeader{
				Xmin: 30,
				Xmax: storage.InvalidTransactionID,
			},
			want: false,
		},
		{
			name: "committed delete invisible",
			h: storage.HeapTupleHeader{
				Xmin: 8,
				Xmax: 9,
			},
			want: false,
		},
		{
			name: "delete in progress still visible",
			h: storage.HeapTupleHeader{
				Xmin: 8,
				Xmax: 12,
			},
			want: true,
		},
		{
			name: "future delete still visible",
			h: storage.HeapTupleHeader{
				Xmin: 8,
				Xmax: 22,
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TupleVisible(tc.h, snap, current, nil); got != tc.want {
				t.Fatalf("TupleVisible=%v want=%v header=%+v", got, tc.want, tc.h)
			}
		})
	}
}

func TestTupleVisibleOwnXIDRules(t *testing.T) {
	current := storage.TransactionID(42)
	snap := Snapshot{
		Xmin:       40,
		Xmax:       50,
		InProgress: []storage.TransactionID{42},
	}

	visibleOwnInsert := storage.HeapTupleHeader{
		Xmin: current,
		Xmax: storage.InvalidTransactionID,
	}
	if !TupleVisible(visibleOwnInsert, snap, current, nil) {
		t.Fatal("own insert should be visible")
	}

	deletedOwnInsert := storage.HeapTupleHeader{
		Xmin: current,
		Xmax: current,
	}
	if TupleVisible(deletedOwnInsert, snap, current, nil) {
		t.Fatal("tuple deleted by current transaction should be invisible")
	}
}

// TestTupleVisibleLockOnlyXmax pins the M0021 follow-up step 1
// rule: when xmax has HEAP_XMAX_LOCK_ONLY set, the tuple is
// visible regardless of xmax's progress (committed / aborted /
// in-progress). Without this rule, a SELECT FOR UPDATE that
// stamps a row's xmax would invisibly delete the row from
// concurrent readers — pessimistic locking must not destroy
// visibility.
func TestTupleVisibleLockOnlyXmax(t *testing.T) {
	snap := Snapshot{
		Xmin:       10,
		Xmax:       20,
		InProgress: []storage.TransactionID{15},
	}
	current := storage.TransactionID(18)

	cases := []struct {
		name string
		h    storage.HeapTupleHeader
	}{
		{
			name: "committed-deleter xmax with LOCK_ONLY → visible (it's a lock, not a delete)",
			h: storage.HeapTupleHeader{
				Xmin:     8,
				Xmax:     9, // would be invisible without LOCK_ONLY (committed delete)
				Infomask: storage.HeapXmaxLockOnly | storage.HeapXmaxExclLock,
			},
		},
		{
			name: "in-progress xmax with LOCK_ONLY → visible (lock holder still alive)",
			h: storage.HeapTupleHeader{
				Xmin:     8,
				Xmax:     15,
				Infomask: storage.HeapXmaxLockOnly | storage.HeapXmaxExclLock,
			},
		},
		{
			name: "future xmax with LOCK_ONLY → visible",
			h: storage.HeapTupleHeader{
				Xmin:     8,
				Xmax:     30,
				Infomask: storage.HeapXmaxLockOnly | storage.HeapXmaxKeyShrLock,
			},
		},
		{
			name: "self-locked tuple → still visible to ourselves (Xmin=cur, Xmax=cur, LOCK_ONLY)",
			h: storage.HeapTupleHeader{
				Xmin:     current,
				Xmax:     current,
				Infomask: storage.HeapXmaxLockOnly | storage.HeapXmaxExclLock,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !TupleVisible(tc.h, snap, current, nil) {
				t.Errorf("TupleVisible=false, want true; header=%+v", tc.h)
			}
		})
	}
}

// TestTupleVisibleNonLockXmaxRegression — sanity check that
// adding the LOCK_ONLY branch didn't accidentally also let
// real (non-LOCK_ONLY) committed deletes through. Pins the
// "delete still hides the tuple" property.
func TestTupleVisibleNonLockXmaxRegression(t *testing.T) {
	snap := Snapshot{
		Xmin:       10,
		Xmax:       20,
		InProgress: nil,
	}
	current := storage.TransactionID(18)

	deletedNoLockBit := storage.HeapTupleHeader{
		Xmin: 8,
		Xmax: 9, // committed deleter, no LOCK_ONLY infomask
	}
	if TupleVisible(deletedNoLockBit, snap, current, nil) {
		t.Errorf("plain committed delete should be invisible; header=%+v", deletedNoLockBit)
	}
}

// TestTupleVisibleMultiXact pins the read-consumer half of the
// updater-bearing MultiXact wiring (M0118-0003, read-consumer slice 2).
// When a tuple's xmax has HEAP_XMAX_IS_MULTI set and LOCK_ONLY clear, the
// raw h.Xmax is a MultiXactId (a row updated under a shared lock: one
// updater plus one or more lockers), NOT a transaction id. TupleVisible
// must resolve the updater member through the MultiXact store and judge
// visibility against that real updater xid — mirroring upstream
// HeapTupleSatisfiesMVCC's HEAP_XMAX_IS_MULTI handling and the executor's
// isConcurrentlyUpdated twin (sibling-paths rule).
func TestTupleVisibleMultiXact(t *testing.T) {
	// xid < 10 is seen as committed; 15 is in-progress; >= 20 is in the future.
	snap := Snapshot{
		Xmin:       10,
		Xmax:       20,
		InProgress: []storage.TransactionID{15},
	}
	const current = storage.TransactionID(18)

	store := multixact.NewStore()
	// Each multi pairs a shared locker with one updater of varying liveness.
	updCommitted, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: 9, Status: multixact.StatusNoKeyUpdate}, // committed updater
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(committed): %v", err)
	}
	updInProgress, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: 15, Status: multixact.StatusNoKeyUpdate}, // in-progress updater
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(in-progress): %v", err)
	}
	updFuture, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: 30, Status: multixact.StatusUpdate}, // future updater
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(future): %v", err)
	}
	updSelf, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: current, Status: multixact.StatusUpdate}, // our own xact updated it
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(self): %v", err)
	}
	// Only lockers, no updater. Such a multi should normally carry LOCK_ONLY;
	// the bit is deliberately left clear to exercise the defensive
	// "no resolvable updater → treat as a pure lock" branch.
	lockersOnly, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: 6, Status: multixact.StatusForKeyShare},
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(lockers only): %v", err)
	}

	// mk builds a header whose creator is long-committed (xmin=8) so the scan
	// reaches the xmax logic, with an updater-bearing multi xmax.
	mk := func(multi multixact.MultiXactId) storage.HeapTupleHeader {
		return storage.HeapTupleHeader{
			Xmin:     8,
			Xmax:     storage.TransactionID(multi),
			Infomask: storage.HeapXmaxIsMulti, // IS_MULTI set, LOCK_ONLY clear
		}
	}

	cases := []struct {
		name string
		h    storage.HeapTupleHeader
		mxs  *multixact.Store
		want bool
	}{
		{"committed updater → invisible", mk(updCommitted), store, false},
		{"in-progress updater → visible", mk(updInProgress), store, true},
		{"future updater → visible", mk(updFuture), store, true},
		{"our own updater → invisible", mk(updSelf), store, false},
		{"lockers only (no updater) → visible", mk(lockersOnly), store, true},
		{"store unavailable → invisible (conservative)", mk(updCommitted), nil, false},
		{"multi absent from store → invisible (conservative)", mk(9999), store, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TupleVisible(c.h, snap, current, c.mxs); got != c.want {
				t.Errorf("TupleVisible = %v, want %v; header=%+v", got, c.want, c.h)
			}
		})
	}
}
