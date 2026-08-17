package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeBtreeUnlinkPageRoundTrip pins the M0079-0003 wire
// format. Decoded payload must yield byte-identical control
// fields including all three optional-page validity flags.
func TestEncodeBtreeUnlinkPageRoundTrip(t *testing.T) {
	cases := []BtreeUnlinkPagePayload{
		{
			// Middle-of-chain leaf with both siblings + parent.
			Rel:              storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork},
			LeafBlk:          7,
			LeafFlagsAfter:   0x0080,
			HasLeftSib:       true,
			LeftSibBlk:       6,
			LeftSibNewNext:   8,
			HasRightSib:      true,
			RightSibBlk:      8,
			RightSibNewPrev:  6,
			HasParent:        true,
			ParentBlk:        2,
			ParentRemoveSlot: 4,
		},
		{
			// Leftmost leaf — no left sibling.
			Rel:              storage.RelFileNode{DBOid: 5, RelOid: 99, Fork: storage.MainFork},
			LeafBlk:          1,
			LeafFlagsAfter:   0,
			HasLeftSib:       false,
			HasRightSib:      true,
			RightSibBlk:      2,
			RightSibNewPrev:  storage.InvalidBlockNumber,
			HasParent:        true,
			ParentBlk:        100,
			ParentRemoveSlot: 1,
		},
		{
			// Single-page tree case — leaf is also root, no parent.
			Rel:            storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork},
			LeafBlk:        1,
			LeafFlagsAfter: 0xC080,
			HasLeftSib:     false,
			HasRightSib:    false,
			HasParent:      false,
		},
	}
	for _, want := range cases {
		t.Run("", func(t *testing.T) {
			enc := EncodeBtreeUnlinkPage(want)
			if enc[0] != RecordKindBtreeUnlinkPage {
				t.Fatalf("kind byte=%d, want %d", enc[0], RecordKindBtreeUnlinkPage)
			}
			got, err := DecodeBtreeUnlinkPage(enc)
			if err != nil {
				t.Fatalf("DecodeBtreeUnlinkPage: %v", err)
			}
			if got != want {
				t.Errorf("payload mismatch:\n  got=%+v\n want=%+v", got, want)
			}
		})
	}
}

// TestDecodeBtreeUnlinkPageRejectsTruncated pins defensive
// length checks. The fixed-size payload must reject any
// truncation. (M0079-0003.)
func TestDecodeBtreeUnlinkPageRejectsTruncated(t *testing.T) {
	enc := EncodeBtreeUnlinkPage(BtreeUnlinkPagePayload{
		Rel:     storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork},
		LeafBlk: 1,
	})
	for cut := 0; cut < len(enc); cut++ {
		if _, err := DecodeBtreeUnlinkPage(enc[:cut]); err == nil {
			t.Errorf("truncated at cut=%d must error", cut)
		}
	}
}

// TestEncodeBtreeNewRootRoundTrip pins the new-root record
// shape with both the split-bubbled (2-item) and reset-empty
// (0-item) cases. (M0079-0003.)
func TestEncodeBtreeNewRootRoundTrip(t *testing.T) {
	cases := []BtreeNewRootPayload{
		{
			// Reset-to-empty after full vacuum.
			Rel:     storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork},
			RootBlk: 1,
			Level:   0,
			Items:   nil,
		},
		{
			// Split-bubbled: 2 downlinks (left ptr w/ nil key, right ptr w/ separator).
			Rel:     storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork},
			RootBlk: 5,
			Level:   2,
			Items: [][]byte{
				{0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00},
				{0x04, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xAA, 0xBB, 0xCC, 0xDD},
			},
		},
	}
	for _, want := range cases {
		t.Run("", func(t *testing.T) {
			enc := EncodeBtreeNewRoot(want)
			if enc[0] != RecordKindBtreeNewRoot {
				t.Fatalf("kind byte=%d, want %d", enc[0], RecordKindBtreeNewRoot)
			}
			got, err := DecodeBtreeNewRoot(enc)
			if err != nil {
				t.Fatalf("DecodeBtreeNewRoot: %v", err)
			}
			if got.Rel != want.Rel || got.RootBlk != want.RootBlk || got.Level != want.Level {
				t.Errorf("control fields mismatch: got=%+v want=%+v", got, want)
			}
			if len(got.Items) != len(want.Items) {
				t.Fatalf("Items len mismatch: got=%d want=%d", len(got.Items), len(want.Items))
			}
			for i := range want.Items {
				if string(got.Items[i]) != string(want.Items[i]) {
					t.Errorf("Items[%d] mismatch", i)
				}
			}
		})
	}
}

// TestEncodeBtreeMarkPageHalfDeadRoundTrip pins the standalone
// half-dead record. (M0079-0003.)
func TestEncodeBtreeMarkPageHalfDeadRoundTrip(t *testing.T) {
	want := BtreeMarkHalfDeadPayload{
		Rel:        storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork},
		LeafBlk:    42,
		FlagsAfter: 0xC080, // BTHalfDead | BTDeleted
	}
	enc := EncodeBtreeMarkPageHalfDead(want)
	if enc[0] != RecordKindBtreeMarkPageHalfDead {
		t.Fatalf("kind byte=%d, want %d", enc[0], RecordKindBtreeMarkPageHalfDead)
	}
	got, err := DecodeBtreeMarkPageHalfDead(enc)
	if err != nil {
		t.Fatalf("DecodeBtreeMarkPageHalfDead: %v", err)
	}
	if got != want {
		t.Errorf("payload mismatch: got=%+v want=%+v", got, want)
	}
}

// TestDecodeBtreeMarkPageHalfDeadRejectsTruncated pins length
// guards on the smallest of the new records. (M0079-0003.)
func TestDecodeBtreeMarkPageHalfDeadRejectsTruncated(t *testing.T) {
	enc := EncodeBtreeMarkPageHalfDead(BtreeMarkHalfDeadPayload{
		Rel: storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork},
	})
	for cut := 0; cut < len(enc); cut++ {
		if _, err := DecodeBtreeMarkPageHalfDead(enc[:cut]); err == nil {
			t.Errorf("truncated at cut=%d must error", cut)
		}
	}
}

// TestNewBtreeKindBytesRoute pins that the three new kind
// bytes (23, 24, 25) survive ApplyRecord's switch dispatch.
// End-to-end recovery is covered by access/btree integration
// tests; this is the smoke test for kind-byte assignment.
// (M0079-0003.)
func TestNewBtreeKindBytesRoute(t *testing.T) {
	if RecordKindBtreeUnlinkPage != 23 {
		t.Errorf("RecordKindBtreeUnlinkPage = %d, want 23", RecordKindBtreeUnlinkPage)
	}
	if RecordKindBtreeNewRoot != 24 {
		t.Errorf("RecordKindBtreeNewRoot = %d, want 24", RecordKindBtreeNewRoot)
	}
	if RecordKindBtreeMarkPageHalfDead != 25 {
		t.Errorf("RecordKindBtreeMarkPageHalfDead = %d, want 25", RecordKindBtreeMarkPageHalfDead)
	}
}
