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

type txState struct {
	isolation     IsolationLevel
	firstSnapshot *Snapshot

	// xid is the assigned transaction ID, or
	// storage.InvalidTransactionID if no write has materialised one
	// yet. Set by AssignXID. Once non-zero, never changes.
	xid storage.TransactionID

	// snapshotXmin tracks the minimum Xmin observed across every
	// snapshot this transaction has taken. It pins VACUUM's
	// reclamation horizon for long-running read-only REPEATABLE
	// READ transactions whose own xid would otherwise be Invalid.
	// Initialised to Invalid; updated on each SnapshotFor call to
	// min(prev-or-MaxUint32, snap.Xmin). OldestXmin folds this in
	// alongside assigned xids (M0093 R-B6 correctness fix).
	snapshotXmin storage.TransactionID
}

// Manager tracks active transactions and creates statement snapshots.
// It is safe for concurrent use.
type Manager struct {
	mu         sync.Mutex
	nextXID    storage.TransactionID
	nextHandle TxnHandle
	active     map[TxnHandle]*txState
	xactMarker func(storage.TransactionID, XactMarker) error

	// relcacheInvalPending is set by DDL that writes to nailed catalog
	// relations (pg_class, pg_attribute, pg_proc, pg_type) so the
	// xact-marker hook can emit RecordKindXactCommitInval and unlink
	// both pg_internal.init files at commit time.
	relcacheInvalPending atomic.Bool

	// commitCond is broadcast whenever a transaction commits or aborts.
	// Used by WaitForXID to block until a specific XID finishes.
	commitCond *sync.Cond

	// abortedXIDs tracks XIDs whose transactions were rolled back.
	// Populated by finish() on rollback; included in every snapshot's
	// Aborted field so rolled-back rows remain invisible even after
	// their xmin falls below the snapshot's Xmin. This is a lightweight
	// substitute for a full clog (M0100-0002). Sorted ascending.
	abortedXIDs []storage.TransactionID

	// subxact tracking (M0050-0002): maps subxact XIDs to their parent
	// XIDs, and records individually-aborted subxact XIDs. Lazily
	// initialised on first use. Protected by subxactMu.
	subxactFields

	// ssiState is the SERIALIZABLE-transaction bookkeeping registry
	// (M0104-0002). Lazily initialised when the first serializable
	// transaction Begins; protected by Manager.mu. See ssi.go.
	ssiState ssiState

	// predicateLocks is the SIREAD predicate-lock registry
	// (M0104-0003). Lazy-initialised on first AcquirePredicateLock;
	// protected by Manager.mu. RC/RR workloads never allocate it.
	// See predlock.go.
	predicateLocks predicateLocksRegistry
}

// NewManager returns a fresh manager whose first assigned xid is 3,
// mirroring PostgreSQL's first normal xid.
func NewManager() *Manager {
	m := &Manager{
		nextXID:    FirstNormalTransactionID,
		nextHandle: 1,
		active:     map[TxnHandle]*txState{},
	}
	m.commitCond = sync.NewCond(&m.mu)
	return m
}

// SetNextXID advances nextXID forward without allocating it. Used on
// startup to skip over xids already present in persisted heap tuples
// from previous sessions, so the new session's snapshots see those
// tuples as committed (xmin < snap.Xmin) instead of in-progress.
//
// The change is monotonic: a smaller value is ignored. Concurrent
// callers are serialised with the regular Manager mutex.
func (m *Manager) SetNextXID(x storage.TransactionID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if x > m.nextXID {
		m.nextXID = x
	}
}

// ReplayXactCommit is called by the standby's StreamReplayer when it applies
// a RecordKindXactCommit from the primary. It advances nextXID past xid so
// that snapshots taken on the standby see the replayed tuples as committed
// (xmin < snap.Xmax). Without this call, the snapshot's Xmax equals the
// committed XID, causing "xid >= Xmax → invisible" for every replayed tuple.
func (m *Manager) ReplayXactCommit(xid storage.TransactionID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := xid + 1
	if next > m.nextXID {
		m.nextXID = next
	}
}

// ReplayXactAbort is called by the standby's StreamReplayer when it applies a
// RecordKindXactAbort from the primary. It advances nextXID past xid (so the
// aborted transaction is not treated as "future") and records it in abortedXIDs
// (so its heap tuples remain invisible to queries on the standby).
func (m *Manager) ReplayXactAbort(xid storage.TransactionID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := xid + 1
	if next > m.nextXID {
		m.nextXID = next
	}
	m.abortedXIDs = insertSortedXID(m.abortedXIDs, xid)
}

// NextXID returns the value the next Begin will allocate. Used by
// the bootstrap path to snapshot transaction state across restarts.
func (m *Manager) NextXID() storage.TransactionID {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nextXID
}

// xidWarnAge is how many XIDs before uint32 overflow to emit a warning.
// Mirrors upstream's GetNewTransactionId warning threshold (~40M before max).
const xidWarnAge = storage.TransactionID(40_000_000)

// xidStopAge is how many XIDs before uint32 overflow to refuse new txns.
const xidStopAge = storage.TransactionID(3_000_000)

// xidMaxSafe is the last "safe" XID before the hard stop threshold.
// uint32 max = 4,294,967,295; reserved XIDs 0-2 put the usable ceiling here.
const xidMaxSafe = ^storage.TransactionID(0) - xidStopAge

// Begin allocates a handle and tracks the transaction as in-progress.
// M0093: NO XID is allocated; the first write path calls AssignXID
// to materialise one. Returns a Transaction with Handle != 0 and
// XID == storage.InvalidTransactionID.
func (m *Manager) Begin(iso IsolationLevel) (Transaction, error) {
	switch iso {
	case IsolationReadCommitted, IsolationRepeatableRead, IsolationSerializable:
	default:
		return Transaction{}, fmt.Errorf("mvcc: unsupported isolation level %v", iso)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	handle := m.nextHandle
	m.nextHandle++
	m.active[handle] = &txState{
		isolation:    iso,
		xid:          storage.InvalidTransactionID,
		snapshotXmin: ^storage.TransactionID(0), // sentinel: no snapshot yet
	}
	// M0104-0002: allocate per-txn SSI bookkeeping for SERIALIZABLE
	// transactions. RC/RR never register; the registry stays nil
	// for those workloads. Subsequent slices (predicate locks,
	// rw-conflict tracking, pre-commit failure) attach to the
	// SerializableXact returned here.
	if iso == IsolationSerializable {
		m.registerSerializableLocked(handle)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.active[tx.Handle]
	if !ok {
		return 0, ErrUnknownTransaction
	}
	if state.xid != storage.InvalidTransactionID {
		return state.xid, nil
	}
	if m.nextXID == ^storage.TransactionID(0) {
		return 0, ErrXIDWraparound
	}
	// Anti-wraparound guard (M0046-0005): refuse new transactions
	// when too close to uint32 overflow.
	if m.nextXID > xidMaxSafe {
		return 0, fmt.Errorf(
			"mvcc: database must be vacuumed within %d transactions to prevent XID wraparound",
			^storage.TransactionID(0)-m.nextXID)
	}
	state.xid = m.nextXID
	m.nextXID++
	// M0104-0002: stamp the new top-level XID onto the SSI
	// bookkeeping object so future slices that key conflict
	// records by XID can find the SerializableXact. Read-only
	// SERIALIZABLE transactions never reach this branch and keep
	// SerializableXact.XID == InvalidTransactionID.
	if state.isolation == IsolationSerializable && m.ssiState.xacts != nil {
		if sx, ok := m.ssiState.xacts[tx.Handle]; ok {
			sx.XID = state.xid
		}
	}
	return state.xid, nil
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
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.active[tx.Handle]
	if !ok {
		return Snapshot{}, ErrUnknownTransaction
	}
	if state.isolation != tx.Isolation {
		return Snapshot{}, fmt.Errorf("mvcc: transaction isolation mismatch handle=%d", tx.Handle)
	}
	switch state.isolation {
	case IsolationReadCommitted:
		s := m.captureSnapshotLocked()
		if s.Xmin < state.snapshotXmin {
			state.snapshotXmin = s.Xmin
		}
		return s, nil
	case IsolationRepeatableRead, IsolationSerializable:
		// SERIALIZABLE shares snapshot acquisition with RR pending the
		// predicate-lock substrate (M0104-0003); conflict detection
		// will overlay on top of the pinned snapshot, not replace it.
		if state.firstSnapshot == nil {
			s := m.captureSnapshotLocked()
			state.firstSnapshot = &s
			state.snapshotXmin = s.Xmin
		}
		return state.firstSnapshot.Clone(), nil
	default:
		return Snapshot{}, fmt.Errorf("mvcc: unsupported isolation level %v", state.isolation)
	}
}

// Commit marks tx committed and removes it from the active set.
// The XactMarkerLogger hook (when installed) is invoked under the
// manager's lock with kind=XactCommit before the active-set
// removal so a hook failure surfaces as a Commit error and the
// transaction stays in-progress for the caller to retry.
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
// it as a child of parentXid. The sub-XID is not tracked in the active map
// (subxact XIDs are not independent top-level transactions); visibility is
// handled entirely by SeesCommittedXIDWithSubxacts via the subxact map.
//
// M0093 note: parentXid must already be a real (non-Invalid) XID. The
// caller — executor's SAVEPOINT path — calls Context.MaterializeWriterXID
// first so the parent has a real XID before sub-XIDs are allocated under
// it. AllocateSubXid does NOT walk the active set to materialise the
// parent lazily, because by the time control reaches here the executor
// has already taken the page-level latches required for the actual
// subxact-introducing statement.
func (m *Manager) AllocateSubXid(parentXid storage.TransactionID) (storage.TransactionID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nextXID == ^storage.TransactionID(0) {
		return 0, ErrXIDWraparound
	}
	subXid := m.nextXID
	m.nextXID++
	m.addSubxactEntry(subXid, parentXid)
	return subXid, nil
}

// ActiveCount returns the number of in-progress transactions.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

// OldestXmin is the lowest xid still potentially observable by any
// in-progress or future snapshot. Returns nextXID when no transaction
// is active. VACUUM uses this as the horizon below which xmax-tagged
// tuples can be reclaimed.
//
// M0093: folds in both (a) assigned XIDs of active transactions AND
// (b) the snapshotXmin of any active txn that has taken a snapshot
// but not yet been assigned an XID. (b) preserves VACUUM correctness
// for long-running read-only REPEATABLE READ transactions whose
// snapshot is still observing tuples xmin >= snapshotXmin.
func (m *Manager) OldestXmin() storage.TransactionID {
	m.mu.Lock()
	defer m.mu.Unlock()
	xmin := m.nextXID
	for _, state := range m.active {
		if state.xid != storage.InvalidTransactionID && state.xid < xmin {
			xmin = state.xid
		}
		if state.snapshotXmin != ^storage.TransactionID(0) && state.snapshotXmin < xmin {
			xmin = state.snapshotXmin
		}
	}
	return xmin
}

func (m *Manager) finish(tx Transaction, kind XactMarker) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.active[tx.Handle]
	if !ok {
		return ErrUnknownTransaction
	}
	if state.isolation != tx.Isolation {
		return fmt.Errorf("mvcc: transaction isolation mismatch handle=%d", tx.Handle)
	}
	// M0104-0006: run the pre-commit dangerous-structure scan before
	// any side effects. On detection, return a typed
	// *SerializationFailureError so the executor can surface
	// SQLSTATE 40001 and call Rollback to perform the actual
	// cleanup. The transaction remains in m.active until the caller
	// rolls back, mirroring upstream's `ereport(ERROR, ...)` flow
	// out of `PreCommit_CheckForSerializationFailure`.
	//
	// Pre-commit also runs the doom-the-pivot scan that may transition
	// peers into the doomed state; this MUST happen while the
	// committing xact is still addressable through ssiState.xacts and
	// BEFORE releaseSerializableLocked scrubs its edges from peers,
	// because the scan walks `me.inConflicts -> pivot.inConflicts`.
	if state.isolation == IsolationSerializable && kind == XactCommit {
		if err := m.preCommitCheckForSerializationFailureLocked(tx.Handle); err != nil {
			return err
		}
	}
	// M0093: invoke the xactMarker hook only when the transaction
	// was assigned a real XID. Read-only commits skip the hook
	// entirely — no WAL XactCommit record, no fsync, no clog
	// write. Mirrors PG's RecordTransactionCommit fast-path.
	if state.xid != storage.InvalidTransactionID && m.xactMarker != nil {
		if err := m.xactMarker(state.xid, kind); err != nil {
			return fmt.Errorf("mvcc: xact-marker hook (xid=%d, kind=%v): %w", state.xid, kind, err)
		}
	}
	// M0100-0002: track rolled-back XIDs so their rows stay invisible
	// even after the XID falls below future snapshots' Xmin.
	if kind == XactAbort && state.xid != storage.InvalidTransactionID {
		m.abortedXIDs = insertSortedXID(m.abortedXIDs, state.xid)
	}
	// M0104-0002: release SSI bookkeeping for SERIALIZABLE
	// transactions (commit or abort). Stamps FinishedAt with the
	// next CommitSeqNo and deletes the entry from the registry.
	// RC/RR transactions skip this branch.
	if state.isolation == IsolationSerializable {
		m.releaseSerializableLocked(tx.Handle)
	}
	delete(m.active, tx.Handle)
	// Broadcast to unblock any goroutine waiting in WaitForXID.
	m.commitCond.Broadcast()
	return nil
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

	m.mu.Lock()
	defer m.mu.Unlock()
	for m.xidInProgress(xid) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.commitCond.Wait()
	}
	return ctx.Err()
}

// xidInProgress reports whether xid is assigned to any currently active
// transaction. Called with m.mu held.
func (m *Manager) xidInProgress(xid storage.TransactionID) bool {
	for _, state := range m.active {
		if state.xid == xid {
			return true
		}
	}
	return false
}

// IsXIDActive reports whether xid belongs to a currently-running
// transaction. Safe to call from any goroutine; acquires m.mu
// internally. Used by upsertOp.findInProgressConflict to detect
// heap tuples whose xmin was materialised after the caller's snapshot
// was captured (M0100-0002).
func (m *Manager) IsXIDActive(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.Lock()
	defer m.mu.Unlock()
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
// classifier sees commit/abort markers in the WAL stream. See
// docs/design/0008-0001-logical-decoding-pipeline.md.
//
// Pass nil to clear a previously installed hook. Tests that care
// only about MVCC mechanics typically leave it unset.
func (m *Manager) SetXactMarkerLogger(fn func(storage.TransactionID, XactMarker) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *Manager) captureSnapshotLocked() Snapshot {
	// M0093: iterate values (not keys) and include only states
	// whose xid has been materialised. Read-only txns contribute
	// no in-progress XID and don't pull Xmin down (their
	// snapshotXmin is tracked separately for OldestXmin).
	inProgress := make([]storage.TransactionID, 0, len(m.active))
	xmin := m.nextXID
	for _, state := range m.active {
		if state.xid == storage.InvalidTransactionID {
			continue
		}
		inProgress = append(inProgress, state.xid)
		if state.xid < xmin {
			xmin = state.xid
		}
	}
	if len(inProgress) == 0 {
		xmin = m.nextXID
	}
	sort.Slice(inProgress, func(i, j int) bool { return inProgress[i] < inProgress[j] })

	// M0100-0002: include ALL aborted XIDs in the snapshot so rolled-back
	// rows remain invisible even when their xmin falls below Xmin.
	// GC is deferred — the set is bounded by the number of rollbacks
	// during the server's lifetime, which is small in practice.
	var aborted []storage.TransactionID
	if len(m.abortedXIDs) > 0 {
		aborted = make([]storage.TransactionID, len(m.abortedXIDs))
		copy(aborted, m.abortedXIDs)
	}

	return Snapshot{
		Xmin:       xmin,
		Xmax:       m.nextXID,
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
