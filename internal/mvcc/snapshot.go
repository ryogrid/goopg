package mvcc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/storage"
)

// IsolationLevel is the SQL transaction isolation level used for
// snapshot acquisition.
type IsolationLevel int

const (
	IsolationReadCommitted IsolationLevel = iota
	IsolationRepeatableRead
	// IsolationSerializable marks transactions that must satisfy
	// PostgreSQL SERIALIZABLE semantics. M0104-0001 surfaces the level
	// as a distinct enum value so downstream SSI hooks (M0104-0002+)
	// can branch on it; snapshot acquisition currently matches
	// REPEATABLE READ until the predicate-lock substrate lands.
	IsolationSerializable
)

func (l IsolationLevel) String() string {
	switch l {
	case IsolationReadCommitted:
		return "read committed"
	case IsolationRepeatableRead:
		return "repeatable read"
	case IsolationSerializable:
		return "serializable"
	default:
		return fmt.Sprintf("unknown(%d)", int(l))
	}
}

// ParseIsolationLevel accepts PostgreSQL-style names.
//
// READ UNCOMMITTED is folded to READ COMMITTED (upstream parity: weakest
// isolation level we honor). SERIALIZABLE returns IsolationSerializable so
// SSI-aware code paths (M0104) can distinguish it from REPEATABLE READ.
func ParseIsolationLevel(v string) (IsolationLevel, error) {
	key := strings.ToLower(strings.TrimSpace(v))
	switch key {
	case "read uncommitted", "read committed":
		return IsolationReadCommitted, nil
	case "repeatable read":
		return IsolationRepeatableRead, nil
	case "serializable":
		return IsolationSerializable, nil
	default:
		return 0, fmt.Errorf("mvcc: unsupported isolation level %q", v)
	}
}

// Snapshot is the immutable visibility horizon for one statement.
//
// XIDs strictly below Xmin are treated as completed (committed for v0);
// XIDs >= Xmax are in the future; in-between XIDs are in-progress iff
// present in InProgress.
//
// Aborted holds XIDs whose transactions were rolled back. A tuple whose
// xmin is in Aborted is invisible even when xmin < Xmin (without Aborted,
// goopg v0 would incorrectly treat rolled-back rows as committed once
// they fall below Xmin). This is a lightweight substitute for a full
// clog (commit log) — M0100-0002.
type Snapshot struct {
	Xmin       storage.TransactionID
	Xmax       storage.TransactionID
	InProgress []storage.TransactionID
	Aborted    []storage.TransactionID

	// PartitionDetachEpoch is the value of the global partition-detach epoch
	// (CurrentPartitionDetachEpoch) at the moment this snapshot was captured.
	// catalog.VisiblePartitionChildren omits a partition child whose
	// DetachPendingEpoch is <= this value, so a snapshot taken before a
	// concurrent DETACH PARTITION CONCURRENTLY still scans the partition while
	// later snapshots do not. Zero for snapshots captured outside the manager
	// (e.g. in unit tests). Design 0118-0058 (M0118-0008).
	PartitionDetachEpoch uint64

	// clog, when non-nil, is the durable commit log consulted as a fallback
	// for in-window XIDs the in-memory InProgress/Aborted arrays cannot
	// classify (gap G4 / M0117-0002). It mirrors PostgreSQL's
	// TransactionIdDidCommit/DidAbort consult after the running-array check in
	// HeapTupleSatisfiesMVCC. nil (the default) preserves the pre-M0117-0002
	// v0 behaviour exactly. Set via WithCLog / Manager.SetCLog.
	clog *CLog
}

// Clone deep-copies the snapshot so callers can hold it independently
// from manager internals.
func (s Snapshot) Clone() Snapshot {
	out := Snapshot{
		Xmin:                 s.Xmin,
		Xmax:                 s.Xmax,
		InProgress:           make([]storage.TransactionID, len(s.InProgress)),
		Aborted:              make([]storage.TransactionID, len(s.Aborted)),
		clog:                 s.clog,
		PartitionDetachEpoch: s.PartitionDetachEpoch,
	}
	copy(out.InProgress, s.InProgress)
	copy(out.Aborted, s.Aborted)
	return out
}

// WithCLog returns a copy of the snapshot whose visibility checks consult the
// durable commit log c as a fallback for in-window XIDs the in-memory
// InProgress/Aborted arrays cannot classify (M0117-0002). Passing nil restores
// the pure in-memory v0 behaviour. The snapshot is otherwise unchanged; the XID
// arrays are shared (not deep-copied) since they are immutable after capture.
func (s Snapshot) WithCLog(c *CLog) Snapshot {
	s.clog = c
	return s
}

// snapshotLinearScanThreshold is the InProgress array length at or
// below which the per-tuple visibility check uses a straight-line
// scan over the slice. Above this, sort.Search's binary search wins.
// The crossover is empirical (M0069-0007 microbenchmark): at SF=1
// single-thread the typical InProgress is 1-3 entries, so the
// threshold keeps the hot path branch-prediction-friendly while
// guarding against pathological N (concurrent OLTP + analytical
// mix).
const snapshotLinearScanThreshold = 16

// HasInProgress returns true when xid is listed in the snapshot's
// in-progress array.
//
// The array is sorted ascending at snapshot construction time
// (manager.captureSnapshotLocked), so we can binary-search above
// snapshotLinearScanThreshold. For small N the linear scan stays
// because branch prediction + cache locality make the simple loop
// faster than sort.Search's call overhead.
func (s Snapshot) HasInProgress(xid storage.TransactionID) bool {
	n := len(s.InProgress)
	if n <= snapshotLinearScanThreshold {
		for _, in := range s.InProgress {
			if in == xid {
				return true
			}
		}
		return false
	}
	idx := sort.Search(n, func(i int) bool {
		return s.InProgress[i] >= xid
	})
	return idx < n && s.InProgress[idx] == xid
}

// HasAborted returns true when xid belongs to a rolled-back transaction
// whose status is tracked in this snapshot's Aborted list (M0100-0002).
func (s Snapshot) HasAborted(xid storage.TransactionID) bool {
	n := len(s.Aborted)
	if n == 0 {
		return false
	}
	if n <= snapshotLinearScanThreshold {
		for _, a := range s.Aborted {
			if a == xid {
				return true
			}
		}
		return false
	}
	idx := sort.Search(n, func(i int) bool { return s.Aborted[i] >= xid })
	return idx < n && s.Aborted[idx] == xid
}

// XidIsConcurrent reports whether xid belongs to a transaction that ran
// concurrently with this snapshot — i.e. the snapshot does NOT already see
// xid's effects as committed. It mirrors PostgreSQL's XidIsConcurrent
// (predicate.c): an xid strictly below Xmin committed before the snapshot was
// taken (not concurrent); an xid at or above Xmax began after the snapshot (or
// is still running — concurrent); an in-window xid is concurrent iff it is in
// the in-progress set.
//
// SSI's CheckForSerializableConflictOut uses this to suppress a phantom
// rw-conflict against a writer the reader already sees as committed: reading a
// tuple version whose writer committed before our snapshot is an ordinary read
// (wr-dependency), not an anti-dependency.
func (s Snapshot) XidIsConcurrent(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	if xid < s.Xmin {
		return false
	}
	if xid >= s.Xmax {
		return true
	}
	return s.HasInProgress(xid)
}

// SeesCommittedXID reports whether xid is visible as committed to this
// snapshot under the v0 model.
func (s Snapshot) SeesCommittedXID(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	// Explicitly-aborted XIDs are never visible, even if they fall below
	// Xmin (where they'd otherwise be assumed committed). M0100-0002.
	if s.HasAborted(xid) {
		return false
	}
	// M0131-S30.7: the CLOG consult must precede the below-Xmin shortcut, not
	// only guard the in-window residual case. After crash recovery every XID
	// that was in flight at the crash is stamped Aborted by
	// initdb.Open's MarkUnknownAsAborted sweep, but NextXID is then advanced
	// past all of them — so the FIRST snapshot a post-restart session takes has
	// Xmin above the whole aborted range and the shortcut below declared their
	// replayed heap changes committed. That is how a torn pgbench transaction
	// (accounts/tellers updates durable, its history INSERT past the WAL cut)
	// became visible and broke sum(abalance) == sum(history.delta). PostgreSQL
	// has no such shortcut: HeapTupleSatisfiesMVCC always resolves a non-hinted
	// xmin through TransactionIdDidCommit → the CLOG, whatever the snapshot's
	// xmin is (heapam_visibility.c). The hint bits (HeapXminCommitted /
	// HeapXminInvalid, checked by the caller in visibility.go) are what keep
	// this off the repeat-scan hot path, exactly as upstream.
	if !s.clogSaysNotAborted(xid) {
		return false
	}
	if xid < s.Xmin {
		return true
	}
	if xid >= s.Xmax {
		return false
	}
	if s.HasInProgress(xid) {
		return false
	}
	// In-window residual case: xid is not running relative to this snapshot and
	// not in the in-memory Aborted list. The in-memory arrays assume such an XID
	// committed, but that list is rebuilt empty on restart and is NOT the durable
	// commit log. Consult the CLOG when available (gap G4 / M0117-0002), mirroring
	// PostgreSQL's TransactionIdDidAbort consult in HeapTupleSatisfiesMVCC.
	//
	// Conservative contract: only a positive TxnStatusAborted overrides the v0
	// default (see clogSaysNotAborted, already consulted above).
	return true
}

// clogSaysNotAborted reports whether the durable commit log permits treating
// xid as committed: true unless the CLOG positively says TxnStatusAborted.
//
// Conservative contract: only a positive TxnStatusAborted is authoritative here.
// TxnStatusUnknown (a runtime XID whose CLOG lane has not been stamped yet) and
// TxnStatusCommitted both read as "not aborted", so consulting the CLOG cannot
// hide live work — it only hides aborts the in-memory Aborted array forgot,
// which after a restart is all of them (the array is rebuilt empty).
func (s Snapshot) clogSaysNotAborted(xid storage.TransactionID) bool {
	if s.clog == nil {
		return true
	}
	// Below the oldest retained CLOG XID, status has been truncated away and the
	// XID is older than every relfrozenxid (treat as committed/frozen). Use the
	// wraparound-safe comparison (M0117-0001).
	if oldest := s.clog.OldestClogXid(); oldest != 0 && storage.XIDPrecedes(xid, oldest) {
		return true
	}
	return s.clog.GetStatus(xid) != TxnStatusAborted
}
