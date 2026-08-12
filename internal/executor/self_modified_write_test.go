package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestIsSelfModifiedWrite guards the M0131-S32.1 TM_SelfModified guard shared by
// the index-scan and seq-scan UPDATE loops in operators_storage.go.
//
// The guard exists to stop one statement from stamping the same physical tuple
// twice after an EPQ chain-follow (the pgbench_branches "~10x over-applied"
// signature). It originally tested only `Xmax == myXID`, which over-matched two
// header shapes that upstream HeapTupleSatisfiesUpdate excludes *before* it ever
// compares the raw xmax (postgres/src/backend/access/heap/heapam_visibility.c):
//
//   - HEAP_XMAX_IS_LOCKED_ONLY → TM_BeingModified, not TM_SelfModified. This is
//     the M-NIGHTLY AI-20260813-005117-012 regression: after
//     `SELECT ... FOR UPDATE`, the tuple carries our own lock-only xmax, so a
//     following key-changing UPDATE (the only HOT-ineligible shape, hence the
//     only one reaching this guard) skipped the row and reported UPDATE 0.
//     Upstream isolation spec insert-conflict-do-update-4 permutations 1 and 2
//     are the end-to-end form.
//   - HEAP_XMAX_IS_MULTI → a raw Xmax holding a MultiXactId must never be
//     compared against a TransactionId; the id spaces are disjoint and a
//     numeric collision would silently skip an unrelated row.
func TestIsSelfModifiedWrite(t *testing.T) {
	const myXID storage.TransactionID = 700

	cases := []struct {
		name string
		hdr  storage.HeapTupleHeader
		want bool
	}{
		{
			name: "no xmax at all",
			hdr:  storage.HeapTupleHeader{Xmax: storage.InvalidTransactionID},
			want: false,
		},
		{
			name: "our own update/delete write intent",
			hdr:  storage.HeapTupleHeader{Xmax: myXID},
			want: true,
		},
		{
			name: "another transaction's write intent",
			hdr:  storage.HeapTupleHeader{Xmax: myXID + 1},
			want: false,
		},
		{
			// The AI-20260813-005117-012 regression.
			name: "our own FOR UPDATE lock (lock-only) is still updatable",
			hdr: storage.HeapTupleHeader{
				Xmax:     myXID,
				Infomask: storage.HeapXmaxLockOnly | storage.HeapXmaxExclLock,
			},
			want: false,
		},
		{
			name: "our own FOR KEY SHARE lock (lock-only) is still updatable",
			hdr: storage.HeapTupleHeader{
				Xmax:     myXID,
				Infomask: storage.HeapXmaxLockOnly | storage.HeapXmaxKeyShrLock,
			},
			want: false,
		},
		{
			// A MultiXactId numerically equal to our xid: different id spaces,
			// so this must not be read as our own write intent.
			name: "multixact xmax colliding numerically with our xid",
			hdr: storage.HeapTupleHeader{
				Xmax:     myXID,
				Infomask: storage.HeapXmaxIsMulti,
			},
			want: false,
		},
		{
			name: "lock-only multixact xmax",
			hdr: storage.HeapTupleHeader{
				Xmax:     myXID,
				Infomask: storage.HeapXmaxIsMulti | storage.HeapXmaxLockOnly,
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSelfModifiedWrite(tc.hdr, myXID); got != tc.want {
				t.Errorf("isSelfModifiedWrite(%+v, %d) = %v, want %v",
					tc.hdr, myXID, got, tc.want)
			}
		})
	}
}
