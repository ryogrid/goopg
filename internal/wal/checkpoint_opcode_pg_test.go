package wal

import (
	"encoding/binary"
	"path/filepath"
	"testing"
)

// A9-checkpoint-opcode pins: PG-compat checkpoints carry their EXPLICIT
// opcode (XLOG_CHECKPOINT_ONLINE / XLOG_CHECKPOINT_SHUTDOWN) in the
// XLogRecord header via the assembled-record path, replacing the retired
// classify-by-len==88 hack (which could only ever stamp SHUTDOWN), and an
// ONLINE checkpoint is preceded by an XLOG_RUNNING_XACTS record so a
// hot-standby PG can reach STANDBY_SNAPSHOT_READY.

// TestCheckpointPGExplicitOpcode round-trips both opcodes through a real
// writer and asserts header-driven routing: the record reaches the decoded
// path (Payload nil, struct in XLog.MainData) and isCheckpointRecord
// recognises it.
func TestCheckpointPGExplicitOpcode(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}

	// redoLSN0 low byte == RecordKindSmgrCreate (11): the historical
	// main-data-only collision class. The explicit-opcode header must keep
	// the record on the decoded path regardless (no RecordKind maps to
	// (RmgrXLog, 0x00|0x10)).
	const redo0 = uint64(11)
	online, err := EncodeCheckpointPG(false, redo0, 1, 42, 20000, 40)
	if err != nil {
		t.Fatal(err)
	}
	shutdown, err := EncodeCheckpointPG(true, redo0, 1, 42, 20000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(online); err != nil {
		t.Fatal(err)
	}
	_, end, err := w.Append(shutdown)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("record count = %d, want 2", len(recs))
	}
	wantInfo := []uint8{xlogCheckpointOnline, xlogCheckpointShutdown}
	wantOldestActive := []uint32{40, 0}
	for i, r := range recs {
		if r.XLog == nil {
			t.Fatalf("rec[%d]: no decoded XLog", i)
		}
		if r.XLog.Header.Rmid != RmgrXLog {
			t.Errorf("rec[%d] rmid = %v, want RmgrXLog", i, r.XLog.Header.Rmid)
		}
		if got := r.XLog.Header.Info & XLRRmgrInfoMask; got != wantInfo[i] {
			t.Errorf("rec[%d] info = %#x, want %#x", i, got, wantInfo[i])
		}
		if r.Payload != nil {
			t.Errorf("rec[%d] Payload non-nil (%d bytes) — explicit-opcode checkpoint must route decoded", i, len(r.Payload))
		}
		if len(r.XLog.MainData) != 88 {
			t.Fatalf("rec[%d] main-data len = %d, want 88", i, len(r.XLog.MainData))
		}
		if !isCheckpointRecord(r) {
			t.Errorf("rec[%d]: isCheckpointRecord = false, want true", i)
		}
		if got := binary.LittleEndian.Uint64(r.XLog.MainData[0:8]); got != redo0 {
			t.Errorf("rec[%d] redo = %d, want %d", i, got, redo0)
		}
		if got := binary.LittleEndian.Uint32(r.XLog.MainData[80:84]); got != wantOldestActive[i] {
			t.Errorf("rec[%d] oldestActiveXid = %d, want %d", i, got, wantOldestActive[i])
		}
	}
}

// TestRunningXactsPGRoundTrip pins the xl_running_xacts wire layout
// (storage/standbydefs.h): header fields at their C offsets, top-level
// xids appended, subxid_overflow=false only for an idle snapshot.
func TestRunningXactsPGRoundTrip(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}

	busy, err := EncodeRunningXactsPG(50, 44, 49, []uint32{44, 47})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := EncodeRunningXactsPG(50, 50, 49, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(busy); err != nil {
		t.Fatal(err)
	}
	_, end, err := w.Append(idle)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("record count = %d, want 2", len(recs))
	}
	le := binary.LittleEndian
	for i, r := range recs {
		if r.XLog == nil || r.XLog.Header.Rmid != RmgrStandby {
			t.Fatalf("rec[%d]: not an RmgrStandby record", i)
		}
		if got := r.XLog.Header.Info & XLRRmgrInfoMask; got != xlogStandbyRunningXacts {
			t.Errorf("rec[%d] info = %#x, want XLOG_RUNNING_XACTS (%#x)", i, got, xlogStandbyRunningXacts)
		}
	}
	md := recs[0].XLog.MainData
	if len(md) != minSizeOfXlRunningXacts+8 {
		t.Fatalf("busy main-data len = %d, want %d", len(md), minSizeOfXlRunningXacts+8)
	}
	if got := le.Uint32(md[0:4]); got != 2 {
		t.Errorf("busy xcnt = %d, want 2", got)
	}
	if md[8] != 1 {
		t.Errorf("busy subxid_overflow = %d, want 1 (conservative when xacts run)", md[8])
	}
	if got := le.Uint32(md[12:16]); got != 50 {
		t.Errorf("busy nextXid = %d, want 50", got)
	}
	if got := le.Uint32(md[16:20]); got != 44 {
		t.Errorf("busy oldestRunningXid = %d, want 44", got)
	}
	if got := le.Uint32(md[20:24]); got != 49 {
		t.Errorf("busy latestCompletedXid = %d, want 49", got)
	}
	if x0, x1 := le.Uint32(md[24:28]), le.Uint32(md[28:32]); x0 != 44 || x1 != 47 {
		t.Errorf("busy xids = [%d %d], want [44 47]", x0, x1)
	}
	md = recs[1].XLog.MainData
	if len(md) != minSizeOfXlRunningXacts {
		t.Fatalf("idle main-data len = %d, want %d", len(md), minSizeOfXlRunningXacts)
	}
	if got := le.Uint32(md[0:4]); got != 0 {
		t.Errorf("idle xcnt = %d, want 0", got)
	}
	if md[8] != 0 {
		t.Errorf("idle subxid_overflow = %d, want 0 (exact when no xacts run)", md[8])
	}
}

// TestCheckpointerEmitsExplicitOpcodes drives a real Checkpointer in
// PG-compat mode: CheckpointNow (online) must emit XLOG_RUNNING_XACTS
// immediately followed by an ONLINE checkpoint; CheckpointShutdown must
// emit a SHUTDOWN checkpoint with no running-xacts record (upstream skips
// LogStandbySnapshot when shutting down).
func TestCheckpointerEmitsExplicitOpcodes(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		PGCompatCheckpoints: true,
		NextXIDFn:           func() uint64 { return 42 },
	})
	if err := cp.CheckpointNow(); err != nil {
		t.Fatal(err)
	}
	if err := cp.CheckpointShutdown(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	type hdr struct {
		rmid Rmgr
		info uint8
	}
	var got []hdr
	for _, r := range recs {
		if r.XLog == nil {
			t.Fatalf("record without decoded XLog: payload %v", r.Payload)
		}
		got = append(got, hdr{r.XLog.Header.Rmid, r.XLog.Header.Info & XLRRmgrInfoMask})
	}
	want := []hdr{
		{RmgrXLog, xlogCheckpointRedo},
		{RmgrStandby, xlogStandbyRunningXacts},
		{RmgrXLog, xlogCheckpointOnline},
		{RmgrXLog, xlogCheckpointShutdown},
	}
	if len(got) != len(want) {
		t.Fatalf("record headers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rec[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	// PG17+ recovery reads the record AT CheckPoint.redo and demands it be
	// XLOG_CHECKPOINT_REDO: the online checkpoint's redo field must point
	// exactly at the marker record's start (0-based).
	if gotRedo, wantRedo := binary.LittleEndian.Uint64(recs[2].XLog.MainData[0:8]), recs[0].StartLSN-1; gotRedo != wantRedo {
		t.Errorf("online checkpoint redo = %d, want %d (CHECKPOINT_REDO record start)", gotRedo, wantRedo)
	}
	// The online checkpoint's oldestActiveXid falls back to nextXid (no
	// RunningXactsFn wired, no xact running); the shutdown one stamps
	// InvalidTransactionId, exactly like upstream CreateCheckPoint.
	if got := binary.LittleEndian.Uint32(recs[2].XLog.MainData[80:84]); got != 42 {
		t.Errorf("online oldestActiveXid = %d, want 42", got)
	}
	if got := binary.LittleEndian.Uint32(recs[3].XLog.MainData[80:84]); got != 0 {
		t.Errorf("shutdown oldestActiveXid = %d, want 0", got)
	}
	// The idle running-xacts snapshot is exact: nextXid, no overflow.
	md := recs[1].XLog.MainData
	if gotNext := binary.LittleEndian.Uint32(md[12:16]); gotNext != 42 {
		t.Errorf("running-xacts nextXid = %d, want 42", gotNext)
	}
	if md[8] != 0 {
		t.Errorf("running-xacts subxid_overflow = %d, want 0", md[8])
	}
}
