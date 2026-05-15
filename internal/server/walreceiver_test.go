package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/wal"
)

// TestWalReceiverStreamsRecordsToLocalWAL: the canonical M0005
// integration test. A primary server (built by
// startReplicationTestServerFull) hosts a slot; a WalReceiver dials
// it, issues START_REPLICATION, and persists incoming WAL records
// into a local WAL writer. The test verifies that records appended
// on the primary appear byte-identically in the standby's local WAL
// and that the receiver's ApplyLSN advances accordingly.
func TestWalReceiverStreamsRecordsToLocalWAL(t *testing.T) {
	addr, _, primaryWAL, stop := startReplicationTestServerFull(t)
	defer stop()

	// Pre-create the slot via a separate replication-mode connection
	// (the receiver doesn't issue CREATE_REPLICATION_SLOT itself in
	// v0; that's a caller responsibility).
	conn, r, w := dialReplication(t, addr)
	sendQuery(t, w, `CREATE_REPLICATION_SLOT standby PHYSICAL`)
	_ = readUntilReadyForQuery(t, r)
	_ = conn.Close()

	// Build the standby's local WAL writer.
	standbyDir := filepath.Join(t.TempDir(), "standby_pg_wal")
	standbyWAL, err := wal.NewWriter(wal.Config{WALDir: standbyDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = standbyWAL.Close() }()

	rec, err := DialWalReceiver(context.Background(), WalReceiverConfig{
		PrimaryAddr:    addr,
		User:           "rep",
		SlotName:       "standby",
		WAL:            standbyWAL,
		StatusInterval: 200 * time.Millisecond,
		DialTimeout:    3 * time.Second,
	})
	if err != nil {
		t.Fatalf("DialWalReceiver: %v", err)
	}
	defer rec.Close()

	runCtx, cancelRun := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- rec.Run(runCtx) }()

	// Push three records on the primary.
	want := [][]byte{[]byte("alpha"), []byte("beta"), []byte("gamma")}
	for _, p := range want {
		if _, _, err := primaryWAL.Append(p); err != nil {
			t.Fatalf("primary Append: %v", err)
		}
	}

	// Wait for the receiver to durably append all three.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if standbyWAL.WrittenLSN() >= primaryWAL.WrittenLSN() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if standbyWAL.WrittenLSN() != primaryWAL.WrittenLSN() {
		t.Fatalf("standby LSN = %d, primary LSN = %d (expected match within 3s)",
			standbyWAL.WrittenLSN(), primaryWAL.WrittenLSN())
	}

	cancelRun()
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run returned %v, want nil on context cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s of cancel")
	}

	// Verify the standby's local WAL has byte-identical record
	// payloads to what we Append'd on the primary, in the same
	// order. ReadAll iterates the standby's pg_wal segments and
	// decodes records via the same framing the writer uses.
	standbyRecords, err := wal.ReadAll(standbyDir, 4096)
	if err != nil {
		t.Fatalf("standby ReadAll: %v", err)
	}
	if len(standbyRecords) != len(want) {
		t.Fatalf("standby record count = %d, want %d", len(standbyRecords), len(want))
	}
	for i, p := range want {
		if string(standbyRecords[i].Payload) != string(p) {
			t.Errorf("standby record %d payload = %q, want %q",
				i, standbyRecords[i].Payload, p)
		}
	}
	// ApplyLSN must equal the standby writer's WrittenLSN by now.
	if rec.ApplyLSN() != standbyWAL.WrittenLSN() {
		t.Errorf("ApplyLSN = %d, want %d", rec.ApplyLSN(), standbyWAL.WrittenLSN())
	}
}

func TestWalReceiverTrimsOverlappingRawWALData(t *testing.T) {
	srcDir := filepath.Join(t.TempDir(), "src_pg_wal")
	srcWAL, err := wal.NewWriter(wal.Config{
		WALDir:             srcDir,
		SegmentSize:        wal.DefaultSegmentSize,
		PageHeaders:        true,
		SystemID:           1,
		TimelineID:         1,
		SenderMemoryBuffer: 32,
	})
	if err != nil {
		t.Fatalf("NewWriter src: %v", err)
	}
	defer func() { _ = srcWAL.Close() }()

	if _, _, err := srcWAL.Append([]byte("alpha")); err != nil {
		t.Fatalf("src Append alpha: %v", err)
	}
	_, end, err := srcWAL.Append([]byte("beta"))
	if err != nil {
		t.Fatalf("src Append beta: %v", err)
	}
	if err := srcWAL.FlushUpTo(end); err != nil {
		t.Fatalf("src FlushUpTo: %v", err)
	}
	rawPath := filepath.Join(srcDir, "000000000000000000000000")
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("ReadFile raw WAL: %v", err)
	}
	raw = raw[:srcWAL.WrittenLSN()]
	split := len(raw) / 2
	overlapOff := split / 2

	dstDir := filepath.Join(t.TempDir(), "dst_pg_wal")
	dstWAL, err := wal.NewWriter(wal.Config{
		WALDir:             dstDir,
		SegmentSize:        wal.DefaultSegmentSize,
		PageHeaders:        true,
		SystemID:           1,
		TimelineID:         1,
		WALBuffers:         256,
		SenderMemoryBuffer: 32,
	})
	if err != nil {
		t.Fatalf("NewWriter dst: %v", err)
	}
	defer func() { _ = dstWAL.Close() }()

	rec := &WalReceiver{cfg: WalReceiverConfig{WAL: dstWAL}}
	if err := rec.handleCopyData(protocol.EncodeWALData(0, uint64(split), time.Now(), raw[:split])); err != nil {
		t.Fatalf("handleCopyData first frame: %v", err)
	}
	if err := rec.handleCopyData(protocol.EncodeWALData(uint64(overlapOff), uint64(len(raw)), time.Now(), raw[overlapOff:])); err != nil {
		t.Fatalf("handleCopyData overlapping frame: %v", err)
	}
	if got := dstWAL.WrittenLSN(); got != uint64(len(raw)) {
		t.Fatalf("dst WrittenLSN = %d, want %d", got, len(raw))
	}
	if err := dstWAL.FlushUpTo(dstWAL.WrittenLSN()); err != nil {
		t.Fatalf("dst FlushUpTo: %v", err)
	}
	gotRaw, err := os.ReadFile(filepath.Join(dstDir, "000000000000000000000000"))
	if err != nil {
		t.Fatalf("ReadFile dst WAL: %v", err)
	}
	gotRaw = gotRaw[:len(raw)]
	if string(gotRaw) != string(raw) {
		t.Fatalf("dst raw WAL differs from source after overlap trimming")
	}
}
