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
	mu      sync.Mutex
	nextXID storage.TransactionID
	active  map[storage.TransactionID]*txState
}

// NewManager returns a fresh manager whose first assigned xid is 3,
// mirroring PostgreSQL's first normal xid.
func NewManager() *Manager {
	return &Manager{
		nextXID: FirstNormalTransactionID,
		active:  map[storage.TransactionID]*txState{},
	}
}

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
func (m *Manager) Commit(tx Transaction) error {
	return m.finish(tx)
}

// Rollback marks tx aborted and removes it from the active set.
func (m *Manager) Rollback(tx Transaction) error {
	return m.finish(tx)
}

// ActiveCount returns the number of in-progress transactions.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

func (m *Manager) finish(tx Transaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.active[tx.XID]
	if !ok {
		return ErrUnknownTransaction
	}
	if state.isolation != tx.Isolation {
		return fmt.Errorf("mvcc: transaction isolation mismatch xid=%d", tx.XID)
	}
	delete(m.active, tx.XID)
	return nil
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
