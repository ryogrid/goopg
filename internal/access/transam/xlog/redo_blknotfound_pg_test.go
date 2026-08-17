package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// M0131-S21f. Three PG-format redo families used to hand-roll their own
// "NBlocks + ReadBlock" prologue and turn a missing page into a hard error,
// which refuses the whole cluster start. Upstream reads every one of them with
// XLogReadBufferForRedo in RBM_NORMAL, where an absent or all-zero page is
// BLK_NOTFOUND and the redo routine simply does nothing (xlogutils.c:500-540).
// The page can only be missing because a LATER record in the same stream drops
// or truncates the relation, so the mutation is moot — refusing it turns an
// ordinary "created and dropped before the checkpoint" tail into an unstartable
// cluster.
//
// Each sub-test asserts BOTH halves of the contract: no error, AND the fork was
// not extended to reach the block (an insert-like record WOULD extend, and
// materialising an empty page here would fail "invalid lp" downstream).

func TestApplyRecordPGRedoSkipsAbsentPage(t *testing.T) {
	tests := []struct {
		name  string
		relID uint32
		build func(t *testing.T, rel storage.RelFileNode) []byte
	}{
		{
			// heap_xlog_delete: the record stamps xmax on an existing tuple.
			name:  "heap delete",
			relID: 5401,
			build: func(t *testing.T, rel storage.RelFileNode) []byte {
				framed, err := EncodeHeapDeletePG(rel, 9, 1, 77, nil)
				if err != nil {
					t.Fatal(err)
				}
				return framed
			},
		},
		{
			// heap_xlog_prune_freeze: redirects/reclaims slots in place.
			name:  "heap prune",
			relID: 5402,
			build: func(t *testing.T, rel storage.RelFileNode) []byte {
				framed, err := EncodeHeapPruneOptPG(rel, 9, [][2]uint16{{1, 3}}, []uint16{2})
				if err != nil {
					t.Fatal(err)
				}
				return framed
			},
		},
		{
			// btree_xlog_dedup — the representative of every edit-shaped btree
			// record, all of which go through replayExistingXLogBlock.
			name:  "btree dedup",
			relID: 5403,
			build: func(t *testing.T, rel storage.RelFileNode) []byte {
				return buildBtreeDedupPG(t, rel, 9, []nbtree.DedupInterval{{BaseOff: 1, NItems: 2}})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
			defer mgr.Close()
			rel := storage.RelFileNode{DBOid: 1, RelOid: tc.relID, Fork: storage.MainFork}
			// One real page at block 0 — so the fork exists and the skip is
			// decided by block 9 being past its end, not by a missing file.
			seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

			if err := applyPGRecordErr(t, mgr, tc.build(t, rel), 200); err != nil {
				t.Fatalf("ApplyRecord on an absent block: %v, want a silent skip (BLK_NOTFOUND)", err)
			}
			nblocks, err := mgr.NBlocks(rel)
			if err != nil {
				t.Fatal(err)
			}
			if nblocks != 1 {
				t.Fatalf("nblocks = %d, want 1 — the fork must not have been extended to reach block 9", nblocks)
			}
		})
	}
}
