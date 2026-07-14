package initdb

// replayCLogFromWAL scans all WAL records after the last checkpoint and
// stamps commit/abort status into the clog and advances txnMgr.NextXID.
//
// PostgreSQL handles this in StartupXLOG via xact_redo_commit /
// xact_redo_abort: the CLOG (pg_xact) is treated as a write-behind cache
// whose authoritative state is the WAL, not an independent durable store.
// goopg mirrors that contract here: after physical WAL replay restores heap
// pages, a second pass over the same WAL records ensures every committed or
// aborted XID is visible to the clog and that txnMgr.NextXID is at least
// max(committed/aborted XID) + 1.
//
// This handles the narrow crash window where the WAL commit record was
// fsynced (making the transaction durable) but the clog flat-file and SLRU
// writes had not yet completed. It is complementary to the SLRU-load
// performed by EnablePGSLRUMirror (which covers the common SIGKILL case
// where the SLRU fsync completed but the flat-file write may be stale).
//
// (M0106-0013)

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// replayCLogFromWAL reads every WAL record under walDir and, for each
// commit or abort record, stamps the XID in clog and advances
// txnMgr.NextXID. A missing walDir is treated as a no-op (fresh cluster).
//
// Both native goopg records (RecordKindXactCommit / RecordKindXactAbort /
// RecordKindXactCommitInval) and canonical PG-format records
// (RmgrXact / xlogXactCommit / xlogXactAbort emitted in PageHeaders mode)
// are processed. The native records carry the XID at payload bytes 1..4;
// canonical records carry it in the XLogRecord header's XID field.
func replayCLogFromWAL(walDir string, clog *mvcc.CLog, txnMgr *mvcc.Manager) error {
	if _, err := os.Stat(walDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		// Non-existent wal dir is fine; other errors are unexpected but
		// we treat them as non-fatal (physical replay already succeeded).
		return nil
	}
	records, err := wal.ReadAll(filepath.Clean(walDir), 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	// Walk records from the first post-checkpoint position onward. This
	// skips pre-checkpoint records whose XIDs are already covered by the
	// checkpoint's pg_control state.
	startIdx, _ := wal.ExportedReplayStart(records)
	for _, r := range records[startIdx:] {
		// --- Native goopg commit/abort records ---
		if len(r.Payload) >= wal.XactRecordSize {
			switch r.Payload[0] {
			case wal.RecordKindXactCommit, wal.RecordKindXactCommitInval:
				xid := storage.TransactionID(binary.LittleEndian.Uint32(r.Payload[1:5]))
				xactStampAndAdvance(clog, txnMgr, xid, true)
				continue
			case wal.RecordKindXactAbort:
				xid := storage.TransactionID(binary.LittleEndian.Uint32(r.Payload[1:5]))
				xactStampAndAdvance(clog, txnMgr, xid, false)
				continue
			case wal.RecordKindClogTruncate:
				// G9: re-apply the (idempotent) CLOG truncation so the
				// post-recovery clog matches the durable WAL state. PG's
				// clog_redo CLOG_TRUNCATE branch does the same
				// (postgres/src/backend/access/transam/clog.c:1131).
				oldestXid := storage.TransactionID(binary.LittleEndian.Uint32(r.Payload[1:5]))
				replayClogTruncate(clog, oldestXid)
				continue
			}
		}
		// --- Canonical PG-format records (emitted alongside native in
		//     PageHeaders mode, M0106-0010 batched-46). ---
		if r.XLog != nil && r.XLog.Header.Rmid == wal.RmgrXact && r.XLog.Header.XID != 0 {
			xid := storage.TransactionID(r.XLog.Header.XID)
			isCommit := (r.XLog.Header.Info & wal.XlogXactOpMask) == wal.XlogXactCommit
			xactStampAndAdvance(clog, txnMgr, xid, isCommit)
		}
	}
	return nil
}

// replayClogTruncate re-applies a CLOG_TRUNCATE record (G9). Truncation is
// idempotent: TruncateCLOG advances oldestClogXid monotonically and removes
// only segments/banks/flat-file-prefix entirely below oldestXid's page, so
// replaying the same (or an older) record after recovery is a no-op. Errors
// are logged-and-ignored at the caller's discretion; the WAL record is the
// authoritative durability guarantee. We pass the in-recovery clog so its
// truncate-logger hook is NOT re-fired (open.go installs the logger only
// after this replay pass completes), preventing a recursive WAL append
// during recovery.
func replayClogTruncate(clog *mvcc.CLog, oldestXid storage.TransactionID) {
	if oldestXid == storage.InvalidTransactionID {
		return
	}
	_ = clog.TruncateCLOG(oldestXid)
}

// xactStampAndAdvance stamps the clog and advances txnMgr.NextXID past xid.
// Errors from SetCommitted/SetAborted are silently ignored (the WAL record
// is the authoritative durability guarantee; the clog is a write-behind
// cache per M0106-0013 design).
func xactStampAndAdvance(clog *mvcc.CLog, txnMgr *mvcc.Manager, xid storage.TransactionID, commit bool) {
	if xid == storage.InvalidTransactionID {
		return
	}
	if commit {
		_ = clog.SetCommitted(xid)
	} else {
		_ = clog.SetAborted(xid)
	}
	txnMgr.SetNextXID(xid + 1)
}

// walHasXactRecords reports whether the WAL under walDir contains at least
// one transaction commit/abort record (native or canonical). Used by
// initdb.Open to disambiguate CLog.IsEmpty()==true (C2-S3 review MUST-FIX):
// post-cut, commit-path CLOG stamps are memory-only, so a crashed cluster
// that never reached its first checkpoint has an all-zero pg_xact on disk —
// indistinguishable, by lanes alone, from a genuinely-fresh or
// pre-M0030-0007 cluster. Routing such a cluster into the
// InitializeAsCommitted upgrade branch would resurrect crashed in-flight
// transactions as committed; the presence of ANY xact record proves txn
// history and forces the crash-recovery sweep branch instead. Runs under
// the recovery ReadAll memoization, so the decode is shared with the replay
// passes.
func walHasXactRecords(walDir string) (bool, error) {
	if _, err := os.Stat(walDir); os.IsNotExist(err) {
		return false, nil
	}
	records, err := wal.ReadAll(walDir, 0)
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if len(r.Payload) >= 5 {
			switch r.Payload[0] {
			case wal.RecordKindXactCommit, wal.RecordKindXactCommitInval, wal.RecordKindXactAbort:
				return true, nil
			}
		}
		if r.XLog != nil && r.XLog.Header.Rmid == wal.RmgrXact && r.XLog.Header.XID != 0 {
			return true, nil
		}
	}
	return false, nil
}
