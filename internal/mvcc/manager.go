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

// Transaction is one open transaction handle.
type Transaction struct {
	XID       storage.TransactionID
	Isolation IsolationLevel
}

type txState struct {
	isolation     IsolationLevel
	firstSnapshot *Snapshot
}

// Manager tracks active transactions and creates statement snapshots.
// It is safe for concurrent use.
type Manager struct {
	mu         sync.Mutex
	nextXID    storage.TransactionID
	active     map[storage.TransactionID]*txState
	xactMarker func(storage.TransactionID, XactMarker) error
}

// NewManager returns a fresh manager whose first assigned xid is 3,
// mirroring PostgreSQL's first normal xid.
func NewManager() *Manager {
	return &Manager{
		nextXID: FirstNormalTransactionID,
		active:  map[storage.TransactionID]*txState{},
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

// Begin allocates an xid and tracks the transaction as in-progress.
func (m *Manager) Begin(iso IsolationLevel) (Transaction, error) {
	if iso != IsolationReadCommitted && iso != IsolationRepeatableRead {
		return Transaction{}, fmt.Errorf("mvcc: unsupported isolation level %v", iso)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nextXID == ^storage.TransactionID(0) {
		return Transaction{}, ErrXIDWraparound
	}
	// Anti-wraparound guard (M0046-0005): refuse new transactions when too
	// close to uint32 overflow; warn when approaching the danger zone.
	if m.nextXID > xidMaxSafe {
		return Transaction{}, fmt.Errorf(
			"mvcc: database must be vacuumed within %d transactions to prevent XID wraparound",
			^storage.TransactionID(0)-m.nextXID)
	}
	xid := m.nextXID
	m.nextXID++
	tx := Transaction{XID: xid, Isolation: iso}
	m.active[xid] = &txState{isolation: iso}
	return tx, nil
}

// SnapshotFor returns the statement snapshot for tx.
//
// READ COMMITTED gets a fresh snapshot on every call.
// REPEATABLE READ pins the first snapshot for the whole transaction.
func (m *Manager) SnapshotFor(tx Transaction) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.active[tx.XID]
	if !ok {
		return Snapshot{}, ErrUnknownTransaction
	}
	if state.isolation != tx.Isolation {
		return Snapshot{}, fmt.Errorf("mvcc: transaction isolation mismatch xid=%d", tx.XID)
	}
	switch state.isolation {
	case IsolationReadCommitted:
		return m.captureSnapshotLocked(), nil
	case IsolationRepeatableRead:
		if state.firstSnapshot == nil {
			s := m.captureSnapshotLocked()
			state.firstSnapshot = &s
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
func (m *Manager) OldestXmin() storage.TransactionID {
	m.mu.Lock()
	defer m.mu.Unlock()
	xmin := m.nextXID
	for xid := range m.active {
		if xid < xmin {
			xmin = xid
		}
	}
	return xmin
}

func (m *Manager) finish(tx Transaction, kind XactMarker) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.active[tx.XID]
	if !ok {
		return ErrUnknownTransaction
	}
	if state.isolation != tx.Isolation {
		return fmt.Errorf("mvcc: transaction isolation mismatch xid=%d", tx.XID)
	}
	if m.xactMarker != nil {
		if err := m.xactMarker(tx.XID, kind); err != nil {
			return fmt.Errorf("mvcc: xact-marker hook (xid=%d, kind=%v): %w", tx.XID, kind, err)
		}
	}
	delete(m.active, tx.XID)
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
	inProgress := make([]storage.TransactionID, 0, len(m.active))
	xmin := m.nextXID
	for xid := range m.active {
		inProgress = append(inProgress, xid)
		if xid < xmin {
			xmin = xid
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
