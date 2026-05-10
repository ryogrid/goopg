package wal

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeHeapUpdateRoundTrip pins the M0080-0002 atomic
// non-HOT UPDATE record's wire format. The record carries the
// old tuple's xmax stamp + the new tuple's body so replay can
// apply both halves under one pd_lsn boundary.
func TestEncodeHeapUpdateRoundTrip(t *testing.T) {
	tuple := []byte{0x01, 0x02, 0x03, 0x04, 0xAA, 0xBB, 0xCC, 0xDD}
	want := HeapUpdatePayload{
		Rel:         storage.RelFileNode{DBOid: 1, RelOid: 16402, Fork: storage.MainFork},
		OldBlk:      7,
		OldLineSlot: 3,
		Xmax:        100,
		NewBlk:      8,
		NewLineSlot: 1,
		Tuple:       tuple,
	}
	enc := EncodeHeapUpdate(want)
	if enc[0] != RecordKindHeapUpdate {
		t.Fatalf("kind byte=%d, want %d", enc[0], RecordKindHeapUpdate)
	}
	got, err := DecodeHeapUpdate(enc)
	if err != nil {
		t.Fatalf("DecodeHeapUpdate: %v", err)
	}
	if got.Rel != want.Rel || got.OldBlk != want.OldBlk || got.OldLineSlot != want.OldLineSlot ||
		got.Xmax != want.Xmax || got.NewBlk != want.NewBlk || got.NewLineSlot != want.NewLineSlot {
		t.Errorf("control fields mismatch: got=%+v want=%+v", got, want)
	}
	if string(got.Tuple) != string(want.Tuple) {
		t.Errorf("Tuple mismatch")
	}
}

// TestDecodeHeapUpdateRejectsTruncated pins length guards.
func TestDecodeHeapUpdateRejectsTruncated(t *testing.T) {
	enc := EncodeHeapUpdate(HeapUpdatePayload{
		Rel:   storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork},
		Tuple: []byte{1, 2, 3},
	})
	for cut := 0; cut < len(enc); cut++ {
		if _, err := DecodeHeapUpdate(enc[:cut]); err == nil {
			t.Errorf("truncated at cut=%d must error", cut)
		}
	}
}

// TestEncodeHeapMultiInsertRoundTrip pins the M0080-0002 bulk
// insert record. Carries N tuples destined for the same page.
func TestEncodeHeapMultiInsertRoundTrip(t *testing.T) {
	want := HeapMultiInsertPayload{
		Rel: storage.RelFileNode{DBOid: 1, RelOid: 16402, Fork: storage.MainFork},
		Blk: 5,
		Entries: []HeapMultiInsertEntry{
			{LineSlot: 1, Tuple: []byte{0x10, 0x20, 0x30}},
			{LineSlot: 2, Tuple: []byte{0x40, 0x50, 0x60, 0x70, 0x80}},
			{LineSlot: 3, Tuple: nil},
		},
	}
	enc := EncodeHeapMultiInsert(want)
	if enc[0] != RecordKindHeapMultiInsert {
		t.Fatalf("kind byte=%d, want %d", enc[0], RecordKindHeapMultiInsert)
	}
	got, err := DecodeHeapMultiInsert(enc)
	if err != nil {
		t.Fatalf("DecodeHeapMultiInsert: %v", err)
	}
	if got.Rel != want.Rel || got.Blk != want.Blk {
		t.Errorf("rel/blk mismatch")
	}
	if len(got.Entries) != len(want.Entries) {
		t.Fatalf("entries len mismatch: got=%d want=%d", len(got.Entries), len(want.Entries))
	}
	for i := range want.Entries {
		if got.Entries[i].LineSlot != want.Entries[i].LineSlot {
			t.Errorf("entry %d LineSlot mismatch", i)
		}
		if string(got.Entries[i].Tuple) != string(want.Entries[i].Tuple) {
			t.Errorf("entry %d Tuple mismatch", i)
		}
	}
}

// TestEncodeHeapVisibleRoundTrip pins the M0080-0003
// visibility-map record's wire format including both
// flag bits.
func TestEncodeHeapVisibleRoundTrip(t *testing.T) {
	cases := []HeapVisiblePayload{
		{
			Rel:       storage.RelFileNode{DBOid: 1, RelOid: 16402, Fork: storage.MainFork},
			HeapBlk:   100,
			Flags:     HeapVisibleSetAllVisible,
			CutoffXid: 1234,
		},
		{
			Rel:       storage.RelFileNode{DBOid: 1, RelOid: 16402, Fork: storage.MainFork},
			HeapBlk:   0,
			Flags:     HeapVisibleSetAllVisible | HeapVisibleSetAllFrozen,
			CutoffXid: 5678,
		},
		{
			// Clear (no flag bits set).
			Rel:       storage.RelFileNode{DBOid: 99, RelOid: 88, Fork: storage.MainFork},
			HeapBlk:   42,
			Flags:     0,
			CutoffXid: 0,
		},
	}
	for _, want := range cases {
		t.Run("", func(t *testing.T) {
			enc := EncodeHeapVisible(want)
			if enc[0] != RecordKindHeapVisible {
				t.Fatalf("kind byte=%d, want %d", enc[0], RecordKindHeapVisible)
			}
			got, err := DecodeHeapVisible(enc)
			if err != nil {
				t.Fatalf("DecodeHeapVisible: %v", err)
			}
			if got != want {
				t.Errorf("payload mismatch: got=%+v want=%+v", got, want)
			}
		})
	}
}

// TestEncodeBtreeReusePageRoundTrip pins the M0080-0004
// page-recycle record.
func TestEncodeBtreeReusePageRoundTrip(t *testing.T) {
	want := BtreeReusePagePayload{
		Rel:             storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork},
		Blk:             42,
		RecycledFromXid: 999,
	}
	enc := EncodeBtreeReusePage(want)
	if enc[0] != RecordKindBtreeReusePage {
		t.Fatalf("kind byte=%d, want %d", enc[0], RecordKindBtreeReusePage)
	}
	got, err := DecodeBtreeReusePage(enc)
	if err != nil {
		t.Fatalf("DecodeBtreeReusePage: %v", err)
	}
	if got != want {
		t.Errorf("payload mismatch: got=%+v want=%+v", got, want)
	}
}

// TestEncodeBtreeMetaCleanupRoundTrip pins the M0080-0004
// metapage cleanup-XID update record.
func TestEncodeBtreeMetaCleanupRoundTrip(t *testing.T) {
	want := BtreeMetaCleanupPayload{
		Rel:                         storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork},
		NumHeapTuples:               1234567,
		LastCleanupNumDeletedTuples: 89012,
	}
	enc := EncodeBtreeMetaCleanup(want)
	if enc[0] != RecordKindBtreeMetaCleanup {
		t.Fatalf("kind byte=%d, want %d", enc[0], RecordKindBtreeMetaCleanup)
	}
	got, err := DecodeBtreeMetaCleanup(enc)
	if err != nil {
		t.Fatalf("DecodeBtreeMetaCleanup: %v", err)
	}
	if got != want {
		t.Errorf("payload mismatch: got=%+v want=%+v", got, want)
	}
}

// TestM0080KindByteAssignments pins the byte assignments so a
// future record kind can't accidentally collide.
func TestM0080KindByteAssignments(t *testing.T) {
	cases := []struct {
		name string
		got  byte
		want byte
	}{
		{"HeapFreeze", RecordKindHeapFreeze, 26},
		{"HeapUpdate", RecordKindHeapUpdate, 27},
		{"HeapMultiInsert", RecordKindHeapMultiInsert, 28},
		{"HeapVisible", RecordKindHeapVisible, 29},
		{"BtreeReusePage", RecordKindBtreeReusePage, 30},
		{"BtreeMetaCleanup", RecordKindBtreeMetaCleanup, 31},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
