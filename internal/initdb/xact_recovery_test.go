package initdb

import (
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
	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
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

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
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

// (TestReplayCLogFromWAL_CommitInvalAlsoStamps was removed in A9 — the standalone
// RecordKindXactCommitInval is retired; a PG xl_xact_commit with the HAS_INVALS
// chunk stamps the commit via the RmgrXact scanner branch, already covered.)

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
	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
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

// TestReplayCLogFromWAL_RecoversUnflushedSyncCommit is the C2-S3 sibling of
// the async exemplar above: since the cut, a SYNCHRONOUS commit also leaves
// its CLOG page dirty in memory (SetCommittedDurable no longer performs the
// eager write-back — applyGroupBatchLocked is I/O-free). Durability rides on
// the already-flushed WAL commit record: after a simulated kill, replay must
// re-stamp the lane Committed.
func TestReplayCLogFromWAL_RecoversUnflushedSyncCommit(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	slruDir := filepath.Join(dir, "pg_xact")
	const xid = storage.TransactionID(mvcc.FirstNormalTransactionID + 200)
	const lsn = uint64(66666)

	live, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := live.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatal(err)
	}
	// The sync path since C2-S2..S4: LSN associated, NO disk write-back
	// (the sync branch now calls the same SetCommittedWithLSN as async).
	if err := live.SetCommittedWithLSN(xid, lsn); err != nil {
		t.Fatalf("SetCommittedWithLSN: %v", err)
	}
	if got := live.GetStatus(xid); got != mvcc.TxnStatusCommitted {
		t.Fatalf("live GetStatus(%d) = %v, want Committed", xid, got)
	}

	// Simulated SIGKILL: reopen against the same SLRU dir; the lane must NOT
	// be on disk (this is the discriminator that fails if the eager
	// write-back comes back).
	recovered, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact_flat2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatal(err)
	}
	if got := recovered.GetStatus(xid); got == mvcc.TxnStatusCommitted {
		t.Fatalf("recovered GetStatus(%d) already Committed before replay — sync commit still writes pg_xact eagerly (C2-S3 cut regressed)", xid)
	}

	// The WAL commit record IS durable (FlushUpTo before ack, fatal on error
	// since C2-S3) — replay reconstructs the commit.
	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
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

// TestReplayCLogFromWAL_OverridesMarkUnknownAsAborted pins the C2 I4
// override semantics: a blanket MarkUnknownAsAborted sweep and WAL replay
// compose safely in EITHER order, because the sweep is Unknown-only and
// replay stamps are terminal. (Production order since the C2-S3 rework is
// replay FIRST, then sweep — post-cut the on-disk lanes lag, so the sweep
// must not run before durable commits are re-stamped; this test exercises
// the reverse order to pin that a sweep can never clobber a replayed
// commit and that replay overrides a swept-aborted lane.)
func TestReplayCLogFromWAL_OverridesMarkUnknownAsAborted(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	slruDir := filepath.Join(dir, "pg_xact")
	const committedXid = storage.TransactionID(mvcc.FirstNormalTransactionID + 300)
	const inFlightXid = committedXid + 1

	c, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatal(err)
	}

	// Startup order, step 1: unknown lanes go aborted (both XIDs).
	if err := c.MarkUnknownAsAborted(inFlightXid + 1); err != nil {
		t.Fatalf("MarkUnknownAsAborted: %v", err)
	}
	if got := c.GetStatus(committedXid); got != mvcc.TxnStatusAborted {
		t.Fatalf("after MarkUnknownAsAborted GetStatus(%d) = %v, want Aborted", committedXid, got)
	}

	// Step 2: WAL replay overrides the acked commit back to Committed; the
	// genuinely in-flight XID (no commit record) stays aborted.
	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(wal.EncodeXactCommit(committedXid)); err != nil {
		t.Fatalf("Append commit: %v", err)
	}
	_ = w.Close()
	if err := replayCLogFromWAL(walDir, c, mvcc.NewManager()); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}
	if got := c.GetStatus(committedXid); got != mvcc.TxnStatusCommitted {
		t.Errorf("GetStatus(%d) = %v, want Committed (WAL must override the blanket abort)", committedXid, got)
	}
	if got := c.GetStatus(inFlightXid); got != mvcc.TxnStatusAborted {
		t.Errorf("GetStatus(%d) = %v, want Aborted (no commit record — stays aborted)", inFlightXid, got)
	}
}

// TestReplayCLogFromWAL_RecoversUnflushedAbort is the ROLLBACK sibling
// (C2-S3 adversarial MUST-FIX 2): an acked rollback's lane is memory-only
// since the cut, but its abort record is flushed before the ack (goopg
// deviation from PG — native records carry no xl_xid, so the durable abort
// record is also what advances NextXID past the XID, closing the
// XID-reuse/resurrection window). After a simulated kill, replay must
// re-stamp Aborted and advance NextXID.
func TestReplayCLogFromWAL_RecoversUnflushedAbort(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	slruDir := filepath.Join(dir, "pg_xact")
	const xid = storage.TransactionID(mvcc.FirstNormalTransactionID + 400)

	live, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := live.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatal(err)
	}
	if err := live.SetAborted(xid); err != nil {
		t.Fatalf("SetAborted: %v", err)
	}

	recovered, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact_flat2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatal(err)
	}
	if got := recovered.GetStatus(xid); got == mvcc.TxnStatusAborted {
		t.Fatalf("aborted lane already on disk before replay — the C2-S3 cut regressed for aborts")
	}

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(wal.EncodeXactAbort(xid)); err != nil {
		t.Fatalf("Append abort: %v", err)
	}
	_ = w.Close()

	txnMgr := mvcc.NewManager()
	if err := replayCLogFromWAL(walDir, recovered, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}
	if got := recovered.GetStatus(xid); got != mvcc.TxnStatusAborted {
		t.Errorf("GetStatus(%d) after replay = %v, want Aborted", xid, got)
	}
	if next := txnMgr.NextXID(); next <= xid {
		t.Errorf("NextXID = %d after replay, want > %d (XID-reuse window)", next, xid)
	}
}

// TestReplayCLogFromWAL_AdvancesPastInFlightXID pins M0131-S30.7: nextXID after
// crash recovery must be beyond the XID of EVERY replayed record, not only
// beyond the XIDs that reached a commit/abort record.
//
// The crashprobe30 measurement that motivated this: the WAL tail carried
// records for XIDs up to 59985 (transactions still in flight when the server
// was SIGKILLed — a concurrent commit had already flushed their heap records),
// the last commit record was 59974, and the restarted server resumed handing
// out XIDs at 59977. Those reused XIDs were then stamped Committed by NEW
// transactions, resurrecting the in-flight transactions' replayed half and
// breaking pgbench's atomicity invariant
// (sum(pgbench_accounts.abalance) != sum(pgbench_history.delta)) in both
// directions. Upstream advances unconditionally per record —
// AdvanceNextFullTransactionIdPastXid(record->xl_xid),
// postgres/src/backend/access/transam/xlogrecovery.c:1942.
func TestReplayCLogFromWAL_AdvancesPastInFlightXID(t *testing.T) {
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

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	const committed = storage.TransactionID(11)
	const inFlight = storage.TransactionID(17)
	if _, _, err := w.Append(wal.EncodeXactCommit(committed)); err != nil {
		t.Fatalf("Append commit: %v", err)
	}
	// A record belonging to a transaction that never committed: same shape as
	// the pgbench UPDATE whose commit record never made it to disk.
	framed, err := wal.EncodeSmgrCreatePG(storage.RelFileNode{DBOid: 5, RelOid: 16407}, inFlight)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(framed); err != nil {
		t.Fatalf("Append in-flight record: %v", err)
	}
	_ = w.Close()

	if err := replayCLogFromWAL(walDir, clog, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}

	if got := txnMgr.NextXID(); got <= inFlight {
		t.Errorf("NextXID = %d after replay, want > %d — the in-flight XID would be REUSED", got, inFlight)
	}
	// The in-flight XID must not be committed: the implicit-abort sweep in
	// initdb.Open (MarkUnknownAsAborted, bounded by NextXID) is what stamps it
	// Aborted, and it can only reach XIDs below NextXID.
	if got := clog.GetStatus(inFlight); got == mvcc.TxnStatusCommitted {
		t.Errorf("GetStatus(%d) = Committed after replay, want not-committed", inFlight)
	}
	if err := clog.MarkUnknownAsAborted(txnMgr.NextXID()); err != nil {
		t.Fatalf("MarkUnknownAsAborted: %v", err)
	}
	if got := clog.GetStatus(inFlight); got != mvcc.TxnStatusAborted {
		t.Errorf("GetStatus(%d) after sweep = %v, want Aborted", inFlight, got)
	}
	if got := clog.GetStatus(committed); got != mvcc.TxnStatusCommitted {
		t.Errorf("GetStatus(%d) after sweep = %v, want Committed", committed, got)
	}
}

// TestReplayNextOIDFromWAL is the initdb half of M0131-S21a's XLOG_NEXTOID
// replay. The physical pass recognises the record and reports "no page
// changed"; this scan is what actually recovers the counter.
//
// The failure it prevents: initdb.Open seeds the OID counter from
// pg_control's checkPointCopy.nextOid, which is only refreshed at a
// checkpoint. PG allocates OIDs in blocks of VAR_OID_PREFETCH (8192) and
// WAL-logs each new block's ceiling (XLogPutNextOid, xlog.c:8114-8138). A PG
// cluster that crashed after allocating a block therefore holds catalog rows
// whose OIDs are ABOVE anything pg_control knows — and goopg, starting on that
// directory, would hand those same OIDs out again.
func TestReplayNextOIDFromWAL(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	// Two blocks, logged in allocation order, with an unrelated record
	// between them: the scan must return the HIGHEST, not the last-seen or
	// the first.
	for _, oid := range []uint32{24576, 32768} {
		payload, eerr := wal.EncodeXLogNextOidPG(oid)
		if eerr != nil {
			t.Fatal(eerr)
		}
		if _, _, aerr := w.Append(payload); aerr != nil {
			t.Fatalf("Append NEXTOID %d: %v", oid, aerr)
		}
		if _, _, aerr := w.Append(wal.EncodeXactCommit(storage.TransactionID(7))); aerr != nil {
			t.Fatal(aerr)
		}
	}
	_ = w.Close()

	got, err := replayNextOIDFromWAL(walDir)
	if err != nil {
		t.Fatalf("replayNextOIDFromWAL: %v", err)
	}
	if got != 32768 {
		t.Fatalf("nextOID = %d, want 32768 (highest XLOG_NEXTOID in the tail)", got)
	}
}

// TestReplayNextOIDFromWALNoRecords: a WAL with no XLOG_NEXTOID — every WAL
// goopg itself writes — must return 0 so the caller's AdvanceNextOIDPast is a
// no-op and the pg_control seed stands. A missing pg_wal is the same answer.
func TestReplayNextOIDFromWALNoRecords(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(wal.EncodeXactCommit(storage.TransactionID(11))); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	got, err := replayNextOIDFromWAL(walDir)
	if err != nil {
		t.Fatalf("replayNextOIDFromWAL: %v", err)
	}
	if got != 0 {
		t.Fatalf("nextOID = %d, want 0 (no XLOG_NEXTOID in this WAL)", got)
	}

	if got, err = replayNextOIDFromWAL(filepath.Join(dir, "absent_wal")); err != nil || got != 0 {
		t.Fatalf("missing walDir: got (%d, %v), want (0, nil)", got, err)
	}
}

// --- M0131-S22: opcode dispatch + commit-record subxacts[] ---------------

// newRecoveryCLog opens the pair of stores the clog replay pass writes through
// (flat file + PG SLRU mirror), matching the other tests in this file.
func newRecoveryCLog(t *testing.T, dir string) *mvcc.CLog {
	t.Helper()
	clog, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clog.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact_slru")); err != nil {
		t.Fatal(err)
	}
	return clog
}

// TestReplayCLogFromWAL_PGCommitStampsSubxacts is the S22 core guard. A real
// PG commit record for a transaction that used a SAVEPOINT carries an
// XACT_XINFO_HAS_SUBXACTS chunk, and upstream's xact_redo_commit stamps the
// whole tree via TransactionIdCommitTree (xact.c:6182). Before S22 goopg
// stamped only the top-level XID, so initdb.Open's MarkUnknownAsAborted sweep
// stamped every subtransaction ABORTED — the committed transaction's
// after-the-savepoint rows silently vanished after a reverse crash start.
func TestReplayCLogFromWAL_PGCommitStampsSubxacts(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	clog := newRecoveryCLog(t, dir)
	txnMgr := mvcc.NewManager()

	const top = storage.TransactionID(900)
	subs := []storage.TransactionID{901, 902, 903}

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := wal.EncodeXactCommitPGWithSubxacts(top, subs, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(payload); err != nil {
		t.Fatalf("Append commit: %v", err)
	}
	_ = w.Close()

	if err := replayCLogFromWAL(walDir, clog, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}
	for _, xid := range append([]storage.TransactionID{top}, subs...) {
		if got := clog.GetStatus(xid); got != mvcc.TxnStatusCommitted {
			t.Errorf("XID %d: got %v, want TxnStatusCommitted (whole tree must be stamped)", xid, got)
		}
	}
	// nextXID must clear the HIGHEST XID in the tree, not just the top-level
	// one (upstream: TransactionIdLatest over xid+subxacts, xact.c:6190).
	if got := txnMgr.NextXID(); got <= subs[len(subs)-1] {
		t.Errorf("NextXID = %d, want > %d (highest subxact)", got, subs[len(subs)-1])
	}
}

// TestReplayCLogFromWAL_PGAbortStampsSubxacts: the abort twin
// (TransactionIdAbortTree, xact.c:6259). A subtransaction of an aborted
// transaction must be Aborted explicitly, not left to the sweep — the sweep
// only covers XIDs below nextXID, and it is the same code path either way, so
// a silent divergence here is a sibling-path bug waiting to happen.
func TestReplayCLogFromWAL_PGAbortStampsSubxacts(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	clog := newRecoveryCLog(t, dir)
	txnMgr := mvcc.NewManager()

	const top = storage.TransactionID(950)
	subs := []storage.TransactionID{951, 952}

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := wal.EncodeXactAbortPGWithSubxacts(top, subs)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(payload); err != nil {
		t.Fatalf("Append abort: %v", err)
	}
	_ = w.Close()

	if err := replayCLogFromWAL(walDir, clog, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}
	for _, xid := range append([]storage.TransactionID{top}, subs...) {
		if got := clog.GetStatus(xid); got != mvcc.TxnStatusAborted {
			t.Errorf("XID %d: got %v, want TxnStatusAborted", xid, got)
		}
	}
}

// TestReplayCLogFromWAL_PGAssignmentIsNotAnAbort pins the second half of the
// S22 bug. The scanner used to compute `isCommit := info&OpMask == COMMIT` for
// ANY RM_XACT_ID record with a non-zero XID, so an XLOG_XACT_ASSIGNMENT (0x50)
// — which a real PG emits for every batch of PGPROC_MAX_CACHED_SUBXIDS
// subtransactions, long BEFORE the transaction ends — stamped its still-running
// XID ABORTED. The later commit record for the same tree then had to fight a
// durable abort stamp. Upstream's xact_redo switches on the opcode and touches
// the clog only for COMMIT/ABORT (xact.c:6398-6444).
func TestReplayCLogFromWAL_PGAssignmentIsNotAnAbort(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	clog := newRecoveryCLog(t, dir)
	txnMgr := mvcc.NewManager()

	const top = storage.TransactionID(1000)
	subs := []storage.TransactionID{1001, 1002}

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	assign, err := wal.EncodeXactAssignmentPG(top, subs)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(assign); err != nil {
		t.Fatalf("Append assignment: %v", err)
	}
	_ = w.Close()

	if err := replayCLogFromWAL(walDir, clog, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}
	for _, xid := range append([]storage.TransactionID{top}, subs...) {
		if got := clog.GetStatus(xid); got == mvcc.TxnStatusAborted {
			t.Errorf("XID %d: XLOG_XACT_ASSIGNMENT stamped it Aborted; an assignment is not a completion record", xid)
		}
	}
}

// TestReplayCLogFromWAL_PGAssignmentThenCommit is the end-to-end shape of the
// real tail: assignment records first, the commit last. The tree must end up
// COMMITTED. Measured with a scripted revert: this one does NOT catch the
// missing opcode dispatch on its own (the later commit overwrites the bogus
// abort stamp, so that half self-heals in this ordering — which is why the
// standalone-assignment guard above exists), but it does catch a dropped
// subxact walk, and it pins the two halves working together on the record
// sequence a real PG actually writes.
func TestReplayCLogFromWAL_PGAssignmentThenCommit(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	clog := newRecoveryCLog(t, dir)
	txnMgr := mvcc.NewManager()

	const top = storage.TransactionID(1100)
	subs := []storage.TransactionID{1101, 1102}

	w, err := wal.NewWriter(wal.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	assign, err := wal.EncodeXactAssignmentPG(top, subs)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(assign); err != nil {
		t.Fatal(err)
	}
	commit, err := wal.EncodeXactCommitPGWithSubxacts(top, subs, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(commit); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	if err := replayCLogFromWAL(walDir, clog, txnMgr); err != nil {
		t.Fatalf("replayCLogFromWAL: %v", err)
	}
	for _, xid := range append([]storage.TransactionID{top}, subs...) {
		if got := clog.GetStatus(xid); got != mvcc.TxnStatusCommitted {
			t.Errorf("XID %d: got %v, want TxnStatusCommitted", xid, got)
		}
	}
}
