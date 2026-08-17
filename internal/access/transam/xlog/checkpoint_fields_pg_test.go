package xlog

import (
	"encoding/binary"
	"path/filepath"
	"testing"
)

// M0131-S18.4 guards for the CheckPoint struct encoder.
//
// The discovery these tests exist for: `encodeCheckPointStruct` never wrote
// payload offsets 12 (PrevTimeLineID) or 16 (fullPageWrites) AT ALL, and
// hardcoded literals into six more members. A missing write is invisible to
// any round-trip test — the byte is simply the zero the freshly-allocated
// payload already held — so each field needs a SETTABILITY assertion: encode
// a value that differs from both zero and the old literal, then read it back
// at the offset the PG18 struct layout puts it at.

// checkPointOffsets is the PG18 CheckPoint layout (sizeof = 88), verified
// against the compiled PG18 binary's DWARF and re-confirmed against
// `pg_controldata` output on the reference cluster.
var checkPointOffsets = struct {
	redo, thisTLI, prevTLI, fullPageWrites, walLevel   int
	nextXid, nextOid, nextMulti, nextMultiOffset       int
	oldestXid, oldestXidDB, oldestMulti, oldestMultiDB int
	time, oldestCommitTsXid, newestCommitTsXid         int
	oldestActiveXid                                    int
}{
	redo: 0, thisTLI: 8, prevTLI: 12, fullPageWrites: 16, walLevel: 20,
	nextXid: 24, nextOid: 32, nextMulti: 36, nextMultiOffset: 40,
	oldestXid: 44, oldestXidDB: 48, oldestMulti: 52, oldestMultiDB: 56,
	time: 64, oldestCommitTsXid: 72, newestCommitTsXid: 76,
	oldestActiveXid: 80,
}

// TestEncodeCheckPointStructSettable proves every CheckPointFields member
// actually reaches the wire at the offset PG reads it from. Break any single
// `le.PutUint32` line in encodeCheckPointStruct and exactly one subtest fails,
// naming the field — which is the property the old code silently lacked for
// PrevTimeLineID and fullPageWrites.
func TestEncodeCheckPointStructSettable(t *testing.T) {
	f := CheckPointFields{
		RedoLSN0:          0x0102030405060708,
		ThisTLI:           7,
		PrevTLI:           6,
		FullPageWrites:    true,
		WalLevel:          2, // logical
		NextXid:           0x00000001_000004d2,
		NextOid:           30000,
		NextMulti:         44,
		NextMultiOffset:   55,
		OldestXid:         66,
		OldestXidDB:       77,
		OldestMulti:       88,
		OldestMultiDB:     99,
		OldestCommitTsXid: 111,
		NewestCommitTsXid: 222,
		OldestActiveXid:   333,
	}
	got := encodeCheckPointStruct(f)
	if len(got) != 88 {
		t.Fatalf("CheckPoint struct is %d bytes, want 88", len(got))
	}
	le := binary.LittleEndian
	u32 := func(off int) uint32 { return le.Uint32(got[off:]) }

	if v := le.Uint64(got[checkPointOffsets.redo:]); v != f.RedoLSN0 {
		t.Errorf("redo = %#x, want %#x", v, f.RedoLSN0)
	}
	if v := le.Uint64(got[checkPointOffsets.nextXid:]); v != f.NextXid {
		t.Errorf("nextXid = %#x, want %#x", v, f.NextXid)
	}
	for _, tc := range []struct {
		name string
		off  int
		want uint32
	}{
		{"ThisTimeLineID", checkPointOffsets.thisTLI, f.ThisTLI},
		{"PrevTimeLineID", checkPointOffsets.prevTLI, f.PrevTLI},
		{"wal_level", checkPointOffsets.walLevel, f.WalLevel},
		{"nextOid", checkPointOffsets.nextOid, f.NextOid},
		{"nextMulti", checkPointOffsets.nextMulti, f.NextMulti},
		{"nextMultiOffset", checkPointOffsets.nextMultiOffset, f.NextMultiOffset},
		{"oldestXid", checkPointOffsets.oldestXid, f.OldestXid},
		{"oldestXidDB", checkPointOffsets.oldestXidDB, f.OldestXidDB},
		{"oldestMulti", checkPointOffsets.oldestMulti, f.OldestMulti},
		{"oldestMultiDB", checkPointOffsets.oldestMultiDB, f.OldestMultiDB},
		{"oldestCommitTsXid", checkPointOffsets.oldestCommitTsXid, f.OldestCommitTsXid},
		{"newestCommitTsXid", checkPointOffsets.newestCommitTsXid, f.NewestCommitTsXid},
		{"oldestActiveXid", checkPointOffsets.oldestActiveXid, f.OldestActiveXid},
	} {
		if v := u32(tc.off); v != tc.want {
			t.Errorf("%s (offset %d) = %d, want %d", tc.name, tc.off, v, tc.want)
		}
	}
	// fullPageWrites is a C bool: one byte, then three pad bytes that must
	// stay zero or the wal_level read at offset 20 would be corrupted.
	if got[checkPointOffsets.fullPageWrites] != 1 {
		t.Errorf("fullPageWrites byte = %d, want 1", got[checkPointOffsets.fullPageWrites])
	}
	for off := 17; off < 20; off++ {
		if got[off] != 0 {
			t.Errorf("pad byte %d = %d, want 0", off, got[off])
		}
	}
	f.FullPageWrites = false
	if off := encodeCheckPointStruct(f); off[checkPointOffsets.fullPageWrites] != 0 {
		t.Errorf("fullPageWrites=false encoded as %d, want 0", off[checkPointOffsets.fullPageWrites])
	}
}

// TestCheckPointFieldsDefaultsMatchPG pins the floors withDefaults applies to
// the values a real PG 18 `pg_controldata` prints for a freshly-initdb'd
// cluster. Two of them are corrections, not preservation: goopg used to write
// oldestCommitTsXid = newestCommitTsXid = 3 (PG writes InvalidTransactionId=0
// while track_commit_timestamp is off) and oldestXidDB = oldestMultiDB = 0
// (PG's bootstrap names Template1DbOid = 1).
func TestCheckPointFieldsDefaultsMatchPG(t *testing.T) {
	got := encodeCheckPointStruct(CheckPointFields{RedoLSN0: 0x01000028})
	le := binary.LittleEndian
	for _, tc := range []struct {
		name string
		off  int
		want uint32
	}{
		{"ThisTimeLineID", checkPointOffsets.thisTLI, 1},
		{"PrevTimeLineID", checkPointOffsets.prevTLI, 1},
		{"wal_level", checkPointOffsets.walLevel, 1}, // replica
		{"nextOid", checkPointOffsets.nextOid, 16384},
		{"nextMulti", checkPointOffsets.nextMulti, 1},
		{"nextMultiOffset", checkPointOffsets.nextMultiOffset, 0},
		{"oldestXid", checkPointOffsets.oldestXid, 3},
		{"oldestXidDB", checkPointOffsets.oldestXidDB, 1},
		{"oldestMulti", checkPointOffsets.oldestMulti, 1},
		{"oldestMultiDB", checkPointOffsets.oldestMultiDB, 1},
		{"oldestCommitTsXid", checkPointOffsets.oldestCommitTsXid, 0},
		{"newestCommitTsXid", checkPointOffsets.newestCommitTsXid, 0},
	} {
		if v := le.Uint32(got[tc.off:]); v != tc.want {
			t.Errorf("default %s = %d, want %d", tc.name, v, tc.want)
		}
	}
	if v := le.Uint64(got[checkPointOffsets.nextXid:]); v != 3 {
		t.Errorf("default nextXid = %d, want 3 (FirstNormalTransactionId)", v)
	}
	// PrevTimeLineID must MIRROR an explicitly-set ThisTimeLineID, not fall
	// back to 1 — upstream CreateCheckPoint (xlog.c:7030-7034) only differs
	// on the end-of-recovery path.
	promoted := encodeCheckPointStruct(CheckPointFields{ThisTLI: 4})
	if v := le.Uint32(promoted[checkPointOffsets.prevTLI:]); v != 4 {
		t.Errorf("PrevTimeLineID on TLI 4 = %d, want 4 (mirrors ThisTimeLineID)", v)
	}
}

// TestCheckpointerStampsLiveTimelineAndFPW is the S18.3 guard: a checkpointer
// wired to a promoted writer must emit the LIVE timeline, not the hardcoded 1,
// and must honour full_page_writes = off. Before the fix, the first checkpoint
// after M0130-S8.5's finalizePromotion wrote TLI 1 into a cluster whose
// segments were named for TLI 2, and a real PG booted from it PANICs
// "could not locate a valid checkpoint record".
func TestCheckpointerStampsLiveTimelineAndFPW(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 1 << 20, TimelineID: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.TimelineID() != 3 {
		t.Fatalf("writer TLI = %d, want 3 (test precondition)", w.TimelineID())
	}

	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		PGCompatCheckpoints: true,
		NextXIDFn:           func() uint64 { return 42 },
		TimelineIDFn:        w.TimelineID,
		FullPageWritesFn:    func() bool { return false },
		NextMultiXactFn:     func() (uint32, uint32, uint32) { return 9, 0, 1 },
	})
	if err := cp.CheckpointShutdown(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var main []byte
	for _, r := range recs {
		if r.XLog == nil || r.XLog.Header.Rmid != RmgrXLog {
			continue
		}
		if r.XLog.Header.Info&XLRRmgrInfoMask == xlogCheckpointShutdown {
			main = r.XLog.MainData
		}
	}
	if len(main) != 88 {
		t.Fatalf("shutdown checkpoint main data is %d bytes, want 88", len(main))
	}
	le := binary.LittleEndian
	if v := le.Uint32(main[checkPointOffsets.thisTLI:]); v != 3 {
		t.Errorf("record ThisTimeLineID = %d, want 3 (the live writer timeline)", v)
	}
	if v := le.Uint32(main[checkPointOffsets.prevTLI:]); v != 3 {
		t.Errorf("record PrevTimeLineID = %d, want 3", v)
	}
	if main[checkPointOffsets.fullPageWrites] != 0 {
		t.Errorf("record fullPageWrites = %d, want 0 (full_page_writes = off)",
			main[checkPointOffsets.fullPageWrites])
	}
	if v := le.Uint32(main[checkPointOffsets.nextMulti:]); v != 9 {
		t.Errorf("record nextMulti = %d, want 9 (the live allocator)", v)
	}
}
