package mvcc

import (
	"testing"

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
			if got := TupleVisible(tc.h, snap, current); got != tc.want {
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
	if !TupleVisible(visibleOwnInsert, snap, current) {
		t.Fatal("own insert should be visible")
	}

	deletedOwnInsert := storage.HeapTupleHeader{
		Xmin: current,
		Xmax: current,
	}
	if TupleVisible(deletedOwnInsert, snap, current) {
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
			if !TupleVisible(tc.h, snap, current) {
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
	if TupleVisible(deletedNoLockBit, snap, current) {
		t.Errorf("plain committed delete should be invisible; header=%+v", deletedNoLockBit)
	}
}
