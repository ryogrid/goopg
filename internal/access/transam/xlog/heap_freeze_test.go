package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeHeapFreezeRoundTrip pins the M0080-0001 wire format
// for `RecordKindHeapFreeze`. Decoded payload must yield the
// same rel + block + frozen-slots list.
func TestEncodeHeapFreezeRoundTrip(t *testing.T) {
	cases := []struct {
		rel    storage.RelFileNode
		blk    storage.BlockNumber
		frozen []uint16
	}{
		{
			rel:    storage.RelFileNode{DBOid: 1, RelOid: 16402, Fork: storage.MainFork},
			blk:    0,
			frozen: nil,
		},
		{
			rel:    storage.RelFileNode{DBOid: 1, RelOid: 16402, Fork: storage.MainFork},
			blk:    42,
			frozen: []uint16{1, 3, 5, 7, 9, 100, 200},
		},
		{
			rel:    storage.RelFileNode{DBOid: 99, RelOid: 88, Fork: storage.MainFork},
			blk:    1234567,
			frozen: []uint16{65535}, // max uint16
		},
	}
	for _, want := range cases {
		t.Run("", func(t *testing.T) {
			enc := EncodeHeapFreeze(want.rel, want.blk, want.frozen)
			if enc[0] != RecordKindHeapFreeze {
				t.Fatalf("kind byte=%d, want %d", enc[0], RecordKindHeapFreeze)
			}
			rel, blk, frozen, err := DecodeHeapFreeze(enc)
			if err != nil {
				t.Fatalf("DecodeHeapFreeze: %v", err)
			}
			if rel != want.rel {
				t.Errorf("rel mismatch: got=%+v want=%+v", rel, want.rel)
			}
			if blk != want.blk {
				t.Errorf("blk mismatch: got=%d want=%d", blk, want.blk)
			}
			if len(frozen) != len(want.frozen) {
				t.Fatalf("frozen len mismatch: got=%d want=%d", len(frozen), len(want.frozen))
			}
			for i := range want.frozen {
				if frozen[i] != want.frozen[i] {
					t.Errorf("frozen[%d] mismatch: got=%d want=%d", i, frozen[i], want.frozen[i])
				}
			}
		})
	}
}

// TestDecodeHeapFreezeRejectsTruncated pins length guards.
// (M0080-0001.)
func TestDecodeHeapFreezeRejectsTruncated(t *testing.T) {
	enc := EncodeHeapFreeze(
		storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork},
		1,
		[]uint16{1, 2, 3},
	)
	for cut := 0; cut < len(enc); cut++ {
		if _, _, _, err := DecodeHeapFreeze(enc[:cut]); err == nil {
			t.Errorf("truncated at cut=%d must error", cut)
		}
	}
}

// TestDecodeHeapFreezeRejectsWrongKind pins the kind-byte guard.
// (M0080-0001.)
func TestDecodeHeapFreezeRejectsWrongKind(t *testing.T) {
	enc := EncodeHeapFreeze(
		storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork},
		0,
		[]uint16{1},
	)
	enc[0] = RecordKindHeapVacuum
	if _, _, _, err := DecodeHeapFreeze(enc); err == nil {
		t.Error("DecodeHeapFreeze must reject non-HeapFreeze kind bytes")
	}
}

// TestHeapFreezeKindByteAssignment pins the byte assignment so a
// future record kind can't accidentally collide. (M0080-0001.)
func TestHeapFreezeKindByteAssignment(t *testing.T) {
	if RecordKindHeapFreeze != 26 {
		t.Errorf("RecordKindHeapFreeze = %d, want 26", RecordKindHeapFreeze)
	}
}
