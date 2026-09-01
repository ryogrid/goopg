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

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/access/transam/xlog"
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
// nativeKindByte reports whether r.Payload[0] may be read as a goopg
// RecordKind tag, given that the caller needs at least minLen payload bytes.
// The kind byte is only trustworthy when the record's (xl_rmid, xl_info) is
// the pair recordKindToRmgrInfo assigns to it: a structurally real PG record
// carries raw struct bytes there, and its first byte can collide with a
// RecordKind constant — IsGoopgNativeRecord's own documentation names the
// WAL-scanning startup paths as the callers that must ask first. Both scanners
// in this file used to switch on the byte unguarded, latent only because a
// PG-format record reaches them with a nil Payload (review/260831-2 IN-3).
func nativeKindByte(r xlog.Record, minLen int) bool {
	return len(r.Payload) >= minLen && xlog.IsGoopgNativeRecord(r)
}

func replayCLogFromWAL(walDir string, clog *transam.CLog, txnMgr *transam.Manager) error {
	if _, err := os.Stat(walDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		// Non-existent wal dir is fine; other errors are unexpected but
		// we treat them as non-fatal (physical replay already succeeded).
		return nil
	}
	records, err := xlog.ReadAll(filepath.Clean(walDir), 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	// Walk records from the first post-checkpoint position onward. This
	// skips pre-checkpoint records whose XIDs are already covered by the
	// checkpoint's pg_control state.
	startIdx, _ := xlog.ExportedReplayStart(records)
	for _, r := range records[startIdx:] {
		// M0131-S30.7: nextXID must be beyond EVERY replayed record's XID,
		// not only the XIDs that reached a commit/abort record. Upstream does
		// exactly this, unconditionally, for each record it applies:
		// AdvanceNextFullTransactionIdPastXid(record->xl_xid) in
		// ApplyWalRecord (postgres/src/backend/access/transam/xlogrecovery.c:1942).
		//
		// Without it goopg's post-crash nextXID was max(committed XID)+1, so
		// the XIDs of transactions that were IN FLIGHT at the crash — whose
		// heap records are in the WAL (a concurrent commit flushes them) and
		// ARE replayed — got handed out again to new transactions. When such a
		// new transaction committed, it stamped the crashed transaction's XID
		// Committed and resurrected its replayed half, tearing an atomic
		// transaction apart. Measured on the preserved crashprobe30 clusters:
		// WAL tail carried XIDs up to 59985 with the last commit at 59974,
		// while the restarted server resumed at nextXid 59977.
		//
		// Advancing here also tightens the MarkUnknownAsAborted sweep in
		// initdb.Open, which stamps every still-Unknown XID below nextXID
		// Aborted — that is what makes an in-flight transaction's replayed
		// tuples correctly invisible, as upstream's clog-absent XIDs are.
		if r.XLog != nil && r.XLog.Header.XID != 0 {
			txnMgr.SetNextXID(storage.TransactionID(r.XLog.Header.XID) + 1)
		}
		// --- Native goopg commit/abort records ---
		if nativeKindByte(r, xlog.XactRecordSize) {
			switch r.Payload[0] {
			case xlog.RecordKindXactCommit:
				xid := storage.TransactionID(binary.LittleEndian.Uint32(r.Payload[1:5]))
				xactStampAndAdvance(clog, txnMgr, xid, true)
				continue
			case xlog.RecordKindXactAbort:
				xid := storage.TransactionID(binary.LittleEndian.Uint32(r.Payload[1:5]))
				xactStampAndAdvance(clog, txnMgr, xid, false)
				continue
			case xlog.RecordKindClogTruncate:
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
		if r.XLog != nil && r.XLog.Header.Rmid == xlog.RmgrXact {
			// M0131-S22: dispatch on the OPCODE. This branch used to read
			// "isCommit := info&OpMask == XLOG_XACT_COMMIT" and stamp
			// unconditionally, which made every RM_XACT_ID record that is not a
			// commit — XLOG_XACT_ASSIGNMENT (0x50) and XLOG_XACT_INVALIDATIONS
			// (0x60), both of which a real PG tail carries for any
			// savepoint-using or catalog-touching workload — stamp its
			// (still-running!) XID ABORTED. Upstream's xact_redo (xact.c:6398)
			// switches on the opcode and touches the clog only for
			// COMMIT/ABORT (and their two-phase variants, which goopg refuses
			// in the physical pass).
			op := r.XLog.Header.Info & xlog.XlogXactOpMask
			if op != xlog.XlogXactCommit && op != xlog.XlogXactAbort {
				continue
			}
			if r.XLog.Header.XID == 0 {
				continue
			}
			xid := storage.TransactionID(r.XLog.Header.XID)
			isCommit := op == xlog.XlogXactCommit
			xactStampAndAdvance(clog, txnMgr, xid, isCommit)
			// M0131-S22: stamp the whole transaction TREE, as
			// TransactionIdCommitTree / TransactionIdAbortTree do
			// (xact.c:6182, 6259). Without this a committed transaction's
			// subtransaction XIDs stay Unknown and initdb.Open's
			// MarkUnknownAsAborted sweep stamps them Aborted, so every row
			// written after a SAVEPOINT disappears from an otherwise-committed
			// transaction. A body goopg cannot parse leaves the top-level stamp
			// standing (the pre-S22 behaviour) rather than failing the start:
			// the reader already CRC-verified these bytes, so a parse error
			// means an unfamiliar record shape, and the top-level status is
			// still strictly better than none.
			if parsed, perr := xlog.ParseXactRecord(r.XLog.Header.Info, r.XLog.MainData); perr == nil {
				for _, sub := range parsed.Subxacts {
					xactStampAndAdvance(clog, txnMgr, storage.TransactionID(sub), isCommit)
				}
			}
			continue
		}
		// A9: PG-format clog truncation (RM_CLOG / CLOG_TRUNCATE). Decode the
		// xl_clog_truncate body and re-apply the (idempotent) truncation — the
		// PG-format twin of the native RecordKindClogTruncate branch above.
		if r.XLog != nil && r.XLog.Header.Rmid == xlog.RmgrCLOG &&
			(r.XLog.Header.Info&xlog.XLRRmgrInfoMask) == xlog.XlogClogTruncate {
			_, oldestXact, _, derr := xlog.DecodeXLogClogTruncate(r.XLog.MainData)
			if derr == nil {
				replayClogTruncate(clog, oldestXact)
			}
		}
	}
	return nil
}

// replayNextOIDFromWAL returns the highest OID published by an XLOG_NEXTOID
// record in the WAL under walDir, or 0 if the WAL carries none.
//
// M0131-S21a. This is the second half of XLOG_NEXTOID's replay: the physical
// pass (wal.replayDecodedXLogRecord) recognises the record and reports it as a
// page-level no-op, because the OID counter lives in the in-memory catalog and
// that pass is handed only a *storage.Manager. The split is the same one
// CLOG_TRUNCATE already uses.
//
// Why the record matters at all: initdb.Open seeds the counter from
// pg_control's checkPointCopy.nextOid, which is only as fresh as the LAST
// CHECKPOINT. PG allocates OIDs in blocks of VAR_OID_PREFETCH (8192) and WAL-logs
// each new block's ceiling exactly so the counter survives a crash between
// checkpoints (XLogPutNextOid, xlog.c:8114-8138). Without this pass, goopg
// starting on a PG cluster that crashed after allocating a fresh OID block
// would re-issue OIDs PG had already handed to real catalog rows.
//
// Upstream "believe the record exactly" (xlog.c:8296-8300) is a SET, not a max,
// because it must survive OID wraparound. goopg's counter has no wraparound
// path (catalog.InMemory.advanceNextOIDLocked is monotone) and the caller
// applies this alongside the pg_control seed, so taking the maximum of the two
// is the faithful behaviour here: both sources are lower bounds on what PG had
// already allocated, and the counter must clear both.
func replayNextOIDFromWAL(walDir string) (uint32, error) {
	if _, err := os.Stat(walDir); err != nil {
		return 0, nil
	}
	records, err := xlog.ReadAll(filepath.Clean(walDir), 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	// Unlike the clog pass this walks from record ZERO, not from
	// ExportedReplayStart: a NEXTOID record emitted BEFORE the last checkpoint
	// is not redundant with pg_control unless that checkpoint actually
	// refreshed nextOid, and reading one extra (idempotent, monotone) maximum
	// costs nothing. The records are already decoded by the shared recovery
	// ReadAll memoization.
	var maxOID uint32
	for _, r := range records {
		if r.XLog == nil || r.XLog.Header.Rmid != xlog.RmgrXLog {
			continue
		}
		if (r.XLog.Header.Info & xlog.XLRRmgrInfoMask) != xlog.XlogXLogNextOid {
			continue
		}
		nextOID, derr := xlog.DecodeXLogNextOid(r.XLog.MainData)
		if derr != nil {
			// A malformed body is not a reason to refuse the start — the
			// reader already CRC-verified the bytes, so this can only mean a
			// record shape goopg does not understand. Skipping it leaves the
			// counter at the pg_control seed, which is the pre-S21a behaviour.
			continue
		}
		if nextOID > maxOID {
			maxOID = nextOID
		}
	}
	return maxOID, nil
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
func replayClogTruncate(clog *transam.CLog, oldestXid storage.TransactionID) {
	if oldestXid == storage.InvalidTransactionID {
		return
	}
	_ = clog.TruncateCLOG(oldestXid)
}

// xactStampAndAdvance stamps the clog and advances txnMgr.NextXID past xid.
// Errors from SetCommitted/SetAborted are silently ignored (the WAL record
// is the authoritative durability guarantee; the clog is a write-behind
// cache per M0106-0013 design).
func xactStampAndAdvance(clog *transam.CLog, txnMgr *transam.Manager, xid storage.TransactionID, commit bool) {
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
	records, err := xlog.ReadAll(walDir, 0)
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if nativeKindByte(r, 5) {
			switch r.Payload[0] {
			case xlog.RecordKindXactCommit, xlog.RecordKindXactAbort:
				return true, nil
			}
		}
		if r.XLog != nil && r.XLog.Header.Rmid == xlog.RmgrXact && r.XLog.Header.XID != 0 {
			return true, nil
		}
	}
	return false, nil
}
