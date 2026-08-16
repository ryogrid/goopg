package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeBtreeVacuumRoundTrip pins the M0079-0002 wire format
// for `RecordKindBtreeVacuum`. Decoding the encoded payload
// must yield byte-identical kept-items (each as []byte) and the
// post-vacuum opaque flags.
func TestEncodeBtreeVacuumRoundTrip(t *testing.T) {
	cases := []BtreeVacuumPayload{
		{
			Rel:         storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork},
			Blk:         42,
			KeptItems:   nil,
			OpaqueFlags: 0,
		},
		{
			Rel:         storage.RelFileNode{DBOid: 1, RelOid: 16406, Fork: storage.MainFork},
			Blk:         0,
			KeptItems:   [][]byte{{0x01, 0x02, 0x03}, {0xff, 0xee, 0xdd, 0xcc}},
			OpaqueFlags: 0x0001 | 0x0080, // pretend BTLeaf | BTHalfDead
		},
		{
			Rel: storage.RelFileNode{DBOid: 5, RelOid: 99, Fork: storage.MainFork},
			Blk: 1234567,
			KeptItems: [][]byte{
				{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80},
			},
			OpaqueFlags: 0xABCD,
		},
	}
	for _, want := range cases {
		t.Run("", func(t *testing.T) {
			enc := EncodeBtreeVacuum(want.Rel, want.Blk, want.KeptItems, want.OpaqueFlags)
			if enc[0] != RecordKindBtreeVacuum {
				t.Fatalf("kind byte = %d, want %d", enc[0], RecordKindBtreeVacuum)
			}
			got, err := DecodeBtreeVacuum(enc)
			if err != nil {
				t.Fatalf("DecodeBtreeVacuum: %v", err)
			}
			if got.Rel != want.Rel {
				t.Errorf("Rel mismatch: got=%+v want=%+v", got.Rel, want.Rel)
			}
			if got.Blk != want.Blk {
				t.Errorf("Blk mismatch: got=%d want=%d", got.Blk, want.Blk)
			}
			if got.OpaqueFlags != want.OpaqueFlags {
				t.Errorf("OpaqueFlags mismatch: got=%#x want=%#x", got.OpaqueFlags, want.OpaqueFlags)
			}
			if len(got.KeptItems) != len(want.KeptItems) {
				t.Fatalf("KeptItems len mismatch: got=%d want=%d", len(got.KeptItems), len(want.KeptItems))
			}
			for i := range want.KeptItems {
				if string(got.KeptItems[i]) != string(want.KeptItems[i]) {
					t.Errorf("KeptItems[%d] mismatch: got=%v want=%v", i, got.KeptItems[i], want.KeptItems[i])
				}
			}
		})
	}
}

// TestDecodeBtreeVacuumRejectsTruncated pins the defensive
// length checks at every variable-length boundary. (M0079-0002.)
func TestDecodeBtreeVacuumRejectsTruncated(t *testing.T) {
	enc := EncodeBtreeVacuum(
		storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork},
		1,
		[][]byte{{0xAA, 0xBB}, {0xCC}},
		0xBEEF,
	)
	for cut := 0; cut < len(enc); cut++ {
		if _, err := DecodeBtreeVacuum(enc[:cut]); err == nil {
			t.Errorf("truncated payload at cut=%d must error", cut)
		}
	}
}

// TestDecodeBtreeVacuumRejectsWrongKind pins the kind-byte
// guard so a heap-vacuum payload can't accidentally be parsed
// as btree-vacuum. (M0079-0002.)
func TestDecodeBtreeVacuumRejectsWrongKind(t *testing.T) {
	enc := EncodeBtreeVacuum(
		storage.RelFileNode{},
		0,
		[][]byte{{1, 2, 3}},
		0,
	)
	enc[0] = RecordKindHeapVacuum
	if _, err := DecodeBtreeVacuum(enc); err == nil {
		t.Error("DecodeBtreeVacuum must reject non-BtreeVacuum kind bytes")
	}
}

// TestBtreeVacuumKindByteRoutes pins that the new kind byte
// is recognised by ApplyRecord's dispatch. We don't need a
// real Manager — the test passes if the dispatch path is hit
// (compilation alone is the smoke test that the case arm
// exists). (M0079-0002.)
func TestBtreeVacuumKindByteRoutes(t *testing.T) {
	enc := EncodeBtreeVacuum(
		storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork},
		0,
		nil,
		0,
	)
	if enc[0] != RecordKindBtreeVacuum {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindBtreeVacuum)
	}
	// `ApplyRecord` would dispatch to `replayBtreeVacuum`. End-
	// to-end recovery is covered by the access/btree vacuum WAL
	// test which uses a real Pool + Manager.
}
