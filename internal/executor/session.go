package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// Session stores per-connection state the Transaction operator needs
// to manage BEGIN/COMMIT/ROLLBACK.
type Session interface {
	IsolationLevel() mvcc.IsolationLevel
	InExplicitTransaction() bool
	CurrentTransaction() (mvcc.Transaction, mvcc.Snapshot, bool)
	BeginExplicitTransaction(tx mvcc.Transaction, snap mvcc.Snapshot)
	EndExplicitTransaction()
}

// DDLUndoEntry records one CREATE TABLE or CREATE INDEX performed inside
// an explicit transaction so that ROLLBACK can reverse the catalog mutation
// and remove the physical relfile (M0030-0006).
type DDLUndoEntry struct {
	Name    parser.ObjectName
	RelOID  uint32 // physical relfile OID (= table.OID or index.OID)
	IsIndex bool
}

// BasicSession is a minimal Session implementation for the v0
// executor path.
type BasicSession struct {
	isolation     mvcc.IsolationLevel
	inTx          bool
	tx            mvcc.Transaction
	snap          mvcc.Snapshot
	pendingDDL    []DDLUndoEntry    // DDL creates pending rollback
	subxactStack  mvcc.SubxactStack // savepoint stack (M0050-0004)
	currentSubXid storage.TransactionID // 0 = use top-level tx.XID
	txFailed      bool              // in_failed_sql_transaction (25P02)
}

// NewBasicSession constructs an explicit-transaction session state
// holder with READ COMMITTED as default isolation.
func NewBasicSession() *BasicSession {
	return &BasicSession{isolation: mvcc.IsolationReadCommitted}
}

// SetIsolationLevel updates the default isolation level used by BEGIN.
func (s *BasicSession) SetIsolationLevel(level mvcc.IsolationLevel) error {
	if level != mvcc.IsolationReadCommitted && level != mvcc.IsolationRepeatableRead {
		return fmt.Errorf("executor: unsupported isolation level %v", level)
	}
	s.isolation = level
	return nil
}

func (s *BasicSession) IsolationLevel() mvcc.IsolationLevel { return s.isolation }

func (s *BasicSession) InExplicitTransaction() bool { return s.inTx }

func (s *BasicSession) CurrentTransaction() (mvcc.Transaction, mvcc.Snapshot, bool) {
	if !s.inTx {
		return mvcc.Transaction{}, mvcc.Snapshot{}, false
	}
	return s.tx, s.snap.Clone(), true
}

func (s *BasicSession) BeginExplicitTransaction(tx mvcc.Transaction, snap mvcc.Snapshot) {
	s.tx = tx
	s.snap = snap.Clone()
	s.inTx = true
}

func (s *BasicSession) EndExplicitTransaction() {
	s.tx = mvcc.Transaction{}
	s.snap = mvcc.Snapshot{}
	s.inTx = false
	s.pendingDDL = nil
	s.subxactStack = mvcc.SubxactStack{}
	s.currentSubXid = 0
	s.txFailed = false
}

// EffectiveWriterXID returns the XID to stamp on new heap tuples.
// Inside an open savepoint this is the sub-transaction's XID; otherwise
// it is the top-level transaction XID.
func (s *BasicSession) EffectiveWriterXID() storage.TransactionID {
	if s.currentSubXid != 0 {
		return s.currentSubXid
	}
	return s.tx.XID
}

// PushSavepoint records a new savepoint on the stack with the given
// sub-transaction XID. The sub-XID is stamped on subsequent heap mutations.
func (s *BasicSession) PushSavepoint(name string, snap mvcc.Snapshot, subXid storage.TransactionID) {
	entry := s.subxactStack.Push(name, snap)
	entry.SubXid = subXid
	s.currentSubXid = subXid
}

// ReleaseSavepoint marks the named savepoint and all savepoints above it
// as committed and removes them from the stack. Returns released entries.
func (s *BasicSession) ReleaseSavepoint(name string) ([]*mvcc.SubTransactionState, error) {
	released, err := s.subxactStack.Release(name)
	if err != nil {
		return nil, err
	}
	top := s.subxactStack.Top()
	if top != nil {
		s.currentSubXid = top.SubXid
	} else {
		s.currentSubXid = 0
	}
	return released, nil
}

// RollbackToSavepoint aborts the named savepoint and all savepoints above
// it, then pushes a fresh entry for the same name with the given snapshot
// and new sub-XID. Returns the aborted entries.
func (s *BasicSession) RollbackToSavepoint(name string, newSnap mvcc.Snapshot, newSubXid storage.TransactionID) ([]*mvcc.SubTransactionState, *mvcc.SubTransactionState, error) {
	aborted, fresh, err := s.subxactStack.RollbackTo(name, newSnap)
	if err != nil {
		return nil, nil, err
	}
	fresh.SubXid = newSubXid
	s.currentSubXid = newSubXid
	s.txFailed = false
	return aborted, fresh, nil
}

// IsTransactionFailed reports whether the transaction has an unresolved
// error that prevents non-rollback statements (SQLSTATE 25P02).
func (s *BasicSession) IsTransactionFailed() bool { return s.txFailed }

// SetTransactionFailed marks the transaction as failed.
func (s *BasicSession) SetTransactionFailed() { s.txFailed = true }

// RecordDDLCreate records a CREATE TABLE or CREATE INDEX for potential rollback.
// Called by DDL operators after catalog.CreateTable / catalog.CreateIndex succeed.
func (s *BasicSession) RecordDDLCreate(e DDLUndoEntry) {
	s.pendingDDL = append(s.pendingDDL, e)
}

// TakePendingDDLCreates drains and returns the pending DDL undo list.
// Called by execRollback to obtain the list of creates that need undoing.
func (s *BasicSession) TakePendingDDLCreates() []DDLUndoEntry {
	p := append([]DDLUndoEntry(nil), s.pendingDDL...)
	s.pendingDDL = nil
	return p
}
