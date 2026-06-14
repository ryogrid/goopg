package mvcc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

const (
	BootstrapTransactionID   storage.TransactionID = 1
	FrozenTransactionID      storage.TransactionID = 2
	FirstNormalTransactionID storage.TransactionID = 3
)

var (
	ErrUnknownTransaction = errors.New("mvcc: unknown transaction")
	ErrXIDWraparound      = errors.New("mvcc: xid wraparound is not implemented")
)

// TxnHandle is an opaque, monotonically-allocated identifier for an
// in-progress transaction that exists independently of the XID. It
// is the active-set key so that read-only transactions (which never
// receive an XID under the lazy-allocation model — M0093, PG parity)
// don't collide on the InvalidTransactionID sentinel.
//
// Handles are not durable; they exist only within a single Manager
// lifetime. The first handle is 1; 0 is reserved as the zero value.
type TxnHandle uint64

// Transaction is one open transaction handle.
type Transaction struct {
	// Handle is the manager-internal identity of this transaction,
	// always non-zero for a tx returned by Begin. Used as the
	// active-set key. Survives lazy XID assignment.
	Handle TxnHandle

	// XID is the assigned transaction ID. Zero
	// (storage.InvalidTransactionID) until the first write-path
	// operation calls Manager.AssignXID — this is the M0093 lazy
	// allocation mirroring PostgreSQL's RecordTransactionCommit
	// fast-path for read-only transactions.
	XID storage.TransactionID

	Isolation IsolationLevel
}

// Manager tracks active transactions and creates statement snapshots.
// It is safe for concurrent use.
type Manager struct {
	xidgen    XidGen
	procArray ProcArray

	abortedMu   sync.RWMutex
	abortedXIDs []storage.TransactionID

	xactMarkerMu sync.RWMutex
	xactMarker   func(storage.TransactionID, XactMarker) error

	// onTxnEnd is called after every transaction commit or abort, once the
	// XID slot has been cleared and commitCond has been broadcast. Use case:
	// cleaning up cross-session registries (e.g. spec insert registry for
	// pg_locks). The callback must be non-blocking and short.
	onTxnEndMu sync.RWMutex
	onTxnEnd   func(xid storage.TransactionID)

	// relcacheInvalPending is set by DDL that writes to nailed catalog
	// relations (pg_class, pg_attribute, pg_proc, pg_type) so the
	// xact-marker hook can emit RecordKindXactCommitInval and unlink
	// both pg_internal.init files at commit time.
	relcacheInvalPending atomic.Bool

	// WaitForXID uses waitMu + commitCond only.
	waitMu     sync.Mutex
	commitCond *sync.Cond

	// autoProcNum is an atomic counter for callers (mostly tests) that do
	// not supply an explicit procNum. Auto-assigned slots are never recycled
	// within a Manager lifetime, so the counter wraps modulo procArray size.
	autoProcNum atomic.Int32

	// Cold path: SSI + predicate locks share ssiMu.
	ssiMu          sync.Mutex
	ssiState       ssiState
	predicateLocks predicateLocksRegistry

	// subxact tracking (M0050-0002): maps subxact XIDs to their parent
	// XIDs, and records individually-aborted subxact XIDs. Lazily
	// initialised on first use. Protected by subxactMu.
	subxactFields
}

// NewManager returns a fresh manager whose first assigned xid is 3,
// mirroring PostgreSQL's first normal xid.
func NewManager() *Manager {
	m := &Manager{}
	// init xidgen: next stores the next XID to assign.
	// Allocate() returns next and advances to next+1; Peek() returns next.
	// FirstNormalTransactionID = 3 mirrors PostgreSQL's first normal XID.
	m.xidgen.next.Store(uint64(FirstNormalTransactionID))
	m.procArray = newProcArray(defaultProcArraySize)
	m.commitCond = sync.NewCond(&m.waitMu)
	return m
}

// SetNextXID advances nextXID forward without allocating it. Used on
// startup to skip over xids already present in persisted heap tuples
// from previous sessions, so the new session's snapshots see those
// tuples as committed (xmin < snap.Xmin) instead of in-progress.
//
// The change is monotonic: a smaller value is ignored.
func (m *Manager) SetNextXID(x storage.TransactionID) {
	m.xidgen.SetNext(x)
}

// ReplayXactCommit is called by the standby's StreamReplayer when it applies
// a RecordKindXactCommit from the primary. It advances nextXID past xid so
// that snapshots taken on the standby see the replayed tuples as committed
// (xmin < snap.Xmax). Without this call, the snapshot's Xmax equals the
// committed XID, causing "xid >= Xmax → invisible" for every replayed tuple.
func (m *Manager) ReplayXactCommit(xid storage.TransactionID) {
	m.xidgen.SetNext(xid + 1)
}

// ReplayXactAbort is called by the standby's StreamReplayer when it applies a
// RecordKindXactAbort from the primary. It advances nextXID past xid (so the
// aborted transaction is not treated as "future") and records it in abortedXIDs
// (so its heap tuples remain invisible to queries on the standby).
func (m *Manager) ReplayXactAbort(xid storage.TransactionID) {
	m.xidgen.SetNext(xid + 1)
	m.abortedMu.Lock()
	m.abortedXIDs = insertSortedXID(m.abortedXIDs, xid)
	m.abortedMu.Unlock()
}

// NextXID returns the value the next Begin will allocate. Used by
// the bootstrap path to snapshot transaction state across restarts.
func (m *Manager) NextXID() storage.TransactionID {
	return m.xidgen.Peek()
}

// xidWarnAge is how many XIDs before uint32 overflow to emit a warning.
// Mirrors upstream's GetNewTransactionId warning threshold (~40M before max).
const xidWarnAge = storage.TransactionID(40_000_000)

// xidStopAge is how many XIDs before uint32 overflow to refuse new txns.
const xidStopAge = storage.TransactionID(3_000_000)

// xidMaxSafe is the last "safe" XID before the hard stop threshold.
// uint32 max = 4,294,967,295; reserved XIDs 0-2 put the usable ceiling here.
const xidMaxSafe = ^storage.TransactionID(0) - xidStopAge

// Begin allocates a slot and tracks the transaction as in-progress.
// M0093: NO XID is allocated; the first write path calls AssignXID
// to materialise one. Returns a Transaction with Handle != 0 and
// XID == storage.InvalidTransactionID.
//
// procNum identifies the backend's slot in the ProcArray (0-based).
// If omitted (variadic), a slot is auto-assigned from an internal
// counter — suitable for tests and callers that have not yet threaded
// an explicit procNum. Production server code should always supply one.
func (m *Manager) Begin(iso IsolationLevel, procNums ...int32) (Transaction, error) {
	var procNum int32
	if len(procNums) > 0 {
		procNum = procNums[0]
	} else {
		// Auto-assign: scan for a free slot (inTxn==0) starting from slot 1.
		// Slot 0 is reserved for connections that supply explicit procNum=0.
		// CAS-based scan avoids colliding with slots held by active connections
		// that pass an explicit procNum — the old counter-based approach falsely
		// allocated slot 1 on the very first call, clobbering a live connection.
		found := false
		sz := len(m.procArray.slots)
		for i := 1; i < sz; i++ {
			if m.procArray.slots[i].inTxn.CompareAndSwap(0, 1) {
				procNum = int32(i)
				found = true
				break
			}
		}
		if !found {
			return Transaction{}, fmt.Errorf("mvcc: no free process slots for internal transaction")
		}
		// Initialise the slot fields before returning — skip the later block
		// that does the same for explicit-procNum callers.
		s := &m.procArray.slots[procNum]
		s.xid.Store(0)
		s.xmin.Store(^uint64(0))
		s.firstSnap = nil
		s.snapshotXmin = ^uint32(0)
		s.isolation = int32(iso)
		// inTxn already set to 1 by CAS above.
		handle := TxnHandle(procNum + 1)
		if iso == IsolationSerializable {
			m.ssiMu.Lock()
			m.registerSerializableLocked(handle)
			m.ssiMu.Unlock()
		}
		return Transaction{Handle: handle, XID: storage.InvalidTransactionID, Isolation: iso}, nil
	}
	switch iso {
	case IsolationReadCommitted, IsolationRepeatableRead, IsolationSerializable:
	default:
		return Transaction{}, fmt.Errorf("mvcc: unsupported isolation level %v", iso)
	}
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return Transaction{}, fmt.Errorf("mvcc: procNum %d out of range [0, %d)", procNum, len(m.procArray.slots))
	}

	s := &m.procArray.slots[procNum]
	s.xid.Store(0)
	s.xmin.Store(^uint64(0))
	s.firstSnap = nil
	s.snapshotXmin = ^uint32(0)
	s.isolation = int32(iso)
	s.inTxn.Store(1)

	// M0104-0002: allocate per-txn SSI bookkeeping for SERIALIZABLE
	// transactions. RC/RR never register; the registry stays nil
	// for those workloads. Subsequent slices (predicate locks,
	// rw-conflict tracking, pre-commit failure) attach to the
	// SerializableXact returned here.
	// Handle = procNum+1 so that Handle is always ≥1 (Handle=0 remains the
	// "invalid/unset" sentinel, since procNum=0 is a valid slot index).
	handle := TxnHandle(procNum + 1)
	if iso == IsolationSerializable {
		m.ssiMu.Lock()
		m.registerSerializableLocked(handle)
		m.ssiMu.Unlock()
	}
	return Transaction{Handle: handle, XID: storage.InvalidTransactionID, Isolation: iso}, nil
}

// AssignXID lazily allocates a real XID for tx (M0093, PG-parity).
// Idempotent: returns the existing XID on subsequent calls.
//
// CRITICAL: callers MUST refresh their Transaction.XID with the
// returned value BEFORE any code that consults tx.XID as a real
// XID — in particular before isConcurrentlyUpdated (M0090),
// NewHeapTuple xmin, PageSetHeapTupleXmax, PageSetHeapTupleLockOnly.
// Use executor.Context.MaterializeWriterXID() which does this
// atomically for the common executor call sites.
func (m *Manager) AssignXID(tx Transaction) (storage.TransactionID, error) {
	procNum := int32(tx.Handle) - 1
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return 0, ErrUnknownTransaction
	}
	s := &m.procArray.slots[procNum]
	if s.inTxn.Load() == 0 {
		return 0, ErrUnknownTransaction
	}

	if existing := s.xid.Load(); existing != 0 {
		return storage.TransactionID(existing), nil
	}

	newXID := m.xidgen.Allocate()
	if newXID == ^storage.TransactionID(0) {
		return 0, ErrXIDWraparound
	}
	// Anti-wraparound guard (M0046-0005): refuse new transactions
	// when too close to uint32 overflow.
	if newXID > xidMaxSafe {
		return 0, fmt.Errorf(
			"mvcc: database must be vacuumed within %d transactions to prevent XID wraparound",
			^storage.TransactionID(0)-newXID)
	}
	s.xid.Store(uint64(newXID))

	// M0104-0002: stamp the new top-level XID onto the SSI
	// bookkeeping object so future slices that key conflict
	// records by XID can find the SerializableXact. Read-only
	// SERIALIZABLE transactions never reach this branch and keep
	// SerializableXact.XID == InvalidTransactionID.
	if tx.Isolation == IsolationSerializable {
		m.ssiMu.Lock()
		if m.ssiState.xacts != nil {
			if sx, ok := m.ssiState.xacts[tx.Handle]; ok {
				sx.XID = newXID
			}
		}
		m.ssiMu.Unlock()
	}
	return newXID, nil
}

// SnapshotFor returns the statement snapshot for tx.
//
// READ COMMITTED gets a fresh snapshot on every call.
// REPEATABLE READ pins the first snapshot for the whole transaction.
//
// M0093: lookup is by Handle (not XID), and the per-state
// snapshotXmin is updated to the min observed Xmin so OldestXmin
// can pin VACUUM correctly even for read-only RR transactions.
func (m *Manager) SnapshotFor(tx Transaction) (Snapshot, error) {
	procNum := int32(tx.Handle) - 1
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return Snapshot{}, ErrUnknownTransaction
	}
	s := &m.procArray.slots[procNum]
	if s.inTxn.Load() == 0 {
		return Snapshot{}, ErrUnknownTransaction
	}
	if IsolationLevel(s.isolation) != tx.Isolation {
		return Snapshot{}, fmt.Errorf("mvcc: transaction isolation mismatch handle=%d", tx.Handle)
	}

	switch tx.Isolation {
	case IsolationReadCommitted:
		snap := m.captureSnapshot()
		// Update slot xmin to track the lowest seen Xmin for OldestXmin.
		for {
			cur := s.xmin.Load()
			want := uint64(snap.Xmin)
			if want >= cur {
				break
			}
			if s.xmin.CompareAndSwap(cur, want) {
				break
			}
		}
		return snap, nil

	case IsolationRepeatableRead, IsolationSerializable:
		// SERIALIZABLE shares snapshot acquisition with RR pending the
		// predicate-lock substrate (M0104-0003); conflict detection
		// will overlay on top of the pinned snapshot, not replace it.
		if s.firstSnap == nil {
			snap := m.captureSnapshot()
			s.firstSnap = &snap
			s.xmin.Store(uint64(snap.Xmin))
		}
		return s.firstSnap.Clone(), nil

	default:
		return Snapshot{}, fmt.Errorf("mvcc: unsupported isolation level %v", tx.Isolation)
	}
}

// Commit marks tx committed and removes it from the active set.
// The XactMarkerLogger hook (when installed) is invoked with
// kind=XactCommit before the active-set removal so a hook failure
// surfaces as a Commit error and the transaction stays in-progress
// for the caller to retry.
//
// M0093: the hook fires ONLY when the transaction was assigned a
// real XID. Read-only commits skip the hook entirely (no WAL
// XactCommit record, no fsync, no clog write) — mirroring PG's
// RecordTransactionCommit fast-path for txns with no XID.
func (m *Manager) Commit(tx Transaction) error {
	return m.finish(tx, XactCommit)
}

// Rollback marks tx aborted and removes it from the active set.
// The XactMarkerLogger hook is invoked the same way as Commit
// but with kind=XactAbort. A hook failure on rollback also keeps
// the transaction in-progress; the caller is expected to retry
// or escalate.
func (m *Manager) Rollback(tx Transaction) error {
	return m.finish(tx, XactAbort)
}

// AllocateSubXid allocates a fresh XID for a subtransaction and registers
// it as a child of parentXid. The sub-XID is not tracked in the proc-array
// (subxact XIDs are not independent top-level transactions); visibility is
// handled entirely by SeesCommittedXIDWithSubxacts via the subxact map.
//
// M0093 note: parentXid must already be a real (non-Invalid) XID. The
// caller — executor's SAVEPOINT path — calls Context.MaterializeWriterXID
// first so the parent has a real XID before sub-XIDs are allocated under
// it.
func (m *Manager) AllocateSubXid(parentXid storage.TransactionID) (storage.TransactionID, error) {
	subXid := m.xidgen.Allocate()
	if subXid == ^storage.TransactionID(0) {
		return 0, ErrXIDWraparound
	}
	if subXid > xidMaxSafe {
		return 0, fmt.Errorf(
			"mvcc: database must be vacuumed within %d transactions to prevent XID wraparound",
			^storage.TransactionID(0)-subXid)
	}
	m.addSubxactEntry(subXid, parentXid)
	return subXid, nil
}

// ActiveCount returns the number of in-progress transactions.
func (m *Manager) ActiveCount() int {
	count := 0
	for i := range m.procArray.slots {
		if m.procArray.slots[i].inTxn.Load() == 1 {
			count++
		}
	}
	return count
}

// OldestXmin is the lowest xid still potentially observable by any
// in-progress or future snapshot. Returns nextXID when no transaction
// is active. VACUUM uses this as the horizon below which xmax-tagged
// tuples can be reclaimed.
//
// M0093: folds in both (a) assigned XIDs of active transactions AND
// (b) the xmin of any active txn that has taken a snapshot
// but not yet been assigned an XID.
func (m *Manager) OldestXmin() storage.TransactionID {
	result := storage.TransactionID(m.xidgen.Peek())
	for i := range m.procArray.slots {
		s := &m.procArray.slots[i]
		// Skip idle slots — their zero xmin value would falsely pin VACUUM at 0.
		if s.inTxn.Load() == 0 {
			continue
		}
		if xid := s.xid.Load(); xid != 0 && storage.TransactionID(xid) < result {
			result = storage.TransactionID(xid)
		}
		if xmin := s.xmin.Load(); xmin != ^uint64(0) && storage.TransactionID(xmin) < result {
			result = storage.TransactionID(xmin)
		}
	}
	return result
}

func (m *Manager) finish(tx Transaction, kind XactMarker) error {
	procNum := int32(tx.Handle) - 1
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return ErrUnknownTransaction
	}
	s := &m.procArray.slots[procNum]
	if s.inTxn.Load() == 0 {
		return ErrUnknownTransaction
	}
	if IsolationLevel(s.isolation) != tx.Isolation {
		return fmt.Errorf("mvcc: transaction isolation mismatch handle=%d", tx.Handle)
	}

	// Save the XID before clearing the slot.
	xid := storage.TransactionID(s.xid.Load())

	// M0104-0006: run the pre-commit dangerous-structure scan before
	// any side effects. On detection, return a typed
	// *SerializationFailureError so the executor can surface
	// SQLSTATE 40001 and call Rollback to perform the actual
	// cleanup. The transaction remains in the slot until the caller
	// rolls back, mirroring upstream's `ereport(ERROR, ...)` flow
	// out of `PreCommit_CheckForSerializationFailure`.
	if tx.Isolation == IsolationSerializable && kind == XactCommit {
		m.ssiMu.Lock()
		err := m.preCommitCheckForSerializationFailureLocked(tx.Handle)
		m.ssiMu.Unlock()
		if err != nil {
			return err
		}
	}

	// M0093: invoke the xactMarker hook only when the transaction
	// was assigned a real XID. Read-only commits skip the hook
	// entirely — no WAL XactCommit record, no fsync, no clog
	// write. Mirrors PG's RecordTransactionCommit fast-path.
	if xid != storage.InvalidTransactionID {
		m.xactMarkerMu.RLock()
		hook := m.xactMarker
		m.xactMarkerMu.RUnlock()
		if hook != nil {
			if err := hook(xid, kind); err != nil {
				return fmt.Errorf("mvcc: xact-marker hook (xid=%d, kind=%v): %w", xid, kind, err)
			}
		}
	}

	// Clear the slot.
	s.xid.Store(0)
	s.firstSnap = nil
	s.xmin.Store(^uint64(0))
	s.inTxn.Store(0)

	// M0100-0002: track rolled-back XIDs so their rows stay invisible
	// even after the XID falls below future snapshots' Xmin.
	if kind == XactAbort && xid != storage.InvalidTransactionID {
		m.abortedMu.Lock()
		m.abortedXIDs = insertSortedXID(m.abortedXIDs, xid)
		m.abortedMu.Unlock()
	}

	// M0104-0002: release SSI bookkeeping for SERIALIZABLE
	// transactions (commit or abort). Stamps FinishedAt with the
	// next CommitSeqNo and deletes the entry from the registry.
	// RC/RR transactions skip this branch.
	if tx.Isolation == IsolationSerializable {
		m.ssiMu.Lock()
		m.releaseSerializableLocked(tx.Handle)
		m.ssiMu.Unlock()
	}

	// Broadcast to unblock any goroutine waiting in WaitForXID.
	m.waitMu.Lock()
	m.commitCond.Broadcast()
	m.waitMu.Unlock()

	if xid != storage.InvalidTransactionID {
		m.onTxnEndMu.RLock()
		hook := m.onTxnEnd
		m.onTxnEndMu.RUnlock()
		if hook != nil {
			hook(xid)
		}
	}

	return nil
}

// SetOnTxnEnd registers a callback fired after every transaction commit or
// abort (once the slot is cleared and WaitForXID waiters are unblocked).
// Only one callback is supported; a second call replaces the first.
func (m *Manager) SetOnTxnEnd(fn func(xid storage.TransactionID)) {
	m.onTxnEndMu.Lock()
	m.onTxnEnd = fn
	m.onTxnEndMu.Unlock()
}

// WaitForXID blocks until the transaction identified by xid is no longer
// in-progress (committed or aborted) or ctx is cancelled.  Returns nil when
// the transaction has finished, or ctx.Err() on cancellation.
//
// This is used by the ON CONFLICT executor to implement the "speculative
// insert" row-wait: when an upsert detects that a conflicting row belongs
// to an in-progress transaction, it blocks here until that transaction
// commits or aborts before re-evaluating the conflict.
func (m *Manager) WaitForXID(ctx context.Context, xid storage.TransactionID) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Wake up the Cond.Wait below so it can check ctx.
			m.commitCond.Broadcast()
		case <-done:
		}
	}()
	defer close(done)

	m.waitMu.Lock()
	defer m.waitMu.Unlock()
	for m.xidInProgress(xid) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.commitCond.Wait()
	}
	return ctx.Err()
}

// WaitForOlderSlotsToCommit waits until every backend slot that was in an
// active transaction (inTxn==1) at the time of the call has finished its
// transaction (inTxn back to 0). The caller's own slot (selfHandle) is
// excluded to prevent the caller from waiting for itself.
//
// Used by DROP INDEX CONCURRENTLY to drain all snapshots that were open
// before the drop: once this returns, no transaction that started before
// the DROP is still running, so the index may be safely removed.
// M0100-0009.
func (m *Manager) WaitForOlderSlotsToCommit(ctx context.Context, selfHandle TxnHandle) error {
	selfIdx := int(selfHandle) - 1 // Handle = procNum+1

	// Snapshot which slots are active right now (excluding self).
	active := make([]int, 0, 4)
	for i := range m.procArray.slots {
		if i == selfIdx {
			continue
		}
		if m.procArray.slots[i].inTxn.Load() == 1 {
			active = append(active, i)
		}
	}
	if len(active) == 0 {
		return nil
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			m.commitCond.Broadcast()
		case <-done:
		}
	}()
	defer close(done)

	m.waitMu.Lock()
	defer m.waitMu.Unlock()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		allDone := true
		for _, i := range active {
			if m.procArray.slots[i].inTxn.Load() == 1 {
				allDone = false
				break
			}
		}
		if allDone {
			return nil
		}
		m.commitCond.Wait()
	}
}

// xidInProgress reports whether xid is assigned to any currently active
// transaction. Lock-free: uses atomic loads on procArray slots.
func (m *Manager) xidInProgress(xid storage.TransactionID) bool {
	for i := range m.procArray.slots {
		if m.procArray.slots[i].xid.Load() == uint64(xid) {
			return true
		}
	}
	return false
}

// IsXIDActive reports whether xid belongs to a currently-running
// transaction. Safe to call from any goroutine; uses atomic loads
// internally. Used by upsertOp.findInProgressConflict to detect
// heap tuples whose xmin was materialised after the caller's snapshot
// was captured (M0100-0002).
func (m *Manager) IsXIDActive(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	return m.xidInProgress(xid)
}

// HasAbortedXID reports whether xid is recorded in the manager's aborted
// set (transactions that ran Rollback). Lives next to IsXIDActive because
// callers typically pair the two: WaitForXID returns once the xact is
// settled, and the caller then asks "did it commit or abort?" — committed
// is "not active AND not aborted"; aborted is HasAbortedXID. Used by the
// FK-on-delete wait path to translate "in-flight child INSERT settled" into
// the correct post-wait action: commit → raise 40001 in RR/Serializable,
// abort → no FK conflict.
func (m *Manager) HasAbortedXID(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	m.abortedMu.RLock()
	defer m.abortedMu.RUnlock()
	n := len(m.abortedXIDs)
	if n == 0 {
		return false
	}
	idx := sort.Search(n, func(i int) bool { return m.abortedXIDs[i] >= xid })
	return idx < n && m.abortedXIDs[idx] == xid
}

// XactMarker discriminates the two transaction-end markers fed to
// the M0008 logical decoder via SetXactMarkerLogger. Mirrors the
// upstream xact-end records.
type XactMarker int

const (
	// XactCommit is the kind passed to the hook from Commit.
	XactCommit XactMarker = iota
	// XactAbort is the kind passed to the hook from Rollback.
	XactAbort
)

func (k XactMarker) String() string {
	switch k {
	case XactCommit:
		return "commit"
	case XactAbort:
		return "abort"
	}
	return "unknown"
}

// SetXactMarkerLogger installs a callback the manager invokes on
// every Commit / Rollback before removing the xact from the
// active set. The hook is the seam the M0008 logical decoder
// uses to learn about transaction boundaries — production wires
// it to wal.Writer.Append(EncodeXactCommit/Abort(xid)) so the
// classifier sees commit/abort markers in the WAL stream.
//
// Pass nil to clear a previously installed hook. Tests that care
// only about MVCC mechanics typically leave it unset.
func (m *Manager) SetXactMarkerLogger(fn func(storage.TransactionID, XactMarker) error) {
	m.xactMarkerMu.Lock()
	defer m.xactMarkerMu.Unlock()
	m.xactMarker = fn
}

// SetRelcacheInvalPending marks the current transaction as having written to
// a nailed catalog relation (pg_class, pg_attribute, pg_proc, or pg_type).
// The xact-marker hook reads this flag at commit time to choose between
// EncodeXactCommit and EncodeXactCommitInval and to unlink both
// pg_internal.init files so the next backend reloads fresh descriptors.
func (m *Manager) SetRelcacheInvalPending() {
	m.relcacheInvalPending.Store(true)
}

// TakeRelcacheInvalPending atomically reads and clears the relcache-inval
// pending flag. Returns true if the flag was set, meaning the committing
// transaction touched a nailed catalog relation.
func (m *Manager) TakeRelcacheInvalPending() bool {
	return m.relcacheInvalPending.Swap(false)
}

// captureSnapshot builds a consistent snapshot with a lock-free walk of
// the proc-array slots.
func (m *Manager) captureSnapshot() Snapshot {
	xmax := m.xidgen.Peek()
	inProgress := make([]storage.TransactionID, 0, 16)
	xmin := xmax
	for i := range m.procArray.slots {
		xid := storage.TransactionID(m.procArray.slots[i].xid.Load())
		if xid == 0 || xid >= xmax {
			continue
		}
		inProgress = append(inProgress, xid)
		if xid < xmin {
			xmin = xid
		}
	}
	if len(inProgress) == 0 {
		xmin = xmax
	}
	sort.Slice(inProgress, func(i, j int) bool { return inProgress[i] < inProgress[j] })

	// M0100-0002: include ALL aborted XIDs in the snapshot so rolled-back
	// rows remain invisible even when their xmin falls below Xmin.
	m.abortedMu.RLock()
	var aborted []storage.TransactionID
	if len(m.abortedXIDs) > 0 {
		aborted = make([]storage.TransactionID, len(m.abortedXIDs))
		copy(aborted, m.abortedXIDs)
	}
	m.abortedMu.RUnlock()

	return Snapshot{
		Xmin:       xmin,
		Xmax:       xmax,
		InProgress: inProgress,
		Aborted:    aborted,
	}
}

// insertSortedXID inserts xid into a sorted slice of XIDs (ascending).
func insertSortedXID(s []storage.TransactionID, xid storage.TransactionID) []storage.TransactionID {
	idx := sort.Search(len(s), func(i int) bool { return s[i] >= xid })
	if idx < len(s) && s[idx] == xid {
		return s // already present
	}
	s = append(s, 0)
	copy(s[idx+1:], s[idx:])
	s[idx] = xid
	return s
}
