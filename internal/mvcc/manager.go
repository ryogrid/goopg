package mvcc

import (
	"errors"
	"fmt"
	"sort"
	"sync"

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

	// subxact tracking (M0050-0002): maps subxact XIDs to their parent
	// XIDs, and records individually-aborted subxact XIDs. Lazily
	// initialised on first use. Protected by subxactMu.
	subxactFields
}

// NewManager returns a fresh manager whose first assigned xid is 3,
// mirroring PostgreSQL's first normal xid.
func NewManager() *Manager {
	return &Manager{
		nextXID:    FirstNormalTransactionID,
		nextHandle: 1,
		active:     map[TxnHandle]*txState{},
	}
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
	if iso != IsolationReadCommitted && iso != IsolationRepeatableRead {
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
	case IsolationRepeatableRead:
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
	// M0093: invoke the xactMarker hook only when the transaction
	// was assigned a real XID. Read-only commits skip the hook
	// entirely — no WAL XactCommit record, no fsync, no clog
	// write. Mirrors PG's RecordTransactionCommit fast-path.
	if state.xid != storage.InvalidTransactionID && m.xactMarker != nil {
		if err := m.xactMarker(state.xid, kind); err != nil {
			return fmt.Errorf("mvcc: xact-marker hook (xid=%d, kind=%v): %w", state.xid, kind, err)
		}
	}
	delete(m.active, tx.Handle)
	return nil
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
	return Snapshot{
		Xmin:       xmin,
		Xmax:       m.nextXID,
		InProgress: inProgress,
	}
}
