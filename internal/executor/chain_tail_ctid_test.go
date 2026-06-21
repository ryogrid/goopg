package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestIsChainTailCTID guards the t_ctid chain-tail termination logic shared by
// epqFollowChainFull and lockRowsOp.stampLockInner. The InvalidBlockNumber and
// zero-offset cases are the regression for the "short read at block" crash
// (M0118-0004 tuplelock-upgrade-no-deadlock perm 5): a goopg DELETE leaves the
// original {InvalidBlockNumber,0} CTID in place, and FOR UPDATE following a
// committed delete must treat it as a tail rather than Pinning a non-existent
// block.
func TestIsChainTailCTID(t *testing.T) {
	const (
		curBlk  storage.BlockNumber = 0
		curSlot uint16              = 1
	)
	cases := []struct {
		name string
		ctid storage.ItemPointer
		want bool
	}{
		{
			name: "goopg initial / deleted sentinel {InvalidBlockNumber,0}",
			ctid: storage.ItemPointer{Block: storage.InvalidBlockNumber, Offset: 0},
			want: true,
		},
		{
			name: "invalid block, nonzero offset",
			ctid: storage.ItemPointer{Block: storage.InvalidBlockNumber, Offset: 3},
			want: true,
		},
		{
			name: "valid block, zero offset",
			ctid: storage.ItemPointer{Block: 5, Offset: 0},
			want: true,
		},
		{
			name: "self-pointing (latest version)",
			ctid: storage.ItemPointer{Block: curBlk, Offset: curSlot},
			want: true,
		},
		{
			name: "live successor on another block",
			ctid: storage.ItemPointer{Block: 2, Offset: 4},
			want: false,
		},
		{
			name: "live successor on same block, different slot",
			ctid: storage.ItemPointer{Block: curBlk, Offset: curSlot + 1},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isChainTailCTID(tc.ctid, curBlk, curSlot); got != tc.want {
				t.Errorf("isChainTailCTID(%+v, %d, %d) = %v, want %v",
					tc.ctid, curBlk, curSlot, got, tc.want)
			}
		})
	}
}
