package initdb

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// TestReplayCLogFromWAL_NativeCommit verifies that a native
// RecordKindXactCommit record stamps the clog and advances txnMgr.NextXID.
func TestReplayCLogFromWAL_NativeCommit(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")

	clog, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clog.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact_slru")); err != nil {
		t.Fatal(err)
	}
	txnMgr := mvcc.NewManager()

	// Write a WAL segment with one commit record for XID=5.
	xid := storage.TransactionID(5)
	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: false})
	if err != nil {
		t.Fatal(err)
	}
	payload := wal.EncodeXactCommit(xid)
	if _, _, err := w.Append(payload); err != nil {
		t.Fatalf("Append commit: %v", err)
	}
	_ = w.Close()

	if err := replayCLogFromWAL(walDir, clog, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}

	if got := clog.GetStatus(xid); got != mvcc.TxnStatusCommitted {
		t.Errorf("XID %d: got %v want TxnStatusCommitted", xid, got)
	}
	if got := txnMgr.NextXID(); got <= xid {
		t.Errorf("NextXID %d <= xid %d after commit replay", got, xid)
	}
}

// TestReplayCLogFromWAL_NativeAbort verifies that a native
// RecordKindXactAbort record stamps the clog as aborted.
func TestReplayCLogFromWAL_NativeAbort(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")

	clog, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clog.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact_slru")); err != nil {
		t.Fatal(err)
	}
	txnMgr := mvcc.NewManager()

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: false})
	if err != nil {
		t.Fatal(err)
	}
	xid := storage.TransactionID(7)
	payload := wal.EncodeXactAbort(xid)
	if _, _, err := w.Append(payload); err != nil {
		t.Fatalf("Append abort: %v", err)
	}
	_ = w.Close()

	if err := replayCLogFromWAL(walDir, clog, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}

	if got := clog.GetStatus(xid); got != mvcc.TxnStatusAborted {
		t.Errorf("XID %d: got %v want TxnStatusAborted", xid, got)
	}
}

// TestReplayCLogFromWAL_CommitInvalAlsoStamps verifies that a
// RecordKindXactCommitInval record is treated as a commit.
func TestReplayCLogFromWAL_CommitInvalAlsoStamps(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")

	clog, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clog.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact_slru")); err != nil {
		t.Fatal(err)
	}
	txnMgr := mvcc.NewManager()

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: false})
	if err != nil {
		t.Fatal(err)
	}
	xid := storage.TransactionID(9)
	// Build a CommitInval payload manually (same 5-byte format).
	payload := make([]byte, wal.XactRecordSize)
	payload[0] = wal.RecordKindXactCommitInval
	binary.LittleEndian.PutUint32(payload[1:5], uint32(xid))
	if _, _, err := w.Append(payload); err != nil {
		t.Fatalf("Append commitInval: %v", err)
	}
	_ = w.Close()

	if err := replayCLogFromWAL(walDir, clog, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}

	if got := clog.GetStatus(xid); got != mvcc.TxnStatusCommitted {
		t.Errorf("XID %d (CommitInval): got %v want TxnStatusCommitted", xid, got)
	}
}

// TestReplayCLogFromWAL_RecoversUnflushedAsyncCommit pins the correctness
// invariant M0117-0007 Part B's lazy async-commit write-back (mvcc.CLog.
// setStatusWithLSN) now depends on: a CLOG page dirtied by an async commit
// but never durably flushed (no checkpoint, no eviction, no later sync
// commit) before a crash must still have its status reconstructed by WAL
// replay — the same backstop that already covers the pre-existing "WAL
// fsynced but clog write not yet complete" narrow window this change widens
// from nanoseconds to up to checkpoint_timeout.
func TestReplayCLogFromWAL_RecoversUnflushedAsyncCommit(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	slruDir := filepath.Join(dir, "pg_xact")
	const xid = storage.TransactionID(mvcc.FirstNormalTransactionID + 100)
	const lsn = uint64(55555)

	// Simulate the live server: an async commit marks the page dirty and
	// commits successfully without a durable write-back (no FlushWALHook
	// installed, mirroring the default-off barrier; no FlushAll/checkpoint
	// runs either).
	live, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := live.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatal(err)
	}
	if err := live.SetCommittedWithLSN(xid, lsn); err != nil {
		t.Fatalf("SetCommittedWithLSN: %v", err)
	}
	if got := live.GetStatus(xid); got != mvcc.TxnStatusCommitted {
		t.Fatalf("live in-memory GetStatus(%d) = %v, want Committed", xid, got)
	}
	// Deliberately do NOT call live.FlushAll() — the dirty page is "lost" in
	// the simulated crash, exactly like an in-memory-only buffer pool would
	// be on a real process kill.

	// Simulate the crash + restart: open a fresh CLog against the SAME SLRU
	// directory. Since the page was never flushed, disk still shows the
	// pre-commit (in-progress) lane.
	recovered, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact_flat2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatal(err)
	}
	if got := recovered.GetStatus(xid); got == mvcc.TxnStatusCommitted {
		t.Fatalf("recovered GetStatus(%d) already Committed before replay — the dirty page must not have been flushed for this test to be meaningful", xid)
	}

	// The WAL, however, is durable (that's the whole point of async commit:
	// the client only waited for the local WAL flush, not the CLOG
	// write-back) — write the matching commit record and replay it, exactly
	// as initdb.Open's crash-recovery path does.
	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(wal.EncodeXactCommit(xid)); err != nil {
		t.Fatalf("Append commit: %v", err)
	}
	_ = w.Close()

	txnMgr := mvcc.NewManager()
	if err := replayCLogFromWAL(walDir, recovered, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}
	if got := recovered.GetStatus(xid); got != mvcc.TxnStatusCommitted {
		t.Errorf("recovered GetStatus(%d) after replay = %v, want Committed", xid, got)
	}
}

// TestReplayCLogFromWAL_MissingWalDir verifies a missing WAL directory is
// treated as a no-op (fresh cluster has no WAL yet).
func TestReplayCLogFromWAL_MissingWalDir(t *testing.T) {
	dir := t.TempDir()
	clog, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clog.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact_slru")); err != nil {
		t.Fatal(err)
	}
	txnMgr := mvcc.NewManager()
	// Non-existent WAL dir — must not error.
	if err := replayCLogFromWAL(filepath.Join(dir, "nonexistent_pg_wal"), clog, txnMgr); err != nil {
		t.Errorf("replayCLogFromWAL with missing dir: %v", err)
	}
}
