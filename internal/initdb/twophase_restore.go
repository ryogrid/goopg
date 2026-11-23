package initdb

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/storage"
)

// RecoveredPrepared is a prepared transaction a restart brought back.
type RecoveredPrepared struct {
	GID string
	XID storage.TransactionID
	// Tx holds the restored transaction's dedicated proc slot; COMMIT /
	// ROLLBACK PREPARED finish it through Manager.Commit / Rollback.
	Tx         transam.Transaction
	Database   uint32
	Owner      uint32
	PreparedAt int64
}

// restoreTwoPhaseData is restoreTwoPhaseData + the PREPARE half of crash
// recovery + RecoverPreparedTransactions, for a primary's startup:
//
//  1. every pg_twophase/<XID> file (CRC-checked) is a prepared transaction
//     whose PREPARE preceded the last checkpoint's redo point;
//  2. the WAL from redo onward adds each XLOG_XACT_PREPARE
//     (PrepareRedoAdd) and drops each COMMIT/ABORT PREPARED
//     (PrepareRedoRemove) — their clog stamps come from replayCLogFromWAL;
//  3. what is left is put back in progress (Manager.RestorePrepared) and
//     returned for the server to finish later.
//
// A file for a transaction whose finishing record is replayed is removed.
// M0149-0015a.
func restoreTwoPhaseData(dataDir string, txnMgr *transam.Manager) ([]RecoveredPrepared, error) {
	type pending struct {
		st     xlog.ParsedTwoPhaseState
		raw    []byte
		end    uint64
		onDisk bool
	}
	found := map[storage.TransactionID]*pending{}

	dir := filepath.Join(dataDir, transam.TwoPhaseDirName)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || len(name) != 16 {
			continue
		}
		if _, perr := strconv.ParseUint(name, 16, 64); perr != nil {
			continue
		}
		raw, rerr := transam.ReadTwoPhaseFile(filepath.Join(dir, name))
		if rerr != nil {
			// ProcessTwoPhaseBuffer: a corrupt file is a WARNING and the file
			// is ignored.
			slog.Warn("removing corrupt two-phase state file", "file", name, "err", rerr)
			continue
		}
		st, perr := xlog.ParseTwoPhaseState(raw)
		if perr != nil {
			slog.Warn("ignoring unreadable two-phase state file", "file", name, "err", perr)
			continue
		}
		found[st.XID] = &pending{st: st, raw: raw, onDisk: true}
	}

	walDir := filepath.Join(dataDir, "pg_wal")
	if _, serr := os.Stat(walDir); serr == nil {
		records, rerr := xlog.ReadAll(walDir, 0)
		if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return nil, rerr
		}
		startIdx, _ := xlog.ExportedReplayStart(records)
		for _, r := range records[startIdx:] {
			if r.XLog == nil || r.XLog.Header.Rmid != xlog.RmgrXact {
				continue
			}
			op := r.XLog.Header.Info & xlog.XlogXactOpMask
			switch op {
			case xlog.XlogXactPrepare:
				st, perr := xlog.ParseTwoPhaseState(r.XLog.MainData)
				if perr != nil {
					return nil, fmt.Errorf("prepare record at %X: %w", r.EndLSN, perr)
				}
				raw := append([]byte(nil), r.XLog.MainData...)
				if p, ok := found[st.XID]; ok && p.onDisk {
					// Already restored from its file (PrepareRedoAdd skips a
					// transaction whose state file exists).
					p.end = r.EndLSN
					continue
				}
				found[st.XID] = &pending{st: st, raw: raw, end: r.EndLSN}
			case xlog.XlogXactCommitPrepared, xlog.XlogXactAbortPrepared:
				p, perr := xlog.ParseXactRecord(r.XLog.Header.Info, r.XLog.MainData)
				if perr != nil {
					return nil, fmt.Errorf("finish-prepared record at %X: %w", r.EndLSN, perr)
				}
				xid := storage.TransactionID(p.TwophaseXid)
				if q, ok := found[xid]; ok && q.onDisk {
					_ = os.Remove(filepath.Join(dir, transam.TwoPhaseFileName(xid)))
				}
				delete(found, xid)
			}
		}
	}

	xids := make([]storage.TransactionID, 0, len(found))
	for x := range found {
		xids = append(xids, x)
	}
	sort.Slice(xids, func(i, j int) bool { return xids[i] < xids[j] })
	var out []RecoveredPrepared
	for _, x := range xids {
		p := found[x]
		tx, rerr := txnMgr.RestorePrepared(transam.PreparedXact{
			XID: x, Subxacts: p.st.Subxacts, GID: p.st.GID, State: p.raw,
			PrepareEndLSN: p.end, OnDisk: p.onDisk,
		})
		if rerr != nil {
			return nil, rerr
		}
		slog.Info("recovering prepared transaction", "xid", x, "gid", p.st.GID, "subxacts", len(p.st.Subxacts))
		out = append(out, RecoveredPrepared{
			GID: p.st.GID, XID: x, Tx: tx, Database: p.st.Database, Owner: p.st.Owner, PreparedAt: p.st.PreparedAt,
		})
	}
	return out, nil
}
