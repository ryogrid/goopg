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
