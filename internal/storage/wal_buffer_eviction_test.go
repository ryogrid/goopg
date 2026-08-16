package storage_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// segmentTotalBytes mirrors the helper in
// internal/wal/wal_buffer_test.go but lives here too because we
// can't reach across packages for an unexported helper. With
// preallocate=false (default in these tests) segment files only
// grow when state.writeAt actually pwrites, so this is the
// operational signal for "did Stage 1 drain run?"
func segmentTotalBytes(t *testing.T, walDir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		st, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += st.Size()
	}
	return total
}

// TestEvictionDrainsBufferedWALBeforeWritingPage is the M0013-0002
// headline regression guard: with wal_buffers > 0, a small WAL
// record stays in walBuf and never touches disk. When the buffer
// pool evicts a heap page whose pd_lsn references that buffered
// record, the eviction's wal.FlushUpTo(pd_lsn) must run Stage 1
// (drain the buffer) AND Stage 2 (dataSync) before
// mgr.WriteBlock fires.
//
// If anyone disables Stage 1 in flushUpTo (drops drainBufferUpTo),
// the heap byte still persists but the WAL segment file stays
// empty — recovery would replay zero records while the heap
// mutation survived. The dual assertion below catches that.
func TestEvictionDrainsBufferedWALBeforeWritingPage(t *testing.T) {
	tmp := t.TempDir()
	walDir := filepath.Join(tmp, "pg_wal")
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := xlog.NewWriter(xlog.Config{
		WALDir:      walDir,
		SegmentSize: 4096,
		WALBuffers:  1 << 16, // 64 KiB — small Append stays in RAM
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: tmp})
	defer mgr.Close()

	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 1, WAL: w})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// 1. Append a small WAL record. With wal_buffers=64 KiB it
	// stays in walBuf — segment file is still empty.
	payload := []byte("redo-record-v1")
	_, endLSN, err := w.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := segmentTotalBytes(t, walDir); got != 0 {
		t.Fatalf("segment bytes=%d after Append, want 0 (record should be in walBuf)", got)
	}

	// 2. Mutate a heap page and stamp its pd_lsn to endLSN. The
	// pool's eviction path will see this LSN and call
	// FlushUpTo(endLSN) before WriteBlock.
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9001, Fork: storage.MainFork}
	s0, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	s0.Page()[300] = 0x7A
	pool.MarkDirtyWithLSN(s0, storage.LSN(endLSN))
	pool.Unpin(s0)

	// 3. Force eviction with a second PinNew on the one-slot pool.
	s1, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s1)

	// 4a. Heap page mutation persisted.
	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[300] != 0x7A {
		t.Errorf("persisted heap byte = %#x, want 0x7A", got[300])
	}

	// 4b. WAL segment file is non-empty — Stage 1 drained walBuf
	// to disk before WriteBlock fired. THIS is the M0013-0002
	// invariant check: without Stage 1, the WAL bytes stay in
	// RAM, the heap byte still hits disk, and a crash here loses
	// the WAL record but keeps the data mutation.
	if got := segmentTotalBytes(t, walDir); got == 0 {
		t.Errorf("WAL segment bytes=0 after eviction — Stage 1 drain didn't run")
	}

	// 4c. ReadAll round-trip: the appended record is durable.
	records, err := xlog.ReadAll(walDir, 4096)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	found := false
	for _, r := range records {
		if bytes.Equal(r.Payload, payload) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("appended record not found in WAL after eviction; ReadAll returned %d records", len(records))
	}
}

// TestFlushAllPacedDrainsBufferedWAL pins the same WAL-before-data
// invariant for the checkpoint-driven batched flush path
// (Pool.FlushAllPaced → flushDirtyBatch → wal.FlushUpTo(maxLSN)).
// Three pages with three distinct buffered LSNs all need to round-
// trip through the WAL before their heap blocks hit disk.
func TestFlushAllPacedDrainsBufferedWAL(t *testing.T) {
	tmp := t.TempDir()
	walDir := filepath.Join(tmp, "pg_wal")
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := xlog.NewWriter(xlog.Config{
		WALDir:      walDir,
		SegmentSize: 4096,
		WALBuffers:  1 << 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: tmp})
	defer mgr.Close()
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 4, WAL: w})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9002, Fork: storage.MainFork}

	payloads := [][]byte{
		[]byte("redo-page-0"),
		[]byte("redo-page-1"),
		[]byte("redo-page-2"),
	}
	for i, p := range payloads {
		_, endLSN, err := w.Append(p)
		if err != nil {
			t.Fatal(err)
		}
		s, _, err := pool.PinNew(rel)
		if err != nil {
			t.Fatal(err)
		}
		s.Page()[200+i] = byte(0xA0 + i)
		pool.MarkDirtyWithLSN(s, storage.LSN(endLSN))
		pool.Unpin(s)
	}

	// Pre-condition: all three records still in walBuf.
	if got := segmentTotalBytes(t, walDir); got != 0 {
		t.Fatalf("segment bytes=%d before flush, want 0", got)
	}

	// Drive the checkpointer-style batched flush.
	if err := pool.FlushAllPaced(nil); err != nil {
		t.Fatalf("FlushAllPaced: %v", err)
	}

	// Every payload must be durable in the WAL.
	records, err := xlog.ReadAll(walDir, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range payloads {
		found := false
		for _, r := range records {
			if bytes.Equal(r.Payload, p) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("payload[%d]=%q missing from WAL after FlushAllPaced", i, p)
		}
	}

	// Every heap byte must be durable.
	for i := 0; i < 3; i++ {
		got := make(storage.Page, storage.BlockSize)
		if err := mgr.ReadBlock(rel, storage.BlockNumber(i), got); err != nil {
			t.Fatal(err)
		}
		if got[200+i] != byte(0xA0+i) {
			t.Errorf("page[%d] byte = %#x, want %#x", i, got[200+i], 0xA0+i)
		}
	}
}
