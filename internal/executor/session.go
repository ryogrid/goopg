package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
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
	isolation  mvcc.IsolationLevel
	inTx       bool
	tx         mvcc.Transaction
	snap       mvcc.Snapshot
	pendingDDL []DDLUndoEntry // DDL creates pending rollback
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
	s.pendingDDL = nil // cleared on both commit (no undo needed) and rollback
}

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
